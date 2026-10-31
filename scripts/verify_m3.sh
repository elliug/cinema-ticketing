#!/usr/bin/env bash
# M3 end-to-end verification: the milestone's promise, proven against the real
# compose services (PostgreSQL, Redis and Floci) and a real API process.
#
# It proves, through the public HTTP interface and, where a claim needs a number the
# API does not serve, through the database that stored the sale (the ticket and
# payment-attempt counts, and the outbox row):
#   1. A run-scoped stack: its own database, its own Redis logical database, its own
#      API port and its own bucket, so it can never touch the development data.
#   2. The project's own migration runner brings that database to every migration on
#      disk, and the bootstrap command creates the bucket the tickets are stored in.
#   3. The purchase: hold two seats, pay them with one Idempotency-Key, and the
#      answer carries an approved payment and one ticket per seat.
#   4. Each ticket answers its own seat, the amount taken for the order, the
#      venue-local screening time and a signed code; the door check accepts that
#      code as a valid ticket, and the code has an SVG image.
#   5. The PDF: the download route redirects to the bucket under the deterministic
#      key, the object is a real PDF, and a second pre-signed link serves the same
#      bytes.
#   6. The guards cooperate: replaying the payment key answers the same tickets and
#      creates none, a tampered code and a genuine code naming an unknown ticket are
#      refused identically, and a second key on the paid reservation is a 409.
#   7. D9: the Floci container is restarted **and then recreated on the same
#      volume**, and the stored artefact still answers through a fresh pre-signed
#      link with the same bytes. A local S3 that loses its data on restart — or that
#      only survives a restart because the process was still warm — would make the
#      milestone's promise false.
#
# Everything it creates is disposable: the database name, the bucket name and the
# Redis index are unique to this run, and nothing here touches the development
# `cinema` database or Redis index 0.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unique="$$"
db="cinema_m3_verify_$unique"
bucket="cinema-m3-verify-$unique"
# A database index of its own, distinct from `make verify-m2`'s default (9), so the two gates
# no longer refuse each other over one shared index: M3_REDIS_DB overrides it.
redis_db="${M3_REDIS_DB:-10}"
s3_endpoint="${M3_S3_ENDPOINT:-http://127.0.0.1:4566}"
s3_endpoint="${s3_endpoint%/}"
admin_token="m3-verify-admin-$unique"
# A key that is not the repository's development default and is long enough for the
# signer. This script mints one code of its own with it, which is how it proves that a
# genuine code naming an unknown ticket is refused like a forged one.
qr_key="cinema-m3-verification-only-ticket-qr-key-$unique"
# Europe/Madrid is on CEST (+02:00) on the seeded date, so the venue-local hour is
# distinct from UTC and a renderer printing the machine's zone fails here.
venue_timezone="Europe/Madrid"
starts_at="2031-05-04T18:30:00Z"
starts_at_local="2031-05-04T20:30:00+02:00"
price_cents=1250
seats_bought=2
expected_amount=$((price_cents * seats_bought))
tmp="$(mktemp -d)"
checks="$tmp/checks"
body="$tmp/body"
headers="$tmp/headers"
api_pid=""
api_log="$tmp/api.log"
compose="$(if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)"

: >"$checks"

note() { printf '  ...    %s\n' "$*"; }
ok() {
  printf '  [ok]   %s\n' "$*"
  printf '%s\n' "$*" >>"$checks"
}
fail() {
  printf 'M3 verification FAILED: %s\n' "$*" >&2
  if [ -s "$checks" ]; then
    printf 'checks that passed before the failure:\n' >&2
    sed 's/^/  [ok]   /' "$checks" >&2
  fi
  exit 1
}
snippet() { head -c 400 "$body" 2>/dev/null | tr '\n' ' '; }

cleanup() {
  local status=$?
  set +e
  local cleanup_log
  cleanup_log="$tmp/cleanup"
  : >"$cleanup_log"

  if [ -n "$api_pid" ]; then
    kill "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
    printf 'the API process is stopped\n' >>"$cleanup_log"
  fi
  $compose exec -T postgres dropdb -U cinema --if-exists "$db" >/dev/null 2>&1 \
    && printf 'dropped database %s\n' "$db" >>"$cleanup_log"
  # The reservation keys live in a dedicated logical database, and it is flushed only when
  # this run claimed it: a run that refused to touch an index must leave it exactly as it
  # found it, refusals included.
  if [ "$redis_owned" = 1 ]; then
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

  if [ -s "$cleanup_log" ]; then sed 's/^/cleanup: /' "$cleanup_log"; fi
  rm -rf "$tmp"
  exit "$status"
}
# The flush below is conditional on this. The guard refuses an index that already holds
# data, and exiting through such a refusal must leave that index exactly as it was found.
redis_owned=0
trap cleanup EXIT INT TERM

