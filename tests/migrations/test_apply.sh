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
set +e
output=$(run env MIGRATION_VERSION=../../other 2>&1); status=$?
set -e
[ "$status" -eq 2 ] && [ "$output" = 'MIGRATION_VERSION must be 0001_init or 0002_venue_layout' ] || { printf 'invalid version was not rejected: %s (%s)\n' "$output" "$status" >&2; exit 1; }
printf 'migration apply contract passed\n'
