#!/usr/bin/env bash
# M4.1 end-to-end verification: the door.
#
# It proves, over the public HTTP interface and — only where a claim needs a number the
# API does not serve (how many rows carry a redemption) — against the database that
# stored it:
#   1. A run-scoped stack: its own database, its own Redis logical database, its own API
#      port and its own bucket, so it can never touch the development data.
#   2. The fixture **through the API and never through SQL**, which M4.1's S9 made
#      possible: a cinema in Europe/Madrid, a room, a published layout, a film and a
#      screening created on sale at a price in one request, listed by the public
#      catalogue in the venue's own hour.
#   3. The sale: two seats held, paid once with one Idempotency-Key, one ticket per seat.
#   4. The first admission: POST /tickets/redeem answers 200, alreadyRedeemed:false, the
#      seat the ticket owns, and redeemedAtLocal in the venue's clock — the same offset
#      the ticket's own startsAtLocal carries.
#   5. The second presentation is not an error and not an admission: 200 with
#      alreadyRedeemed:true and the *first* redeemedAt and redeemedAtLocal, byte for
#      byte, for the same ticket.
#   6. A forged code redeems nothing: 400 invalid_token, a body identical to what
#      POST /tickets/verify answers for the same code, and the stored redemption count
#      unchanged.
#   7. The sale is untouched by admission: the ticket read still answers its status, its
#      order amount and its own seat, and the PDF in the bucket is still the same bytes.
#   8. Two doors at once: two concurrent redemptions of a third ticket, exactly one of
#      which admits it and the other of which is told the first instant — the invariant
#      the Go race test pins, now over HTTP.
#
# Everything it creates is disposable: the database name, the bucket name and the Redis
# index are unique to this run, and nothing here touches the development `cinema`
# database or Redis index 0. Cleanup runs on every exit path and prints what it removed.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unique="$$"
db="cinema_m41_verify_$unique"
bucket="cinema-m41-verify-$unique"
# Its own Redis logical database: 8, 9 and 10 belong to the other gates, so this one is
# 11 and M41_REDIS_DB overrides it.
redis_db="${M41_REDIS_DB:-11}"
s3_endpoint="${M41_S3_ENDPOINT:-http://127.0.0.1:4566}"
s3_endpoint="${s3_endpoint%/}"
admin_token="m41-verify-admin-$unique"
qr_key="cinema-m41-verification-only-ticket-qr-key-$unique"
venue_timezone="Europe/Madrid"
price_cents=1250
seats_bought=2
expected_amount=$((price_cents * seats_bought))
# The screening is placed in the same half of the year as the run. This gate compares the
# offset `redeemedAtLocal` carries with the one `startsAtLocal` carries, and Madrid spends
# half the year at +01:00 and half at +02:00: a fixed summer date would make that
# comparison fail every winter for a reason that has nothing to do with the product. Both
# hours are distinct from UTC, so a renderer printing the machine's zone fails either way.
if [ "$(TZ="$venue_timezone" date +%z 2>/dev/null || echo +0100)" = "+0200" ]; then
  starts_at="2031-05-04T18:30:00Z"
  starts_at_local="2031-05-04T20:30:00+02:00"
else
  starts_at="2031-01-04T17:30:00Z"
  starts_at_local="2031-01-04T18:30:00+01:00"
fi
venue_offset="${starts_at_local: -6}"
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
  printf 'M4.1 verification FAILED: %s\n' "$*" >&2
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
  # The reservation keys live in a dedicated logical database, so the development
  # keyspace (index 0) is never flushed.
  $compose exec -T redis redis-cli -n "$redis_db" flushdb >/dev/null 2>&1 \
    && printf 'flushed Redis logical database %s\n' "$redis_db" >>"$cleanup_log"
  # The bucket is emptied and removed, so a failed run leaves no objects behind.
  # Bounded like every other request: cleanup runs from the EXIT trap, so a hung object store
  # here would delay the exit that reports why the gate failed in the first place.
  if curl --silent --fail --max-time 10 "$s3_endpoint/$bucket?list-type=2" >"$tmp/cleanup-list.xml" 2>/dev/null; then
    local key
    for key in $(grep -o '<Key>[^<]*</Key>' "$tmp/cleanup-list.xml" | sed 's/<[^>]*>//g'); do
      curl --silent --max-time 10 --output /dev/null -X DELETE "$s3_endpoint/$bucket/$key" || true
    done
    if curl --silent --max-time 10 --output /dev/null -w '%{http_code}' -X DELETE "$s3_endpoint/$bucket" 2>/dev/null | grep -q '^204$'; then
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
trap cleanup EXIT INT TERM