case "$db" in cinema_m3_verify_[0-9]*) ;; *) fail "refusing to drop an unexpected database name" ;; esac
case "$bucket" in cinema-m3-verify-[0-9]*) ;; *) fail "refusing to create an unexpected bucket name" ;; esac
[ -n "$compose" ] || fail "Docker Compose is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v openssl >/dev/null 2>&1 || fail "openssl is required to mint the unknown-ticket code"
command -v curl >/dev/null 2>&1 || fail "curl is required"

cd "$root"

# The API port is chosen rather than hardcoded: a developer machine may already have
# something on the usual one, and binding it would make the gate fail for a reason
# that has nothing to do with the product.
free_port() {
  local candidate
  for candidate in $(seq 18082 18160); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}
port="$(free_port)" || fail "no free API port in 18082-18160"
base="http://127.0.0.1:$port"
public="$base/api/v1"
admin="$public/admin"
dburl="postgres://cinema:cinema_local_only@127.0.0.1:5432/$db?sslmode=disable"
redisurl="redis://127.0.0.1:6379/$redis_db"

psql_db() { $compose exec -T postgres psql -X -q -tA -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"; }

# call <method> <url> [curl args...] — sets STATUS, writes $body and $headers.
call() {
  local method="$1" url="$2"
  shift 2
  STATUS="$(curl --silent --show-error --dump-header "$headers" --output "$body" \
    --write-out '%{http_code}' -X "$method" "$@" "$url")" \
    || fail "curl could not complete $method $url"
}
expect_status() {
  local label="$1" want="$2"
  [ "$STATUS" = "$want" ] || fail "$label: answered $STATUS, expected $want — $(snippet)"
}
header() { grep -i "^$1:" "$headers" | tr -d '\r' | cut -d' ' -f2-; }
# jq_assert <label> <filter> [jq args...] — the body must match the filter.
jq_assert() {
  local label="$1" filter="$2"
  shift 2
  jq -e "$filter" "$@" <"$body" >/dev/null 2>&1 \
    || fail "$label: body does not match $filter — $(snippet)"
}
# sign_token <ticketID> <eventID> <seatID> — mints a code the way the service does:
# unpadded URL-safe base64 of the canonical payload and of its HMAC-SHA256.
sign_token() {
  local payload="v1|$1|$2|$3" encoded signature
  encoded="$(printf '%s' "$payload" | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
  signature="$(printf '%s' "$payload" | openssl dgst -sha256 -hmac "$qr_key" -binary \
    | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
  printf '%s.%s' "$encoded" "$signature"
}
# tamper <token> — flips the first character of the signature, keeping the payload.
tamper() {
  local payload="${1%%.*}" signature="${1#*.}" first="${1#*.}"
  first="${first:0:1}"
  if [ "$first" = "A" ]; then first="B"; else first="A"; fi
  printf '%s.%s%s' "$payload" "$first" "${signature:1}"
}

note "isolation: database $db, Redis database $redis_db, API port $port, bucket $bucket"

# Dependencies, then a fresh database migrated by the project's own runner.
$compose up -d --wait postgres redis floci >/dev/null || fail "the compose dependencies did not become healthy"

# This script flushes one Redis logical database on cleanup, so it must own it — and it can
# only claim what it could actually read: an unreadable dbsize is not proof of an empty
# index, and treating it as such is how a refused run destroys data on the way out.
# A developer using the same index for something else loses nothing.
dbsize="$($compose exec -T redis redis-cli -n "$redis_db" dbsize 2>/dev/null | tr -d '\r' || true)"
case "$dbsize" in
  0) redis_owned=1 ;;
  "") fail "could not read the size of Redis logical database $redis_db, so this script cannot prove it owns it; is Redis reachable?" ;;
  *) fail "Redis database $redis_db already holds $dbsize keys; set M3_REDIS_DB to a free database" ;;
