#!/usr/bin/env bash
# Guest identity end-to-end verification: the login, proven through the public surface.
#
# This is the last gate of the identity feature and the only one that exercises the whole of it
# at once. The API's own tests prove each half against real PostgreSQL; what they cannot prove is
# that the two halves are wired to each other in a running process — that the link a stranger
# asks for is the link a mailbox receives, that the token in that link is the token a browser
# will hold, and that the list of "my tickets" is read by the account the mail just verified.
# That is what this gate is for, and it is the whole reason T9 exists:
#   1. A run-scoped stack: its own database, its own Redis logical database, its own API port and
#      its own bucket, so it can never touch the development data. The scope is enforced, not
#      assumed: it refuses to run against the `cinema` development database by name, against
#      Redis logical database 0 (the development keyspace), and against a Redis index that
#      already holds keys, because it drops the one and flushes the other. Mailpit is the one
#      shared service it starts on purpose — the login is mail — and it stops it on the way out
#      only when this run is what started it.
#   2. The link: POST /api/v1/identity/links with an address answers **202**, and within the
#      sender's budget exactly one message reaches that address whose body carries
#      `<origin>/in/<token>`. Exactly one, never "at least one": the address also receives the
#      purchase's own ticket message, so what is counted is the messages that carry a sign-in
#      link and not the messages at all.
#   3. The redemption: POST /api/v1/identity/sessions with that token answers **201** with a
#      token and an expiry, and — checked in the database, because no route serves it —
#      `cinema.guest_customers` holds that address with `verified_at` set, and `cinema.sessions`
#      holds one row whose `token_hash` is the SHA-256 of the token that was returned. That
#      equality is the property S1 exists for: the credential is the client's, and a dump of
#      the table is not a set of live sessions.
#   4. The account's list (S5, D5): the sale is paid with that address *before* the redemption,
#      exactly as verify_email.sh pays one. After the redemption claims it, GET
#      /api/v1/me/tickets with the returned token answers exactly that order's ticket ids — and
#      the same call without a token, and with a random one, answers the login route's own
#      opaque 401, byte for byte the same body.
#   5. Single use (S2): redeeming the same link a second time answers that same 401 and writes no
#      second session row for the account.
#   6. The limit (S6): three sign-in links per address per window. The allowance is spent in the
#      open — after the link this run redeemed, two more asks are admitted and do mail — and then
#      four more asks answer **202 with the same body** while Mailpit gains nothing, because the
#      refusal belongs to the operator's log and not to the caller's answer.
#   7. Revocation (S1), where no route exists yet: `revoked_at` is set with SQL for the session
#      this run created, and GET /me/tickets with that token answers the same opaque 401. The
#      product has no sign-out route, so this proves the *predicate* the row decides — a revoked
#      session is not a session — and not a flow, and the header below says so.
#
# What this gate cannot prove is printed at the end, in the same place and the same voice as the
# other gates': a browser (the cookie Next writes is not exercised here), a real mail provider, a
# sign-out *route*, an expired link or an expired session waited out, and anything about who
# controls the mailbox.
#
# Everything it creates is disposable: the database name, the bucket name and the Redis index are
# unique to this run, the address it mails to carries this run's process id, and nothing here
# touches the development `cinema` database or Redis index 0.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unique="$$"
db="cinema_identity_verify_$unique"
bucket="cinema-identity-verify-$unique"
# Its own Redis logical database: 8, 9, 10, 11 and 12 belong to `verify-m35`, `verify-m2`,
# `verify-m3`, `verify-m41` and `verify-email`, so this one is 13 and IDENTITY_REDIS_DB
# overrides it. The sign-in counter lives here, so a shared index would make two gates count one
# address's allowance against each other.
redis_db="${IDENTITY_REDIS_DB:-13}"
s3_endpoint="${IDENTITY_S3_ENDPOINT:-http://127.0.0.1:4566}"
s3_endpoint="${s3_endpoint%/}"
# Mailpit's ports come from the same compose variables the service publishes them on, so a
# developer who moved them gets a gate that follows instead of one that dials a port nobody
# listens on.
smtp_addr="127.0.0.1:${MAILPIT_SMTP_PORT:-1025}"
mailpit_api="http://127.0.0.1:${MAILPIT_UI_PORT:-8025}"
admin_token="identity-verify-admin-$unique"
# Three keys that are not the repository's development defaults, and that differ from each
# other: the QR signer, the ticket link signer and the access link signer are handed run-scoped
# secrets here, the way a deployment would, so no gate can be green because of a key it shares.
qr_key="cinema-identity-verification-only-ticket-qr-key-$unique"
ticket_link_key="cinema-identity-verification-only-ticket-link-key-$unique"
access_link_key="cinema-identity-verification-only-access-link-key-$unique"
email_from="cinema-identity-verify@example.test"
# The origin the mailed link is built from. It is a name that resolves nowhere on purpose: the
# gate never opens the web page, it asserts that the link the person receives points at the
# configured origin under /in/, which is the contract between the API and the web.
public_web_url="https://tickets.example.test"
link_prefix="$public_web_url/in/"
# The one address, this run's own, so a mailbox somebody else has been filling cannot be
# mistaken for this run's and a leftover of this run cannot be mistaken for somebody else's.
# `example.test` is reserved for documentation and can never receive real mail.
address="verify-identity-$unique@example.test"
# The email consumer's cadence, as `emailWorkerInterval` in apps/api/cmd/api states it. The
# purchase's message is delivered by that consumer, so the budget below is counted in ticks of
# this length rather than in a sleep with a comment, and a reader can check the arithmetic
# against the constant.
worker_tick_seconds=5
purchase_wait_seconds=$((worker_tick_seconds * 6))
# The sign-in link is not queued: the issuer sends it inside the request that asked for it, so
# this bound only decides how long a broken sender may take to fail this gate by name.
link_wait_seconds=30
# After the four refused asks, a few seconds of slack for the SMTP handshake and Mailpit's own
# write, so "the mailbox did not grow" is a measurement and not a reading taken too early.
settle_seconds=3
venue_timezone="Europe/Madrid"
starts_at="2031-05-04T18:30:00Z"
price_cents=1250
seats_bought=2
expected_amount=$((price_cents * seats_bought))
tmp="$(mktemp -d)"
checks="$tmp/checks"
body="$tmp/body"
api_pid=""
api_log="$tmp/api.log"
compose="$(if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)"
# Whether this run took something it may give back. All stay 0 until the run owns what cleanup
# would otherwise remove, so a refusal never deletes somebody else's keys, bucket or container —
# and never reports having removed something it never created.
redis_owned=0
bucket_created=0
db_created=0
mailpit_started=0
mailpit_considered=0

