#!/usr/bin/env bash
# Ticket email end-to-end verification: the half of T7 that no gate could prove before.
#
# Every other gate runs with **no mail provider at all**, and that proves only the
# direction the design promises in S5: a sale does not need the mail. This one proves the
# other direction, against a real SMTP server (Mailpit) and a real API process, through the
# public HTTP interface and — only where a claim needs a number no route serves — against
# the database that stored the sale:
#   1. A run-scoped stack: its own database, its own Redis logical database, its own API
#      port and its own bucket, so it can never touch the development data. The scope is
#      enforced, not assumed: it refuses to run against the `cinema` development database
#      by name, against Redis logical database 0 (the development keyspace), and against a
#      Redis index that already holds keys, because it drops the one and flushes the other.
#      Mailpit is the one shared service it starts on purpose — and it stops it on the way
#      out only when this run is what started it, exactly as the other gates leave the
#      services they share standing.
#   2. A sale paid **with an address in the payment body** produces, within the consumer's
#      tick budget, exactly one message in Mailpit's own API, to that address, and the
#      message body carries a /t/<token> link. Exactly one, never "at least one": a
#      consumer that delivers twice is the defect this number exists to catch.
#   3. That token, exchanged at GET /api/v1/ticket-links/<token>, answers the order's own
#      ticket ids — the link in the mailbox is the link that opens the sale.
#   4. Two more ticks later Mailpit still holds exactly one message for that order, while
#      the order's `ticket.issued` outbox row is **already published** (`published_at` set
#      by the PDF worker) and `cinema.outbox_deliveries` holds exactly one row for
#      ('email', that outbox row). That combination is the whole reason S4 has a delivery
#      record of its own: a second consumer filtering on `published_at IS NULL` would have
#      mailed nothing at all, and the only way to see that is to assert the count *after*
#      the publisher on the row has run.
#   5. The resend route: POST /api/v1/tickets/{id}/email answers 202 for a second address,
#      a new message reaches that address, and that address is stored nowhere — the order
#      still carries the first one. Three sends per reservation per hour are allowed; the
#      fourth is 429 with a `Retry-After` header and adds no message. That is the
#      anti-relay bound, proven where it matters: in the mailbox count.
#
# What this gate cannot prove is printed at the end, in the same place and the same voice as
# the other gates': a real provider, bounce handling, a verified address, and the link's
# expiry (reading a link now is not waiting seven days for it to close).
#
# Everything it creates is disposable: the database name, the bucket name and the Redis
# index are unique to this run, the addresses it mails to carry this run's process id, and
# nothing here touches the development `cinema` database or Redis index 0.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unique="$$"
db="cinema_email_verify_$unique"
bucket="cinema-email-verify-$unique"
# Its own Redis logical database: 8, 9, 10 and 11 belong to `verify-m35`, `verify-m2`,
# `verify-m3` and `verify-m41`, so this one is 12 and EMAIL_REDIS_DB overrides it.
redis_db="${EMAIL_REDIS_DB:-12}"
s3_endpoint="${EMAIL_S3_ENDPOINT:-http://127.0.0.1:4566}"
s3_endpoint="${s3_endpoint%/}"
# Mailpit's ports come from the same compose variables the service publishes them on, so a
# developer who moved them gets a gate that follows instead of one that dials a port
# nobody listens on.
smtp_addr="127.0.0.1:${MAILPIT_SMTP_PORT:-1025}"
mailpit_api="http://127.0.0.1:${MAILPIT_UI_PORT:-8025}"
admin_token="email-verify-admin-$unique"
# Two keys that are not the repository's development defaults, and that differ from each
# other: the QR signer and the link signer are handed run-scoped secrets here, the way a
# deployment would.
qr_key="cinema-email-verification-only-ticket-qr-key-$unique"
link_key="cinema-email-verification-only-ticket-link-key-$unique"
email_from="cinema-email-verify@example.test"
# The origin the mailed link is built from. It is a name that resolves nowhere on purpose:
# the gate never opens the web page, it asserts that the link the buyer receives points at
# the configured origin under /t/, which is the contract between the API and the web.
public_web_url="https://tickets.example.test"
# The two addresses are this run's own, so a mailbox somebody else has been filling cannot
# be mistaken for this run's, and a leftover of this run cannot be mistaken for somebody
# else's. `example.test` is reserved for documentation and can never receive real mail.
first_address="verify-email-$unique-one@example.test"
second_address="verify-email-$unique-two@example.test"
# The consumer's cadence, as `emailWorkerInterval` in apps/api/cmd/api states it. The
# budgets below are counted in ticks of this length rather than in sleeps with a comment,
# so a reader can check the arithmetic against the constant.
worker_tick_seconds=5
# Six ticks for the sale's own message: the worker's first pass runs as soon as it starts,
# so one tick is enough and this only bounds how long a broken one may take.
mail_wait_seconds=$((worker_tick_seconds * 6))
# Two more ticks, plus a second of slack for the SMTP handshake and Mailpit's own write,
# before the mailbox is asked again. It is what makes step 4 a measurement of "it did not
# grow" rather than a reading taken too early to grow.
settle_seconds=$((worker_tick_seconds * 2 + 1))
venue_timezone="Europe/Madrid"
starts_at="2031-05-04T18:30:00Z"
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
# Whether this run took something it may give back. All stay 0 until the run owns what
# cleanup would otherwise remove, so a refusal never deletes somebody else's keys, bucket or
# container — and never reports having removed something it never created.
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
  printf 'email verification FAILED: %s\n' "$*" >&2
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
  # The consumer's resend counters live in Redis, in a logical database of their own, so the
  # development keyspace (index 0) is never flushed — and neither is any index this run did
  # not take empty, which is what makes a refused run harmless.
  if [ "$redis_owned" = 1 ] && [ -n "$compose" ]; then
    $compose exec -T redis redis-cli -n "$redis_db" flushdb >/dev/null 2>&1 \
      && printf 'flushed Redis logical database %s\n' "$redis_db" >>"$cleanup_log"
  fi
  # The bucket is emptied and removed, so a failed run leaves no objects behind — but only
  # the bucket this run created is touched, never one that was already there.
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
  # Mailpit is the only shared service this gate starts, and it gives back only what it
  # took. A Mailpit that was already running belongs to the developer: stopping it would
  # close somebody else's inbox mid-session, so it is left exactly as it was found. Both
  # lines are only reached by a run that got as far as deciding, so a refusal before the
  # stack exists does not pretend to have an opinion about a container.
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

