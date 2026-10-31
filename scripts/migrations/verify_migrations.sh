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
EXPIRY_TEST_FILE="${EXPIRY_TEST_FILE:-/workspace/tests/migrations/m2_expiry.sql}"
CHECKOUT_TEST_FILE="${CHECKOUT_TEST_FILE:-/workspace/tests/migrations/m3_checkout.sql}"
OPEN_ATTEMPT_TEST_FILE="${OPEN_ATTEMPT_TEST_FILE:-/workspace/tests/migrations/m3_one_open_attempt.sql}"
TIMEZONE_TEST_FILE="${TIMEZONE_TEST_FILE:-/workspace/tests/migrations/m4_timezone_backfill.sql}"

log() { printf '[migrations] %s\n' "$*"; }
fail() { printf '[migrations] ERROR: %s\n' "$*" >&2; exit 1; }

command -v psql >/dev/null 2>&1 || fail "psql is required"

# The version pairs are derived from the directory, never enumerated here. A version
# written down in this script is a second place that has to be edited before the migration
# it does not name is verified at all, and the directory is what actually gets applied; a
# migration on disk with only one direction is a broken chain, so both files are required.
for file in "$MIGRATIONS_DIR"/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in $MIGRATIONS_DIR"
  name="$(basename "$file" .up.sql)"
  test -r "$MIGRATIONS_DIR/$name.down.sql" || fail "missing $name.down.sql"
done

test -r "$M1_TEST_FILE" || fail "missing M1 schema test"
test -r "$M2_TEST_FILE" || fail "missing M2 schema test"
test -r "$IDEMPOTENCY_TEST_FILE" || fail "missing M2 idempotency schema test"
test -r "$EXPIRY_TEST_FILE" || fail "missing M2 expiry sweep test"
test -r "$CHECKOUT_TEST_FILE" || fail "missing M3 checkout schema test"
test -r "$OPEN_ATTEMPT_TEST_FILE" || fail "missing M3 one-open-attempt schema test"
test -r "$TIMEZONE_TEST_FILE" || fail "missing M4.1 timezone backfill schema test"

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