# The names this script drops and deletes are checked before anything is created, so a
# bug in the naming above cannot reach the development database or a real bucket.
case "$db" in cinema_m41_verify_[0-9]*) ;; *) fail "refusing to drop an unexpected database name" ;; esac
case "$bucket" in cinema-m41-verify-[0-9]*) ;; *) fail "refusing to create an unexpected bucket name" ;; esac
case "$redis_db" in
  0) fail "refusing to run: M41_REDIS_DB=0 is the development Redis logical database, which this gate flushes on cleanup; set M41_REDIS_DB to a free index other than 0 (the default is 11)" ;;
  "" | *[!0123456789]*) fail "refusing to run: M41_REDIS_DB='$redis_db' is not a Redis logical database index; set it to a free index other than 0 (the default is 11)" ;;
  *) ;;
esac
[ -n "$compose" ] || fail "Docker Compose is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v openssl >/dev/null 2>&1 || fail "openssl is required to forge a code"
command -v curl >/dev/null 2>&1 || fail "curl is required"

cd "$root"

# The API port is chosen rather than hardcoded: a developer machine may already have
# something on the usual one, and binding it would make the gate fail for a reason that
# has nothing to do with the product.
free_port() {
  local candidate
  for candidate in $(seq 18402 18480); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}
port="$(free_port)" || fail "no free API port in 18402-18480"
base="http://127.0.0.1:$port"
public="$base/api/v1"
admin="$public/admin"
dburl="postgres://cinema:cinema_local_only@127.0.0.1:5432/$db?sslmode=disable"
redisurl="redis://127.0.0.1:6379/$redis_db"

psql_db() { $compose exec -T postgres psql -X -q -tA -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"; }

# Every HTTP request in this gate is bounded. An API or object store that stops answering must
# fail this gate in seconds, naming the route, instead of idling until the CI job's own timeout
# decides for us: a hung stack would then look like a slow gate. The readiness probes below use
# a shorter bound of their own, because a probe that outlives the next attempt is already stale.
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
# tamper <token> — flips the first character of the signature, keeping the payload: one
# character away from a genuine code, which is exactly what a forgery looks like.
tamper() {
  local payload="${1%%.*}" signature="${1#*.}" first="${1#*.}"
  first="${first:0:1}"
  if [ "$first" = "A" ]; then first="B"; else first="A"; fi
  printf '%s.%s%s' "$payload" "$first" "${signature:1}"
}
# redeem <token> [label] — the door's one action, against the running API.
redeem() {
  call POST "$public/tickets/redeem" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg token "$1" --arg label "${2:-}" '{token: $token, label: $label}')"
}
# redeemed_count — the stored redemptions, read from the database because no public route
# answers how many rows carry an instant.
redeemed_count() { psql_db -c "SELECT count(*) FROM cinema.tickets WHERE redeemed_at IS NOT NULL"; }

note "isolation: database $db, Redis database $redis_db, API port $port, bucket $bucket"

$compose up -d --wait postgres redis floci >/dev/null || fail "the compose dependencies did not become healthy"

# This script flushes one Redis logical database on cleanup, so it must own it. A
# developer using the same index for something else loses nothing: the run stops before
# touching it.
dbsize="$($compose exec -T redis redis-cli -n "$redis_db" dbsize 2>/dev/null | tr -d '\r' || echo 0)"
case "$dbsize" in
  0|"") ;;
  *) fail "Redis database $redis_db already holds $dbsize keys; set M41_REDIS_DB to a free database" ;;
esac

$compose exec -T postgres createdb -U cinema "$db" || fail "could not create the throwaway database $db"
schemas="$(psql_db -c "SELECT count(*) FROM pg_namespace WHERE nspname IN ('platform','cinema')")"
[ "$schemas" = "0" ] || fail "the throwaway database must start with no schema, found $schemas schemas"
migration_output="$($compose --profile tools run --rm -e DB_NAME="$db" migrate-all 2>&1)" \
  || { printf '%s\n' "$migration_output" >&2; fail "the migration runner failed on $db"; }