# The names this script drops and deletes are checked before anything is created, so a bug
# in the naming above cannot reach the development database or a real bucket.
case "$db" in
  cinema | postgres | template0 | template1)
    fail "refusing to run against the development \`cinema\` database: this gate creates and drops its own, and every name it uses ends in this run's process id (cinema_email_verify_<pid>)" ;;
  cinema_email_verify_[0-9]*) ;;
  *) fail "refusing to drop an unexpected database name '$db'" ;;
esac
case "$bucket" in cinema-email-verify-[0-9]*) ;; *) fail "refusing to create an unexpected bucket name '$bucket'" ;; esac
case "$redis_db" in
  0) fail "refusing to run: EMAIL_REDIS_DB=0 is the development Redis logical database, which this gate flushes on cleanup; set EMAIL_REDIS_DB to a free index other than 0 (the default is 12)" ;;
  "" | *[!0123456789]*) fail "refusing to run: EMAIL_REDIS_DB='$redis_db' is not a Redis logical database index; set it to a free index other than 0 (the default is 12)" ;;
  *) ;;
esac
[ -n "$compose" ] || fail "Docker Compose is required"
command -v go >/dev/null 2>&1 || fail "Go is required to build the API and the bootstrap command"
command -v jq >/dev/null 2>&1 || fail "jq is required to read the API's and Mailpit's answers"
command -v curl >/dev/null 2>&1 || fail "curl is required"

