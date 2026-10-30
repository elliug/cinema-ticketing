#!/usr/bin/env bash
# M3.5 end-to-end verification: the milestone's exit criterion, in a browser.
#
# It proves, through a real Chromium driven by Playwright, that a stranger can buy a
# seat and that an operator can put one on sale — the purchase *and* the administration,
# on one stack, in one browser context per scenario:
#   1. A run-scoped stack: its own database, its own Redis logical database, its own API
#      port, its own web port and its own bucket, so it can never touch the development
#      data. It refuses to run against the `cinema` development database by name.
#   2. The project's own migration runner brings that database to 0006, the project's own
#      bootstrap command seeds it (a cinema, a room, a published layout, two films and two
#      screenings) and creates the bucket the ticket PDFs are stored in.
#   3. The web application is **built once and served**, never run through `next dev`, so
#      a developer's dev server and this gate cannot collide over `.next`.
#   4. The administration: a film is created, a screening is scheduled by choosing the
#      room, the film and the room's published layout version from the selects, its
#      price is set to 12.50 and its status to on_sale. A submit that changes nothing is
#      refused locally, a price of 12.505 is refused and its input is marked invalid, and
#      neither refusal writes anything.
#   5. The purchase: the catalogue lists the new screening at the venue's own hour, an
#      order of two seats is chosen with the keyboard alone, paid, and the ticket says
#      the box office answered "This ticket is valid", shows the API's QR image and hands
#      the browser a PDF.
#   6. The ticket list: what this tab kept, across a reload; a stale record is forgotten
#      and empties the list; a fresh context says it kept nothing; a context that denies
#      `sessionStorage` says it cannot read the list instead of claiming it is empty.
#   7. The negatives: an unknown ticket identifier is answered as unknown, every screen
#      renders exactly one `h1`, and no console error or hydration warning appears during
#      the whole run.
#
# Everything it creates is disposable: the database name, the bucket name, the Redis index
# and all three ports are unique to this run, and nothing here touches the development
# `cinema` database or Redis index 0. Cleanup runs on every exit path — the two server
# processes are stopped, the database is dropped, the bucket is emptied and deleted, and
# the Redis logical database this run owned is flushed.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unique="$$"
db="cinema_m35_verify_$unique"
bucket="cinema-m35-verify-$unique"
redis_db="${M35_REDIS_DB:-8}"
s3_endpoint="${M35_S3_ENDPOINT:-http://127.0.0.1:4566}"
s3_endpoint="${s3_endpoint%/}"
admin_token="m35-verify-admin-$unique"
# A key that is not the repository's development default: the QR signer is only ever
# handed a run-scoped secret here.
qr_key="cinema-m35-verification-only-ticket-qr-key-$unique"
tmp="$(mktemp -d)"
checks="$tmp/checks"
api_pid=""
web_pid=""
api_log="$tmp/api.log"
web_log="$tmp/web.log"
artifacts="${TMPDIR:-/tmp}/cinema-m35-verify-$unique-artifacts"
playwright_out="$tmp/playwright"
api_port=""
web_port=""
base=""
web=""
compose="$(if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)"

: >"$checks"

note() { printf '  ...    %s\n' "$*"; }
ok() {
  printf '  [ok]   %s\n' "$*"
  printf '%s\n' "$*" >>"$checks"
}
fail() {
  printf 'M3.5 verification FAILED: %s\n' "$*" >&2
  if [ -s "$checks" ]; then
    printf 'checks that passed before the failure:\n' >&2
    sed 's/^/  [ok]   /' "$checks" >&2
  fi
  exit 1
}

