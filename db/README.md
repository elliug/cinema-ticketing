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

Only successful holds are recorded. A request that lost a seat conflict is re-evaluated on retry, because storing its failure would keep reporting a conflict after the seats were freed. `expires_at` exists so records can be pruned by the durable expiry sweep (see below), which never prunes a record whose hold is still live. Both the record shape and the availability predicate are pinned by `tests/migrations/m2_idempotency.sql`, which contains the same availability query the API runs.

## Durable expiry sweep (M2)

Expiry is lazy in the schema: a hold whose `expires_at` has passed stops counting as held immediately (availability checks the deadline), and a re-claim of the seat frees it through the claim trigger. What is left is the durable cleanup of holds nobody re-claims, plus the retention deadline of the idempotency records. That is one sweep, implemented by `PostgresStore.ExpireHolds` in `apps/api/internal/reservation/expiry.go` and driven by `reservation.Sweeper`, which `cmd/api` starts as a goroutine bound to the process context.

The worker is wired, not implicit, and every part of its shape is configuration or a documented constant:

- **The deadline comes from PostgreSQL.** `Sweeper` reads `PostgresStore.DatabaseNow` (`SELECT now()`) before each batch and passes that value as the sweep deadline. The process clock is never used, so a skewed API host cannot expire live holds early.
- **`API_EXPIRY_INTERVAL`** is the cadence, a Go duration such as `30s` or `2s`. It defaults to `30s`, is lowered to half the hold window when that window is shorter, and is rejected when it exceeds half of `API_HOLD_TTL`, so the sweep always fires at least twice per hold.
- **`API_EXPIRY_BATCH`** is how many expired holds one sweep transaction locks. It defaults to `100` and is rejected outside `1..1000`.
- **Each sweep is bounded**: 30s per batch including the clock read, and at most 8 batches per tick, so a backlog drains across ticks instead of holding the worker in one long loop.
- **A failed tick is logged and retried on the next one.** Nothing about expiry is fatal, and correctness never depends on the worker running at all: the lazy predicate and the claim trigger keep every read and every claim correct.
- **Shutdown stops the worker before the pools close**, so no sweep runs against a closed store.

The sweep is a single transaction of four statements, and it visits the same tables in the order a claim takes them (`idempotency_keys`, `reservations`, `reservation_seats`). The table order matches but the row order does not: a claim locks its seats in ascending `event_seat_id` order while the seat update locks the rows of its reservation ids in plan order, so a collision with a multi-seat claim can deadlock (`40P01`). That is expected and harmless: the batch is retried up to three times, and a rolled back attempt changed nothing.

1. Prune the idempotency records past their retention deadline, at most one batch:

   ```sql
   WITH prunable AS (
     SELECT idempotency_key
     FROM cinema.idempotency_keys
     WHERE expires_at <= $1          -- retention deadline passed
       AND hold_expires_at <= $1     -- and its hold is no longer live either
     ORDER BY expires_at, idempotency_key
     LIMIT $2
     FOR UPDATE SKIP LOCKED
   )
   DELETE FROM cinema.idempotency_keys k USING prunable p
   WHERE k.idempotency_key = p.idempotency_key
   ```

2. Lock at most one batch of holds whose window elapsed, oldest deadline first:

   ```sql
   SELECT r.id::text
   FROM cinema.reservations r
   WHERE r.status IN ('held', 'payment_pending')
     AND r.expires_at <= $1
   ORDER BY r.expires_at, r.id
   LIMIT $2
   FOR NO KEY UPDATE SKIP LOCKED
   ```

3. Release the seat rows of exactly those holds, keeping a row that is already released:

   ```sql
   UPDATE cinema.reservation_seats
   SET released_at = $1
   WHERE released_at IS NULL
     AND reservation_id = ANY($2::uuid[])
   ```

4. Close the holds themselves, without ever rewriting another status:

   ```sql
   UPDATE cinema.reservations
   SET status = 'expired'
   WHERE id = ANY($1::uuid[])
     AND status IN ('held', 'payment_pending')
   ```

Why this is safe and restart-safe:

- **One transaction per batch.** A process killed between batches leaves the previous batches committed and nothing half-done, so a restarted process resumes from the rows alone. No in-memory state is needed for correctness.
- **Idempotent by predicate.** A second sweep on the same data selects nothing, because the first one already moved those rows out of the held set. A hold that is partially released (a re-claim takeover freed some of its seats) still expires, and the seats that are already free keep their original `released_at`.
- **`SKIP LOCKED` divides the work between sweeps.** Two instances running the sweep concurrently lock disjoint sets of rows instead of waiting for each other, so nothing is expired twice and two sweeps cannot deadlock. A sweep colliding with a live claim is a different case: it can deadlock, and the bounded retry covers it. `FOR NO KEY UPDATE` is the weakest lock that still excludes a second sweep, and unlike `FOR UPDATE` it does not block the `KEY SHARE` lock a claim takes on the reservation its idempotency record references.
- **`cancelled` is never touched**, because only `held` and `payment_pending` rows are selected and the status is re-checked in the update.
- **The deadline is a parameter**, so the sweep compares against the value the caller passes instead of `now()`. That is what makes the expiry behaviour reproducible in tests. The caller therefore owes the sweep a deadline taken from the database clock: passing a value ahead of it would expire live holds early and write a future-dated `released_at`.

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

Verify the M2 end-to-end contract: the accepted concurrency race, the loser conflicts, availability after the race, the running expiry worker, and the killed-worker recovery evidence.

```sh
make verify-m2
```

That script creates a disposable database named `cinema_m2_smoke_<pid>`, applies every migration to it, runs the gated PostgreSQL tests against it (`M2_RECOVERY_DATABASE_URL`), starts the API binary on port 18081 with local administration, the public reservation surface and a 60s hold window, runs `go test -tags acceptance_m2 ./tests/concurrency/` with the seeded `M2_EVENT_ID` and `M2_SEAT_IDS`, then drops the database, flushes only the dedicated Redis database it used, and stops its own process. The default `go test ./apps/api/...` suite needs no database: the PostgreSQL-only tests skip unless `M2_RECOVERY_DATABASE_URL` is set.
