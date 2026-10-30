#!/usr/bin/env bash
# Idempotence proof for scripts/migrations/apply_all.sh, the target behind
# `make migrate-all`.
#
# It runs against a throwaway database named cinema_m3_bootstrap_migrate_<pid>
# that it creates and drops, and proves:
#   1. An empty database with no schema at all is brought to the full migration
#      chain in version order.
#   2. A second run applies nothing and changes no row: the recorded versions and
#      their applied_at timestamps are identical.
#
# The development `cinema` database, the other containers and the
# cinema_m0_verify* databases are never touched.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
db="cinema_m3_bootstrap_migrate_$$"
compose="$(if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)"

fail() { printf 'migrate-all verification failed: %s\n' "$*" >&2; exit 1; }
log() { printf '[migrate-all test] %s\n' "$*"; }

case "$db" in cinema_m3_bootstrap_migrate_[0-9]*) ;; *) fail "refusing an unexpected database name $db" ;; esac
[ -n "$compose" ] || fail "Docker Compose is required"

psql_admin() { $compose exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -U cinema -d postgres "$@"; }
psql_db() { $compose exec -T postgres psql -X -q -tA -v ON_ERROR_STOP=1 -U cinema -d "$db" "$@"; }

created=0
cleanup() {
  if [ "$created" = 1 ]; then
    psql_admin -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$db' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
    psql_admin -c "DROP DATABASE IF EXISTS \"$db\";" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

cd "$root"

# The expected chain is derived from the migration directory, never from a literal
# list: every NNNN_name.up.sql the runner can see has to be recorded in
# platform.schema_migrations, otherwise the chain and the database disagree. A new
# migration therefore cannot leave this test asserting yesterday's chain.
#
# The glob sorts by filename, which is version order because the numeric prefix is
# zero-padded, and it is the same order scripts/migrations/apply_all.sh applies.
expected_versions=""
expected_count=0
for file in db/migrations/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in db/migrations"
  name="$(basename "$file" .up.sql)"
  expected_versions="${expected_versions:+$expected_versions,}$name"
  expected_count=$((expected_count + 1))
done
[ "$expected_count" -gt 0 ] || fail "the expected migration chain is empty"
log "expected migration chain: $expected_versions ($expected_count versions)"

# apply_all runs inside the postgres image, where psql and the migration files
# mounted read-only are available; DB_NAME points it at the throwaway database.
apply_all() { $compose --profile tools run --rm -e DB_NAME="$db" migrate-all; }

versions() {
  psql_db -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations;"
}
record_count() { psql_db -c "SELECT count(*) FROM platform.schema_migrations;"; }
applied_at() { psql_db -c "SELECT string_agg(version || '@' || applied_at::text, ',' ORDER BY version) FROM platform.schema_migrations;"; }

log "creating throwaway database $db"
psql_admin -c "CREATE DATABASE \"$db\";"
created=1

[ "$(psql_db -c "SELECT count(*) FROM pg_namespace WHERE nspname IN ('platform','cinema');")" = 0 ] \
  || fail "the throwaway database must start with no schema"

log "first run: applying the whole chain to an empty database"
first="$(apply_all 2>&1)" || { printf '%s\n' "$first"; fail "the first run failed"; }
printf '%s\n' "$first"

[ "$(versions)" = "$expected_versions" ] || fail "expected versions $expected_versions, got $(versions)"
[ "$(record_count)" = "$expected_count" ] || fail "expected $expected_count recorded migrations, got $(record_count)"
case "$first" in *"applied $expected_count migration(s), $expected_count recorded"*) ;; *) fail "the first run did not report $expected_count applied migrations" ;; esac

before_versions="$(versions)"
before_records="$(record_count)"
before_applied_at="$(applied_at)"

log "second run: nothing is missing, so nothing is applied"
second="$(apply_all 2>&1)" || { printf '%s\n' "$second"; fail "the second run failed"; }
printf '%s\n' "$second"

case "$second" in *"applied 0 migration(s), $expected_count recorded"*) ;; *) fail "the second run did not report 0 applied migrations" ;; esac
[ "$(versions)" = "$before_versions" ] || fail "the second run changed the recorded versions"
[ "$(record_count)" = "$before_records" ] || fail "the second run changed the record count"
[ "$(applied_at)" = "$before_applied_at" ] \
  || fail "the second run rewrote a recorded migration, so it ran one again"

log "migrate-all applied $before_records migrations once and changed nothing on the second run, so a database already at $expected_versions stays a no-op"