cleanup() {
  local status=$?
  set +e
  local cleanup_log
  cleanup_log="$tmp/cleanup"
  : >"$cleanup_log"

  # The two server processes are stopped before anything they were using is removed, so
  # neither can write to a dropped database or a deleted bucket while it shuts down.
  if [ -n "$web_pid" ]; then
    kill "$web_pid" 2>/dev/null || true
    wait "$web_pid" 2>/dev/null || true
    printf 'the web server is stopped\n' >>"$cleanup_log"
  fi
  if [ -n "$api_pid" ]; then
    kill "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
    printf 'the API process is stopped\n' >>"$cleanup_log"
  fi
  if [ -n "$compose" ] && [ -n "$db" ]; then
    $compose exec -T postgres dropdb -U cinema --if-exists "$db" >/dev/null 2>&1 \
      && printf 'dropped database %s\n' "$db" >>"$cleanup_log"
    # The reservation keys live in a dedicated logical database, so the development
    # keyspace (index 0) is never flushed.
    $compose exec -T redis redis-cli -n "$redis_db" flushdb >/dev/null 2>&1 \
      && printf 'flushed Redis logical database %s\n' "$redis_db" >>"$cleanup_log"
  fi
  # The bucket is emptied and removed, so a failed run leaves no objects behind.
  if curl --silent --fail "$s3_endpoint/$bucket?list-type=2" >"$tmp/cleanup-list.xml" 2>/dev/null; then
    local key
    for key in $(grep -o '<Key>[^<]*</Key>' "$tmp/cleanup-list.xml" | sed 's/<[^>]*>//g'); do
      curl --silent --output /dev/null -X DELETE "$s3_endpoint/$bucket/$key" || true
    done
    if curl --silent --output /dev/null -w '%{http_code}' -X DELETE "$s3_endpoint/$bucket" 2>/dev/null | grep -q '^204$'; then
      printf 'deleted bucket %s (objects first)\n' "$bucket" >>"$cleanup_log"
    else
      printf 'emptied bucket %s but it could not be deleted\n' "$bucket" >>"$cleanup_log"
    fi
  else
    printf 'bucket %s was never created\n' "$bucket" >>"$cleanup_log"
  fi

  # A failed run keeps the browser's own artefacts — the trace, the screenshot, the
  # downloaded PDF — outside the repository, and names the directory that holds them.
  if [ "$status" -ne 0 ] && [ -d "$playwright_out" ]; then
    mkdir -p "$artifacts"
    cp -R "$playwright_out/." "$artifacts/" 2>/dev/null
    printf 'playwright artefacts kept in %s\n' "$artifacts" >&2
    find "$artifacts" -type f -name '*.png' 2>/dev/null | sed 's/^/  screenshot: /' >&2
  fi

  if [ -s "$cleanup_log" ]; then sed 's/^/cleanup: /' "$cleanup_log"; fi
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup EXIT INT TERM

# The names this script drops and deletes are checked before anything is created, so a
# bug in the naming above cannot reach the development database or a real bucket.
case "$db" in cinema_m35_verify_[0-9]*) ;; *) fail "refusing to drop an unexpected database name" ;; esac
[ "$db" != "cinema" ] || fail "refusing to run against the development database"
case "$bucket" in cinema-m35-verify-[0-9]*) ;; *) fail "refusing to create an unexpected bucket name" ;; esac
[ -n "$compose" ] || fail "Docker Compose is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v go >/dev/null 2>&1 || fail "Go is required to build the API and the bootstrap command"
command -v pnpm >/dev/null 2>&1 || fail "pnpm is required to build and serve the web application"
[ -x "$root/apps/web/node_modules/.bin/playwright" ] \
  || fail "Playwright is not installed: run pnpm install, then pnpm --dir apps/web exec playwright install --with-deps chromium"

cd "$root"

# Both ports are chosen rather than hardcoded: a developer machine may already have
# something on the usual ones, and binding them would make the gate fail for a reason
# that has nothing to do with the product.
free_port() { # <first> <last>
  local candidate
  for candidate in $(seq "$1" "$2"); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}
api_port="$(free_port 18182 18260)" || fail "no free API port in 18182-18260"
web_port="$(free_port 18300 18400)" || fail "no free web port in 18300-18400"
base="http://127.0.0.1:$api_port"
web="http://127.0.0.1:$web_port"
dburl="postgres://cinema:cinema_local_only@127.0.0.1:5432/$db?sslmode=disable"
redisurl="redis://127.0.0.1:6379/$redis_db"

psql_db() { $compose exec -T postgres psql -X -q -tA -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"; }

note "isolation: database $db, Redis database $redis_db, API port $api_port, web port $web_port, bucket $bucket"

# ── 1. The three services ────────────────────────────────────────────────────
note "waiting for postgres, redis and floci to report healthy (docker compose up --wait)"
$compose up -d --wait postgres redis floci >/dev/null 2>&1 \
  || fail "the compose dependencies did not become healthy: $($compose ps 2>/dev/null | tr '\n' ' ')"
ok "dependencies: postgres, redis and floci are healthy"

# This script flushes one Redis logical database on cleanup, so it must own it. A
# developer using the same index for something else loses nothing: the run stops before
# touching it.
dbsize="$($compose exec -T redis redis-cli -n "$redis_db" dbsize 2>/dev/null | tr -d '\r' || echo 0)"
case "$dbsize" in
  0|"") ;;
  *) fail "Redis database $redis_db already holds $dbsize keys; set M35_REDIS_DB to a free database" ;;
esac