: >"$checks"

note() { printf '  ...    %s\n' "$*"; }
ok() {
  printf '  [ok]   %s\n' "$*"
  printf '%s\n' "$*" >>"$checks"
}
fail() {
  printf 'identity verification FAILED: %s\n' "$*" >&2
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
  if [ -n "$compose" ] && [ "$db_created" = 1 ]; then
    $compose exec -T postgres dropdb -U cinema --if-exists "$db" >/dev/null 2>&1 \
      && printf 'dropped database %s\n' "$db" >>"$cleanup_log"
  fi
  # The sign-in counter lives in Redis, in a logical database of its own, so the development
  # keyspace (index 0) is never flushed — and neither is any index this run did not take empty,
  # which is what makes a refused run harmless.
  if [ "$redis_owned" = 1 ] && [ -n "$compose" ]; then
    $compose exec -T redis redis-cli -n "$redis_db" flushdb >/dev/null 2>&1 \
      && printf 'flushed Redis logical database %s\n' "$redis_db" >>"$cleanup_log"
  fi
  # The bucket is emptied and removed, so a failed run leaves no objects behind — but only the
  # bucket this run created is touched, never one that was already there.
  if [ "$bucket_created" = 1 ] \
    && curl --silent --fail --max-time 10 "$s3_endpoint/$bucket?list-type=2" >"$tmp/cleanup-list.xml" 2>/dev/null; then
    local key
    for key in $(grep -o '<Key>[^<]*</Key>' "$tmp/cleanup-list.xml" | sed 's/<[^>]*>//g'); do
      curl --silent --output /dev/null -X DELETE "$s3_endpoint/$bucket/$key" || true
    done
    if curl --silent --output /dev/null --max-time 10 -w '%{http_code}' -X DELETE "$s3_endpoint/$bucket" 2>/dev/null | grep -q '^204$'; then
      printf 'deleted bucket %s (objects first)\n' "$bucket" >>"$cleanup_log"
    else
      printf 'emptied bucket %s but it could not be deleted\n' "$bucket" >>"$cleanup_log"
    fi
  fi
  # Mailpit is the only shared service this gate starts, and it gives back only what it took. A
  # Mailpit that was already running belongs to the developer: stopping it would close somebody
  # else's inbox mid-session, so it is left exactly as it was found. Both lines are only reached
  # by a run that got as far as deciding, so a refusal before the stack exists does not pretend
  # to have an opinion about a container.
  if [ -n "$compose" ] && [ "$mailpit_considered" = 1 ]; then
    if [ "$mailpit_started" = 1 ]; then
      $compose --profile mail stop mailpit >/dev/null 2>&1 \
        && printf 'stopped the Mailpit container this run started\n' >>"$cleanup_log"
    else
      printf 'the Mailpit container was already running before this run, so it was left running and nothing was purged from its inbox\n' >>"$cleanup_log"
    fi
  fi

  if [ -s "$cleanup_log" ]; then sed 's/^/cleanup: /' "$cleanup_log"; fi
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup EXIT INT TERM

# The names this script drops and deletes are checked before anything is created, so a bug in the
# naming above cannot reach the development database or a real bucket.
case "$db" in
  cinema | postgres | template0 | template1)
    fail "refusing to run against the development \`cinema\` database: this gate creates and drops its own, and every name it uses ends in this run's process id (cinema_identity_verify_<pid>)" ;;
  cinema_identity_verify_[0-9]*) ;;
  *) fail "refusing to drop an unexpected database name '$db'" ;;
esac
case "$bucket" in cinema-identity-verify-[0-9]*) ;; *) fail "refusing to create an unexpected bucket name '$bucket'" ;; esac
case "$redis_db" in
  0) fail "refusing to run: IDENTITY_REDIS_DB=0 is the development Redis logical database, which this gate flushes on cleanup; set IDENTITY_REDIS_DB to a free index other than 0 (the default is 13)" ;;
  "" | *[!0123456789]*) fail "refusing to run: IDENTITY_REDIS_DB='$redis_db' is not a Redis logical database index; set it to a free index other than 0 (the default is 13)" ;;
  *) ;;
esac
[ -n "$compose" ] || fail "Docker Compose is required"
command -v go >/dev/null 2>&1 || fail "Go is required to build the API and the bootstrap command"
command -v jq >/dev/null 2>&1 || fail "jq is required to read the API's and Mailpit's answers"
command -v curl >/dev/null 2>&1 || fail "curl is required"

