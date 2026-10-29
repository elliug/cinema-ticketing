# Database foundation

M0 establishes the `platform` PostgreSQL schema and migration metadata. The M1 migration adds preliminary cinema, room, layout, film, event, and event-seat tables. The M2 migrations add durable reservation holds with per-seat exclusivity and expiry, plus durable idempotency records for hold creation.

## Migration files

Every `NNNN_name.up.sql` file has a matching `NNNN_name.down.sql` file. Apply migrations in ascending order and roll them back in descending order:

- `0001_init` creates the `platform` schema.
- `0002_venue_layout` creates the `cinema` schema and depends on `0001_init`.
- `0003_reservation` adds the reservation hold tables and depends on `0002_venue_layout`.
- `0004_idempotency` adds the idempotency records for hold creation and depends on `0003_reservation`, because a record references the hold it produced.

Rollback of `0002_venue_layout` removes **all M1 data** in that schema. Do not run it on a database containing data you need to keep. Rollback of `0003_reservation` removes only the M2 hold objects and keeps every M1 table and row. Rollback of `0004_idempotency` removes only the idempotency records, so roll it back *before* `0003_reservation`; the record's foreign key to `cinema.reservations` otherwise blocks that drop.

## Reservation holds (M2)

- `cinema.reservations` — one purchase intent: `event_id` (foreign key to `cinema.events`), `status`, and the durable `expires_at` hold deadline. The TTL itself is configuration, not schema.
- `cinema.reservation_seats` — one row per held seat: `reservation_id`, `event_id`, `event_seat_id` (foreign key to `cinema.event_seats`), and `released_at`. A row is *active* while `released_at IS NULL`.

The two foreign keys on `reservation_seats` are composite and share `event_id`, so a reservation can never hold a seat of another event.

Two schema rules enforce the hold invariants:

1. The partial unique index `reservation_seats_active_hold` on `(event_seat_id) WHERE released_at IS NULL` allows at most one active hold per event seat. Two concurrent claimers cannot both commit an active row: the loser gets a `unique_violation`. The predicate cannot reference the clock, so it never has to be rebuilt when a hold expires.
2. The `release_expired_seat_hold` trigger on `cinema.reservation_seats` runs before each insert, rejects a claim whose `expires_at` has already passed, and releases the expired rows of the claimed seat inside the acquiring transaction. Expiry therefore frees a seat for the next hold even before the expiry worker sweeps, and the sweep only has to clean up seats nobody re-claims.

Both rules are pinned by `tests/migrations/m2_schema.sql`. Released rows are kept as history; the active hold is a single row, so availability reads must treat a seat as occupied only while `released_at IS NULL AND reservations.expires_at > now()`: an expired hold that nobody has re-claimed yet is still stored as active until the claim trigger or the expiry worker releases it.

## Hold idempotency (M2)

- `cinema.idempotency_keys` — one row per client supplied `Idempotency-Key`, holding `request_fingerprint` (the payload the key was first used with), `status` (the stored outcome, `201` for a hold), `hold_id` (the reservation it produced), `hold_expires_at` (that hold's deadline, replayed verbatim so a replay never extends the window), `created_at`, and `expires_at` (the record's retention deadline).

The key is the primary key, so the database decides the winner between two concurrent requests that carry the same key: the loser gets a `unique_violation` on `idempotency_keys_pkey` and the API answers it from the stored record or rejects it as a reused key with a different payload. A record and its reservation are written in one transaction, so a hold and the record that replays it can never disagree.

Only successful holds are recorded. A request that lost a seat conflict is re-evaluated on retry, because storing its failure would keep reporting a conflict after the seats were freed. `expires_at` exists so records can be pruned; the retention sweep belongs to the durable expiry worker. Both the record shape and the availability predicate are pinned by `tests/migrations/m2_idempotency.sql`, which contains the same availability query the API runs.

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

To apply or roll back M2:

```sh
MIGRATION_VERSION=0003_reservation docker compose --profile tools run --rm migrate
MIGRATION_VERSION=0004_idempotency docker compose --profile tools run --rm migrate
MIGRATION_VERSION=0004_idempotency MIGRATION_DIRECTION=down docker compose --profile tools run --rm migrate
MIGRATION_VERSION=0003_reservation MIGRATION_DIRECTION=down docker compose --profile tools run --rm migrate
```

Verify the M0, M1 and M2 up/down/up lifecycles and schema assertions against an isolated temporary database:

```sh
docker compose --profile tools run --rm migration-verify
```

The verification command drops only a database whose name begins with `cinema_m0_verify`; it does not remove named volumes or touch the development database.