esac

$compose exec -T postgres createdb -U cinema "$db" || fail "could not create the throwaway database $db"
versions="$(psql_db -c "SELECT count(*) FROM pg_namespace WHERE nspname IN ('platform','cinema')")"
[ "$versions" = "0" ] || fail "the throwaway database must start with no schema, found $versions schemas"
migration_output="$($compose --profile tools run --rm -e DB_NAME="$db" migrate-all 2>&1)" \
  || { printf '%s\n' "$migration_output" >&2; fail "the migration runner failed on $db"; }
applied_versions="$(psql_db -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations")"
# The expected chain is derived from the migration directory, never enumerated: the
# directory is what the runner mounts, so a version added there cannot disagree with a list
# written down in this script, and no migration has to be edited into it before its own
# database can be verified. The assertion is the same strength as the literal list was —
# every migration on disk is recorded, in order, and nothing else is.
expected_versions=""
for file in "$root"/db/migrations/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in $root/db/migrations"
  name="$(basename "$file" .up.sql)"
  expected_versions="${expected_versions:+$expected_versions,}$name"
done
[ "$applied_versions" = "$expected_versions" ] \
  || fail "the migration runner recorded [$applied_versions], expected [$expected_versions]"
ok "migrations: scripts/migrations/apply_all.sh brought $db to [$applied_versions]"

# The checkout package's gated tests against that same database, one package at a time.
# Nothing in this project used to set M3_CHECKOUT_DATABASE_URL, so every one of these
# tests skipped itself and the gate reported green while a thousand lines of capture
# guards — the one-open-attempt index, the amount and fingerprint rules, the sale writes
# — never ran. A skip is not a pass: the two assertions below are what turn "the suite
# ran and passed" into a claim this gate can make.
M3_CHECKOUT_DATABASE_URL="$dburl" \
  go test -count=1 -p 1 -v ./apps/api/internal/checkout/ 2>&1 | tee "$tmp/checkout-gated.log"
grep -q -- '--- PASS: TestReconcileStaleAttemptsOnPostgres' "$tmp/checkout-gated.log" \
  || fail "the payment reconciliation evidence did not pass against PostgreSQL"
if grep -q -- '--- SKIP' "$tmp/checkout-gated.log"; then
  fail "a gated checkout test skipped itself, so this gate proves less than it claims"
fi
ok "checkout: the gated payment suite passed against $db, and none of it skipped"

# The bucket, through the bootstrap command the project uses (never an ad-hoc PUT):
# this is the same path `make bootstrap` and CI take.
go build -o "$tmp/api" ./apps/api/cmd/api || fail "the API did not build"
go build -o "$tmp/bootstrap" ./apps/api/cmd/bootstrap || fail "the bootstrap command did not build"
bucket_output="$(API_S3_ENDPOINT="$s3_endpoint" API_S3_BUCKET="$bucket" "$tmp/bootstrap" ensure-bucket 2>&1)" \
  || { printf '%s\n' "$bucket_output" >&2; fail "bootstrap ensure-bucket failed"; }
case "$bucket_output" in
  *"bucket $bucket is ready"*) ;;
  *) fail "bootstrap ensure-bucket answered '$bucket_output', expected 'bucket $bucket is ready'" ;;
esac
call GET "$s3_endpoint/$bucket?list-type=2"
[ "$STATUS" = "200" ] || fail "GET $s3_endpoint/$bucket answered $STATUS, so the bucket does not exist"
ok "bucket: bootstrap ensure-bucket created $bucket, and Floci answers it (200, empty)"

# The API runs from the built binary, so the recorded PID is the server itself and
# shutdown cannot leave an orphan holding the port.
API_ENV=development \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN="$admin_token" \
API_DATABASE_URL="$dburl" \
API_REDIS_URL="$redisurl" \
API_ADDR="127.0.0.1:$port" \
API_HOLD_TTL=2m \
API_EXPIRY_INTERVAL=10s \
API_EXPIRY_BATCH=10 \
API_S3_ENDPOINT="$s3_endpoint" \
API_S3_BUCKET="$bucket" \
API_S3_PRESIGN_TTL=5m \
API_TICKET_QR_KEY="$qr_key" \
"$tmp/api" >"$api_log" 2>&1 &
api_pid=$!