# ── 2. A throwaway database, migrated and seeded by the project's own commands ──
note "waiting for createdb to answer on the postgres service (database $db)"
$compose exec -T postgres createdb -U cinema "$db" || fail "could not create the throwaway database $db"
versions="$(psql_db -c "SELECT count(*) FROM pg_namespace WHERE nspname IN ('platform','cinema')")"
[ "$versions" = "0" ] || fail "the throwaway database must start with no schema, found $versions schemas"

note "waiting for the project's migration runner to finish on $db (0001 through 0006)"
migration_output="$($compose --profile tools run --rm -e DB_NAME="$db" migrate-all 2>&1)" \
  || { printf '%s\n' "$migration_output" >&2; fail "the migration runner failed on $db"; }
applied_versions="$(psql_db -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations")"
[ "$applied_versions" = "0001_init,0002_venue_layout,0003_reservation,0004_idempotency,0005_checkout,0006_one_open_payment_attempt" ] \
  || fail "the migration runner recorded [$applied_versions], expected 0001_init through 0006_one_open_payment_attempt"
ok "migrations: scripts/migrations/apply_all.sh brought $db to [$applied_versions]"

go build -o "$tmp/api" ./apps/api/cmd/api || fail "the API did not build"
go build -o "$tmp/bootstrap" ./apps/api/cmd/bootstrap || fail "the bootstrap command did not build"

# The seed is the project's own, applied to the throwaway database through the project's
# own command: a cinema, a room, a published layout, two films and two screenings. The
# browser scenarios create everything else through the interface.
note "waiting for the development seed to be applied to $db (cinema, room, published layout, films)"
seed_output="$(DB_NAME="$db" API_ENV=development "$tmp/bootstrap" seed-dev 2>&1)" \
  || { printf '%s\n' "$seed_output" >&2; fail "bootstrap seed-dev failed on $db"; }
seeded_cinemas="$(psql_db -c "SELECT count(*) FROM cinema.cinemas")"
seeded_layouts="$(psql_db -c "SELECT count(*) FROM cinema.layout_versions WHERE status = 'published'")"
[ "$seeded_cinemas" = "1" ] || fail "the seed created $seeded_cinemas cinemas, expected 1"
[ "$seeded_layouts" = "1" ] || fail "the seed published $seeded_layouts layout versions, expected 1"
ok "seed: $seeded_cinemas cinema, $seeded_layouts published layout version, its rooms and films"

note "waiting for bootstrap ensure-bucket to answer the object store (bucket $bucket)"
bucket_output="$(API_S3_ENDPOINT="$s3_endpoint" API_S3_BUCKET="$bucket" "$tmp/bootstrap" ensure-bucket 2>&1)" \
  || { printf '%s\n' "$bucket_output" >&2; fail "bootstrap ensure-bucket failed"; }
case "$bucket_output" in
  *"bucket $bucket is ready"*) ;;
  *) fail "bootstrap ensure-bucket answered '$bucket_output', expected 'bucket $bucket is ready'" ;;
esac
ok "bucket: bootstrap ensure-bucket created $bucket and Floci answers it"

# ── 3. The API ───────────────────────────────────────────────────────────────
# The API runs from the built binary, so the recorded PID is the server itself and
# shutdown cannot leave an orphan holding the port.
API_ENV=development \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN="$admin_token" \
API_DATABASE_URL="$dburl" \
API_REDIS_URL="$redisurl" \
API_ADDR="127.0.0.1:$api_port" \
API_HOLD_TTL=5m \
API_EXPIRY_INTERVAL=10s \
API_EXPIRY_BATCH=10 \
API_S3_ENDPOINT="$s3_endpoint" \
API_S3_BUCKET="$bucket" \
API_S3_PRESIGN_TTL=5m \
API_TICKET_QR_KEY="$qr_key" \
"$tmp/api" >"$api_log" 2>&1 &
api_pid=$!

note "waiting for the API to answer GET /health/ready on $base"
ready=""
for _ in $(seq 1 240); do
  if grep -q 'address already in use' "$api_log" 2>/dev/null; then
    cat "$api_log" >&2
    fail "port $api_port is already in use; stop the process holding it"
  fi
  if curl --fail --silent "$base/health/live" >/dev/null 2>&1 \
    && curl --fail --silent "$base/health/ready" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
[ -n "$ready" ] || { cat "$api_log" >&2; fail "the API did not become ready on $base within 60s"; }