applied_versions="$(psql_db -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations")"
# The expected chain is derived from the migration directory, never enumerated, so a
# version added there cannot disagree with a list written down here. The door needs
# 0008_ticket_redemption: a database that stopped short of it has no redeemed_at column,
# and this assertion reports that instead of a confusing 503 later.
expected_versions=""
for file in "$root"/db/migrations/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in $root/db/migrations"
  name="$(basename "$file" .up.sql)"
  expected_versions="${expected_versions:+$expected_versions,}$name"
done
case ",$expected_versions," in
  *,0008_ticket_redemption,*) ;;
  *) fail "the migrations on disk do not include 0008_ticket_redemption, so the door has nowhere to record an admission" ;;
esac
[ "$applied_versions" = "$expected_versions" ] \
  || fail "the migration runner recorded [$applied_versions], expected [$expected_versions]"
redeemed_column="$(psql_db -c "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cinema' AND table_name = 'tickets' AND column_name = 'redeemed_at'")"
[ "$redeemed_column" = "1" ] || fail "cinema.tickets has no redeemed_at column on $db"
ok "migrations: scripts/migrations/apply_all.sh brought $db to [$applied_versions], with cinema.tickets.redeemed_at"

# The bucket, through the bootstrap command the project uses (never an ad-hoc PUT).
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
API_HOLD_TTL=5m \
API_EXPIRY_INTERVAL=10s \
API_EXPIRY_BATCH=10 \
API_S3_ENDPOINT="$s3_endpoint" \
API_S3_BUCKET="$bucket" \
API_S3_PRESIGN_TTL=5m \
API_TICKET_QR_KEY="$qr_key" \
"$tmp/api" >"$api_log" 2>&1 &
api_pid=$!

# Readiness is waited for, never slept through, and the log is read so a port collision is
# named instead of timing out. The delay doubles from 0.25s to 2s: prompt while a healthy
# service converges, quiet once it is up or genuinely stuck, so a slow start is not hammered
# with ten requests a second for the whole deadline. The whole wait is still bounded at 40s
# because the deadline, not the attempt count, is what the caller is promised.
ready=""
deadline=$(( $(date +%s) + 40 ))
delay_ms=250
while [ "$(date +%s)" -lt "$deadline" ]; do
  if grep -q 'address already in use' "$api_log" 2>/dev/null; then
    cat "$api_log" >&2
    fail "port $port is already in use; stop the process holding it"
  fi
  if curl --fail --silent --max-time 2 "$base/health/live" >/dev/null 2>&1 \
    && curl --fail --silent --max-time 2 "$base/health/ready" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep "$(printf '%d.%03d' $((delay_ms / 1000)) $((delay_ms % 1000)))"
  delay_ms=$(( delay_ms * 2 ))
  if [ "$delay_ms" -gt 2000 ]; then delay_ms=2000; fi
done
[ -n "$ready" ] || { cat "$api_log" >&2; fail "the API did not become ready on $base within 40s"; }

code_routes=("/api/v1/tickets/{ticketId}/qr.svg" "/api/v1/tickets/verify" "/api/v1/tickets/redeem")
mounted="$(grep -c 'public reservation API mounted' "$api_log" || true)"
[ "$mounted" = "1" ] || { cat "$api_log" >&2; fail "the public reservation API was not mounted"; }
# The code routes, asserted as the paths the API logged rather than as the sentence around
# them. The admission route is the one this gate exists for, so a deployment that mounts
# verification without redemption fails here instead of at the first POST.
for code_route in "${code_routes[@]}"; do
  grep -q "\"$code_route\"" "$api_log" \
    || { cat "$api_log" >&2; fail "the API did not mount the ticket code route $code_route"; }
done
ok "api: ready on $base with the QR, verification and admission code routes mounted"

# The fixture, entirely through the administrative API: S9 is what makes the price and the
# sale state part of the create, so this gate needs no SQL to put a screening on sale and
# would fail if that one request stopped carrying either field.
call POST "$admin/cinemas" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"name\":\"Cine Verificación M4.1\",\"timezone\":\"$venue_timezone\",\"address\":\"Calle 41\"}"
expect_status "create cinema" 201
cinema="$(jq -r '.id' <"$body")"
call POST "$admin/rooms" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"cinemaId\":\"$cinema\",\"name\":\"Sala M4.1\"}"
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
  -d '{"title":"La película verificada M4.1","runtimeMinutes":104,"rating":"PG"}'