cd "$root"

# The API port is chosen rather than hardcoded: a developer machine may already have something
# on the usual one, and binding it would make the gate fail for a reason that has nothing to do
# with the product.
free_port() {
  local candidate
  for candidate in $(seq 18602 18680); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}
port="$(free_port)" || fail "no free API port in 18602-18680"
base="http://127.0.0.1:$port"
public="$base/api/v1"
admin="$public/admin"
dburl="postgres://cinema:cinema_local_only@127.0.0.1:5432/$db?sslmode=disable"
redisurl="redis://127.0.0.1:6379/$redis_db"

psql_db() { $compose exec -T postgres psql -X -q -tA -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"; }

# Every request in this gate is bounded. A provider or an API that stops answering must fail this
# gate in seconds, naming what it was asked, instead of idling until the CI job's own timeout
# decides for us: a hung stack would then look like a slow gate.
curl_max_time=30
curl_connect_timeout=5

# call <method> <url> [curl args...] — sets STATUS, writes $body.
call() {
  local method="$1" url="$2"
  shift 2
  STATUS="$(curl --silent --show-error --max-time "$curl_max_time" \
    --connect-timeout "$curl_connect_timeout" \
    --output "$body" --write-out '%{http_code}' -X "$method" "$@" "$url")" \
    || fail "curl could not complete $method $url within ${curl_max_time}s"
}
expect_status() {
  local label="$1" want="$2"
  [ "$STATUS" = "$want" ] || fail "$label: answered $STATUS, expected $want — $(snippet)"
}
# jq_assert <label> <filter> [jq args...] — the body must match the filter.
jq_assert() {
  local label="$1" filter="$2"
  shift 2
  jq -e "$filter" "$@" <"$body" >/dev/null 2>&1 \
    || fail "$label: body does not match $filter — $(snippet)"
}

# Mailpit's own API is the mailbox this gate measures, and two details decide whether a number
# from it means what the gate claims. It reads `messages_count` and never `total`: Mailpit's
# search answers `total` as the size of the whole mailbox, so asserting on it would pass with
# somebody else's mail present and fail with none of ours. And it searches instead of listing,
# because `GET /api/v1/messages` in this version takes no query at all — a `search` parameter
# there is silently ignored, which is exactly the kind of green reading this gate exists to
# refuse. The address is quoted into the query, so a character in it cannot rewrite the filter.
search_address() { # <address> — writes Mailpit's search answer to $tmp/search.json
  curl --silent --show-error --max-time "$curl_max_time" --connect-timeout "$curl_connect_timeout" \
    --get --data-urlencode "query=to:\"$1\"" "$mailpit_api/api/v1/search" >"$tmp/search.json" \
    || fail "Mailpit did not answer its search API at $mailpit_api/api/v1/search"
  jq -e 'has("messages_count")' <"$tmp/search.json" >/dev/null 2>&1 \
    || fail "Mailpit's search answered no messages_count — $(head -c 300 "$tmp/search.json" | tr '\n' ' ')"
}
# mail_count <address> — how many messages Mailpit holds for one address, of any kind.
mail_count() {
  search_address "$1"
  jq -r '.messages_count' <"$tmp/search.json"
}
# fetch_message <id> — writes one message's full document to $body.
fetch_message() {
  curl --silent --show-error --max-time "$curl_max_time" --connect-timeout "$curl_connect_timeout" \
    "$mailpit_api/api/v1/message/$1" >"$body" \
    || fail "Mailpit did not answer the full message $1"
}
message_text() { jq -r '.Text // ""' <"$body"; }
# messages_with_link — the ids of the address's messages in the last search whose body carries a
# sign-in link, one per line. It reads $tmp/search.json and takes no argument: the caller ran the
# search, and a second search here could answer about a different mailbox than the caller asked
# about.
#
# By body and not by address, because this gate mails the same address twice for two different
# reasons: the sale mails its ticket (`<origin>/t/<token>`) and the login mails its link
# (`<origin>/in/<token>`). Counting messages to the address would count both and neither
# assertion would mean anything.
messages_with_link() {
  local id
  for id in $(jq -r '.messages[]?.ID' <"$tmp/search.json"); do
    fetch_message "$id"
    if message_text | grep -qF "$link_prefix"; then printf '%s\n' "$id"; fi
  done
}
# link_mail_count <address> — how many sign-in links Mailpit holds for that address.
link_mail_count() {
  search_address "$1"
  messages_with_link | grep -c . || true
}
# wait_for_mail <address> <expected total> <label> — poll until Mailpit holds exactly that many
# messages for the address, and report the last count it saw when it never does.
wait_for_mail() { # <address> <expected count> <label> <budget seconds>
  local seen="" attempt
  for attempt in $(seq 1 "$4"); do
    seen="$(mail_count "$1")"
    [ "$seen" = "$2" ] && return 0
    sleep 1
  done
  fail "$3: Mailpit holds $seen message(s) for $1, expected $2, after ${4}s"
}
# wait_for_links <address> <expected link count> <label>
wait_for_links() { # <address> <expected count> <label>
  local seen="" attempt
  for attempt in $(seq 1 "$link_wait_seconds"); do
    seen="$(link_mail_count "$1")"
    [ "$seen" = "$2" ] && return 0
    sleep 1
  done
  fail "$3: Mailpit holds $seen message(s) carrying $link_prefix for $1, expected $2, after ${link_wait_seconds}s (the issuer sends the link inside the request that asked for it, so this is not a queue)"
}

note "isolation: database $db, Redis database $redis_db, API port $port, bucket $bucket, Mailpit SMTP $smtp_addr and inbox $mailpit_api"

# Dependencies first, and Mailpit with them: the login is mail, so a mail server is not optional
# here — it is the environment the claim is made in. Whether this run started it is recorded
# before it is started, because that is what decides whether cleanup may stop it.
$compose up -d --wait postgres redis floci >/dev/null || fail "the compose dependencies did not become healthy"
mailpit_considered=1
if [ -n "$($compose ps -q mailpit 2>/dev/null | tr -d '\r')" ]; then
  note "Mailpit was already running; this run will use it and leave it running"
else
  $compose --profile mail up -d --wait mailpit >/dev/null \
    || fail "the Mailpit container did not become healthy (docker compose --profile mail up -d --wait mailpit)"
  mailpit_started=1
fi
call GET "$mailpit_api/api/v1/messages"
expect_status "Mailpit's API" 200
jq_assert "Mailpit's API" 'has("total")'
ok "mailpit: up on $smtp_addr with its inbox at $mailpit_api, and its own API answers GET /api/v1/messages"

# This script flushes one Redis logical database on cleanup, so it must own it — and it can only
# claim what it could actually read: an unreadable dbsize is not proof of an empty index, and
# treating it as such is how a refused run destroys data on the way out.
dbsize="$($compose exec -T redis redis-cli -n "$redis_db" dbsize 2>/dev/null | tr -d '\r' || true)"
case "$dbsize" in
  0) redis_owned=1 ;;
  "") fail "could not read the size of Redis logical database $redis_db, so this script cannot prove it owns it; is Redis reachable?" ;;
  *) fail "Redis database $redis_db already holds $dbsize keys; set IDENTITY_REDIS_DB to a free database" ;;