cd "$root"

# The API port is chosen rather than hardcoded: a developer machine may already have
# something on the usual one, and binding it would make the gate fail for a reason that has
# nothing to do with the product.
free_port() {
  local candidate
  for candidate in $(seq 18502 18580); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}
port="$(free_port)" || fail "no free API port in 18502-18580"
base="http://127.0.0.1:$port"
public="$base/api/v1"
admin="$public/admin"
dburl="postgres://cinema:cinema_local_only@127.0.0.1:5432/$db?sslmode=disable"
redisurl="redis://127.0.0.1:6379/$redis_db"

psql_db() { $compose exec -T postgres psql -X -q -tA -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"; }

# Every request in this gate is bounded. A provider or an API that stops answering must
# fail this gate in seconds, naming what it was asked, instead of idling until the CI job's
# own timeout decides for us: a hung stack would then look like a slow gate.
curl_max_time=30
curl_connect_timeout=5

# call <method> <url> [curl args...] — sets STATUS, writes $body and $headers.
call() {
  local method="$1" url="$2"
  shift 2
  STATUS="$(curl --silent --show-error --max-time "$curl_max_time" \
    --connect-timeout "$curl_connect_timeout" \
    --dump-header "$headers" --output "$body" \
    --write-out '%{http_code}' -X "$method" "$@" "$url")" \
    || fail "curl could not complete $method $url within ${curl_max_time}s"
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

# mail_count <address> — how many messages Mailpit holds for one address.
#
# Two details decide whether this number means what the gate claims. It reads
# `messages_count` and never `total`: Mailpit's search answers `total` as the size of the
# whole mailbox, so asserting on it would pass with somebody else's mail present and fail
# with none of ours. And it searches instead of listing, because
# `GET /api/v1/messages` in this version takes no query at all — a `search` parameter there
# is silently ignored, which is exactly the kind of green reading this gate exists to
# refuse. The address is quoted into the query, so a character in it cannot rewrite the
# filter.
mail_count() {
  curl --silent --show-error --max-time "$curl_max_time" --connect-timeout "$curl_connect_timeout" \
    --get --data-urlencode "query=to:\"$1\"" "$mailpit_api/api/v1/search" >"$tmp/search.json" \
    || fail "Mailpit did not answer its search API at $mailpit_api/api/v1/search"
  jq -e 'has("messages_count")' <"$tmp/search.json" >/dev/null 2>&1 \
    || fail "Mailpit's search answered no messages_count — $(head -c 300 "$tmp/search.json" | tr '\n' ' ')"
  jq -r '.messages_count' <"$tmp/search.json"
}
# wait_for_mail <address> <count> <label> — poll until Mailpit holds exactly that many
# messages for the address, and report the last count it saw when it never does.
wait_for_mail() { # <address> <expected count> <label>
  local seen="" attempt
  for attempt in $(seq 1 "$mail_wait_seconds"); do
    seen="$(mail_count "$1")"
    [ "$seen" = "$2" ] && return 0
    sleep 1
  done
  fail "$3: Mailpit holds $seen message(s) for $1, expected $2, after ${mail_wait_seconds}s (the consumer's cadence is ${worker_tick_seconds}s, so that is $((mail_wait_seconds / worker_tick_seconds)) ticks)"
}

note "isolation: database $db, Redis database $redis_db, API port $port, bucket $bucket, Mailpit SMTP $smtp_addr and inbox $mailpit_api"

# Dependencies first, and Mailpit with them: the point of this gate is that a sale is
# mailed, so a mail server is not optional here — it is the environment the claim is made
# in. Whether this run started it is recorded before it is started, because that is what
# decides whether cleanup may stop it.
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

# This script flushes one Redis logical database on cleanup, so it must own it — and it can
# only claim what it could actually read: an unreadable dbsize is not proof of an empty
# index, and treating it as such is how a refused run destroys data on the way out.
dbsize="$($compose exec -T redis redis-cli -n "$redis_db" dbsize 2>/dev/null | tr -d '\r' || true)"
case "$dbsize" in
  0) redis_owned=1 ;;
  "") fail "could not read the size of Redis logical database $redis_db, so this script cannot prove it owns it; is Redis reachable?" ;;
  *) fail "Redis database $redis_db already holds $dbsize keys; set EMAIL_REDIS_DB to a free database" ;;
