#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
migrations="$root/db/migrations"
tmp=$(mktemp -d "$root/tests/migrations/.apply-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
cat > "$tmp/psql" <<'MOCK'
#!/bin/sh
printf '%s\n' "$*"
MOCK
chmod +x "$tmp/psql"
run() { env PATH="$tmp:$PATH" DB_HOST=db DB_PORT=5432 DB_USER=cinema DB_NAME=cinema MIGRATIONS_DIR="$migrations" "$@" /bin/sh "$root/scripts/migrations/apply.sh"; }
assert_output() {
  expected=$1; shift
  output=$(run "$@")
  [ "$output" = "$expected" ] || { printf 'unexpected output: %s\n' "$output" >&2; exit 1; }
}
assert_refused() {
  status=$1; expected=$2; shift 2
  set +e
  output=$(run "$@" 2>&1); actual=$?
  set -e
  [ "$actual" -eq "$status" ] && [ "$output" = "$expected" ] \
    || { printf 'unexpected refusal: %s (%s)\n' "$output" "$actual" >&2; exit 1; }
}

# The documented default is still 0001_init, so `make migrate` without a version keeps
# doing what db/README.md says it does.
assert_output "-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f $migrations/0001_init.up.sql" env

# Every migration on disk is applied by naming it, in both directions. The list here is
# the directory's, not a literal, for the same reason scripts/migrations/apply.sh derives
# its own: a new pair must be covered by this contract test without editing it.
expected_versions=""
for file in "$migrations"/*.up.sql; do
  name="$(basename "$file" .up.sql)"
  assert_output "-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f $migrations/$name.up.sql" env MIGRATION_VERSION="$name"
  assert_output "-X -v ON_ERROR_STOP=1 -h db -p 5432 -U cinema -d cinema -f $migrations/$name.down.sql" env MIGRATION_VERSION="$name" MIGRATION_DIRECTION=down
  expected_versions="${expected_versions:+$expected_versions, }$name"
done

# A version with no file is still refused before psql is called, and the refusal names
# the versions that do exist: that refusal is the reason the list exists at all.
assert_refused 2 "MIGRATION_VERSION must be one of the versions in $migrations: $expected_versions" env MIGRATION_VERSION=../../other
assert_refused 2 'MIGRATION_DIRECTION must be up or down' env MIGRATION_VERSION=0001_init MIGRATION_DIRECTION=sideways
# A directory with no migration in it is named as the problem instead of silencing the
# derivation, because an empty derived list would otherwise refuse every version for the
# wrong reason.
assert_refused 2 "no *.up.sql migration found in $tmp/absent" env MIGRATIONS_DIR="$tmp/absent"
printf 'migration apply contract passed\n'
