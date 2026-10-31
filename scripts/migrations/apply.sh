#!/bin/sh
set -eu

DB_HOST="${DB_HOST:-postgres}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-cinema}"
DB_NAME="${DB_NAME:-cinema}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/workspace/db/migrations}"
MIGRATION_DIRECTION="${MIGRATION_DIRECTION:-up}"
MIGRATION_VERSION="${MIGRATION_VERSION:-0001_init}"

# The accepted versions are the migration files that exist, derived from the directory
# rather than enumerated here. A version number written down in this script is a second
# place that can disagree with the directory — it has to be edited before the migration it
# does not name can be applied at all — and the directory is what actually gets run.
#
# What the derivation keeps is the whole point of the list: a version with no file is
# refused here, with the versions that do exist named, instead of reaching psql as a
# missing path and failing halfway through an apply.
available=""
for file in "$MIGRATIONS_DIR"/*.up.sql; do
  if [ ! -e "$file" ]; then
    printf 'no *.up.sql migration found in %s\n' "$MIGRATIONS_DIR" >&2
    exit 2
  fi
  base="$(basename "$file" .up.sql)"
  case "$base" in
    [0-9][0-9][0-9][0-9]_*) ;;
    *) printf '%s is not named NNNN_name.up.sql\n' "$file" >&2; exit 2 ;;
  esac
  available="${available:+$available, }$base"
done
case ", $available, " in
  *", $MIGRATION_VERSION, "*) ;;
  *) printf 'MIGRATION_VERSION must be one of the versions in %s: %s\n' "$MIGRATIONS_DIR" "$available" >&2; exit 2 ;;
esac
case "$MIGRATION_DIRECTION" in
  up|down) migration="$MIGRATIONS_DIR/$MIGRATION_VERSION.$MIGRATION_DIRECTION.sql" ;;
  *) printf 'MIGRATION_DIRECTION must be up or down\n' >&2; exit 2 ;;
esac

exec psql -X -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$migration"