esac

$compose exec -T postgres createdb -U cinema "$db" || fail "could not create the throwaway database $db"
db_created=1
schemas="$(psql_db -c "SELECT count(*) FROM pg_namespace WHERE nspname IN ('platform','cinema')")"
[ "$schemas" = "0" ] || fail "the throwaway database must start with no schema, found $schemas schemas"
migration_output="$($compose --profile tools run --rm -e DB_NAME="$db" migrate-all 2>&1)" \
  || { printf '%s\n' "$migration_output" >&2; fail "the migration runner failed on $db"; }
applied_versions="$(psql_db -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations")"
# The expected chain is derived from the migration directory, never enumerated: the
# directory is what the runner mounts, so a version added there cannot disagree with a list
# written down in this script.
expected_versions=""
for file in "$root"/db/migrations/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in $root/db/migrations"
  name="$(basename "$file" .up.sql)"
  expected_versions="${expected_versions:+$expected_versions,}$name"
done
[ "$applied_versions" = "$expected_versions" ] \
  || fail "the migration runner recorded [$applied_versions], expected [$expected_versions]"
# The two objects the whole gate depends on: a consumer with its own record cannot work on a
# schema that has no record to write.
delivery_table="$(psql_db -c "SELECT to_regclass('cinema.outbox_deliveries') IS NOT NULL")"
[ "$delivery_table" = "t" ] || fail "cinema.outbox_deliveries is missing, so no consumer could record a delivery"
contact_column="$(psql_db -c "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cinema' AND table_name = 'reservations' AND column_name = 'contact_email'")"
[ "$contact_column" = "1" ] || fail "cinema.reservations.contact_email is missing, so no sale could carry an address"
ok "migrations: scripts/migrations/apply_all.sh brought $db to [$applied_versions], with reservations.contact_email and outbox_deliveries present"

# The bucket, through the bootstrap command the project uses (never an ad-hoc PUT). It is
# not decoration: the PDF worker is the publisher of the outbox row, so without a bucket the
# row this gate asserts on is never marked published and step 4 proves nothing.
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

# The API runs from the built binary, so the recorded PID is the server itself and shutdown
# cannot leave an orphan holding the port. It is configured the way a deployment that mails
# is: a provider, a sender and the public origin the link is built from.
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
API_TICKET_LINK_KEY="$link_key" \
API_SMTP_ADDR="$smtp_addr" \
API_EMAIL_FROM="$email_from" \
API_PUBLIC_WEB_URL="$public_web_url" \
"$tmp/api" >"$api_log" 2>&1 &
api_pid=$!

# Readiness is waited for, never slept through: the process is asked, and the log is read so
# a port collision is named instead of timing out.
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

# The two halves of this gate are mounted, asserted as what the API logged rather than as
# the sentences around them: the prose is operator-facing and may be improved, while a
# consumer that never started or a route that stopped being mounted is what this gate exists
# to catch. The worker's own start line is read too, so "the store was built" is not
# mistaken for "the thing that mails ran".
grep -q 'public reservation API mounted' "$api_log" \
  || { cat "$api_log" >&2; fail "the public reservation API was not mounted"; }
grep -q 'ticket email worker started' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not start the ticket email consumer"; }
grep -q '"route":"/api/v1/tickets/{ticketId}/email"' "$api_log" \
  || { cat "$api_log" >&2; fail "the API did not mount the ticket email resend route"; }
ok "api: ready on $base, with the ticket email consumer started and the resend route mounted"

