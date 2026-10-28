#!/bin/sh
set -eu

DB_HOST="${DB_HOST:-postgres}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-cinema}"
DB_NAME="${DB_NAME:-cinema}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/workspace/db/migrations}"
MIGRATION_DIRECTION="${MIGRATION_DIRECTION:-up}"

case "$MIGRATION_DIRECTION" in
  up) migration="$MIGRATIONS_DIR/0001_init.up.sql" ;;
  down) migration="$MIGRATIONS_DIR/0001_init.down.sql" ;;
  *) printf 'MIGRATION_DIRECTION must be up or down\n' >&2; exit 2 ;;
esac

exec psql -X -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$migration"