esac

$compose exec -T postgres createdb -U cinema "$db" || fail "could not create the throwaway database $db"
db_created=1
schemas="$(psql_db -c "SELECT count(*) FROM pg_namespace WHERE nspname IN ('platform','cinema')")"
[ "$schemas" = "0" ] || fail "the throwaway database must start with no schema, found $schemas schemas"
migration_output="$($compose --profile tools run --rm -e DB_NAME="$db" migrate-all 2>&1)" \
  || { printf '%s\n' "$migration_output" >&2; fail "the migration runner failed on $db"; }
applied_versions="$(psql_db -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations")"
# The expected chain is derived from the migration directory, never enumerated: the directory is
# what the runner mounts, so a version added there cannot disagree with a list written down here.
expected_versions=""
for file in "$root"/db/migrations/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in $root/db/migrations"
  name="$(basename "$file" .up.sql)"
  expected_versions="${expected_versions:+$expected_versions,}$name"
done
[ "$applied_versions" = "$expected_versions" ] \
  || fail "the migration runner recorded [$applied_versions], expected [$expected_versions]"
# The four objects the whole gate depends on. They are read as names rather than as a version
# number, because a migration that ran and created nothing is a green version and an unusable
# database: the login cannot verify an address without an account table, cannot keep a session
# without one that has a token hash, cannot spend a link without the nonce row, and cannot claim
# an order without the column that joins it to the account.
for object in "guest_customers" "sessions" "access_links"; do
  present="$(psql_db -c "SELECT to_regclass('cinema.$object') IS NOT NULL")"
  [ "$present" = "t" ] || fail "cinema.$object is missing, so the login has nowhere to write"
done
token_hash_column="$(psql_db -c "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cinema' AND table_name = 'sessions' AND column_name = 'token_hash'")"
[ "$token_hash_column" = "1" ] || fail "cinema.sessions.token_hash is missing, so a token could be stored as itself"
customer_column="$(psql_db -c "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cinema' AND table_name = 'reservations' AND column_name = 'customer_id'")"
[ "$customer_column" = "1" ] || fail "cinema.reservations.customer_id is missing, so no order could ever be claimed"
ok "migrations: scripts/migrations/apply_all.sh brought $db to [$applied_versions], with guest_customers, sessions.token_hash, access_links and reservations.customer_id present"

# The bucket, through the bootstrap command the project uses (never an ad-hoc PUT). The sale below
# records a `ticket.issued` outbox row and the PDF worker is one of its two consumers, so the
# bucket is part of the stack a real deployment runs — and a gate that left it out would be
# proving the login on a stack nobody deploys.
go build -o "$tmp/api" ./apps/api/cmd/api || fail "the API did not build"
go build -o "$tmp/bootstrap" ./apps/api/cmd/bootstrap || fail "the bootstrap command did not build"
bucket_output="$(API_S3_ENDPOINT="$s3_endpoint" API_S3_BUCKET="$bucket" "$tmp/bootstrap" ensure-bucket 2>&1)" \
  || { printf '%s\n' "$bucket_output" >&2; fail "bootstrap ensure-bucket failed"; }
case "$bucket_output" in
  *"bucket $bucket is ready"*) ;;
  *) fail "bootstrap ensure-bucket answered '$bucket_output', expected 'bucket $bucket is ready'" ;;
esac
bucket_created=1
call GET "$s3_endpoint/$bucket?list-type=2"
[ "$STATUS" = "200" ] || fail "GET $s3_endpoint/$bucket answered $STATUS, so the bucket does not exist"
ok "bucket: bootstrap ensure-bucket created $bucket, and Floci answers it (200, empty)"