# The fixture: a cinema, a room, a published layout, a film and a screening that is on sale
# at one price per seat. Everything but the price and the sale state goes through the
# administrative API; those two are set with SQL because the administrative surface this
# gate mirrors (verify_m3.sh) sets them that way.
call POST "$admin/cinemas" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"name\":\"Cine Verificación Email\",\"timezone\":\"$venue_timezone\",\"address\":\"Calle 9\"}"
expect_status "create cinema" 201
cinema="$(jq -r '.id' <"$body")"
call POST "$admin/rooms" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"cinemaId\":\"$cinema\",\"name\":\"Sala Email\"}"
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
  -d '{"title":"La película que se envía","runtimeMinutes":104,"rating":"PG"}'
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

# The sale, over HTTP only: a hold, then one payment that carries the address the tickets
# are to be mailed to. The address arrives in the payment body because that is where D1 puts
# it — before the provider is called, in the same request that captures the money.
payment_key="email-verify-payment-$unique"
call POST "$public/reservations/holds" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: email-verify-hold-$unique" \
  -d "{\"eventId\":\"$event\",\"seatIds\":[\"$seat_one\",\"$seat_two\"]}"
expect_status "create hold" 201
reservation="$(jq -r '.id' <"$body")"
[ -n "$reservation" ] && [ "$reservation" != "null" ] || fail "the hold answered no reservation id — $(snippet)"

call POST "$public/reservations/$reservation/payment" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $payment_key" \
  -d "$(jq -nc --arg email "$first_address" '{email: $email}')"
expect_status "pay the held reservation with an address" 200
jq_assert "pay the held reservation with an address" '.paymentStatus == "approved"'
jq_assert "pay the held reservation with an address" '.ticketIds | length == 2'
tickets_json="$(jq -c '.ticketIds' <"$body")"
ticket_one="$(jq -r '.ticketIds[0]' <"$body")"
ticket_two="$(jq -r '.ticketIds[1]' <"$body")"
[ -n "$ticket_one" ] && [ -n "$ticket_two" ] && [ "$ticket_one" != "$ticket_two" ] \
  || fail "the payment answered tickets [$ticket_one] [$ticket_two], expected two distinct ids"
# The address that came with the payment is the one stored on the order, unverified and
# whole: the worker resolves its recipient from here, so a sale whose address never landed
# would produce no message at all and the next assertion would fail for the wrong reason.
stored_address="$(psql_db -c "SELECT COALESCE(contact_email, '') FROM cinema.reservations WHERE id = '$reservation'")"
[ "$stored_address" = "$first_address" ] \
  || fail "the paid order stores '$stored_address' as its address, expected '$first_address'"
ok "purchase: hold $reservation paid with one key, approved for $expected_amount cents and carrying $first_address as the unverified recipient, tickets [$ticket_one $ticket_two]"

# 2. The message, exactly one of it, within the consumer's tick budget.
wait_for_mail "$first_address" 1 "the purchase message"
cp "$tmp/search.json" "$body"
jq_assert "the purchase message" --arg to "$first_address" '(.messages | map(select((.To | map(.Address)) | index($to))) | length) == 1'
message_id="$(jq -r '.messages[0].ID' <"$body")"
[ -n "$message_id" ] && [ "$message_id" != "null" ] || fail "Mailpit answered no message id — $(snippet)"
curl --silent --show-error --max-time "$curl_max_time" --connect-timeout "$curl_connect_timeout" \
  "$mailpit_api/api/v1/message/$message_id" >"$tmp/message.json" \
  || fail "Mailpit did not answer the full message $message_id"
cp "$tmp/message.json" "$body"
jq_assert "the purchase message" --arg to "$first_address" '(.To | map(.Address)) | index($to) != null'
# The link is read out of the body the buyer would read, which is the only place it exists:
# the API never logs it (it is a credential) and the database never stores it.
link_pattern="${public_web_url//./\\.}/t/[A-Za-z0-9._~-]+"
message_links="$(jq -r '.Text // ""' <"$body" | grep -oE "$link_pattern" || true)"
link="$(printf '%s\n' "$message_links" | sed -n 1p)"
[ -n "$link" ] \
  || fail "the message body carries no $public_web_url/t/<token> link — $(jq -r '.Text // ""' <"$body" | head -c 300 | tr '\n' ' ')"
token="${link#"$public_web_url/t/"}"
[ -n "$token" ] && [ "$token" != "$link" ] || fail "the link '$link' is not $public_web_url/t/<token>"
ok "email: exactly one message in Mailpit to $first_address, whose body carries $public_web_url/t/<token>"

# 3. The link in the mailbox is the link that opens the sale.
call GET "$public/ticket-links/$token"
expect_status "open the mailed link" 200
jq_assert "open the mailed link" --arg reservation "$reservation" '.reservationId == $reservation'
jq_assert "open the mailed link" --argjson expected "$tickets_json" '(.ticketIds | sort) == ($expected | sort)'
# S3's claim is an explicit expiry in the signed payload. This gate reads that the link
# carries one; it does not wait seven days to watch the link close, and it says so below.
jq_assert "open the mailed link" '(.expiresAt | type) == "string"'
ok "link: the mailed token answers the order's own tickets $(jq -c '.ticketIds' <"$body") at GET /ticket-links/<token>"

# 4. S4's whole reason for existing.
#
# The outbox row and its delivery record are read by identifier rather than by counting the
# whole table: "there is a row for this sale" is the claim, and a count over everything
# would pass on a database where a second sale existed.
outbox_rows="$(psql_db -c "SELECT count(*) FROM cinema.outbox WHERE topic = 'ticket.issued' AND payload->>'reservationId' = '$reservation'")"
[ "$outbox_rows" = "1" ] || fail "the sale wrote $outbox_rows ticket.issued outbox row(s), expected exactly 1"
outbox_id="$(psql_db -c "SELECT id FROM cinema.outbox WHERE topic = 'ticket.issued' AND payload->>'reservationId' = '$reservation'")"
[ -n "$outbox_id" ] || fail "the sale's outbox row has no identifier"
# Published by the PDF worker, waited for rather than assumed: it is the state that makes
# the next assertion mean something.
published=""
for _ in $(seq 1 60); do
  published="$(psql_db -c "SELECT count(*) FILTER (WHERE published_at IS NOT NULL) || ':' || count(*) FILTER (WHERE published_at IS NULL) FROM cinema.outbox WHERE topic = 'ticket.issued'")"
  [ "$published" = "1:0" ] && break
  sleep 0.5
done
[ "$published" = "1:0" ] \
  || fail "the ticket.issued outbox row is not published by the PDF worker (published:unpublished = $published), so step 4 could not prove anything"

note "waiting two more ticks (${settle_seconds}s) before asking the mailbox again"
sleep "$settle_seconds"
settled_count="$(mail_count "$first_address")"
[ "$settled_count" = "1" ] \
  || fail "S4: two ticks after the first delivery Mailpit holds $settled_count message(s) for $first_address, expected exactly 1 (a consumer that is not idempotent mails twice)"
deliveries="$(psql_db -c "SELECT count(*) || ':' || count(*) FILTER (WHERE consumer = 'email' AND delivered_at IS NOT NULL) FROM cinema.outbox_deliveries WHERE outbox_id = '$outbox_id'")"
[ "$deliveries" = "1:1" ] \
  || fail "cinema.outbox_deliveries holds rows:emailRows=$(printf '%s' "$deliveries" | tr ':' '/') for outbox $outbox_id, expected exactly one row for ('email', $outbox_id) with a delivered_at"
still_published="$(psql_db -c "SELECT count(*) FROM cinema.outbox WHERE id = '$outbox_id' AND published_at IS NOT NULL")"
[ "$still_published" = "1" ] \
  || fail "the outbox row $outbox_id is no longer published (published_at is NULL), so published_at is not being used as the publisher's mark"
ok "S4: two ticks later exactly 1 message is still in Mailpit, while outbox $outbox_id is published ($still_published) and ('email', $outbox_id) is one delivered row"

# 5. The resend route: the same message, to an address typed now, stored nowhere.
resend_to() { # <ticketID> <address>
  call POST "$public/tickets/$1/email" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg email "$2" '{email: $email}')"
}

