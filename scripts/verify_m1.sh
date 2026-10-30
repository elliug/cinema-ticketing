#!/usr/bin/env bash
# M1 functional verification against an isolated temporary database and a real
# API process. Proves the local-administration guard and the venue/layout rules
# through the public HTTP interface only.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
db="cinema_m1_smoke_$$"
token="m1-smoke-token-$$"
tmp="$(mktemp -d)"
api_pid=""
compose="$(if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)"
base="http://127.0.0.1:18080/api/v1/admin"

fail() { printf 'M1 verification failed: %s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -z "$api_pid" ] || kill "$api_pid" 2>/dev/null || true
  wait "$api_pid" 2>/dev/null || true
  $compose exec -T postgres dropdb -U cinema --if-exists "$db" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

case "$db" in cinema_m1_smoke_[0-9]*) ;; *) fail "refusing to drop an unexpected database name" ;; esac

cd "$root"
# On a fresh volume the container is still running initdb right after `up -d`,
# and its temporary server makes readiness flap, so `createdb` can fail on a
# missing socket. Wait for the declared healthcheck before touching the
# database; compose v2 is required for `--wait`.
$compose up -d --wait postgres >/dev/null \
  || fail "postgres did not become healthy"
$compose exec -T postgres createdb -U cinema "$db"
for migration in 0001_init 0002_venue_layout; do
  $compose exec -T postgres psql -q -X -v ON_ERROR_STOP=1 -U cinema -d "$db" \
    < "db/migrations/$migration.up.sql" || fail "migration $migration did not apply"
done

# api <expect-status> <method> <path> [body]
api() {
  local expect="$1" method="$2" path="$3" body="${4:-}" auth="${5:-}"
  local args=(--silent --show-error -o "$tmp/body.json" -w '%{http_code}' -X "$method" "$base$path")
  [ -z "$body" ] || args+=(-H 'Content-Type: application/json' -d "$body")
  [ -z "$auth" ] || args+=(-H "Authorization: Bearer $auth")
  local status
  status="$(curl "${args[@]}")"
  [ "$status" = "$expect" ] || { cat "$tmp/body.json" >&2; fail "$method $path returned $status, expected $expect"; }
}

id_of() { jq -r '.id' <"$tmp/body.json"; }

go build -o "$tmp/api" ./apps/api/cmd/api || fail "the API did not build"

# Run the built binary directly, so the recorded PID is the server itself and
# shutdown cannot leave an orphan holding the port.
API_ENV=development \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN="$token" \
API_DATABASE_URL="postgres://cinema:cinema_local_only@127.0.0.1:5432/$db?sslmode=disable" \
API_ADDR=127.0.0.1:18080 \
"$tmp/api" >"$tmp/api.log" 2>&1 &
api_pid=$!

for _ in $(seq 1 120); do
  if grep -q 'address already in use' "$tmp/api.log" 2>/dev/null; then
    cat "$tmp/api.log" >&2
    fail "port 18080 is already in use; stop the process holding it"
  fi
  curl --fail --silent http://127.0.0.1:18080/health/live >/dev/null 2>&1 && break
  sleep 0.25
done
grep -q 'local administrative venue API mounted' "$tmp/api.log" \
  || { cat "$tmp/api.log" >&2; fail "the local administrative API was not mounted"; }

# An M1 schema has no reservation tables, so the reservation surface must stay
# unmounted and the expiry worker must not start: one clear line, and a 404 on the
# public route instead of a failure logged on every tick.
grep -q 'reservation schema not present; reservation surface disabled' "$tmp/api.log" \
  || { cat "$tmp/api.log" >&2; fail "the reservation surface must be disabled without its schema"; }
if grep -q 'expiry sweep failed' "$tmp/api.log"; then
  cat "$tmp/api.log" >&2
  fail "the expiry worker must not run against a schema that lacks the reservation tables"
fi
status="$(curl --silent --output /dev/null --write-out '%{http_code}' -X POST \
  -H 'Content-Type: application/json' -H "Idempotency-Key: m1-schema-probe" \
  -d '{"eventId":"x","seatIds":[1]}' http://127.0.0.1:18080/api/v1/reservations/holds)"
[ "$status" = "404" ] || fail "the public reservation route returned $status, expected 404 without the reservation schema"

# S7/S9: the administrative subtree is unreachable without the development token.
api 404 POST /cinemas '{"name":"Guarded","timezone":"UTC"}'
api 404 POST /cinemas '{"name":"Guarded","timezone":"UTC"}' wrong-token

