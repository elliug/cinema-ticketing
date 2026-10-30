#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d "$root/tests/migrations/.apply-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
cat > "$tmp/psql" <<'MOCK'
#!/bin/sh
printf '%s\n' "$*"
MOCK
chmod +x "$tmp/psql"
run() { env PATH="$tmp:$PATH" DB_HOST=db DB_PORT=5432 DB_USER=cinema DB_NAME=cinema MIGRATIONS_DIR=/migrations "$@" /bin/sh "$root/scripts/migrations/apply.sh"; }
assert_output() {
  expected=$1; shift
  output=$(run "$@")
  [ "$output" = "$expected" ] || { printf 'unexpected output: %s\n' "$output" >&2; exit 1; }
}
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0001_init.up.sql' env
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0001_init.down.sql' env MIGRATION_DIRECTION=down
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0002_venue_layout.up.sql' env MIGRATION_VERSION=0002_venue_layout
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0002_venue_layout.down.sql' env MIGRATION_VERSION=0002_venue_layout MIGRATION_DIRECTION=down
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0003_reservation.up.sql' env MIGRATION_VERSION=0003_reservation
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0003_reservation.down.sql' env MIGRATION_VERSION=0003_reservation MIGRATION_DIRECTION=down
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0004_idempotency.up.sql' env MIGRATION_VERSION=0004_idempotency
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0004_idempotency.down.sql' env MIGRATION_VERSION=0004_idempotency MIGRATION_DIRECTION=down
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0005_checkout.up.sql' env MIGRATION_VERSION=0005_checkout
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0005_checkout.down.sql' env MIGRATION_VERSION=0005_checkout MIGRATION_DIRECTION=down
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0006_one_open_payment_attempt.up.sql' env MIGRATION_VERSION=0006_one_open_payment_attempt
assert_output '-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f /migrations/0006_one_open_payment_attempt.down.sql' env MIGRATION_VERSION=0006_one_open_payment_attempt MIGRATION_DIRECTION=down
set +e
output=$(run env MIGRATION_VERSION=../../other 2>&1); status=$?
set -e
[ "$status" -eq 2 ] && [ "$output" = 'MIGRATION_VERSION must be 0001_init, 0002_venue_layout, 0003_reservation, 0004_idempotency, 0005_checkout or 0006_one_open_payment_attempt' ] || { printf 'invalid version was not rejected: %s (%s)\n' "$output" "$status" >&2; exit 1; }
printf 'migration apply contract passed\n'
