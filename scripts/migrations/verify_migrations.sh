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

# The purchase and the queued message the delivery and identity cycles are about, named here
# because the assertions below ask about these two rows and not about a count of rows some
# other cycle happened to leave behind.
RESERVATION_ID="00000000-0000-0000-0000-0000000009a8"
OUTBOX_ID="00000000-0000-0000-0000-0000000009a9"
ADDRESS="verify-migrations@example.test"
CUSTOMER_ID="00000000-0000-0000-0000-000000000ab1"
SESSION_ID="00000000-0000-0000-0000-000000000ab2"
ACCESS_LINK_NONCE="00000000-0000-0000-0000-000000000ab3"

# ── 0009: the address the buyer gave, and the record of what was mailed ──────
# A second consumer of the outbox needs its own cursor, because `published_at` already means
# "every consumer acted" and a mail worker filtering on it would find nothing to send. The
# cycle proves the two shapes that make that record safe — an optional address that refuses
# what cannot be an address, and a delivery keyed by (consumer, outbox_id) that cannot outlive
# the outbox row it describes — and then proves the rollback takes the table and the column and
# leaves the queued message and the purchase alone. 0009's *refusal* to roll back while a
# ticket email has been delivered is not armed here: that guard protects mail that was really
# sent, which the email gate owns, and the delivery rows below are written for consumers other
# than 'email' for exactly that reason.
assert_email_delivery_present() {
  columns="$(psql_target -tA -c "
    SELECT string_agg(column_name || ':' || data_type || ':' || is_nullable, ',' ORDER BY ordinal_position)
      FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'outbox_deliveries';")"
  test "$columns" = "consumer:text:NO,outbox_id:uuid:NO,delivered_at:timestamp with time zone:NO" \
    || fail "cinema.outbox_deliveries must be consumer/outbox_id NOT NULL plus delivered_at, got [$columns]"
  address="$(psql_target -tA -c "
    SELECT data_type || ':' || is_nullable FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'reservations' AND column_name = 'contact_email';")"
  test "$address" = "text:YES" \
    || fail "0009 must add a nullable text cinema.reservations.contact_email, got [$address]"
  # The key is the pair, in that order, and that is the whole reason a retried tick cannot
  # send twice: a second consumer records itself under its own name instead of overwriting the
  # first, and one consumer cannot hold the same outbox row twice.
  key="$(psql_target -tA -c "
    SELECT string_agg(a.attname, ',' ORDER BY k.ord)
      FROM pg_constraint c
      CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
      JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
     WHERE c.conrelid = to_regclass('cinema.outbox_deliveries') AND c.contype = 'p';")"
  test "$key" = "consumer,outbox_id" \
    || fail "the delivery record must be keyed by (consumer, outbox_id) in that order, got [$key]"
  # The record is only meaningful while the row it describes exists, so pruning the outbox has
  # to take it with it.
  reference="$(psql_target -tA -c "
    SELECT confdeltype::text
      FROM pg_constraint c
      JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
     WHERE c.conrelid = to_regclass('cinema.outbox_deliveries') AND c.contype = 'f'
       AND a.attname = 'outbox_id' AND c.confrelid = to_regclass('cinema.outbox');")"
  test "$reference" = "c" \
    || fail "outbox_deliveries.outbox_id must reference cinema.outbox ON DELETE CASCADE (confdeltype c), got [$reference]"
  worker_index="$(psql_target -tA -c "
    SELECT string_agg(a.attname, ',' ORDER BY k.ord)
      FROM pg_index i
      CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
      JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
     WHERE i.indexrelid = to_regclass('cinema.outbox_pending_by_topic');")"
  test "$worker_index" = "topic,created_at,id" \
    || fail "outbox_pending_by_topic must lead by topic, created_at, id so the worker finds candidates without published_at, got [$worker_index]"
  # The address rule is not compared against its own definition, because a check read back as
  # text is not a test: the column is offered an address and then something that cannot be one,
  # and the second has to be refused.
  psql_target -q -c "UPDATE cinema.reservations SET contact_email = '$ADDRESS' WHERE id = '$RESERVATION_ID';"
  stored="$(psql_target -tA -c "SELECT count(*) FROM cinema.reservations WHERE id = '$RESERVATION_ID' AND contact_email = '$ADDRESS';")"
  test "$stored" = "1" || fail "0009 must store a well-formed contact address, found [$stored]"
  if psql_target -q -c "UPDATE cinema.reservations SET contact_email = 'not-an-address' WHERE id = '$RESERVATION_ID';" 2>/dev/null; then
    fail "the 0009 address check must refuse a value that cannot be an address, and 'not-an-address' was stored"
  fi
  # One queued row recorded by two consumers is the fact the mail worker needs and the PDF
  # worker's `published_at` cannot give it.
  psql_target -q -c "
    INSERT INTO cinema.outbox_deliveries (consumer, outbox_id) VALUES
      ('verify-migrations-one', '$OUTBOX_ID'),
      ('verify-migrations-two', '$OUTBOX_ID')
    ON CONFLICT DO NOTHING;"
  deliveries="$(psql_target -tA -c "SELECT count(*) FROM cinema.outbox_deliveries WHERE outbox_id = '$OUTBOX_ID';")"
  test "$deliveries" = "2" \
    || fail "one outbox row must be recordable by two consumers, got $deliveries delivery row(s)"
}