expect_status "create film" 201
film="$(jq -r '.id' <"$body")"
# One request: scheduled, priced and on sale. No PATCH and no SQL follow it.
call POST "$admin/events" -H 'Content-Type: application/json' -H "Authorization: Bearer $admin_token" \
  -d "{\"roomId\":\"$room\",\"filmId\":\"$film\",\"layoutVersionId\":\"$layout\",\"startsAt\":\"$starts_at\",\"priceCents\":$price_cents,\"status\":\"on_sale\"}"
expect_status "create event on sale with a price" 201
event="$(jq -r '.id' <"$body")"
jq_assert "create event on sale with a price" '.priceCents == $price and .status == "on_sale"' --argjson price "$price_cents"
call GET "$admin/events/$event" -H "Authorization: Bearer $admin_token"
expect_status "read the screening back" 200
jq_assert "read the screening back" '.priceCents == $price and .status == "on_sale" and .startsAtLocal == $local' \
  --argjson price "$price_cents" --arg local "$starts_at_local"

# The public catalogue, which is where a buyer meets the screening the door will later
# refuse twice: on sale, at the price the create named, in the venue's own hour.
call GET "$public/events?limit=20"
expect_status "public catalogue" 200
jq_assert "public catalogue" '(.events | map(select(.id == $event)) | length) == 1' --arg event "$event"
jq_assert "public catalogue" '(.events[] | select(.id == $event)) | .onSale == true and .priceCents == $price and .startsAtLocal == $local and .filmTitle == "La película verificada M4.1"' \
  --arg event "$event" --arg local "$starts_at_local" --argjson price "$price_cents"
ok "fixture through the API only: cinema ($venue_timezone), room, published layout, film and screening on sale at $price_cents cents in one request; the catalogue lists it at $starts_at_local"

call GET "$admin/events/$event/seats" -H "Authorization: Bearer $admin_token"
expect_status "read event seats" 200
seat_count="$(jq -r '.seats | length' <"$body")"
[ "$seat_count" = "3" ] || fail "the event materialized $seat_count seats, expected 3"
seat_one="$(jq -r '.seats[0].id' <"$body")"
seat_two="$(jq -r '.seats[1].id' <"$body")"
seat_three="$(jq -r '.seats[2].id' <"$body")"
[ "$seat_one" != "$seat_two" ] && [ "$seat_two" != "$seat_three" ] && [ "$seat_one" != "$seat_three" ] \
  || fail "the fixture selected a seat twice"