# S1/S11: irregular geometry, one row with two seats and one with one seat.
api 201 POST /cinemas '{"name":"Cine Centro","timezone":"Europe/Madrid","address":"Calle 1"}' "$token"
cinema="$(id_of)"
api 201 POST /rooms "{\"cinemaId\":\"$cinema\",\"name\":\"Sala 1\"}" "$token"
room="$(id_of)"
api 201 POST /layouts "{\"roomId\":\"$room\"}" "$token"
layout="$(id_of)"
api 200 PUT "/layouts/$layout/rows" '{"rows":[{"label":"A","position":1,"seats":[{"number":"1","position":1,"kind":"standard","enabled":true},{"number":"2","position":2,"kind":"accessible","enabled":true}]},{"label":"B","position":2,"seats":[{"number":"1","position":1,"kind":"standard","enabled":true},{"number":"2","position":2,"kind":"companion","enabled":false}]}]}' "$token"
# Invalid input leaves stored geometry unchanged.
api 400 PUT "/layouts/$layout/rows" '{"rows":[{"label":"A","position":1,"seats":[{"number":"1","position":1,"kind":"standard"},{"number":"1","position":2,"kind":"standard"}]}]}' "$token"
jq -e '[.rows[] | .seats | length] == [2,2]' <(curl --silent -H "Authorization: Bearer $token" "$base/layouts/$layout") >/dev/null \
  || fail "duplicate seat number changed stored geometry"

# S2/S3: publication freezes geometry; a new version is required to edit.
api 200 POST "/layouts/$layout/publish" '' "$token"
api 409 PUT "/layouts/$layout/rows" '{"rows":[{"label":"A","position":1,"seats":[{"number":"9","position":1,"kind":"standard"}]}]}' "$token"
api 409 POST "/layouts/$layout/publish" '' "$token"
api 201 POST "/layouts/$layout/clone" '' "$token"
clone="$(id_of)"
api 200 PUT "/layouts/$clone/rows" '{"rows":[{"label":"A","position":1,"seats":[{"number":"1","position":1,"kind":"standard"}]}]}' "$token"

# S4/S5/S6/S11: an event snapshots the published layout and materializes enabled seats only.
api 201 POST /films '{"title":"Film","runtimeMinutes":95,"rating":"PG"}' "$token"
film="$(id_of)"
api 409 POST /events "{\"roomId\":\"$room\",\"filmId\":\"$film\",\"layoutVersionId\":\"$clone\",\"startsAt\":\"2030-05-01T18:00:00Z\"}" "$token"
api 201 POST /events "{\"roomId\":\"$room\",\"filmId\":\"$film\",\"layoutVersionId\":\"$layout\",\"startsAt\":\"2030-05-01T18:00:00Z\"}" "$token"
event="$(id_of)"
api 200 GET "/events/$event/seats" '' "$token"
jq -e '.seats | length == 3' <"$tmp/body.json" >/dev/null \
  || fail "expected three materialized event seats (four seats exist, one is disabled)"
jq -e '[.seats[].state] | unique == ["available"]' <"$tmp/body.json" >/dev/null || fail "event seats must start available"

# A rejected event leaves no partial data, and the frozen geometry is unchanged.
api 409 POST /events "{\"roomId\":\"$room\",\"filmId\":\"$film\",\"layoutVersionId\":\"$clone\",\"startsAt\":\"2030-05-02T18:00:00Z\"}" "$token"
jq -e '[.rows[] | .seats | length] == [2,2]' <(curl --silent -H "Authorization: Bearer $token" "$base/layouts/$layout") >/dev/null \
  || fail "event creation altered the published layout"

# S10: cinemas, rooms and layout versions are queryable, and a missing cinema is 404.
api 200 GET /cinemas '' "$token"
jq -e --arg id "$cinema" '[.cinemas[] | select(.id == $id)] | length == 1' <"$tmp/body.json" >/dev/null \
  || fail "the created cinema is not listed"
api 200 GET "/cinemas/$cinema/rooms" '' "$token"
jq -e --arg id "$room" '[.rooms[] | select(.id == $id)] | length == 1' <"$tmp/body.json" >/dev/null \
  || fail "the created room is not listed"
api 200 GET "/rooms/$room/layouts" '' "$token"
jq -e --arg published "$layout" --arg draft "$clone" \
  '[.layouts[] | select(.id == $published and .status == "published")] | length == 1' <"$tmp/body.json" >/dev/null \
  || fail "the published layout version is not listed as published"
jq -e --arg draft "$clone" '[.layouts[] | select(.id == $draft and .status == "draft")] | length == 1' <"$tmp/body.json" >/dev/null \
  || fail "the cloned layout version is not listed as draft"
api 404 GET "/cinemas/00000000-0000-0000-0000-000000000000/rooms" '' "$token"

printf 'M1 verification passed: guard denies by default, irregular rows publish, and events materialize enabled seats only.\n'