# The expected chain up to one version, derived from the directory. Each cycle asserts the
# prefix that ends at the version it is staring at, so the assertion stays exactly as strong
# as a literal list — every version up to that point, in order, and nothing after it — while
# the next migration only has to add its own cycle, never to edit a list in this one.
chain_upto() {
  wanted="$1"
  chain=""
  for file in "$MIGRATIONS_DIR"/*.up.sql; do
    name="$(basename "$file" .up.sql)"
    chain="${chain:+$chain,}$name"
    if [ "$name" = "$wanted" ]; then break; fi
  done
  printf '%s' "$chain"
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

assert_checkout_absent() {
  objects="$(psql_target -tA -c "SELECT count(*) FROM pg_class WHERE relnamespace = 'cinema'::regnamespace AND relname IN ('outbox','tickets','payment_attempts','payment_attempts_captured_per_reservation','payment_attempts_open_per_reservation','payment_attempts_provider_reference','payment_attempts_reservation','outbox_unpublished','tickets_reservation','tickets_event_seat_id_key');")"
  test "$objects" = "0" || fail "checkout objects must be absent after 0005 rollback, got $objects"
  columns="$(psql_target -tA -c "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cinema' AND table_name = 'events' AND column_name = 'price_cents';")"
  test "$columns" = "0" || fail "events.price_cents must be absent after 0005 rollback, got $columns"
  columns="$(psql_target -tA -c "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cinema' AND table_name = 'tickets' AND column_name = 'event_id';")"
  test "$columns" = "0" || fail "tickets.event_id must be absent after 0005 rollback, got $columns"
  # The widened reservation status is restored to the 0003 hold lifecycle.
  statuses="$(psql_target -tA -c "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'reservations_status_check' AND conrelid = 'cinema.reservations'::regclass;")"
  test -n "$statuses" || fail "reservations_status_check must survive a 0005 rollback"
  case "$statuses" in *paid*) fail "the 0005 rollback must restore the narrower reservation status constraint, got $statuses" ;; esac
  # The 0004 idempotency records and the 0003 hold schema must survive a 0005 rollback untouched.
  objects="$(psql_target -tA -c "SELECT (SELECT count(*) FROM pg_class WHERE relnamespace = 'cinema'::regnamespace AND relname IN ('idempotency_keys','idempotency_keys_expires_at','reservations','reservation_seats','reservation_seats_active_hold','reservation_seats_event_seat','reservations_expires_at')) + (SELECT count(*) FROM pg_proc WHERE proname = 'release_expired_seat_hold');")"
  test "$objects" = "8" || fail "the 0003 and 0004 objects must survive a 0005 rollback, got $objects of 8 objects"
}

# A 0006 rollback removes only the one-open-attempt index: the M3 checkout tables,
# the event price column and every stored attempt, ticket and outbox row stay.
assert_open_attempt_absent() {
  objects="$(psql_target -tA -c "SELECT count(*) FROM pg_class WHERE relnamespace = 'cinema'::regnamespace AND relname = 'payment_attempts_open_per_reservation';")"
  test "$objects" = "0" || fail "payment_attempts_open_per_reservation must be absent after 0006 rollback, got $objects"
  tables="$(psql_target -tA -c "SELECT string_agg(tablename, ',' ORDER BY tablename) FROM pg_tables WHERE schemaname = 'cinema' AND tablename IN ('outbox','payment_attempts','tickets');")"
  test "$tables" = "outbox,payment_attempts,tickets" || fail "the checkout tables must survive a 0006 rollback, got $tables"
  columns="$(psql_target -tA -c "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cinema' AND table_name = 'events' AND column_name = 'price_cents';")"
  test "$columns" = "1" || fail "events.price_cents must survive a 0006 rollback, got $columns"
  # The 0005 double-payment guard is untouched by this version.
  objects="$(psql_target -tA -c "SELECT count(*) FROM pg_class WHERE relnamespace = 'cinema'::regnamespace AND relname = 'payment_attempts_captured_per_reservation';")"
  test "$objects" = "1" || fail "payment_attempts_captured_per_reservation must survive a 0006 rollback, got $objects"
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
assert_versions "$(chain_upto 0003_reservation)"
log "rolling back 0003_reservation"
psql_target -q -f "$MIGRATIONS_DIR/0003_reservation.down.sql"
assert_versions "$(chain_upto 0002_venue_layout)"
assert_m1_present
psql_target -q -f "$M1_TEST_FILE"
log "reapplying 0003_reservation"
psql_target -q -f "$MIGRATIONS_DIR/0003_reservation.up.sql"
psql_target -q -f "$M2_TEST_FILE"
log "M0, M1 and M2 up/down/up verification passed"

log "applying 0004_idempotency"
psql_target -q -f "$MIGRATIONS_DIR/0004_idempotency.up.sql"
psql_target -q -f "$IDEMPOTENCY_TEST_FILE"
psql_target -q -f "$EXPIRY_TEST_FILE"
assert_versions "$(chain_upto 0004_idempotency)"
log "rolling back 0004_idempotency"
psql_target -q -f "$MIGRATIONS_DIR/0004_idempotency.down.sql"
assert_versions "$(chain_upto 0003_reservation)"
assert_idempotency_absent
psql_target -q -f "$M2_TEST_FILE"
log "reapplying 0004_idempotency"
psql_target -q -f "$MIGRATIONS_DIR/0004_idempotency.up.sql"
psql_target -q -f "$IDEMPOTENCY_TEST_FILE"
psql_target -q -f "$EXPIRY_TEST_FILE"
assert_versions "$(chain_upto 0004_idempotency)"
log "M0, M1, M2, M2 idempotency and M2 expiry sweep up/down/up verification passed"

log "applying 0005_checkout"
psql_target -q -f "$MIGRATIONS_DIR/0005_checkout.up.sql"
psql_target -q -f "$CHECKOUT_TEST_FILE"
assert_versions "$(chain_upto 0005_checkout)"
log "rolling back 0005_checkout"
psql_target -q -f "$MIGRATIONS_DIR/0005_checkout.down.sql"
assert_versions "$(chain_upto 0004_idempotency)"
assert_checkout_absent
psql_target -q -f "$IDEMPOTENCY_TEST_FILE"
psql_target -q -f "$EXPIRY_TEST_FILE"
log "reapplying 0005_checkout"
psql_target -q -f "$MIGRATIONS_DIR/0005_checkout.up.sql"
psql_target -q -f "$CHECKOUT_TEST_FILE"
assert_versions "$(chain_upto 0005_checkout)"
log "M0, M1, M2 and M3 checkout up/down/up verification passed"

log "applying 0006_one_open_payment_attempt"
psql_target -q -f "$MIGRATIONS_DIR/0006_one_open_payment_attempt.up.sql"
psql_target -q -f "$OPEN_ATTEMPT_TEST_FILE"
assert_versions "$(chain_upto 0006_one_open_payment_attempt)"
log "rolling back 0006_one_open_payment_attempt"
psql_target -q -f "$MIGRATIONS_DIR/0006_one_open_payment_attempt.down.sql"
assert_versions "$(chain_upto 0005_checkout)"
assert_open_attempt_absent
psql_target -q -f "$CHECKOUT_TEST_FILE"
log "reapplying 0006_one_open_payment_attempt"
psql_target -q -f "$MIGRATIONS_DIR/0006_one_open_payment_attempt.up.sql"
psql_target -q -f "$OPEN_ATTEMPT_TEST_FILE"
assert_versions "$(chain_upto 0006_one_open_payment_attempt)"
log "M0, M1, M2, M3 checkout and M3 one-open-attempt up/down/up verification passed"

log "applying 0007_repair_unresolvable_timezones"
# The cinemas are stored while the schema is still at 0006, because the migration's job is
# to repair rows that already existed. A cinema stored after it would prove the insert path
# and nothing else. Two carry a name this server does not know (the invented `America/Madrid`
# the M1 and M2 fixtures used, and a real zone with a trailing space), one carries a real
# zone that must survive untouched, and one is already UTC.
psql_target -q -c "INSERT INTO cinema.cinemas (id, name, timezone) VALUES
  ('00000000-0000-0000-0000-00000000f1a1','Cine Zona Inventada','America/Madrid'),
  ('00000000-0000-0000-0000-00000000f1a2','Cine Zona Real','Europe/Madrid'),
  ('00000000-0000-0000-0000-00000000f1a3','Cine Ya En UTC','UTC'),
  ('00000000-0000-0000-0000-00000000f1a4','Cine Zona Con Espacio','Europe/Madrid ');"
# The report is captured without -q, because it is a NOTICE on stderr: a repair that changed
# rows silently is the thing this version exists not to be, so the report is asserted and
# printed rather than trusted to appear.
repair_output="$(psql_target -f "$MIGRATIONS_DIR/0007_repair_unresolvable_timezones.up.sql" 2>&1)"
printf '%s\n' "$repair_output"
case "$repair_output" in
  *"repaired 2 cinema(s) to UTC"*) ;;
  *) fail "the repair must report the two cinemas it repaired, got: $repair_output" ;;
esac
case "$repair_output" in
  *"cinema 00000000-0000-0000-0000-00000000f1a1 had unresolvable timezone America/Madrid"*) ;;
  *) fail "the report must name each repaired cinema and the value it replaced, got: $repair_output" ;;
esac
case "$repair_output" in
  *"had unresolvable timezone Europe/Madrid , replaced with UTC"*) ;;
  *) fail "the report must name the value it replaced verbatim, including the trailing space it did not trim, got: $repair_output" ;;
esac
psql_target -q -f "$TIMEZONE_TEST_FILE"
assert_versions "$(chain_upto 0007_repair_unresolvable_timezones)"
log "rolling back 0007_repair_unresolvable_timezones"
psql_target -q -f "$MIGRATIONS_DIR/0007_repair_unresolvable_timezones.down.sql"
# The rollback is a documented no-op for the data: it forgets the version and keeps every
# repaired value, so the same test file has to pass against it unchanged.
assert_versions "$(chain_upto 0006_one_open_payment_attempt)"
psql_target -q -f "$TIMEZONE_TEST_FILE"
log "reapplying 0007_repair_unresolvable_timezones"
reapply_output="$(psql_target -f "$MIGRATIONS_DIR/0007_repair_unresolvable_timezones.up.sql" 2>&1)"
# A second application changes nothing: the repair is a predicate over the stored values,
# and it says so in its own report instead of looking like it repaired something again.
case "$reapply_output" in
  *"repaired 0 cinema(s) to UTC"*) ;;
  *) fail "a second application must repair nothing, got: $reapply_output" ;;
esac
psql_target -q -f "$TIMEZONE_TEST_FILE"
assert_versions "$(chain_upto 0007_repair_unresolvable_timezones)"
log "M0, M1, M2, M3 checkout, M3 one-open-attempt and M4.1 timezone backfill up/down/up verification passed"

# ── 0008: the redemption record ──────────────────────────────────────────────
# A ticket that can be admitted exactly once is a column, a guard and a trigger. The cycle
# proves the column and the trigger exist after up and are gone after down; the refusal that
# protects admitted tickets is asserted by the primitive's own gated test, because arming it
# here would need a whole sold ticket and that belongs to the checkout, not to a schema cycle.
log "applying 0008_ticket_redemption"
psql_target -q -f "$MIGRATIONS_DIR/0008_ticket_redemption.up.sql"
redeem_schema="$(psql_target -tA -c "
  SELECT count(*) FROM information_schema.columns
   WHERE table_schema = 'cinema' AND table_name = 'tickets' AND column_name = 'redeemed_at';
")"
[ "$redeem_schema" = "1" ] || fail "0008 must add cinema.tickets.redeemed_at, found [$redeem_schema]"
redeem_trigger="$(psql_target -tA -c "
  SELECT count(*) FROM pg_trigger WHERE tgname = 'guard_ticket_redemption' AND NOT tgisinternal;
")"
[ "$redeem_trigger" = "1" ] || fail "0008 must add the guard_ticket_redemption trigger, found [$redeem_trigger]"
assert_versions "$(chain_upto 0008_ticket_redemption)"
log "rolling back 0008_ticket_redemption"
psql_target -q -f "$MIGRATIONS_DIR/0008_ticket_redemption.down.sql"
redeem_schema="$(psql_target -tA -c "
  SELECT count(*) FROM information_schema.columns
   WHERE table_schema = 'cinema' AND table_name = 'tickets' AND column_name = 'redeemed_at';
")"
[ "$redeem_schema" = "0" ] || fail "the 0008 rollback must remove redeemed_at, found [$redeem_schema]"
redeem_trigger="$(psql_target -tA -c "
  SELECT count(*) FROM pg_trigger WHERE tgname = 'guard_ticket_redemption' AND NOT tgisinternal;
")"
[ "$redeem_trigger" = "0" ] || fail "the 0008 rollback must remove its trigger, found [$redeem_trigger]"
assert_versions "$(chain_upto 0007_repair_unresolvable_timezones)"
log "reapplying 0008_ticket_redemption"
psql_target -q -f "$MIGRATIONS_DIR/0008_ticket_redemption.up.sql"
assert_versions "$(chain_upto 0008_ticket_redemption)"
log "M0, M1, M2, M3 checkout, M3 one-open-attempt, M4.1 timezone backfill and M4.1 redemption up/down/up verification passed"