# The API runs from the built binary, so the recorded PID is the server itself and shutdown cannot
# leave an orphan holding the port. It is configured the way a deployment that signs people in
# is: a provider to mail through, a public origin the link is built from, an access link key of
# its own (never `API_TICKET_LINK_KEY`: a sign-in link identifies an address, not a reservation)
# and a Redis database the sign-in counter can be counted in.
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
API_TICKET_LINK_KEY="$ticket_link_key" \
API_IDENTITY_LINK_KEY="$access_link_key" \
API_SMTP_ADDR="$smtp_addr" \
API_EMAIL_FROM="$email_from" \
API_PUBLIC_WEB_URL="$public_web_url" \
"$tmp/api" >"$api_log" 2>&1 &
api_pid=$!

# Readiness is waited for, never slept through: the process is asked, and the log is read so a port
# collision is named instead of timing out.
ready=""
for _ in $(seq 1 160); do
  if grep -q 'address already in use' "$api_log" 2>/dev/null; then
    cat "$api_log" >&2
    fail "port $port is already in use; stop the process holding it"
  fi
  if curl --fail --silent --max-time 2 "$base/health/live" >/dev/null 2>&1 \
    && curl --fail --silent --max-time 2 "$base/health/ready" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
[ -n "$ready" ] || { cat "$api_log" >&2; fail "the API did not become ready on $base within 40s"; }

# What this gate depends on is asserted as what the API logged, not as the sentences around it:
# the prose is operator-facing and may be improved, while a route that stopped being mounted is
# what this gate exists to catch. All three identity routes are named in one line, and the bound
# is read off that same line — an operator's number and this gate's number have to be the same
# number, or the limit proven below is not the limit the deployment advertises.
grep -q 'public reservation API mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the public reservation API was not mounted"; }
grep -qF '"routes":["/api/v1/identity/links","/api/v1/identity/sessions","/api/v1/me/tickets"]' "$api_log" \
  || { cat "$api_log" >&2; fail "the identity routes and the account's list were not mounted together"; }
grep -qF '"limit":"3 per ' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not advertise a three-per-window sign-in limit"; }
grep -q 'ticket emails are delivered from the checkout outbox; worker started' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not start the ticket email consumer, so the mailbox would stay empty"; }
ok "api: ready on $base, with /identity/links, /identity/sessions and /me/tickets mounted, a three-per-window limit and the ticket email consumer started"

# The fixture: a cinema, a room, a published layout, a film and a screening that is on sale at one
# price per seat. Everything but the price and the sale state goes through the administrative API;
# those two are set with SQL because the administrative surface this gate mirrors (verify_m3.sh)
# sets them that way.
call POST "$admin/cinemas" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"name\":\"Cine Verificación Identidad\",\"timezone\":\"$venue_timezone\",\"address\":\"Calle 10\"}"
expect_status "create cinema" 201
cinema="$(jq -r '.id' <"$body")"
call POST "$admin/rooms" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"cinemaId\":\"$cinema\",\"name\":\"Sala Identidad\"}"
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
  -d '{"title":"La película que se recuerda","runtimeMinutes":97,"rating":"PG"}'
expect_status "create film" 201
film="$(jq -r '.id' <"$body")"
call POST "$admin/events" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"roomId\":\"$room\",\"filmId\":\"$film\",\"layoutVersionId\":\"$layout\",\"startsAt\":\"$starts_at\"}"
expect_status "create event" 201
event="$(jq -r '.id' <"$body")"
psql_db -c "UPDATE cinema.events SET price_cents = $price_cents, status = 'on_sale' WHERE id = '$event'" >/dev/null \
  || fail "could not put the event on sale at $price_cents cents"
stored_status="$(psql_db -c "SELECT status FROM cinema.events WHERE id = '$event'")"
[ "$stored_status" = "on_sale" ] || fail "the event status is '$stored_status', expected 'on_sale'"

call GET "$admin/events/$event/seats" -H "Authorization: Bearer $admin_token"
expect_status "read event seats" 200
seat_count="$(jq -r '.seats | length' <"$body")"
[ "$seat_count" = "3" ] || fail "the event materialized $seat_count seats, expected 3"
seat_one="$(jq -r '.seats[0].id' <"$body")"
seat_two="$(jq -r '.seats[1].id' <"$body")"
[ "$seat_one" != "$seat_two" ] || fail "the fixture selected one seat twice"
ok "fixture: cinema/room/published layout/film/event on sale, price_cents=$price_cents, event seats [$seat_one $seat_two] of $seat_count"