# A 0009 rollback takes the delivery record and the address and nothing else. The queued outbox
# row and the purchase it belongs to are what a rollback of a delivery feature must never touch:
# the table is a record *about* that row, so losing the row would lose the thing it described.
assert_email_delivery_absent() {
  objects="$(psql_target -tA -c "
    SELECT count(*) FROM pg_class
     WHERE relnamespace = 'cinema'::regnamespace
       AND relname IN ('outbox_deliveries','outbox_pending_by_topic');")"
  test "$objects" = "0" \
    || fail "the delivery table and its worker index must be absent after a 0009 rollback, got $objects"
  columns="$(psql_target -tA -c "
    SELECT count(*) FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'reservations' AND column_name = 'contact_email';")"
  test "$columns" = "0" || fail "reservations.contact_email must be absent after a 0009 rollback, got $columns"
  survivors="$(psql_target -tA -c "
    SELECT (SELECT count(*) FROM cinema.outbox WHERE id = '$OUTBOX_ID')
         + (SELECT count(*) FROM cinema.reservations WHERE id = '$RESERVATION_ID');")"
  test "$survivors" = "2" \
    || fail "the queued outbox row and the purchase must survive a 0009 rollback, got $survivors of 2 rows"
}

# ── 0010: the account, the session, the link and the order's owner ───────────
# Identity is additive: an account carrying the verification 0009 deliberately did not claim,
# a revocable session, a one-use link, and a nullable thread from an order back to the account
# that bought it. The cycle proves the shape of all four, including the two delete actions,
# which are asserted by the code PostgreSQL stores rather than by the sentence in the migration,
# because getting them backwards is silent until somebody deletes an account: a cascade on the
# order would delete a sale, and a set null on the session would leave a credential whose owner
# no longer exists. Then it proves the rollback forgets identity and keeps every purchase, its
# 0009 address included.
assert_identity_present() {
  columns="$(psql_target -tA -c "
    SELECT string_agg(table_name || '.' || column_name || ':' || data_type || ':' || is_nullable, ',' ORDER BY table_name, ordinal_position)
      FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name IN ('access_links','guest_customers','sessions');")"
  test "$columns" = "access_links.nonce:uuid:NO,access_links.email:text:NO,access_links.created_at:timestamp with time zone:NO,access_links.expires_at:timestamp with time zone:NO,access_links.consumed_at:timestamp with time zone:YES,guest_customers.id:uuid:NO,guest_customers.email:text:NO,guest_customers.phone:text:YES,guest_customers.verified_at:timestamp with time zone:YES,guest_customers.created_at:timestamp with time zone:NO,sessions.id:uuid:NO,sessions.customer_id:uuid:NO,sessions.token_hash:bytea:NO,sessions.created_at:timestamp with time zone:NO,sessions.last_seen_at:timestamp with time zone:YES,sessions.expires_at:timestamp with time zone:NO,sessions.revoked_at:timestamp with time zone:YES" \
    || fail "0010 must leave guest_customers, sessions and access_links with the columns and nullability the feature relies on, got [$columns]"
  # "One account per address" and "one session per token" are facts about a column being
  # unique, not about the name a constraint was given, and the link's nonce is its primary key:
  # being the key is what makes two links issued in the same second two different links.
  keys="$(psql_target -tA -c "
    WITH wanted(rel, att, kind) AS (
      VALUES ('cinema.guest_customers', 'email', 'u'),
             ('cinema.sessions', 'token_hash', 'u'),
             ('cinema.access_links', 'nonce', 'p')
    )
    SELECT string_agg(w.rel || '.' || w.att || ':' || w.kind, ',' ORDER BY w.rel)
      FROM wanted w
     WHERE EXISTS (
       SELECT 1 FROM pg_constraint c
         JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
        WHERE c.conrelid = to_regclass(w.rel) AND c.contype = w.kind
          AND array_length(c.conkey, 1) = 1 AND a.attname = w.att);")"
  test "$keys" = "cinema.access_links.nonce:p,cinema.guest_customers.email:u,cinema.sessions.token_hash:u" \
    || fail "0010 must leave a unique address, a unique token hash and the nonce as access_links' primary key, found only [$keys]"
  # c is CASCADE and n is SET NULL; the words are in the failure because the code is not.
  actions="$(psql_target -tA -c "
    WITH wanted(rel, att, target, action) AS (
      VALUES ('cinema.sessions', 'customer_id', 'cinema.guest_customers', 'c'),
             ('cinema.reservations', 'customer_id', 'cinema.guest_customers', 'n')
    )
    SELECT string_agg(w.rel || '.' || w.att || ':' || w.action, ',' ORDER BY w.rel)
      FROM wanted w
     WHERE EXISTS (
       SELECT 1 FROM pg_constraint c
         JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
        WHERE c.conrelid = to_regclass(w.rel) AND c.contype = 'f'
          AND a.attname = w.att AND c.confrelid = to_regclass(w.target)
          AND c.confdeltype::text = w.action);")"
  test "$actions" = "cinema.reservations.customer_id:n,cinema.sessions.customer_id:c" \
    || fail "deleting an account must CASCADE to its sessions (c) and SET NULL on its orders (n), found only [$actions]"
  # The two reads the columns exist for: one account's sessions, and one account's orders
  # newest first, which is why the order index carries the sort and not just the filter.
  indexes="$(psql_target -tA -c "
    WITH wanted(idx) AS (VALUES ('cinema.sessions_customer'), ('cinema.reservations_by_customer'))
    SELECT string_agg(w.idx || '=' || coalesce((
        SELECT string_agg(a.attname || CASE WHEN ((i.indoption::int2[])[k.ord - 1] & 1) = 1 THEN ' desc' ELSE '' END, ',' ORDER BY k.ord)
          FROM pg_index i
          CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
         WHERE i.indexrelid = to_regclass(w.idx)), '<absent>'), ',' ORDER BY w.idx)
      FROM wanted w;")"
  test "$indexes" = "cinema.reservations_by_customer=customer_id,created_at desc,cinema.sessions_customer=customer_id" \
    || fail "0010 must index sessions by customer and orders by customer then created_at descending, got [$indexes]"
  owner="$(psql_target -tA -c "
    SELECT data_type || ':' || is_nullable FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'reservations' AND column_name = 'customer_id';")"
  test "$owner" = "uuid:YES" \
    || fail "0010 must add a nullable uuid cinema.reservations.customer_id, got [$owner]"
  # The rows the feature will write, offered to the schema: a verified account, a session with
  # a deadline and no revocation yet, an unconsumed link, and an order that now belongs to the
  # account — the thread D5 claims backwards, here only forwards.
  psql_target -q -c "
    INSERT INTO cinema.guest_customers (id, email, verified_at) VALUES
      ('$CUSTOMER_ID', '$ADDRESS', now()) ON CONFLICT DO NOTHING;
    INSERT INTO cinema.sessions (id, customer_id, token_hash, expires_at) VALUES
      ('$SESSION_ID', '$CUSTOMER_ID', sha256('verify-migrations'), now() + interval '1 hour')
      ON CONFLICT DO NOTHING;
    INSERT INTO cinema.access_links (nonce, email, expires_at) VALUES
      ('$ACCESS_LINK_NONCE', '$ADDRESS', now() + interval '5 minutes') ON CONFLICT DO NOTHING;
    UPDATE cinema.reservations SET customer_id = '$CUSTOMER_ID' WHERE id = '$RESERVATION_ID';"
  stored="$(psql_target -tA -c "
    SELECT (SELECT count(*) FROM cinema.guest_customers WHERE id = '$CUSTOMER_ID' AND verified_at IS NOT NULL)
         + (SELECT count(*) FROM cinema.sessions WHERE id = '$SESSION_ID' AND customer_id = '$CUSTOMER_ID')
         + (SELECT count(*) FROM cinema.access_links WHERE nonce = '$ACCESS_LINK_NONCE' AND consumed_at IS NULL)
         + (SELECT count(*) FROM cinema.reservations WHERE id = '$RESERVATION_ID' AND customer_id = '$CUSTOMER_ID');")"
  test "$stored" = "4" \
    || fail "0010 must accept a verified account, its session, an unconsumed link and an order owned by that account, got $stored of 4 rows"
}