resend_to "$ticket_one" "$second_address"
expect_status "resend to a second address" 202
jq_assert "resend to a second address" '.sent == true'
wait_for_mail "$second_address" 1 "resend to a second address"
# The promise this route makes is that the address dies with the request. Both halves are
# checked: the order still carries the first address, and no order carries the second.
stored_address="$(psql_db -c "SELECT COALESCE(contact_email, '') FROM cinema.reservations WHERE id = '$reservation'")"
[ "$stored_address" = "$first_address" ] \
  || fail "the resend changed the order's address to '$stored_address', expected it to stay '$first_address'"
second_stored="$(psql_db -c "SELECT count(*) FROM cinema.reservations WHERE contact_email = '$second_address'")"
[ "$second_stored" = "0" ] \
  || fail "the resent address $second_address is stored on $second_stored reservation(s), but a resend stores nothing"
# And the first address was not mailed again: a resend is one message to one address.
first_count="$(mail_count "$first_address")"
[ "$first_count" = "1" ] \
  || fail "the resend added a message for $first_address (now $first_count), but it was addressed to $second_address"
ok "resend: POST /tickets/<id>/email answered 202, one message reached $second_address, and the order still stores $first_address (the new address is stored on no order)"

# The anti-relay bound: three sends per reservation per hour. The first is the one above.
resend_to "$ticket_one" "$second_address"
expect_status "resend #2" 202
wait_for_mail "$second_address" 2 "resend #2"
resend_to "$ticket_one" "$second_address"
expect_status "resend #3" 202
wait_for_mail "$second_address" 3 "resend #3"
before_limit="$(mail_count "$second_address")"
resend_to "$ticket_one" "$second_address"
expect_status "resend #4" 429
jq_assert "resend #4" '.code == "rate_limited"'
retry_after="$(header 'retry-after')"
case "$retry_after" in
  "" | *[!0123456789]*) fail "resend #4 answered 429 without a numeric Retry-After header (got '$retry_after')" ;;
  *) ;;
