#!/usr/bin/env bash
# M2 end-to-end verification: the accepted concurrency contract against a real
# API process, an isolated database and an isolated Redis database.
#
# It proves, through the public HTTP interface only:
#   1. The availability read serves the layout projection (row label, seat
#      number, seat kind) of the seeded irregular layout, in row-position and
#      seat-position order, with each row's own seat count.
#   2. 16 contenders for one event seat produce exactly one 201 and fifteen 409s,
#      and every loser is a seat conflict.
#   3. Availability reports the winner's seat as held.
#   4. The in-process expiry worker releases a hold whose durable deadline passed.
#   5. The gated worker-recovery evidence: a sweep killed mid-transaction leaves no
#      partial state and the next sweep completes the work.
#
# Everything it creates is disposable: the database name is unique to this run,
# Redis work happens in a separate logical database, and nothing here touches the
# development `cinema` database.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
db="cinema_m2_smoke_$$"
token="m2-smoke-token-$$"
redis_db="${M2_REDIS_DB:-9}"
port=18081
tmp="$(mktemp -d)"
api_pid=""
compose="$(if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)"
base="http://127.0.0.1:$port"
admin="$base/api/v1/admin"
dburl="postgres://cinema:cinema_local_only@127.0.0.1:5432/$db?sslmode=disable"
redisurl="redis://127.0.0.1:6379/$redis_db"
hold_ttl=60s
expiry_interval=2s

fail() { printf 'M2 verification failed: %s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -z "$api_pid" ] || kill "$api_pid" 2>/dev/null || true
  [ -z "$api_pid" ] || wait "$api_pid" 2>/dev/null || true
  $compose exec -T postgres dropdb -U cinema --if-exists "$db" >/dev/null 2>&1 || true
  # The reservation keys live in a dedicated logical database, and it is flushed only when
  # this run claimed it: a run that refused to touch an index must leave it exactly as it
  # found it, refusals included.
  if [ "$redis_owned" = 1 ]; then
    $compose exec -T redis redis-cli -n "$redis_db" flushdb >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
# The flush below is conditional on this. The guard refuses an index that already holds
# data, and exiting through such a refusal must leave that index exactly as it was found.
redis_owned=0
trap cleanup EXIT INT TERM

case "$db" in cinema_m2_smoke_[0-9]*) ;; *) fail "refusing to drop an unexpected database name" ;; esac
[ -n "$compose" ] || fail "Docker Compose is required"

psql_db() {
  $compose exec -T postgres psql -q -X -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"
}

cd "$root"

# api <expect-status> <method> <path> [body] [auth]
api() {
  local expect="$1" method="$2" path="$3" body="${4:-}" auth="${5:-}"
  local args=(--silent --show-error -o "$tmp/body.json" -w '%{http_code}' -X "$method" "$admin$path")
  [ -z "$body" ] || args+=(-H 'Content-Type: application/json' -d "$body")
  [ -z "$auth" ] || args+=(-H "Authorization: Bearer $auth")
  local status
  status="$(curl "${args[@]}")"
  [ "$status" = "$expect" ] || { cat "$tmp/body.json" >&2; fail "$method $path returned $status, expected $expect"; }
}

id_of() { jq -r '.id' <"$tmp/body.json"; }

# Dependencies, then a fresh database with the full M2 schema.
$compose up -d --wait postgres redis >/dev/null || fail "the compose dependencies did not become healthy"

# This script flushes one Redis logical database on cleanup, so it must own it — and it can
# only claim what it could actually read: an unreadable dbsize is not proof of an empty
# index, and treating it as such is how a refused run destroys data on the way out.
# A developer using the same database for something else loses nothing.
dbsize="$($compose exec -T redis redis-cli -n "$redis_db" dbsize 2>/dev/null | tr -d '\r' || true)"
case "$dbsize" in
  0) redis_owned=1 ;;
  "") fail "could not read the size of Redis logical database $redis_db, so this script cannot prove it owns it; is Redis reachable?" ;;
  *) fail "Redis database $redis_db already holds $dbsize keys; set M2_REDIS_DB to a free database" ;;
