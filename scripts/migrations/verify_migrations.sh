#!/bin/sh
set -eu

DB_HOST="${DB_HOST:-postgres}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-cinema}"
DB_NAME="${DB_NAME:-cinema_m0_verify}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/workspace/db/migrations}"
M1_TEST_FILE="${M1_TEST_FILE:-/workspace/tests/migrations/m1_schema.sql}"
M2_TEST_FILE="${M2_TEST_FILE:-/workspace/tests/migrations/m2_schema.sql}"
IDEMPOTENCY_TEST_FILE="${IDEMPOTENCY_TEST_FILE:-/workspace/tests/migrations/m2_idempotency.sql}"

log() { printf '[migrations] %s\n' "$*"; }
fail() { printf '[migrations] ERROR: %s\n' "$*" >&2; exit 1; }

command -v psql >/dev/null 2>&1 || fail "psql is required"
test -r "$MIGRATIONS_DIR/0001_init.up.sql" || fail "missing 0001_init.up.sql"
test -r "$MIGRATIONS_DIR/0001_init.down.sql" || fail "missing 0001_init.down.sql"
test -r "$MIGRATIONS_DIR/0002_venue_layout.up.sql" || fail "missing 0002_venue_layout.up.sql"
test -r "$MIGRATIONS_DIR/0002_venue_layout.down.sql" || fail "missing 0002_venue_layout.down.sql"
test -r "$M1_TEST_FILE" || fail "missing M1 schema test"
test -r "$MIGRATIONS_DIR/0003_reservation.up.sql" || fail "missing 0003_reservation.up.sql"
test -r "$MIGRATIONS_DIR/0003_reservation.down.sql" || fail "missing 0003_reservation.down.sql"
test -r "$M2_TEST_FILE" || fail "missing M2 schema test"
test -r "$MIGRATIONS_DIR/0004_idempotency.up.sql" || fail "missing 0004_idempotency.up.sql"
test -r "$MIGRATIONS_DIR/0004_idempotency.down.sql" || fail "missing 0004_idempotency.down.sql"
test -r "$IDEMPOTENCY_TEST_FILE" || fail "missing M2 idempotency schema test"

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

assert_versions() {
  expected="$1"
  versions="$(psql_target -tA -c "SELECT string_agg(version, ',' ORDER BY version) FROM platform.schema_migrations;")"
  test "$versions" = "$expected" || fail "expected versions $expected, got $versions"
}

assert_m1_present() {
  tables="$(psql_target -tA -c "SELECT string_agg(tablename, ',' ORDER BY tablename) FROM pg_tables WHERE schemaname = 'cinema';")"
  test "$tables" = "cinemas,event_seats,events,films,layout_rows,layout_seats,layout_versions,rooms" \
    || fail "M1 tables must survive a 0003 rollback, got $tables"
  objects="$(psql_target -tA -c "SELECT count(*) FROM pg_class WHERE relnamespace = 'cinema'::regnamespace AND relname IN ('reservation_seats_active_hold','reservation_seats_event_seat','reservations_expires_at');")"
  test "$objects" = "0" || fail "M2 indexes must be absent after rollback, got $objects"
  objects="$(psql_target -tA -c "SELECT (SELECT count(*) FROM pg_proc WHERE proname = 'release_expired_seat_hold') + (SELECT count(*) FROM pg_constraint WHERE conname = 'event_seats_id_event_key');")"
  test "$objects" = "0" || fail "M2 trigger function and event-seat key must be absent after rollback, got $objects"
}

assert_idempotency_absent() {
  objects="$(psql_target -tA -c "SELECT count(*) FROM pg_class WHERE relnamespace = 'cinema'::regnamespace AND relname IN ('idempotency_keys','idempotency_keys_expires_at');")"
  test "$objects" = "0" || fail "idempotency objects must be absent after 0004 rollback, got $objects"
  # The 0003 hold schema must survive a 0004 rollback untouched.
  objects="$(psql_target -tA -c "SELECT (SELECT count(*) FROM pg_class WHERE relnamespace = 'cinema'::regnamespace AND relname IN ('reservations','reservation_seats','reservation_seats_active_hold','reservation_seats_event_seat','reservations_expires_at')) + (SELECT count(*) FROM pg_proc WHERE proname = 'release_expired_seat_hold');")"
  test "$objects" = "6" || fail "the 0003 hold schema must survive a 0004 rollback, got $objects of 6 objects"
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

log "applying 0002_venue_layout"
psql_target -q -f "$MIGRATIONS_DIR/0002_venue_layout.up.sql"
psql_target -q -f "$M1_TEST_FILE"
log "rolling back 0002_venue_layout"
psql_target -q -f "$MIGRATIONS_DIR/0002_venue_layout.down.sql"
assert_current
schema_count="$(psql_target -tA -c "SELECT count(*) FROM pg_namespace WHERE nspname = 'cinema';")"
test "$schema_count" = "0" || fail "cinema schema must be absent after rollback"
log "reapplying 0002_venue_layout"
psql_target -q -f "$MIGRATIONS_DIR/0002_venue_layout.up.sql"
psql_target -q -f "$M1_TEST_FILE"
log "M0 and M1 up/down/up verification passed"

log "applying 0003_reservation"
psql_target -q -f "$MIGRATIONS_DIR/0003_reservation.up.sql"
psql_target -q -f "$M2_TEST_FILE"
assert_versions '0001_init,0002_venue_layout,0003_reservation'
log "rolling back 0003_reservation"
psql_target -q -f "$MIGRATIONS_DIR/0003_reservation.down.sql"
assert_versions '0001_init,0002_venue_layout'
assert_m1_present
psql_target -q -f "$M1_TEST_FILE"
log "reapplying 0003_reservation"
psql_target -q -f "$MIGRATIONS_DIR/0003_reservation.up.sql"
psql_target -q -f "$M2_TEST_FILE"
log "M0, M1 and M2 up/down/up verification passed"

log "applying 0004_idempotency"
psql_target -q -f "$MIGRATIONS_DIR/0004_idempotency.up.sql"
psql_target -q -f "$IDEMPOTENCY_TEST_FILE"
assert_versions '0001_init,0002_venue_layout,0003_reservation,0004_idempotency'
log "rolling back 0004_idempotency"
psql_target -q -f "$MIGRATIONS_DIR/0004_idempotency.down.sql"
assert_versions '0001_init,0002_venue_layout,0003_reservation'
assert_idempotency_absent
psql_target -q -f "$M2_TEST_FILE"
log "reapplying 0004_idempotency"
psql_target -q -f "$MIGRATIONS_DIR/0004_idempotency.up.sql"
psql_target -q -f "$IDEMPOTENCY_TEST_FILE"
assert_versions '0001_init,0002_venue_layout,0003_reservation,0004_idempotency'
log "M0, M1, M2 and M2 idempotency up/down/up verification passed"
