#!/usr/bin/env bash
# Idempotence and guard proof for db/seeds/dev_seed.sql, the file behind
# `make seed-dev`.
#
# It runs against a throwaway database named cinema_m3_bootstrap_seed_<pid> that
# it creates, migrates and drops, and proves:
#   1. The environment guard refuses to seed unless API_ENV=development is
#      explicitly set, and the refusal inserts nothing.
#   2. One cinema, one room, one published irregular layout (row A with three
#      seats, row B with two and one of them disabled), two films and two sessions
#      are created, with the event seats materialized from the enabled seats only.
#   3. A second run changes no row count and no recorded row.
#
# The development `cinema` database, the other containers and the
# cinema_m0_verify* databases are never touched.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
db="cinema_m3_bootstrap_seed_$$"
compose="$(if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)"
tmp="$(mktemp -d)"

fail() { printf 'seed verification failed: %s\n' "$*" >&2; exit 1; }
log() { printf '[seed test] %s\n' "$*"; }

case "$db" in cinema_m3_bootstrap_seed_[0-9]*) ;; *) fail "refusing an unexpected database name $db" ;; esac
[ -n "$compose" ] || fail "Docker Compose is required"

psql_admin() { $compose exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -U cinema -d postgres "$@"; }
psql_db() { $compose exec -T postgres psql -X -q -tA -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"; }

created=0
cleanup() {
  if [ "$created" = 1 ]; then
    psql_admin -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$db' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
    psql_admin -c "DROP DATABASE IF EXISTS \"$db\";" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

cd "$root"

# The seed runs through the bootstrap command itself, so the guard that
# `make seed-dev` relies on is the one under test.
go build -o "$tmp/bootstrap" ./apps/api/cmd/bootstrap || fail "the bootstrap command did not build"

seed() {
  # $1 is the API_ENV value; "unset" leaves the variable out entirely.
  local env_value="$1"
  if [ "$env_value" = "unset" ]; then
    env -u API_ENV DB_NAME="$db" "$tmp/bootstrap" seed-dev
  else
    env DB_NAME="$db" API_ENV="$env_value" "$tmp/bootstrap" seed-dev
  fi
}

counts() {
  psql_db -c "SELECT (SELECT count(*) FROM cinema.cinemas) || '|' ||
                     (SELECT count(*) FROM cinema.rooms) || '|' ||
                     (SELECT count(*) FROM cinema.layout_versions) || '|' ||
                     (SELECT count(*) FROM cinema.layout_rows) || '|' ||
                     (SELECT count(*) FROM cinema.layout_seats) || '|' ||
                     (SELECT count(*) FROM cinema.films) || '|' ||
                     (SELECT count(*) FROM cinema.events) || '|' ||
                     (SELECT count(*) FROM cinema.event_seats);"
}

fingerprint() {
  psql_db -c "SELECT md5(
      (SELECT string_agg(id::text || ':' || name, ',' ORDER BY id) FROM cinema.cinemas) || '|' ||
      (SELECT string_agg(id::text || ':' || name, ',' ORDER BY id) FROM cinema.rooms) || '|' ||
      (SELECT string_agg(id::text || ':' || status, ',' ORDER BY id) FROM cinema.layout_versions) || '|' ||
      (SELECT string_agg(id::text || ':' || label || ':' || position, ',' ORDER BY id) FROM cinema.layout_rows) || '|' ||
      (SELECT string_agg(id::text || ':' || number || ':' || kind || ':' || enabled, ',' ORDER BY id) FROM cinema.layout_seats) || '|' ||
      (SELECT string_agg(id::text || ':' || title, ',' ORDER BY id) FROM cinema.films) || '|' ||
      (SELECT string_agg(id::text || ':' || status, ',' ORDER BY id) FROM cinema.events) || '|' ||
      (SELECT string_agg(id::text, ',' ORDER BY id) FROM cinema.event_seats));"
}

log "creating throwaway database $db"
psql_admin -c "CREATE DATABASE \"$db\";"
created=1
log "migrating it with migrate-all"
$compose --profile tools run --rm -e DB_NAME="$db" migrate-all >/dev/null || fail "migrate-all failed"

log "the guard refuses without an explicit API_ENV=development"
for env_value in unset "" production; do
  set +e
  output="$(seed "$env_value" 2>&1)"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail "seed-dev ran with API_ENV=$env_value"
  case "$output" in *"API_ENV=development"*) ;; *) fail "the guard did not name API_ENV=development: $output" ;; esac
  [ "$(counts)" = "0|0|0|0|0|0|0|0" ] || fail "a refused seed inserted rows (API_ENV=$env_value): $(counts)"
done
log "the guard refused three times and inserted nothing"

log "first run: seeding the development data"
seed development || fail "the first seed run failed"

expected="1|1|1|2|5|2|2|8"
[ "$(counts)" = "$expected" ] || fail "expected counts $expected, got $(counts)"

# The layout is the irregular one from the task: rows with different seat counts,
# one seat disabled, and published so a session can use it.
shape="$(psql_db -c "SELECT string_agg(r.label || ':' || s.seats, ',' ORDER BY r.position) FROM cinema.layout_rows r JOIN (SELECT layout_row_id, count(*) AS seats FROM cinema.layout_seats GROUP BY layout_row_id) s ON s.layout_row_id = r.id;")"
[ "$shape" = "A:3,B:2" ] || fail "expected rows A:3,B:2, got $shape"
disabled="$(psql_db -c "SELECT count(*) FROM cinema.layout_seats WHERE NOT enabled;")"
[ "$disabled" = 1 ] || fail "expected exactly one disabled seat, got $disabled"
published="$(psql_db -c "SELECT count(*) FROM cinema.layout_versions WHERE status = 'published';")"
[ "$published" = 1 ] || fail "expected one published layout, got $published"
# The disabled seat is materialized as an event seat only for the enabled ones:
# three seats in row A, one enabled in row B, two sessions.
log "seed shape is A:3,B:2 with one disabled seat and one published layout"

before_counts="$(counts)"
before_fingerprint="$(fingerprint)"

log "second run: seeding again changes nothing"
seed development || fail "the second seed run failed"
[ "$(counts)" = "$before_counts" ] || fail "the second run changed the row counts: $before_counts -> $(counts)"
[ "$(fingerprint)" = "$before_fingerprint" ] || fail "the second run changed a recorded row"

log "the seed is idempotent: $before_counts on both runs, rows unchanged"