esac
$compose exec -T postgres createdb -U cinema "$db" || fail "could not create the throwaway database $db"
# The schema this gate builds must be the schema the binary it starts actually runs on, and
# that binary is built from this checkout. Enumerating an M2-era prefix here made the gate
# lie: once the sweep's own queries referenced a checkout table (0005), this database had no
# such table, so the candidate lock failed with "relation cinema.payment_attempts does not
# exist" and the killed-worker test reported "the sweep never blocked on the seat row lock".
# An instrument error was wearing the clothes of a product regression, and it cost issue #82 a
# shipped guard. The runner and the directory are the two sources of truth, exactly as in
# scripts/verify_m3.sh: the runner mounts the directory, so no migration has to be written
# into a list in this script before the code that uses it can be verified.
migration_output="$($compose --profile tools run --rm -e DB_NAME="$db" migrate-all 2>&1)" \
  || { printf '%s\n' "$migration_output" >&2; fail "the migration runner failed on $db"; }
applied_versions="$(psql_db -tA -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations")"
expected_versions=""
for file in "$root"/db/migrations/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in $root/db/migrations"
  name="$(basename "$file" .up.sql)"
  expected_versions="${expected_versions:+$expected_versions,}$name"
done
[ "$applied_versions" = "$expected_versions" ] \
  || fail "the migration runner recorded [$applied_versions], expected [$expected_versions]"
printf 'M2 schema: every migration on disk applied to %s (%s)\n' "$db" "$applied_versions"

# S8 worker-recovery evidence. It runs before the API starts: its fixture is an
# elapsed hold that the running worker would otherwise sweep out from under it.
M2_RECOVERY_DATABASE_URL="$dburl" \
  go test -count=1 -v -run 'TestExpirySweepRecoversFromAKilledWorker|TestExpirySweepLeavesAHoldAPaymentIsPayingFor|TestPostgresStoreClassifiesAnUnknownReference|TestListSeatAvailabilityProjectsTheSeededLayout' \
  ./apps/api/internal/reservation/ 2>&1 | tee "$tmp/recovery.log"
grep -q -- '--- PASS: TestExpirySweepRecoversFromAKilledWorker' "$tmp/recovery.log" \
  || fail "the killed-worker recovery evidence did not pass"
grep -q -- '--- PASS: TestExpirySweepLeavesAHoldAPaymentIsPayingFor' "$tmp/recovery.log" \
  || fail "the payment guard on the expiry sweep did not pass"
grep -q -- '--- PASS: TestListSeatAvailabilityProjectsTheSeededLayout' "$tmp/recovery.log" \
  || fail "the availability projection did not pass against PostgreSQL"

go build -o "$tmp/api" ./apps/api/cmd/api || fail "the API did not build"

# Run the built binary directly, so the recorded PID is the server itself and
# shutdown cannot leave an orphan holding the port.
API_ENV=development \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN="$token" \
API_DATABASE_URL="$dburl" \
API_REDIS_URL="$redisurl" \
API_ADDR="127.0.0.1:$port" \
API_HOLD_TTL="$hold_ttl" \
API_EXPIRY_INTERVAL="$expiry_interval" \
API_EXPIRY_BATCH=10 \
"$tmp/api" >"$tmp/api.log" 2>&1 &
api_pid=$!

for _ in $(seq 1 120); do
  if grep -q 'address already in use' "$tmp/api.log" 2>/dev/null; then
    cat "$tmp/api.log" >&2
    fail "port $port is already in use; stop the process holding it"
  fi
  curl --fail --silent "http://127.0.0.1:$port/health/live" >/dev/null 2>&1 && break
  sleep 0.25
done
grep -q 'local administrative venue API mounted' "$tmp/api.log" \
  || { cat "$tmp/api.log" >&2; fail "the local administrative API was not mounted"; }
grep -q 'public reservation API mounted' "$tmp/api.log" \
  || { cat "$tmp/api.log" >&2; fail "the public reservation API was not mounted"; }
grep -q 'durable expiry sweep started' "$tmp/api.log" \
  || { cat "$tmp/api.log" >&2; fail "the durable expiry worker did not start"; }

