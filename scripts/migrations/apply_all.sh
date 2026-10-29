#!/bin/sh
# Apply every pending migration, in version order, in one re-runnable step.
#
# The runner reads platform.schema_migrations, compares it with the files in
# MIGRATIONS_DIR and applies only the versions that are not recorded yet, exactly
# as scripts/migrations/apply.sh applies one version: each file is executed whole
# and records itself in platform.schema_migrations. A database that has never been
# migrated has no such table, which is not an error here: every version is then
# missing and the first file creates the table.
#
# Running it twice applies nothing the second time, so `make migrate-all` is safe
# in a bootstrap path and in CI.
set -eu

DB_HOST="${DB_HOST:-postgres}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-cinema}"
DB_NAME="${DB_NAME:-cinema}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/workspace/db/migrations}"

log() { printf '[migrations] %s\n' "$*"; }
fail() { printf '[migrations] ERROR: %s\n' "$*" >&2; exit 1; }

command -v psql >/dev/null 2>&1 || fail "psql is required"
test -d "$MIGRATIONS_DIR" || fail "missing migrations directory $MIGRATIONS_DIR"

psql_db() {
  psql -X -q -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" "$@"
}

# The table is absent on a database that has never been migrated, so its presence
# is asked for instead of assumed.
table_present="$(psql_db -tA -c "SELECT to_regclass('platform.schema_migrations') IS NOT NULL;")"
applied=""
if [ "$table_present" = "t" ]; then
  applied="$(psql_db -tA -c "SELECT version FROM platform.schema_migrations ORDER BY version;")"
fi

is_applied() {
  [ -n "$applied" ] || return 1
  printf '%s\n' "$applied" | grep -qx "$1"
}

# Every file name is validated before the first migration runs: a malformed file
# discovered halfway would leave the database ahead of the directory.
files=""
for file in "$MIGRATIONS_DIR"/*.up.sql; do
  [ -e "$file" ] || fail "no *.up.sql migration found in $MIGRATIONS_DIR"
  base="$(basename "$file" .up.sql)"
  case "$base" in
    [0-9][0-9][0-9][0-9]_*) ;;
    *) fail "$file is not named NNNN_name.up.sql" ;;
  esac
  files="$files $base"
done

# A recorded version with no file means the directory and the database disagree:
# report it instead of pretending the chain is complete.
if [ -n "$applied" ]; then
  for recorded in $applied; do
    case " $files " in
      *" $recorded "*) ;;
      *) fail "version $recorded is recorded in the database but has no .up.sql file in $MIGRATIONS_DIR" ;;
    esac
  done
fi

applied_count=0
for file in "$MIGRATIONS_DIR"/*.up.sql; do
  base="$(basename "$file" .up.sql)"
  if is_applied "$base"; then
    log "skip $base (already applied)"
    continue
  fi
  log "apply $base"
  # One transaction per file: a migration that fails halfway leaves neither its
  # objects nor its version row, so the next run can simply try again.
  psql_db --single-transaction -f "$file"
  # Each migration records its own version, so the runner only verifies it: a file
  # that forgot to would otherwise be applied again on the next run.
  recorded="$(psql_db -tA -c "SELECT count(*) FROM platform.schema_migrations WHERE version = '$base';")"
  [ "$recorded" = "1" ] || fail "$base did not record itself in platform.schema_migrations"
  applied="$applied
$base"
  applied_count=$((applied_count + 1))
done

total="$(psql_db -tA -c "SELECT count(*) FROM platform.schema_migrations;")"
log "applied $applied_count migration(s), $total recorded in platform.schema_migrations"