# The sale, over HTTP only: a hold for two seats, then one payment with one key.
payment_key="m41-verify-payment-$unique"
call POST "$public/reservations/holds" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: m41-verify-hold-$unique" \
  -d "{\"eventId\":\"$event\",\"seatIds\":[\"$seat_one\",\"$seat_two\"]}"
expect_status "create hold" 201
reservation="$(jq -r '.id' <"$body")"
[ -n "$reservation" ] && [ "$reservation" != "null" ] || fail "the hold answered no reservation id — $(snippet)"
call POST "$public/reservations/$reservation/payment" -H "Idempotency-Key: $payment_key"
expect_status "pay the held reservation" 200
jq_assert "pay the held reservation" '.paymentStatus == "approved"'
jq_assert "pay the held reservation" '.amountCents == $amount' --argjson amount "$expected_amount"
jq_assert "pay the held reservation" '.ticketIds | length == 2'
tickets="$(jq -r '.ticketIds[]' <"$body")"
ticket_one="$(printf '%s\n' "$tickets" | sed -n 1p)"
ticket_two="$(printf '%s\n' "$tickets" | sed -n 2p)"
[ -n "$ticket_one" ] && [ -n "$ticket_two" ] && [ "$ticket_one" != "$ticket_two" ] \
  || fail "the payment answered tickets [$ticket_one] [$ticket_two], expected two distinct ids"
ok "sale: hold $reservation, one payment key, approved for $expected_amount cents, tickets [$ticket_one $ticket_two]"

# The PDF exists before anybody goes to the door, which is what makes "unchanged by the
# admission" a comparison rather than a claim. It is rendered by the checkout's outbox
# worker, so its readiness is waited for, never slept through.
# The same bounded backoff as the readiness wait, for the same reason: a worker writes this
# row, so it is polled rather than slept through, with the whole wait bounded at 30s.
published=""
deadline=$(( $(date +%s) + 30 ))
delay_ms=250
while [ "$(date +%s)" -lt "$deadline" ]; do
  published="$(psql_db -c "SELECT count(*) FILTER (WHERE published_at IS NOT NULL) || ':' || count(*) FILTER (WHERE published_at IS NULL) FROM cinema.outbox WHERE topic = 'ticket.issued'")"
  if [ "$published" = "1:0" ]; then break; fi
  sleep "$(printf '%d.%03d' $((delay_ms / 1000)) $((delay_ms % 1000)))"
  delay_ms=$(( delay_ms * 2 ))
  if [ "$delay_ms" -gt 1000 ]; then delay_ms=1000; fi
done
[ "$published" = "1:0" ] || fail "the ticket.issued outbox row is not published (published:unpublished = $published)"

# fetch_pdf <location> <output> <label> — the pre-signed link must serve a document.
fetch_pdf() {
  local status
  status="$(curl --silent --show-error --max-time "$curl_max_time" \
    --connect-timeout "$curl_connect_timeout" \
    --output "$2" --write-out '%{http_code}' "$1")" \
    || fail "$3: curl could not download the pre-signed URL within ${curl_max_time}s"
  [ "$status" = "200" ] || fail "$3: the pre-signed URL answered $status, expected 200"
  [ -s "$2" ] || fail "$3: the pre-signed URL answered an empty document"
}
call GET "$public/tickets/$ticket_one/pdf"
expect_status "pdf redirect before the admission" 302
fetch_pdf "$(header 'location')" "$tmp/pdf-before.pdf" "pdf download before the admission"
[ "$(head -c 5 "$tmp/pdf-before.pdf")" = "%PDF-" ] || fail "the downloaded artefact is not a PDF document"
pdf_size="$(wc -c <"$tmp/pdf-before.pdf" | tr -d ' ')"
ok "pdf: the ticket's document is in $bucket at $pdf_size bytes before anybody is admitted"

# The ticket read before the admission, in the exact fields the door must not change.
call GET "$public/tickets/$ticket_one"
expect_status "ticket read before the admission" 200
before_read="$(jq -c '{status, orderAmountCents, seat: .seats[0].eventSeatId}' <"$body")"
jq_assert "ticket read before the admission" '.seats[0].eventSeatId == $seat and .status == "issued" and .valid == true' \
  --argjson seat "$seat_one"
token_one="$(jq -r '.qrToken' <"$body")"

# 4. The first admission, over HTTP, with the code the ticket carries.
before_count="$(redeemed_count)"
redeem "$token_one" "puerta-m41"
expect_status "the first admission" 200
jq_assert "the first admission" '.ticketId == $ticket and .alreadyRedeemed == false' --arg ticket "$ticket_one"
jq_assert "the first admission" '(.seats | length) == 1 and .seats[0].eventSeatId == $seat' --argjson seat "$seat_one"
jq_assert "the first admission" '.startsAtLocal == $local' --arg local "$starts_at_local"
jq_assert "the first admission" '.redeemedBy == $by' --arg by "puerta-m41"
first_at="$(jq -r '.redeemedAt' <"$body")"
first_at_local="$(jq -r '.redeemedAtLocal' <"$body")"
[ -n "$first_at" ] && [ "$first_at" != "null" ] || fail "the admission answered no redeemedAt — $(snippet)"
# The venue's clock, not the machine's and not UTC: the offset the admission carries is the
# offset the ticket's own screening hour carries, and it is not zero.
[ "${first_at_local: -6}" = "$venue_offset" ] \
  || fail "the admission answered redeemedAtLocal '$first_at_local', whose offset is not the venue's $venue_offset that startsAtLocal ($starts_at_local) carries"
[ "${first_at_local: -6}" != "+00:00" ] \
  || fail "the admission answered redeemedAtLocal '$first_at_local' in UTC, not in $venue_timezone"
after_count="$(redeemed_count)"
[ "$after_count" = "$((before_count + 1))" ] \
  || fail "the admission changed the redeemed count from $before_count to $after_count, expected exactly one more"
ok "admission: 200 alreadyRedeemed:false for ticket $ticket_one, seat $seat_one, redeemedAtLocal $first_at_local (venue offset $venue_offset), redeemed rows $before_count -> $after_count"

# 5. The second presentation: not an error, not an admission, and the winner's instant.
redeem "$token_one" "puerta-m41-otra"
expect_status "the second presentation" 200
jq_assert "the second presentation" '.ticketId == $ticket and .alreadyRedeemed == true' --arg ticket "$ticket_one"
jq_assert "the second presentation" '.redeemedAt == $at' --arg at "$first_at"
jq_assert "the second presentation" '.redeemedAtLocal == $at' --arg at "$first_at_local"
# The losing call is told the *winning* door's label, not its own: the answer describes the
# stored redemption, not the request that asked about it.
jq_assert "the second presentation" '.redeemedBy == $by' --arg by "puerta-m41"
second_count="$(redeemed_count)"
[ "$second_count" = "$after_count" ] \
  || fail "the second presentation changed the redeemed count from $after_count to $second_count"
ok "second presentation: 200 alreadyRedeemed:true for the same ticket $ticket_one, redeemedAt and redeemedAtLocal byte-identical to the first answer ($first_at_local), redeemed rows unchanged at $second_count"

# 6. A forged code redeems nothing. The redemption count is read before and after, so "no
# row was written" is measured; the refusal is compared byte for byte with the answer the
# verification route gives the same code, so the door and the scanner cannot be told apart.
forged="$(tamper "$token_one")"
count_before_forged="$(redeemed_count)"
call POST "$public/tickets/verify" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg token "$forged" '{token: $token}')"
expect_status "forged code at /tickets/verify" 400
jq_assert "forged code at /tickets/verify" '.code == "invalid_token"'
cp "$body" "$tmp/refusal-verify.json"
redeem "$forged" "puerta-m41-forjada"
expect_status "forged code at /tickets/redeem" 400
jq_assert "forged code at /tickets/redeem" '.code == "invalid_token"'
cmp -s "$tmp/refusal-verify.json" "$body" \
  || fail "a forged code is refused with '$(snippet)' by the door but with '$(cat "$tmp/refusal-verify.json" | tr '\n' ' ')' by the verification route; S3 is that they are one answer"