# 4a. The sale, over HTTP only, **before** the redemption: a hold, then one payment that carries
# the address. The order is the point. D5 claims the orders placed with an address at the instant
# that address is verified, so a sale made after the sign-in would be claimed by nothing and the
# list below would pass without the claim ever happening.
payment_key="identity-verify-payment-$unique"
call POST "$public/reservations/holds" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: identity-verify-hold-$unique" \
  -d "{\"eventId\":\"$event\",\"seatIds\":[\"$seat_one\",\"$seat_two\"]}"
expect_status "create hold" 201
reservation="$(jq -r '.id' <"$body")"
[ -n "$reservation" ] && [ "$reservation" != "null" ] || fail "the hold answered no reservation id — $(snippet)"

call POST "$public/reservations/$reservation/payment" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $payment_key" \
  -d "$(jq -nc --arg email "$address" '{email: $email}')"
expect_status "pay the held reservation with an address" 200
jq_assert "pay the held reservation with an address" '.paymentStatus == "approved"'
jq_assert "pay the held reservation with an address" '.ticketIds | length == 2'
tickets_json="$(jq -c '.ticketIds' <"$body")"
ticket_one="$(jq -r '.ticketIds[0]' <"$body")"
ticket_two="$(jq -r '.ticketIds[1]' <"$body")"
[ -n "$ticket_one" ] && [ -n "$ticket_two" ] && [ "$ticket_one" != "$ticket_two" ] \
  || fail "the payment answered tickets [$ticket_one] [$ticket_two], expected two distinct ids"
stored_address="$(psql_db -c "SELECT COALESCE(contact_email, '') FROM cinema.reservations WHERE id = '$reservation'")"
[ "$stored_address" = "$address" ] \
  || fail "the paid order stores '$stored_address' as its address, expected '$address'"
# And it belongs to nobody yet: the claim below is a change this gate can see happen, not a value
# that was already there.
preclaim_owner="$(psql_db -c "SELECT COALESCE(customer_id::text, '') FROM cinema.reservations WHERE id = '$reservation'")"
[ -z "$preclaim_owner" ] \
  || fail "the order already belongs to account $preclaim_owner before the address was verified, so the claim below would prove nothing"
ok "purchase: hold $reservation paid with one key, approved for $expected_amount cents, carrying $address as the unverified recipient and owned by no account yet, tickets [$ticket_one $ticket_two]"

# The address receives the purchase's own ticket message, mailed by the outbox consumer, and
# waiting for it is not decoration: the two counters below are read off one mailbox, so the
# ticket must have landed before a number read from it can be about the login. It is also the
# assertion that the two messages cannot be confused — the purchase's body carries /t/<token>
# and never a sign-in link.
wait_for_mail "$address" 1 "the purchase message" "$purchase_wait_seconds"
[ "$(link_mail_count "$address")" = "0" ] \
  || fail "the purchase's own message carries a $link_prefix link, so this gate could not tell a sign-in link from a ticket link"
ok "mailbox: the sale mailed exactly one message to $address and it carries no sign-in link, so every link counted from here on is the login's"

# 2. The link: a public request, and one message carrying <origin>/in/<token>.
call POST "$public/identity/links" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg email "$address" '{email: $email}')"
expect_status "ask for a sign-in link" 202
jq_assert "ask for a sign-in link" '.status == "accepted"'
cp "$body" "$tmp/link-accepted"
wait_for_links "$address" 1 "the sign-in link"
# Exactly one, never "at least one": the address is inside its allowance at this point, so a
# second sign-in message would mean one request produced two links. `wait_for_links` polls until
# the count is exactly the expected one, so a second message would make it fail by name.
link_pattern="${public_web_url//./\\.}/in/[A-Za-z0-9._~-]+"
link=""
search_address "$address"
for message_id in $(messages_with_link); do
  fetch_message "$message_id"
  # `|| true` because a message without a sign-in link makes grep exit 1, and an assignment from
  # a failed pipeline would end the run under `set -e` before this loop could look at the next one.
  link="$(message_text | grep -oE "$link_pattern" | sed -n 1p || true)"
  if [ -n "$link" ]; then break; fi
done
[ -n "$link" ] \
  || fail "the sign-in message to $address carries no $public_web_url/in/<token> link — $(message_text | head -c 300 | tr '\n' ' ')"
case "$link" in
  "$link_prefix"*) ;;
  *) fail "the sign-in link '$link' does not start with $link_prefix" ;;
esac
access_token="${link#"$link_prefix"}"
[ -n "$access_token" ] && [ "$access_token" != "$link" ] \
  || fail "the link '$link' is not $link_prefix<token>"
ok "link: POST /api/v1/identity/links answered 202, and exactly one message to $address carries $public_web_url/in/<token>"

# 3. The redemption: one token, one session, and an address that is now verified.
call POST "$public/identity/sessions" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg token "$access_token" '{token: $token}')"
expect_status "redeem the sign-in link" 201
# The body is the only place the session token will ever appear: this API sets no cookie (the web
# does), so there is nothing else to read and nothing to re-read later.
jq_assert "redeem the sign-in link" '((.token | type) == "string") and ((.token | length) == 43)'
jq_assert "redeem the sign-in link" '((.expiresAt | type) == "string") and (.expiresAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T.*Z$"))'
session_token="$(jq -r '.token' <"$body")"
[ -n "$session_token" ] && [ "$session_token" != "null" ] || fail "the redemption answered no session token — $(snippet)"
# The three writes the transaction promised, read where they landed — no route serves any of them,
# and that is what makes them worth reading here.
verified="$(psql_db -c "SELECT count(*) FROM cinema.guest_customers WHERE email = '$address' AND verified_at IS NOT NULL")"
[ "$verified" = "1" ] \
  || fail "cinema.guest_customers holds $verified verified row(s) for $address, expected exactly 1: S3 says the address is verified by the act of redeeming"
customer_id="$(psql_db -c "SELECT id FROM cinema.guest_customers WHERE email = '$address'")"
# S1's property, as an equality rather than a description: the row holds the SHA-256 of the token
# the caller was given, and the token itself is nowhere in the table. A dump of this table cannot
# be replayed as anybody's session, which is the whole reason the hash is there.
hashed_sessions="$(psql_db -c "SELECT count(*) FROM cinema.sessions s JOIN cinema.guest_customers c ON c.id = s.customer_id WHERE s.token_hash = sha256(convert_to('$session_token', 'UTF8')) AND c.email = '$address'")"
[ "$hashed_sessions" = "1" ] \
  || fail "cinema.sessions holds $hashed_sessions row(s) whose token_hash is the SHA-256 of the returned token for $address, expected exactly 1"