# A 0010 rollback forgets the account, the session, the link and the order's owner, and loses
# no purchase: the reservation keeps the address 0009 added and the queued outbox row keeps the
# delivery record 0009 added, because those belong to the sale and not to identity. The rollback
# refuses nothing, by decision, and the rows below are what makes that decision safe to state.
assert_identity_absent() {
  objects="$(psql_target -tA -c "
    SELECT count(*) FROM pg_class
     WHERE relnamespace = 'cinema'::regnamespace
       AND relname IN ('access_links','guest_customers','sessions','sessions_customer','reservations_by_customer');")"
  test "$objects" = "0" \
    || fail "the identity tables and their indexes must be absent after a 0010 rollback, got $objects"
  columns="$(psql_target -tA -c "
    SELECT count(*) FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'reservations' AND column_name = 'customer_id';")"
  test "$columns" = "0" || fail "reservations.customer_id must be absent after a 0010 rollback, got $columns"
  survivors="$(psql_target -tA -c "
    SELECT (SELECT count(*) FROM cinema.reservations WHERE id = '$RESERVATION_ID' AND contact_email = '$ADDRESS')
         + (SELECT count(*) FROM cinema.outbox WHERE id = '$OUTBOX_ID')
         + (SELECT count(*) FROM cinema.outbox_deliveries WHERE consumer = 'verify-migrations-one' AND outbox_id = '$OUTBOX_ID');")"
  test "$survivors" = "3" \
    || fail "the purchase, its address, the queued row and its delivery record must all survive a 0010 rollback, got $survivors of 3 rows"
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

# The purchase and the queued message the next two cycles are about, stored before either
# migration runs because both rollbacks promise to keep them. A reservation is a sale against a
# published layout, not a table with an event id in it, so the venue, the film and the screening
# are scaffolding that exists only to make one row of each real.
log "seeding one purchase and one queued outbox row for the delivery and identity cycles"
psql_target -q -c "
  INSERT INTO cinema.cinemas (id, name, timezone) VALUES
    ('00000000-0000-0000-0000-0000000009a1','Cine Email E Identidad','UTC');
  INSERT INTO cinema.rooms (id, cinema_id, name) VALUES
    ('00000000-0000-0000-0000-0000000009a2','00000000-0000-0000-0000-0000000009a1','Sala Verificacion');
  INSERT INTO cinema.layout_versions (id, room_id, version) VALUES
    ('00000000-0000-0000-0000-0000000009a3','00000000-0000-0000-0000-0000000009a2',1);
  INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES
    ('00000000-0000-0000-0000-0000000009a4','00000000-0000-0000-0000-0000000009a3','A',1);
  INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
    ('00000000-0000-0000-0000-0000000009a5','00000000-0000-0000-0000-0000000009a4','1',1,'standard',true);
  UPDATE cinema.layout_versions SET status = 'published'
   WHERE id = '00000000-0000-0000-0000-0000000009a3';
  INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES
    ('00000000-0000-0000-0000-0000000009a6','Email And Identity Fixture',100,'PG');
  INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status) VALUES
    ('00000000-0000-0000-0000-0000000009a7','00000000-0000-0000-0000-0000000009a2','00000000-0000-0000-0000-0000000009a6','00000000-0000-0000-0000-0000000009a3', now(), 'on_sale');
  INSERT INTO cinema.reservations (id, event_id, status, expires_at) VALUES
    ('$RESERVATION_ID','00000000-0000-0000-0000-0000000009a7','expired', now());
  INSERT INTO cinema.outbox (id, topic, payload) VALUES
    ('$OUTBOX_ID','ticket.issued','{}'::jsonb);"