grep -q 'public reservation API mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the public reservation API was not mounted"; }
grep -q 'catalogue and reservation read routes mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the catalogue and the single screening read were not mounted"; }
grep -q 'ticket QR codes are signed; code presentation and verification routes mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not mount the ticket code routes"; }
grep -q 'ticket PDFs are stored in the object bucket; download route mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not mount the ticket PDF route"; }
grep -q 'local administrative venue API mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not mount the local administrative route the administration screen needs"; }
ok "api: ready on $base with the catalogue, checkout, ticket code, PDF and local administration routes mounted"

# ── 4. The web application: built once, then served ──────────────────────────
# `next dev` is deliberately not used: it would rebuild on demand, race the gate over
# `.next`, and show a development overlay the gate would have to ignore. One build,
# served by `next start`, is also what the browser sees in a deployment.
note "waiting for the web application to build (pnpm --dir apps/web build)"
build_log="$tmp/web-build.log"
if ! API_BASE_URL="$base" API_LOCAL_ADMIN_TOKEN="$admin_token" \
    pnpm --dir "$root/apps/web" build >"$build_log" 2>&1; then
  cat "$build_log" >&2
  fail "the web application did not build"
fi

API_BASE_URL="$base" \
API_LOCAL_ADMIN_TOKEN="$admin_token" \
NEXT_TELEMETRY_DISABLED=1 \
pnpm --dir "$root/apps/web" exec next start -H 127.0.0.1 -p "$web_port" >"$web_log" 2>&1 &
web_pid=$!

note "waiting for the web server to answer GET /events on $web (it reads the catalogue, so this waits for the API too)"
web_ready=""
for _ in $(seq 1 240); do
  if grep -q 'address already in use\|EADDRINUSE' "$web_log" 2>/dev/null; then
    cat "$web_log" >&2
    fail "port $web_port is already in use; stop the process holding it"
  fi
  if curl --fail --silent "$web/api/health" >/dev/null 2>&1 \
    && curl --fail --silent "$web/events" >/dev/null 2>&1; then
    web_ready=1
    break
  fi
  sleep 0.5
done
[ -n "$web_ready" ] || { cat "$web_log" >&2; fail "the web server did not answer $web/events within 120s"; }
ok "web: the build is served on $web and GET /events answers 200 against $base"

# ── 5. The browser ───────────────────────────────────────────────────────────
playwright_version="$(pnpm --dir "$root/apps/web" exec playwright --version 2>/dev/null | tr -d '\r')"
[ -n "$playwright_version" ] || fail "playwright --version answered nothing"
chromium_path="$(cd "$root/apps/web" && node -e \
  'const { chromium } = require("@playwright/test"); process.stdout.write(chromium.executablePath())' 2>/dev/null | tr -d '\r')"
[ -n "$chromium_path" ] && [ -x "$chromium_path" ] \
  || fail "the Chromium build Playwright expects is not installed ($chromium_path): run pnpm --dir apps/web exec playwright install --with-deps chromium"
ok "browser: $playwright_version, chromium at $chromium_path"

note "waiting for Playwright to drive Chromium through the administration and the purchase"
set +e
WEB_BASE_URL="$web" \
API_BASE_URL="$base" \
PLAYWRIGHT_OUT="$playwright_out" \
  pnpm --dir "$root/apps/web" exec playwright test \
    --config "$root/apps/web/playwright.config.ts" \
    --project chromium \
    --reporter list 2>&1 | tee "$tmp/playwright.log"
browser_status="${PIPESTATUS[0]}"
set -e

passed="$(grep -E '[0-9]+ passed' "$tmp/playwright.log" | tail -1 | sed 's/^ *//')"
[ "$browser_status" = "0" ] \
  || fail "the browser gate failed (playwright exited $browser_status): the scenarios and the artefacts are above"
ok "playwright: every scenario passed (${passed:-see the reporter output above})"

# The printed summary: what was proven, in one place.
printf '\nM3.5 verification passed: a screening created, priced at 12.50 and put on sale from the administration screens → refused locally when nothing changed and when the price carried a third decimal → the catalogue lists it at the venue hour and 12.50 → two seats chosen with the keyboard alone, held, paid, and a ticket the box office calls valid → its QR image served by the API and its PDF downloaded → the ticket list keeps what this tab opened, forgets a stale record, and distinguishes an empty list from an unreadable one → an unknown identifier answered as unknown, one h1 per screen, and no console error or hydration warning during the whole run.\n'
printf 'what this gate does not prove: nothing about Firefox or WebKit (only Chromium is verified, because what is asserted is behaviour and not cross-browser parity), no real payment provider, no real object-storage credentials, no accessibility audit beyond the keyboard path, the focus ring, the heading count and aria-current, and no test of the mobile viewport beyond the rules the seat map itself applies.\n'