# Readiness is waited for, never slept through: the process is asked, and the log is
# read so a port collision is named instead of timing out.
ready=""
for _ in $(seq 1 160); do
  if grep -q 'address already in use' "$api_log" 2>/dev/null; then
    cat "$api_log" >&2
    fail "port $port is already in use; stop the process holding it"
  fi
  if curl --fail --silent "$base/health/live" >/dev/null 2>&1 \
    && curl --fail --silent "$base/health/ready" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
[ -n "$ready" ] || { cat "$api_log" >&2; fail "the API did not become ready on $base within 40s"; }

code_routes=("/api/v1/tickets/{ticketId}/qr.svg" "/api/v1/tickets/verify" "/api/v1/tickets/redeem")

mounted="$(grep -c 'public reservation API mounted' "$api_log" || true)"
[ "$mounted" = "1" ] || { cat "$api_log" >&2; fail "the public reservation API was not mounted"; }
grep -q '"checkout":true,"tickets":true,"ticket_pdfs":true' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not mount the checkout, ticket and PDF surfaces together"; }
# The three code routes, asserted as the paths the API logged rather than as the sentence
# around them: the sentence is operator-facing prose and may be improved, while a route that
# stopped being mounted is what this gate exists to catch. Admission joined the list when the
# door learned to write the redemption.
for code_route in "${code_routes[@]}"; do
  grep -q "\"$code_route\"" "$api_log" \
    || { cat "$api_log" >&2; fail "the API did not mount the ticket code route $code_route"; }
done
grep -q 'ticket PDFs are stored in the object bucket; download route mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not mount the ticket PDF route"; }
ok "api: ready on $base with checkout, ticket detail, QR image, verification and PDF routes mounted"

# The fixture: a cinema, a room, a published layout, a film and a screening that is
# on sale at one price per seat. Everything but the price and the sale state goes
# through the administrative API; those two are set with SQL because the
# administrative surface exposes neither yet (recorded in the task log as a gap the
# gate found, not worked around silently).
call POST "$admin/cinemas" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"name\":\"Cine Verificación M3\",\"timezone\":\"$venue_timezone\",\"address\":\"Calle 3\"}"
expect_status "create cinema" 201
cinema="$(jq -r '.id' <"$body")"
call POST "$admin/rooms" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"cinemaId\":\"$cinema\",\"name\":\"Sala M3\"}"
expect_status "create room" 201
room="$(jq -r '.id' <"$body")"
call POST "$admin/layouts" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"roomId\":\"$room\"}"
expect_status "create layout" 201
layout="$(jq -r '.id' <"$body")"
call PUT "$admin/layouts/$layout/rows" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d '{"rows":[{"label":"A","position":1,"seats":[{"number":"1","position":1,"kind":"standard","enabled":true},{"number":"2","position":2,"kind":"standard","enabled":true}]},{"label":"B","position":2,"seats":[{"number":"1","position":1,"kind":"standard","enabled":true}]}]}'
expect_status "set layout rows" 200
call POST "$admin/layouts/$layout/publish" -H "Authorization: Bearer $admin_token"
expect_status "publish layout" 200
call POST "$admin/films" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d '{"title":"La película verificada","runtimeMinutes":104,"rating":"PG"}'
expect_status "create film" 201
film="$(jq -r '.id' <"$body")"
call POST "$admin/events" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"roomId\":\"$room\",\"filmId\":\"$film\",\"layoutVersionId\":\"$layout\",\"startsAt\":\"$starts_at\"}"
expect_status "create event" 201
event="$(jq -r '.id' <"$body")"
psql_db -c "UPDATE cinema.events SET price_cents = $price_cents, status = 'on_sale' WHERE id = '$event'" >/dev/null \
  || fail "could not put the event on sale at $price_cents cents"
stored_price="$(psql_db -c "SELECT price_cents FROM cinema.events WHERE id = '$event'")"
[ "$stored_price" = "$price_cents" ] || fail "the event price is $stored_price, expected $price_cents"
stored_status="$(psql_db -c "SELECT status FROM cinema.events WHERE id = '$event'")"
[ "$stored_status" = "on_sale" ] || fail "the event status is '$stored_status', expected 'on_sale'"
layout_status="$(psql_db -c "SELECT status FROM cinema.layout_versions WHERE id = '$layout'")"
[ "$layout_status" = "published" ] || fail "the layout version is '$layout_status', expected 'published'"

