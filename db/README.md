# Database foundation

M0 establishes the `platform` PostgreSQL schema and migration metadata. The M1 migration adds preliminary cinema, room, layout, film, event, and event-seat tables.

## Migration files

Every `NNNN_name.up.sql` file has a matching `NNNN_name.down.sql` file. Apply migrations in ascending order and roll them back in descending order:

- `0001_init` creates the `platform` schema.
- `0002_venue_layout` creates the `cinema` schema and depends on `0001_init`.

Rollback of `0002_venue_layout` removes **all M1 data** in that schema. Do not run it on a database containing data you need to keep.

## Commands

Start PostgreSQL and Redis:

```sh
docker compose up -d postgres redis
```

Apply the current migration to the development database:

```sh
docker compose --profile tools run --rm migrate
```

Roll it back explicitly:

```sh
MIGRATION_DIRECTION=down docker compose --profile tools run --rm migrate
```

The migration command defaults to `0001_init` for compatibility. After applying it, explicitly select M1:

```sh
MIGRATION_VERSION=0002_venue_layout docker compose --profile tools run --rm migrate
```

To roll back only M1 (this deletes its data):

```sh
MIGRATION_VERSION=0002_venue_layout MIGRATION_DIRECTION=down docker compose --profile tools run --rm migrate
```

Verify the M0 and M1 up/down/up lifecycles and M1 schema assertions against an isolated temporary database:

```sh
docker compose --profile tools run --rm migration-verify
```

The verification command drops only a database whose name begins with `cinema_m0_verify`; it does not remove named volumes or touch the development database.