literal_sessions="$(psql_db -c "SELECT count(*) FROM cinema.sessions WHERE token_hash = convert_to('$session_token', 'UTF8')")"
[ "$literal_sessions" = "0" ] \
  || fail "cinema.sessions stores the session token itself in $literal_sessions row(s); the table must only ever hold its hash"
live_session="$(psql_db -c "SELECT count(*) FROM cinema.sessions WHERE token_hash = sha256(convert_to('$session_token', 'UTF8')) AND revoked_at IS NULL AND expires_at > now()")"
[ "$live_session" = "1" ] \
  || fail "the session row for the returned token is not live (it must have no revoked_at and an expires_at in the future)"
claimed="$(psql_db -c "SELECT count(*) FROM cinema.reservations WHERE id = '$reservation' AND customer_id = '$customer_id'::uuid")"
[ "$claimed" = "1" ] \
  || fail "the order $reservation was not claimed by account $customer_id, so D5 did not happen at the only instant it can"
# The one line D5 is recorded in. It is read because the count is deliberately not published to
# the caller: the log is the operator's copy, and the answer's shape is asserted elsewhere.
# The line is captured once and then inspected with shell patterns rather than piped into `grep
# -q`: a reading side that closes the pipe early makes the writing side die of SIGPIPE, and under
# `pipefail` that would look like a missing line.
claim_line="$(grep '"msg":"identity: address verified"' "$api_log" | tail -n 1 || true)"
[ -n "$claim_line" ] \
  || { tail -n 40 "$api_log" >&2; fail "the redemption wrote no 'identity: address verified' line, so the claim was not recorded"; }
case "$claim_line" in
  *'"claimed":1'*) ;;
  *) fail "the claim line does not report claimed=1, so the verified address did not take over the order it was placed with — $claim_line" ;;
esac
case "$claim_line" in
  *"$address"*) fail "the 'address verified' line carries the address itself, which is a person and not a diagnostic — $claim_line" ;;
esac
ok "session: the link answered 201 with a 43-character token and an expiry, guest_customers holds $address verified, the session row's token_hash is that token's SHA-256 (and not the token), and the redemption claimed order $reservation into account $customer_id, logged as claimed=1"

# 4b. The list the login exists for (D3, S5).
call GET "$public/me/tickets" -H "Authorization: Bearer $session_token"
expect_status "list the account's tickets" 200
expected_tickets="$(printf '%s' "$tickets_json" | jq -c 'sort')"
listed_tickets="$(jq -c '[.tickets[].ticketId] | sort' <"$body")"
[ "$listed_tickets" = "$expected_tickets" ] \
  || fail "GET /me/tickets answered $listed_tickets, expected exactly the order's tickets $expected_tickets"
# And the list is the ticket page's document, not a summary invented for it: the field this gate
# can check without rendering is the signed code, and a list of ids with no code would be a list
# of links that cannot open anything.
jq_assert "list the account's tickets" '(.tickets | length) == 2 and all(.tickets[]; (.qrToken | type) == "string" and (.qrToken | length) > 0)'
ok "list: GET /api/v1/me/tickets with the session token answers exactly the order's tickets $listed_tickets, each with its signed code"

# The refusals: no credential, and a credential shaped like one that was never issued. Both must be
# the login route's own 401, byte for byte — a route that answered "signed out" differently from
# "never existed" would be an oracle for whether a token was ever real.
call GET "$public/me/tickets"
expect_status "list without a token" 401
cp "$body" "$tmp/401-missing"
jq_assert "list without a token" '.code == "unauthorized" and .status == 401'
# A well-formed token that was never issued: the real token with its first base64url character
# changed. This is stronger than a random string, because a random string is refused by its shape
# before any query runs, and the question here is whether the lookup refuses it too.
unknown_first="A"
[ "${session_token:0:1}" = "A" ] && unknown_first="B"
unknown_token="$unknown_first${session_token:1}"
call GET "$public/me/tickets" -H "Authorization: Bearer $unknown_token"
expect_status "list with a token this deployment never issued" 401
cp "$body" "$tmp/401-unknown"
if grep -qF "$session_token" "$tmp/401-unknown"; then
  fail "the 401 echoes the token it was presented; a refusal may not repeat a credential"
fi
ok "refusals: GET /me/tickets without a token and with a well-formed token that was never issued both answer the same 401 the login route uses"

# 5. Single use (S2). The link is spent by the first redemption, and spending it again must write
# nothing at all — not a second session, not a second account, not a moved verified_at.
call POST "$public/identity/sessions" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg token "$access_token" '{token: $token}')"
expect_status "redeem the same link again" 401
cp "$body" "$tmp/401-spent"
jq_assert "redeem the same link again" '.code == "unauthorized"'
for saved in "$tmp/401-missing" "$tmp/401-unknown"; do
  cmp -s "$tmp/401-spent" "$saved" \
    || fail "the 401 for a spent link differs from the one at $(basename "$saved") — the two refusals must be one answer, byte for byte ($(snippet))"
done
sessions_for_account="$(psql_db -c "SELECT count(*) FROM cinema.sessions WHERE customer_id = '$customer_id'::uuid")"
[ "$sessions_for_account" = "1" ] \
  || fail "the account holds $sessions_for_account session row(s) after a second redemption, expected exactly 1: a spent link must write nothing"
accounts_for_address="$(psql_db -c "SELECT count(*) FROM cinema.guest_customers WHERE email = '$address'")"
[ "$accounts_for_address" = "1" ] \
  || fail "cinema.guest_customers holds $accounts_for_address row(s) for $address after a second redemption, expected exactly 1"