call GET "$admin/events/$event/seats" -H "Authorization: Bearer $admin_token"
expect_status "read event seats" 200
seat_count="$(jq -r '.seats | length' <"$body")"
[ "$seat_count" = "3" ] || fail "the event materialized $seat_count seats, expected 3"
seat_one="$(jq -r '.seats[0].id' <"$body")"
seat_two="$(jq -r '.seats[1].id' <"$body")"
[ "$seat_one" != "$seat_two" ] || fail "the fixture selected one seat twice"
ok "fixture: cinema/room/published layout/film/event on sale, price_cents=$price_cents, event seats [$seat_one $seat_two] of $seat_count"

# The purchase, over HTTP only: a hold, then one payment for both seats.
payment_key="m3-verify-payment-$unique"
call POST "$public/reservations/holds" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: m3-verify-hold-$unique" \
  -d "{\"eventId\":\"$event\",\"seatIds\":[\"$seat_one\",\"$seat_two\"]}"
expect_status "create hold" 201
reservation="$(jq -r '.id' <"$body")"
[ -n "$reservation" ] && [ "$reservation" != "null" ] || fail "the hold answered no reservation id — $(snippet)"

call POST "$public/reservations/$reservation/payment" -H "Idempotency-Key: $payment_key"
expect_status "pay the held reservation" 200
jq_assert "pay the held reservation" '.paymentStatus == "approved"'
jq_assert "pay the held reservation" --argjson amount "$expected_amount" '.amountCents == $amount'
jq_assert "pay the held reservation" '.ticketIds | length == 2'
tickets="$(jq -r '.ticketIds[]' <"$body")"
ticket_one="$(printf '%s\n' "$tickets" | sed -n 1p)"
ticket_two="$(printf '%s\n' "$tickets" | sed -n 2p)"
[ -n "$ticket_one" ] && [ -n "$ticket_two" ] && [ "$ticket_one" != "$ticket_two" ] \
  || fail "the payment answered tickets [$ticket_one] [$ticket_two], expected two distinct ids"
ok "purchase: hold $reservation, payment approved for $expected_amount cents, tickets [$ticket_one $ticket_two]"

# Each ticket answers its own seat, the money taken for the order and the venue's
# local hour, with a signed code.
detail_for() { # <ticketID> <expected seat id> <label>
  call GET "$public/tickets/$1"
  expect_status "$3" 200
  local ticket="$1"
  jq_assert "$3" --arg ticket "$ticket" '.ticketId == $ticket'
  jq_assert "$3" --arg reservation "$reservation" '.reservationId == $reservation'
  jq_assert "$3" --argjson amount "$expected_amount" '.orderAmountCents == $amount'
  jq_assert "$3" '.status == "issued" and .valid == true'
  jq_assert "$3" --argjson seat "$2" '(.seats | length) == 1 and .seats[0].eventSeatId == $seat'
  jq_assert "$3" --arg local "$starts_at_local" '.event.startsAtLocal == $local'
  jq_assert "$3" --arg film "La película verificada" '.event.filmTitle == $film'
  jq_assert "$3" '(.qrToken | length) > 0'
}
detail_for "$ticket_one" "$seat_one" "ticket $ticket_one"
token_one="$(jq -r '.qrToken' <"$body")"
seat_label="$(jq -r '.seats[0].rowLabel + .seats[0].seatNumber' <"$body")"
detail_for "$ticket_two" "$seat_two" "ticket $ticket_two"
token_two="$(jq -r '.qrToken' <"$body")"
[ "$token_one" != "$token_two" ] || fail "both tickets answered the same qrToken"
ok "tickets: each answers its own seat (row $seat_label for the first), order $expected_amount cents, startsAtLocal $starts_at_local, a qrToken each"

# The door check accepts the codes the tickets carry.
verify_code() { # <token> <ticket id>
  local label="verify ticket $2"
  call POST "$public/tickets/verify" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg token "$1" '{token: $token}')"
  expect_status "$label" 200
  jq_assert "$label" --arg ticket "$2" '.ticketId == $ticket'
  jq_assert "$label" --arg event "$event" '.eventId == $event'
  jq_assert "$label" '.valid == true and .ticketStatus == "issued"'
  jq_assert "$label" 'has("reason") | not'
}
verify_code "$token_one" "$ticket_one"
verify_code "$token_two" "$ticket_two"
ok "verify: POST /tickets/verify answers valid:true for both served codes, with no reason"