count_after_forged="$(redeemed_count)"
[ "$count_after_forged" = "$count_before_forged" ] \
  || fail "a forged code changed the redeemed count from $count_before_forged to $count_after_forged"
ok "forged code: flipping one character of the signature answers 400 invalid_token, byte-identical to /tickets/verify, and the redeemed count stayed at $count_after_forged"

# 7. The sale is untouched by the admission: the ticket read still answers the same
# status, the same order amount and its own seat, and the PDF is still the same bytes.
call GET "$public/tickets/$ticket_one"
expect_status "ticket read after the admission" 200
after_read="$(jq -c '{status, orderAmountCents, seat: .seats[0].eventSeatId}' <"$body")"
[ "$after_read" = "$before_read" ] \
  || fail "the admission changed the ticket read from $before_read to $after_read"
call GET "$public/tickets/$ticket_one/pdf"
expect_status "pdf redirect after the admission" 302
fetch_pdf "$(header 'location')" "$tmp/pdf-after.pdf" "pdf download after the admission"
pdf_size_after="$(wc -c <"$tmp/pdf-after.pdf" | tr -d ' ')"
[ "$pdf_size_after" = "$pdf_size" ] \
  || fail "the PDF is $pdf_size_after bytes after the admission, not the $pdf_size bytes it was before"
cmp -s "$tmp/pdf-before.pdf" "$tmp/pdf-after.pdf" \
  || fail "the PDF served after the admission is not the document that was stored before it"
ok "sale untouched: the ticket read still answers $after_read, and the PDF is byte-identical at $pdf_size bytes"