ok "single use: the same link redeemed a second time answers the same 401, and the account still holds exactly one session row and one account row"

# 6. The bound (S6): three links per address per window, and the refusal is silent by design.
#
# The allowance is spent in the open first. The link this run redeemed was the first of the three;
# two more asks are inside the allowance and must mail a message each. That is the "three" half of
# the claim, and asserting it is what makes the half after it mean anything — a route that never
# mailed at all would pass a gate that only counted the refusals.
for attempt in 2 3; do
  call POST "$public/identity/links" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg email "$address" '{email: $email}')"
  expect_status "sign-in link #$attempt (inside the allowance)" 202
  jq_assert "sign-in link #$attempt (inside the allowance)" '.status == "accepted"'
  cmp -s "$body" "$tmp/link-accepted" \
    || fail "sign-in link #$attempt answered a different body from the first ask; the answer may not change while the allowance is open"
done
wait_for_links "$address" 3 "the three allowed sign-in links"
[ "$(mail_count "$address")" = "4" ] \
  || fail "Mailpit holds $(mail_count "$address") message(s) for $address after three sign-in links and one purchase, expected 4"
# Then four more asks, which are outside the allowance. Every one of them answers the same 202 with
# the same bytes — a 429 or a different wording would tell a stranger that this address is being
# asked about, which is the one fact the login screen exists to withhold — and none of them signs
# or mails anything.
for attempt in 4 5 6 7; do
  call POST "$public/identity/links" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg email "$address" '{email: $email}')"
  expect_status "sign-in link #$attempt (outside the allowance)" 202
  cmp -s "$body" "$tmp/link-accepted" \
    || fail "sign-in link #$attempt answered a different body from the first ask — a refusal that reads differently is an oracle for whether this address is being asked about ($(snippet))"
done
note "waiting ${settle_seconds}s before asking the mailbox again, so 'it did not grow' is a measurement"
sleep "$settle_seconds"
after_limit_links="$(link_mail_count "$address")"
[ "$after_limit_links" = "3" ] \
  || fail "the four refused asks changed the mailbox to $after_limit_links sign-in message(s) for $address, expected 3: a refused request must mail nothing"
after_limit_total="$(mail_count "$address")"
[ "$after_limit_total" = "4" ] \
  || fail "Mailpit holds $after_limit_total message(s) for $address after four refused asks, expected 4 (the purchase's and the three allowed links)"
ok "limit: two further asks inside the three were admitted and mailed, the next four answered 202 with the same body and mailed nothing ($after_limit_links sign-in links and $after_limit_total messages in the mailbox for $address)"

# 7. Revocation (S1), where no route exists yet.
#
# The product has no sign-out route: the web's sign-out is a later task, and inventing a call here
# would be testing something that is not there. What can be proven today is the predicate the row
# decides — a session with `revoked_at` set is not a session — so the run closes its own session
# with SQL and asks the list again. This is the *predicate*, not a flow, and the summary says so.
revoked="$(psql_db -c "WITH closed AS (UPDATE cinema.sessions SET revoked_at = now() WHERE token_hash = sha256(convert_to('$session_token', 'UTF8')) RETURNING 1) SELECT count(*) FROM closed")"
[ "$revoked" = "1" ] || fail "revoking the run's session touched $revoked row(s), expected exactly 1"
# Revoked is not deleted: the row is still there, and says the end was chosen rather than reached.
still_there="$(psql_db -c "SELECT count(*) FROM cinema.sessions WHERE token_hash = sha256(convert_to('$session_token', 'UTF8')) AND revoked_at IS NOT NULL")"
[ "$still_there" = "1" ] \
  || fail "the revoked session row is gone or was not marked (found $still_there); a revocation is a fact kept, not a row deleted"
call GET "$public/me/tickets" -H "Authorization: Bearer $session_token"
expect_status "list with a revoked session" 401
cp "$body" "$tmp/401-revoked"
for saved in "$tmp/401-missing" "$tmp/401-unknown" "$tmp/401-spent"; do
  cmp -s "$tmp/401-revoked" "$saved" \
    || fail "the 401 for a revoked session differs from the one at $(basename "$saved"); revoked, missing and never-issued must be one answer"
done
ok "revocation: the run's session closed with SQL (revoked_at set, the row kept) and GET /me/tickets with that token answers the same opaque 401 as every other unusable session"

# The printed summary: what was proven, in one place, and what was not.
printf '\nIdentity login verification passed: a sale paid with an address belongs to no account → requesting a sign-in link answers 202 and exactly one message to that address carries <origin>/in/<token> → that token redeems once into a session whose row holds its SHA-256 and claims the older order for the now-verified address → GET /me/tickets with the token answers exactly the tickets of that order while no token and an unissued token answer one opaque 401 → the same link a second time answers that same 401 and writes no second session → three links per address are allowed and the next four answer the same 202 with no message → and a session closed with SQL answers that same 401, because the row decides.\n'
printf 'what this gate does not prove: no browser was involved, so the HttpOnly cookie Next writes, its lifetime rounding and the /in/<token> page are unproven here; Mailpit accepts any sender and any recipient, so no real provider, authentication, SPF or DKIM was involved; the product has no sign-out route yet, so revocation was proven as the predicate the session row decides and not as a flow a person can perform; the link was read and spent while it was still valid and the session was read while it was still live, so neither expiry was waited out (the configured link TTL is fifteen minutes and the session TTL is thirty days); and nothing here shows the mailbox belongs to the buyer — whoever controls the inbox can sign in, which is the definition of a sign-in link and also its limit.\n'
