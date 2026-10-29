#!/bin/sh
set -eu

DB_HOST="${DB_HOST:-postgres}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-cinema}"
DB_NAME="${DB_NAME:-cinema}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/workspace/db/migrations}"
MIGRATION_DIRECTION="${MIGRATION_DIRECTION:-up}"
MIGRATION_VERSION="${MIGRATION_VERSION:-0001_init}"

case "$MIGRATION_VERSION" in
  0001_init|0002_venue_layout|0003_reservation|0004_idempotency) ;;
  *) printf 'MIGRATION_VERSION must be 0001_init, 0002_venue_layout, 0003_reservation or 0004_idempotency\n' >&2; exit 2 ;;
esac
case "$MIGRATION_DIRECTION" in
  up|down) migration="$MIGRATIONS_DIR/$MIGRATION_VERSION.$MIGRATION_DIRECTION.sql" ;;
  *) printf 'MIGRATION_DIRECTION must be up or down\n' >&2; exit 2 ;;
esac

exec psql -X -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$migration"