# An irregular published layout: a two-seat row, a shorter row with a disabled
# seat, and a third row whose position skips a number.
api 201 POST /cinemas '{"name":"Cine Concurrencia","timezone":"Europe/Madrid","address":"Calle 2"}' "$token"
cinema="$(id_of)"
api 201 POST /rooms "{\"cinemaId\":\"$cinema\",\"name\":\"Sala M2\"}" "$token"
room="$(id_of)"
api 201 POST /layouts "{\"roomId\":\"$room\"}" "$token"
layout="$(id_of)"
api 200 PUT "/layouts/$layout/rows" '{"rows":[{"label":"A","position":1,"seats":[{"number":"1","position":1,"kind":"standard","enabled":true},{"number":"2","position":2,"kind":"accessible","enabled":true}]},{"label":"B","position":2,"seats":[{"number":"1","position":1,"kind":"companion","enabled":true},{"number":"2","position":2,"kind":"standard","enabled":false}]},{"label":"D","position":4,"seats":[{"number":"1","position":1,"kind":"standard","enabled":true}]}]}' "$token"
api 200 POST "/layouts/$layout/publish" '' "$token"
api 201 POST /films '{"title":"Concurrency","runtimeMinutes":101,"rating":"PG"}' "$token"
film="$(id_of)"
api 201 POST /events "{\"roomId\":\"$room\",\"filmId\":\"$film\",\"layoutVersionId\":\"$layout\",\"startsAt\":\"2031-05-01T18:00:00Z\"}" "$token"
event="$(id_of)"

api 200 GET "/events/$event/seats" '' "$token"
jq -e '.seats | length == 4' <"$tmp/body.json" >/dev/null \
  || fail "expected four materialized event seats (five exist, one is disabled)"
seat_ids=()
while IFS= read -r seat; do seat_ids+=("$seat"); done < <(jq -r '.seats[].id' <"$tmp/body.json")
contended="${seat_ids[0]}"
sweep_seat="${seat_ids[1]}"

# S9: the public availability read serves the layout projection the seat map
# renders, so the browser draws this irregular layout and not a flat guess. The
# seeded rows are A (two seats), B (two seats, one disabled and therefore never
# materialized) and D (one seat, with position 4 skipping C). Row labels are
# asserted in the order they arrive, which is row-position order, and the per-row
# counts prove each row keeps its own seats instead of being padded into a
# rectangle. A projection regression fails the milestone here.
curl --silent --show-error "$base/api/v1/events/$event/seats" >"$tmp/projection.json"
jq -e '(.seats | length) == 4' <"$tmp/projection.json" >/dev/null \
  || { cat "$tmp/projection.json" >&2; fail "the public availability read does not serve four seats"; }
jq -e '[.seats[].rowLabel] == ["A","A","B","D"]' <"$tmp/projection.json" >/dev/null \
  || { cat "$tmp/projection.json" >&2; fail "the availability read does not project the row labels in row-position order"; }
jq -e '[.seats[].seatNumber] == ["1","2","1","1"]' <"$tmp/projection.json" >/dev/null \
  || { cat "$tmp/projection.json" >&2; fail "the availability read does not project the seat numbers in seat-position order"; }
jq -e '[.seats[].kind] == ["standard","accessible","companion","standard"]' <"$tmp/projection.json" >/dev/null \
  || { cat "$tmp/projection.json" >&2; fail "the availability read does not project the seat kinds"; }
jq -e '[.seats[]] | group_by(.rowLabel) | map({key: .[0].rowLabel, value: length}) | from_entries == {"A":2,"B":1,"D":1}' \
  <"$tmp/projection.json" >/dev/null \
  || { cat "$tmp/projection.json" >&2; fail "the availability read does not report the seeded seats per row (expected the two-seat row A and row B without its disabled seat)"; }
jq -e 'all(.seats[]; has("id") and has("layoutSeatId") and has("state") and has("held") and has("rowLabel") and has("seatNumber") and has("kind"))' \
  <"$tmp/projection.json" >/dev/null \
  || { cat "$tmp/projection.json" >&2; fail "the availability read dropped a field: the projection must be additive"; }