# The code as an image: the same token the page shows, served uncached, identical on
# a second read because the picture is a pure function of the code.
call GET "$public/tickets/$ticket_one/qr.svg"
expect_status "qr.svg" 200
[ "$(header 'content-type')" = "image/svg+xml" ] \
  || fail "qr.svg answered Content-Type '$(header 'content-type')', expected image/svg+xml"
[ "$(header 'cache-control')" = "no-store" ] \
  || fail "qr.svg answered Cache-Control '$(header 'cache-control')', expected no-store"
case "$(head -c 200 "$body")" in
  *"<svg"*) ;;
  *) fail "qr.svg did not answer an SVG document — $(snippet)" ;;
esac
cp "$body" "$tmp/qr-one.svg"
call GET "$public/tickets/$ticket_one/qr.svg"
expect_status "qr.svg (second read)" 200
cmp -s "$tmp/qr-one.svg" "$body" || fail "two reads of /tickets/$ticket_one/qr.svg answered different bytes"
ok "qr.svg: 200 image/svg+xml no-store, $(wc -c <"$body" | tr -d ' ') bytes, byte-identical on a second read"

# The PDF: a redirect to the bucket under the deterministic key, a real document, and
# a second pre-signed link serving the same bytes.
pdf_key="tickets/$event/$ticket_one.pdf"
call GET "$public/tickets/$ticket_one/pdf"
expect_status "pdf redirect" 302
[ ! -s "$body" ] || fail "the PDF redirect answered a body of $(wc -c <"$body" | tr -d ' ') bytes; it must carry none"
[ "$(header 'cache-control')" = "no-store" ] \
  || fail "the PDF redirect answered Cache-Control '$(header 'cache-control')', expected no-store"
location="$(header 'location')"
[ -n "$location" ] || fail "the PDF redirect answered no Location header"
case "$location" in
  *"/$bucket/$pdf_key?X-Amz-"*) ;;
  *) fail "the redirect points at '$location', which is not the deterministic key /$bucket/$pdf_key" ;;
esac
printf '%s' "$location" | grep -q 'X-Amz-Signature=' \
  || fail "the redirect Location carries no signature: $location"

fetch_pdf() { # <location> <output path> <label>
  local status
  status="$(curl --silent --show-error --dump-header "$2.headers" --output "$2" \
    --write-out '%{http_code}' "$1")" \
    || fail "$3: curl could not download the pre-signed URL"
  [ "$status" = "200" ] || fail "$3: the pre-signed URL answered $status, expected 200"
  [ -s "$2" ] || fail "$3: the pre-signed URL answered an empty document"
}
fetch_pdf "$location" "$tmp/pdf-one.pdf" "pdf download"
header_bytes="$(head -c 5 "$tmp/pdf-one.pdf")"
[ "$header_bytes" = "%PDF-" ] \
  || fail "the downloaded artefact starts with '$header_bytes', expected the %PDF- header"
pdf_size="$(wc -c <"$tmp/pdf-one.pdf" | tr -d ' ')"
[ "$pdf_size" -gt 1000 ] \
  || fail "the downloaded PDF is $pdf_size bytes, which is too small to be the ticket document"
# The stored object is served with the content type the archive wrote.
stored_type="$(grep -i '^content-type:' "$tmp/pdf-one.pdf.headers" | tr -d '\r' | cut -d' ' -f2-)"
[ "$stored_type" = "application/pdf" ] \
  || fail "the stored object answered Content-Type '$stored_type', expected application/pdf"
ok "pdf: 302 no-store to /$bucket/$pdf_key, %PDF- document of $pdf_size bytes, application/pdf"

# A second fresh link serves the same bytes: the artefact is immutable, not re-rendered.
call GET "$public/tickets/$ticket_one/pdf"
expect_status "pdf redirect (second link)" 302
second_location="$(header 'location')"
[ -n "$second_location" ] || fail "the second download answered no Location header"
# A signature is minted for the request, but two requests inside one second sign the
# same bytes, so the link is compared by what it serves rather than by its spelling.
printf '%s' "$second_location" | grep -q 'X-Amz-Signature=' \
  || fail "the second download Location carries no signature: $second_location"