esac
[ "$retry_after" -ge 1 ] && [ "$retry_after" -le 3600 ] \
  || fail "resend #4 answered Retry-After: $retry_after, expected a wait inside the one hour window"
# The count is what makes this a refusal and not an error: a 429 that still mailed would be
# a relay with a status code in front of it.
sleep 2
after_limit="$(mail_count "$second_address")"
[ "$after_limit" = "$before_limit" ] \
  || fail "the rate-limited send changed the mailbox from $before_limit to $after_limit message(s) for $second_address"
[ "$after_limit" = "3" ] \
  || fail "Mailpit holds $after_limit message(s) for $second_address after three allowed sends, expected 3"
ok "limit: three sends were allowed, the fourth answered 429 with Retry-After: ${retry_after}s and added no message ($after_limit in the mailbox for $second_address)"

# The printed summary: what was proven, in one place, and what was not.
printf '\nTicket email verification passed: a sale paid with an address is mailed exactly one message carrying a /t/<token> link → that token opens the sale and answers its own tickets → two ticks later the mailbox still holds one message while the outbox row is published and its email delivery is recorded → the resend route mails a second address without storing it, and the fourth send inside the hour is refused with a Retry-After and no message.\n'
printf 'what this gate does not prove: Mailpit accepts any sender and any recipient, so no real provider, authentication, SPF or DKIM was involved; no bounce was exercised, and a message that bounces is still recorded as delivered; the address is unverified by decision (S2), so nothing here shows the mailbox belongs to the buyer; and the signed link was read while it was still valid, so its expiry was not waited out (the configured TTL is seven days).\n'