# S7/S8: the accepted contract, parameterized with the seeded identifiers.
M2_API_URL="$base" M2_EVENT_ID="$event" M2_SEAT_IDS="$contended" \
  go test -tags acceptance_m2 -count=1 -v ./tests/concurrency/ 2>&1 | tee "$tmp/concurrency.log"
grep -q -- '--- PASS: TestOverlappingHoldsAllowExactlyOneWinner' "$tmp/concurrency.log" \
  || fail "the accepted concurrency contract did not pass"

# The loser responses are conflicts: a fresh contender for the same seat is a 409
# seat conflict, not a success and not a retryable failure.
status="$(curl --silent --show-error -o "$tmp/contender.json" -w '%{http_code}' \
  -X POST "$base/api/v1/reservations/holds" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: m2-verification-loser-$$" \
  -d "{\"eventId\":\"$event\",\"seatIds\":[\"$contended\"]}")"
[ "$status" = "409" ] || { cat "$tmp/contender.json" >&2; fail "a losing contender returned $status, expected 409"; }
jq -e '.code == "seat_unavailable"' <"$tmp/contender.json" >/dev/null \
  || { cat "$tmp/contender.json" >&2; fail "a losing contender did not report seat_unavailable"; }

# S10: availability reports the winner's seat as held, and the untouched seats as
# free, so the seat selection UI can refetch after a conflict.
curl --silent --show-error "$base/api/v1/events/$event/seats" >"$tmp/availability.json"
jq -e --arg seat "$contended" '[.seats[] | select(.id == ($seat | tonumber)) | .held] == [true]' \
  <"$tmp/availability.json" >/dev/null \
  || { cat "$tmp/availability.json" >&2; fail "availability does not report the winner's seat as held"; }
jq -e --arg seat "$contended" '[.seats[] | select(.id != ($seat | tonumber)) | .held] | any | not' \
  <"$tmp/availability.json" >/dev/null \
  || { cat "$tmp/availability.json" >&2; fail "availability reports an uncontended seat as held"; }

# S6: the running worker cleans up a hold whose durable deadline already passed.
# The deadline is backdated with the database clock; only a sweep that runs inside
# the process and compares against a value at least that recent can expire it.
status="$(curl --silent --show-error -o "$tmp/sweep-hold.json" -w '%{http_code}' \
  -X POST "$base/api/v1/reservations/holds" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: m2-verification-sweep-$$" \
  -d "{\"eventId\":\"$event\",\"seatIds\":[\"$sweep_seat\"]}")"
[ "$status" = "201" ] || { cat "$tmp/sweep-hold.json" >&2; fail "the sweep fixture hold returned $status, expected 201"; }
sweep_hold="$(jq -r '.id' <"$tmp/sweep-hold.json")"
psql_db -c "UPDATE cinema.reservations SET expires_at = now() - interval '1 hour' WHERE id = '$sweep_hold'" >/dev/null \
  || fail "could not backdate the sweep fixture hold"

expired=""
for _ in $(seq 1 60); do
  expired="$(psql_db -tA -c "SELECT status || ':' || (SELECT count(*) FROM cinema.reservation_seats s WHERE s.reservation_id = r.id AND s.released_at IS NOT NULL) FROM cinema.reservations r WHERE r.id = '$sweep_hold'")"
  [ "$expired" = "expired:1" ] && break
  sleep 0.5
done
[ "$expired" = "expired:1" ] \
  || fail "the running expiry worker did not expire the elapsed hold and release its seat (state $expired)"

curl --silent --show-error "$base/api/v1/events/$event/seats" >"$tmp/availability.json"
jq -e --arg seat "$sweep_seat" '[.seats[] | select(.id == ($seat | tonumber)) | .held] == [false]' \
  <"$tmp/availability.json" >/dev/null \
  || { cat "$tmp/availability.json" >&2; fail "availability still reports the expired hold's seat as held"; }

printf 'M2 verification passed: the availability read projects the irregular layout, one winner in 16 contenders, losers conflict, availability follows the winner, the worker expires an elapsed hold, and a killed sweep recovers.\n'