fetch_pdf "$second_location" "$tmp/pdf-one-again.pdf" "pdf download (second link)"
cmp -s "$tmp/pdf-one.pdf" "$tmp/pdf-one-again.pdf" \
  || fail "two pre-signed links served different bytes for /$bucket/$pdf_key"
ok "pdf: a second pre-signed link serves identical bytes ($pdf_size bytes)"

# The second ticket has its own document, under its own key: one ticket per seat.
call GET "$public/tickets/$ticket_two/pdf"
expect_status "pdf redirect (second ticket)" 302
second_ticket_key="tickets/$event/$ticket_two.pdf"
case "$(header 'location')" in
  *"/$bucket/$second_ticket_key?X-Amz-"*) ;;
  *) fail "the second ticket redirects to '$(header 'location')', which is not /$bucket/$second_ticket_key" ;;
esac
fetch_pdf "$(header 'location')" "$tmp/pdf-two.pdf" "pdf download (second ticket)"
[ "$(head -c 5 "$tmp/pdf-two.pdf")" = "%PDF-" ] || fail "the second ticket's PDF has no %PDF- header"
compound="$(cmp -s "$tmp/pdf-one.pdf" "$tmp/pdf-two.pdf" && echo same || echo different)"
[ "$compound" = "different" ] \
  || fail "both tickets produced byte-identical PDFs, so the seat is not in the document"
ok "pdf: the second ticket has its own object /$bucket/$second_ticket_key with its own document"

# The outbox row the payment wrote is marked published once the artefact exists, which
# is what makes a worker restart cheap (L12).
published=""
for _ in $(seq 1 60); do
  published="$(psql_db -c "SELECT count(*) FILTER (WHERE published_at IS NOT NULL) || ':' || count(*) FILTER (WHERE published_at IS NULL) FROM cinema.outbox WHERE topic = 'ticket.issued'")"
  [ "$published" = "1:0" ] && break
  sleep 0.5
done
[ "$published" = "1:0" ] \
  || fail "the ticket.issued outbox row is not published (published:unpublished = $published)"
ok "outbox: the ticket.issued row is published (1 published, 0 pending)"

# The guards, against the running system. Counts are read from the database before and
# after each attempt, so "no second charge" and "no new ticket" are measured, not
# assumed.
counts() { psql_db -c "SELECT (SELECT count(*) FROM cinema.tickets WHERE event_id = '$event') || ':' || (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id = '$reservation')"; }
before_counts="$(counts)"

call POST "$public/reservations/$reservation/payment" -H "Idempotency-Key: $payment_key"
expect_status "replay the payment key" 200
jq_assert "replay the payment key" '.paymentStatus == "approved"'
jq_assert "replay the payment key" --arg a "$ticket_one" --arg b "$ticket_two" \
  '(.ticketIds | sort) == ([$a, $b] | sort)'
after_counts="$(counts)"
[ "$before_counts" = "$after_counts" ] \
  || fail "replaying the payment key changed tickets:attempts from $before_counts to $after_counts"
ok "guards: replaying the payment key answered the same tickets and created none (tickets:attempts stayed $after_counts)"

# A correctly signed code for a ticket that does not exist, and a tampered copy of a
# real code: one refusal, byte for byte, so probing cannot confirm an identifier.
unknown_token="$(sign_token "11111111-1111-4111-8111-111111111111" "$event" "$seat_one")"
call POST "$public/tickets/verify" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg token "$unknown_token" '{token: $token}')"
expect_status "unknown ticket code" 400
jq_assert "unknown ticket code" '.code == "invalid_token"'
cp "$body" "$tmp/refusal-unknown.json"
jq_assert "unknown ticket code" 'keys == ["code","detail","status","title","type"]'

tampered="$(tamper "$token_one")"
call POST "$public/tickets/verify" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg token "$tampered" '{token: $token}')"
expect_status "tampered code" 400
jq_assert "tampered code" '.code == "invalid_token"'
cmp -s "$tmp/refusal-unknown.json" "$body" \
  || fail "a tampered code and a genuine code naming an unknown ticket are refused differently: $(snippet)"
ok "guards: a tampered code and a genuine code naming an unknown ticket are both 400 invalid_token with identical bodies"