# 8. Two doors at once, over HTTP. A third seat is sold so the race has a ticket nobody
# has presented, then two redemptions of it are fired together and the answers are
# compared: exactly one admits, the other is told the first instant.
call POST "$public/reservations/holds" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: m41-verify-hold-race-$unique" \
  -d "{\"eventId\":\"$event\",\"seatIds\":[\"$seat_three\"]}"
expect_status "create the racing hold" 201
race_reservation="$(jq -r '.id' <"$body")"
call POST "$public/reservations/$race_reservation/payment" -H "Idempotency-Key: m41-verify-payment-race-$unique"
expect_status "pay the racing reservation" 200
jq_assert "pay the racing reservation" '.paymentStatus == "approved" and (.ticketIds | length) == 1'
ticket_three="$(jq -r '.ticketIds[0]' <"$body")"
call GET "$public/tickets/$ticket_three"
expect_status "ticket read for the race" 200
token_three="$(jq -r '.qrToken' <"$body")"

race_dir="$tmp/race"
mkdir -p "$race_dir"
race_payload="$(jq -nc --arg token "$token_three" '{token: $token, label: "puerta-m41-carrera"}')"
race_pids=()
for n in 1 2; do
  (
    curl --silent --show-error --max-time "$curl_max_time" --connect-timeout "$curl_connect_timeout" \
      --output "$race_dir/$n.json" --write-out '%{http_code}' \
      -X POST -H 'Content-Type: application/json' -d "$race_payload" \
      "$public/tickets/redeem" >"$race_dir/$n.status" 2>"$race_dir/$n.err"
  ) &
  race_pids+=($!)
done
# Only the two curls are waited for: a bare `wait` would also wait for the API process,
# which never exits, and the gate would hang on its own server.
for pid in "${race_pids[@]}"; do
  wait "$pid" || fail "a concurrent redemption could not be completed — $(cat "$race_dir"/*.err 2>/dev/null | tr '\n' ' ')"
done

fresh=""
stale=""
for n in 1 2; do
  race_status="$(cat "$race_dir/$n.status")"
  [ "$race_status" = "200" ] \
    || fail "concurrent redemption $n answered $race_status, expected 200 — $(head -c 400 "$race_dir/$n.json" | tr '\n' ' ')"
  jq -e --arg ticket "$ticket_three" '.ticketId == $ticket' "$race_dir/$n.json" >/dev/null 2>&1 \
    || fail "concurrent redemption $n answered ticket $(jq -r '.ticketId' "$race_dir/$n.json"), expected $ticket_three"
  case "$(jq -r '.alreadyRedeemed' "$race_dir/$n.json")" in
    false) fresh="$n" ;;
    true) stale="$n" ;;
    *) fail "concurrent redemption $n answered alreadyRedeemed '$(jq -r '.alreadyRedeemed' "$race_dir/$n.json")', expected a boolean" ;;
  esac
done
[ -n "$fresh" ] && [ -n "$stale" ] \
  || fail "the two concurrent redemptions both answered alreadyRedeemed:$( [ -n "$fresh" ] && echo false || echo true ) — exactly one must admit the ticket and one must be told it already was"
winner_at="$(jq -r '.redeemedAt' "$race_dir/$fresh.json")"
loser_at="$(jq -r '.redeemedAt' "$race_dir/$stale.json")"
[ "$winner_at" = "$loser_at" ] \
  || fail "the losing door answered redeemedAt '$loser_at' while the winning door wrote '$winner_at'"
winner_at_local="$(jq -r '.redeemedAtLocal' "$race_dir/$fresh.json")"
loser_at_local="$(jq -r '.redeemedAtLocal' "$race_dir/$stale.json")"
[ "$winner_at_local" = "$loser_at_local" ] \
  || fail "the losing door answered redeemedAtLocal '$loser_at_local' while the winning door answered '$winner_at_local'"
race_count="$(redeemed_count)"
[ "$race_count" = "$((second_count + 1))" ] \
  || fail "two doors racing one ticket left the redeemed count at $race_count, expected $((second_count + 1))"
ok "two doors at once: of two concurrent redemptions of ticket $ticket_three, one wrote ($winner_at_local) and the other was told the same instant; redeemed rows $second_count -> $race_count"

# The printed summary: what was proven, in one place.
printf '\nM4.1 verification passed: the fixture through the API alone (a screening created on sale at a price in one request, listed by the catalogue in the venue'"'"'s hour) → two seats held and paid once → one ticket per seat → the first admission 200 with alreadyRedeemed:false and the venue-local instant → the second presentation 200 with alreadyRedeemed:true and the first instant, byte for byte → a forged code 400 invalid_token, identical to the verification route and writing no row → the sale and its PDF unchanged → two doors racing one ticket, exactly one admission.\n'
printf 'what this gate does not prove: the door screen at /admin/door (it is a browser surface, pinned by the web tests and by make verify-m35'"'"'s stack, not by this one), anything about who the staff member was (there are no accounts, and redeemedBy is an opaque label), that the venue-local hour is right for a venue in a zone other than Europe/Madrid, that redemption is refused before or after the screening ("still admissible" means status and screening status only, not the clock), and any PDF re-rendering — the document is deliberately immutable.\n'
