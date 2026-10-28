#!/bin/sh
set -eu

DB_HOST="${DB_HOST:-postgres}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-cinema}"
DB_NAME="${DB_NAME:-cinema_m0_verify}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/workspace/db/migrations}"

log() { printf '[migrations] %s\n' "$*"; }
fail() { printf '[migrations] ERROR: %s\n' "$*" >&2; exit 1; }

command -v psql >/dev/null 2>&1 || fail "psql is required"
test -r "$MIGRATIONS_DIR/0001_init.up.sql" || fail "missing 0001_init.up.sql"
test -r "$MIGRATIONS_DIR/0001_init.down.sql" || fail "missing 0001_init.down.sql"

psql_admin() {
  psql -X -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d postgres "$@"
}

psql_target() {
  psql -X -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" "$@"
}

cleanup() {
  psql_admin -q -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DB_NAME' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  psql_admin -q -c "DROP DATABASE IF EXISTS \"$DB_NAME\";" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

case "$DB_NAME" in
  cinema_m0_verify*) ;;
  *) fail "DB_NAME must begin with cinema_m0_verify" ;;
esac

log "creating isolated verification database $DB_NAME"
cleanup
psql_admin -q -c "CREATE DATABASE \"$DB_NAME\";"

assert_absent() {
  count="$(psql_target -tA -c "SELECT count(*) FROM pg_namespace WHERE nspname = 'platform';")"
  test "$count" = "0" || fail "platform schema must be absent after rollback"
}

assert_current() {
  version="$(psql_target -tA -c "SELECT version FROM platform.schema_migrations;")"
  test "$version" = "0001_init" || fail "expected version 0001_init, got $version"
  tables="$(psql_target -tA -c "SELECT string_agg(tablename, ',' ORDER BY tablename) FROM pg_tables WHERE schemaname = 'platform';")"
  test "$tables" = "schema_migrations,settings" || fail "unexpected platform tables: $tables"
}

log "applying 0001_init"
psql_target -q -f "$MIGRATIONS_DIR/0001_init.up.sql"
assert_current

log "rolling back 0001_init"
psql_target -q -f "$MIGRATIONS_DIR/0001_init.down.sql"
assert_absent

log "reapplying 0001_init"
psql_target -q -f "$MIGRATIONS_DIR/0001_init.up.sql"
assert_current

log "up/down/up verification passed"