# A second key on the already-paid reservation: a conflict, never a second charge.
call POST "$public/reservations/$reservation/payment" -H "Idempotency-Key: m3-verify-second-key-$unique"
expect_status "second payment key" 409
jq_assert "second payment key" '.code == "already_paid"'
second_counts="$(counts)"
[ "$before_counts" = "$second_counts" ] \
  || fail "a second payment key changed tickets:attempts from $before_counts to $second_counts"
ok "guards: a second Idempotency-Key on the paid reservation is 409 already_paid, and no ticket or attempt was added"

# D9: Floci persists its S3 data. Two things are asked, because only the second
# separates real persistence from a lucky in-process restart: the artefact must
# still be there after `docker compose restart floci`, and it must still be there
# after the container is *recreated* on the same volume (`up --force-recreate`,
# which is what a `down` followed by an `up` does when it keeps the volume). A
# local S3 that loses objects on restart would make the milestone's promise false.
wait_for_floci() { # <what happened>
  local ready=""
  for _ in $(seq 1 120); do
    if curl --fail --silent "$s3_endpoint/_floci/health" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 0.5
  done
  [ -n "$ready" ] || fail "the floci health endpoint did not answer after $1"
}

# d9_check <what happened> <output path> — the API must sign a fresh link and the
# object must still be the bytes that were stored. Both are retried against the
# service's own cold start, and the last observation is what a failure reports.
d9_check() { # <label> <output path>
  local label="$1" output="$2" status="" signed=""
  for _ in $(seq 1 60); do
    call GET "$public/tickets/$ticket_one/pdf"
    status="$STATUS"
    if [ "$status" = "302" ]; then
      signed="$(header 'location')"
      if [ -n "$signed" ] && curl --silent --fail --output "$output" "$signed"; then
        break
      fi
    fi
    sleep 0.5
  done
  [ "$status" = "302" ] \
    || fail "$label, GET $public/tickets/$ticket_one/pdf answered $status, expected 302 (the bucket $bucket at $s3_endpoint answers $(curl --silent --output /dev/null --write-out '%{http_code}' "$s3_endpoint/$bucket?list-type=2" || echo unreachable), so the artefact did not survive"
  [ -s "$output" ] || fail "$label, the fresh pre-signed link downloaded nothing ($signed)"
  cmp -s "$tmp/pdf-one.pdf" "$output" \
    || fail "$label, the artefact at /$bucket/$pdf_key is not the bytes that were stored ($(wc -c <"$output" | tr -d ' ') bytes instead of $pdf_size)"
}

note "D9: restarting the floci container"
$compose restart floci >/dev/null 2>&1 || fail "could not restart the floci container"
wait_for_floci "the restart"
d9_check "after restarting floci" "$tmp/pdf-after-restart.pdf"
ok "D9: after restarting floci, a fresh pre-signed link served the same $pdf_size bytes from /$bucket/$pdf_key"

note "D9: recreating the floci container on the same volume"
before_container="$($compose ps -q floci 2>/dev/null | tr -d '\r')"
[ -n "$before_container" ] || fail "could not identify the running floci container"
$compose up -d --force-recreate --wait floci >/dev/null 2>&1 \
  || fail "could not recreate the floci container on its volume"
after_container="$($compose ps -q floci 2>/dev/null | tr -d '\r')"
[ -n "$after_container" ] || fail "the recreated floci container is not running"
[ "$before_container" != "$after_container" ] \
  || fail "the floci container was not recreated (still $after_container), so it says nothing about the volume"
wait_for_floci "the container recreation"
d9_check "after recreating floci" "$tmp/pdf-after-recreate.pdf"
ok "D9: the container was recreated ($before_container -> $after_container) and a fresh pre-signed link still served the same $pdf_size bytes from the volume"

# The printed summary: what was proven, in one place.
printf '\nM3 verification passed: hold → pay → one ticket per seat with its own seat, order amount and venue-local time → the signed code verifies and renders as an SVG → the PDF is stored under its deterministic key and served by pre-signed redirect → the payment replay and the second key create nothing → a tampered code and an unknown ticket are refused identically → floci kept the artefact across a restart and a container recreation.\n'
printf 'what this gate does not prove: the ticket was not redeemed at a door (M4), no email was sent (M3 has no delivery channel), the PDF is not re-rendered when the sale changes (it is deliberately immutable), and no real payment provider or real S3 credentials were involved.\n'