log "applying 0009_email_delivery"
psql_target -q -f "$MIGRATIONS_DIR/0009_email_delivery.up.sql"
# The address is optional by decision, so the purchase that existed before the column did must
# be able to keep carrying none: the column is added, no value is invented for it.
addressless="$(psql_target -tA -c "
  SELECT count(*) FROM cinema.reservations WHERE id = '$RESERVATION_ID' AND contact_email IS NULL;")"
test "$addressless" = "1" \
  || fail "0009 must leave a reservation that predates it with no address, found [$addressless]"
assert_email_delivery_present
assert_versions "$(chain_upto 0009_email_delivery)"
log "rolling back 0009_email_delivery"
psql_target -q -f "$MIGRATIONS_DIR/0009_email_delivery.down.sql"
assert_versions "$(chain_upto 0008_ticket_redemption)"
assert_email_delivery_absent
log "reapplying 0009_email_delivery"
psql_target -q -f "$MIGRATIONS_DIR/0009_email_delivery.up.sql"
assert_email_delivery_present
assert_versions "$(chain_upto 0009_email_delivery)"
log "M0, M1, M2, M3 checkout, M3 one-open-attempt, M4.1 timezone backfill, M4.1 redemption and email delivery up/down/up verification passed"

log "applying 0010_identity"
psql_target -q -f "$MIGRATIONS_DIR/0010_identity.up.sql"
assert_identity_present
assert_versions "$(chain_upto 0010_identity)"
log "rolling back 0010_identity"
psql_target -q -f "$MIGRATIONS_DIR/0010_identity.down.sql"
assert_versions "$(chain_upto 0009_email_delivery)"
assert_identity_absent
log "reapplying 0010_identity"
psql_target -q -f "$MIGRATIONS_DIR/0010_identity.up.sql"
assert_identity_present
assert_versions "$(chain_upto 0010_identity)"
log "M0, M1, M2, M3 checkout, M3 one-open-attempt, M4.1 timezone backfill, M4.1 redemption, email delivery and identity up/down/up verification passed"
