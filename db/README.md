# Database foundation

M0 establishes only the `platform` PostgreSQL schema and its migration metadata. Domain tables for cinemas, layouts, events, reservations, payments, and tickets begin in later milestones.

## Migration files

Every `NNNN_name.up.sql` file must have a matching `NNNN_name.down.sql` file. Up files apply in ascending order; down files reverse in descending order. M0 currently contains one reversible migration:

- `0001_init.up.sql` creates `platform.schema_migrations` and `platform.settings`.
- `0001_init.down.sql` removes the complete `platform` schema.

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

Verify the full up/down/up lifecycle against an isolated temporary database:

```sh
docker compose --profile tools run --rm migration-verify
```

The verification command drops only a database whose name begins with `cinema_m0_verify`; it does not remove named volumes or touch the development database.
