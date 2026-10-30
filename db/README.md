# Database foundation

M0 establishes the `platform` PostgreSQL schema and migration metadata. The M1 migration adds preliminary cinema, room, layout, film, event, and event-seat tables. The M2 migrations add durable reservation holds with per-seat exclusivity and expiry, plus durable idempotency records for hold creation. The M3 `0005_checkout` migration adds the price per event, the durable payment attempt, the sold ticket and the transactional outbox, and `0006_one_open_payment_attempt` adds the guard that makes a second charge for one reservation impossible.

## Migration files

Every `NNNN_name.up.sql` file has a matching `NNNN_name.down.sql` file. Apply migrations in ascending order and roll them back in descending order:

- `0001_init` creates the `platform` schema.
- `0002_venue_layout` creates the `cinema` schema and depends on `0001_init`.
- `0003_reservation` adds the reservation hold tables and depends on `0002_venue_layout`.
- `0004_idempotency` adds the idempotency records for hold creation and depends on `0003_reservation`, because a record references the hold it produced.
- `0005_checkout` adds `cinema.events.price_cents`, `cinema.payment_attempts`, `cinema.tickets` and `cinema.outbox`, and depends on `0003_reservation` for the reservation a payment and a ticket belong to.
- `0006_one_open_payment_attempt` adds the partial unique index `payment_attempts_open_per_reservation`, so one reservation can never carry two open (`pending`) payment attempts, and depends on `0005_checkout` for the table it guards.

Rollback of `0002_venue_layout` removes **all M1 data** in that schema. Do not run it on a database containing data you need to keep. Rollback of `0003_reservation` removes only the M2 hold objects and keeps every M1 table and row. Rollback of `0004_idempotency` removes only the idempotency records, so roll it back *before* `0003_reservation`; the record's foreign key to `cinema.reservations` otherwise blocks that drop. Rollback of `0005_checkout` removes the whole checkout schema — the `cinema.tickets` and `cinema.payment_attempts` tables, `cinema.outbox`, the event price column — and restores the narrower 0003 reservation status constraint, while keeping every M0, M1, M2 and 0004 object. It refuses to run at all while a reservation is `paid` or a payment attempt is `captured`, because dropping those rows would delete the record of money already taken, and it has to run *before* `0003_reservation`, which its foreign keys otherwise block. Rollback of `0006_one_open_payment_attempt` drops only the one-open-attempt index: it removes a guard, never a row, so it needs no refusal of its own and every stored attempt, ticket and outbox row survives it. It has to run before `0005_checkout` for the same reason the other versions run in descending order, although dropping the table would take the index with it.

## Reservation holds (M2)

- `cinema.reservations` — one purchase intent: `event_id` (foreign key to `cinema.events`), `status`, and the durable `expires_at` hold deadline. The TTL itself is configuration, not schema.
- `cinema.reservation_seats` — one row per held seat: `reservation_id`, `event_id`, `event_seat_id` (foreign key to `cinema.event_seats`), and `released_at`. A row is *active* while `released_at IS NULL`.

The two foreign keys on `reservation_seats` are composite and share `event_id`, so a reservation can never hold a seat of another event.

Two schema rules enforce the hold invariants:

1. The partial unique index `reservation_seats_active_hold` on `(event_seat_id) WHERE released_at IS NULL` allows at most one active hold per event seat. Two concurrent claimers cannot both commit an active row: the loser gets a `unique_violation`. The predicate cannot reference the clock, so it never has to be rebuilt when a hold expires.
2. The `release_expired_seat_hold` trigger on `cinema.reservation_seats` runs before each insert, rejects a claim whose `expires_at` has already passed, and releases the expired rows of the claimed seat inside the acquiring transaction. Expiry therefore frees a seat for the next hold even before the expiry worker sweeps, and the sweep only has to clean up seats nobody re-claims.

Both rules are pinned by `tests/migrations/m2_schema.sql`. Released rows are kept as history; the active hold is a single row, so availability reads must treat a seat as occupied only while `released_at IS NULL AND reservations.expires_at > now()`: an expired hold that nobody has re-claimed yet is still stored as active until the claim trigger or the expiry worker releases it.

## The availability read and its layout projection (M2)

`GET /api/v1/events/{eventID}/seats` serves every materialized seat of one event with its occupancy **and** the layout projection the browser needs to draw the real seat map. The read (`listSeatAvailabilityQuery` in `apps/api/internal/reservation/service.go`) joins the physical seat and its row:

```sql
FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
JOIN cinema.layout_rows lsr ON lsr.id = ls.layout_row_id
WHERE es.event_id = $1
ORDER BY lsr.position, ls.position
```

Both joins are inner joins because a materialized event seat always comes from one enabled seat of the event's own published layout. The wire shape is additive — `id`, `layoutSeatId`, `state`, `held` as before, plus `rowLabel`, `seatNumber` and `kind`:

- **The occupancy predicate is unchanged**: `released_at IS NULL AND reservations.expires_at > now()`, evaluated by the same `EXISTS` subquery. Adding columns to the projection must never change it, because expiry is lazy.
- **The order is the map order, not an id order.** Rows come in their own `position` order and seats in their `position` order inside the row. `position` is unique per layout version and per row (`layout_rows`, `layout_seats`), so the pair is a total order and the response is deterministic for a given layout. The browser relies on that order: it groups the seats into rows rather than re-sorting by event seat id.
- **Each row keeps its own seat count.** `cinema.materialize_event_seats` copies only *enabled* seats, so a row whose second seat is disabled appears with one seat. Nothing pads a row to the widest one, and the projection is immutable for the life of the event because a published layout and an event's layout are both immutable.

This projection is verified end to end by `make verify-m2`, which seeds the irregular layout below and asserts the served row labels, seat numbers, kinds and per-row counts — a regression in the join or the ordering fails the milestone check — and against PostgreSQL by `TestListSeatAvailabilityProjectsTheSeededLayout`, which also proves the read follows the lazy-expiry predicate. `tests/migrations/m2_idempotency.sql` keeps pinning the predicate itself.

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

## Checkout: price, payment attempts, tickets and outbox (M3)

The reservation is the order, so there is no separate orders table: the row that holds the seats is also the row that is paid, and its seats become tickets. `0005_checkout` adds four things.

- `cinema.events.price_cents` — v1 sells **one price per event**, stored in minor units as `integer NOT NULL DEFAULT 0 CHECK (price_cents >= 0)`. The order amount is `price_cents × seats`. A zero price is storable because a free event is a product question, not a schema one: the checkout (the simulated provider needs a positive amount) decides whether zero can be sold.
- `cinema.payment_attempts` — the durable replay record of one payment attempt: `reservation_id` (foreign key to `cinema.reservations`), `provider`, `provider_reference`, `amount_cents` (`> 0`), `outcome` (`approved`, `declined`), `status` (`pending`, `captured`, `declined`, `failed`), the client `idempotency_key` and `request_fingerprint`, plus `created_at` and `updated_at`.
- `cinema.tickets` — one row per sold seat: `reservation_id`, `event_id`, `event_seat_id` (foreign key to `cinema.event_seats`), `status` (`issued`, `void`) and `issued_at`. Redeeming a ticket (`used`) is an M4 cashier decision, not part of v1.
- `cinema.outbox` — `topic`, `payload` (`jsonb`), `created_at` and nullable `published_at`, written in the checkout transaction and published afterwards by a relay.

**The reservation status gains `paid`.** The 0003 constraint only described the hold lifecycle, so `0005_checkout` replaces `reservations_status_check` under the same name with `held`, `payment_pending`, `paid`, `expired`, `cancelled`. `paid` is what the capture transaction writes, and it is deliberately outside the M2 sweep's candidate set (`status IN ('held','payment_pending')` in `apps/api/internal/reservation/expiry.go`), so **a reservation whose payment was captured can never be expired and have its seats released**. The rollback restores the narrower 0003 constraint, which is why it requires that no reservation is left `paid`.

**A payment attempt can exist before the provider answers.** `provider_reference` and `outcome` are nullable, because the row is inserted before the call: that pre-call row is the crash window the table exists to close. Two constraints make the nullability safe:

- `payment_attempts_decided_check` — a verdict always comes with the provider object that produced it, and a `captured` or `declined` attempt must carry **both** a `provider_reference` and an `outcome`. `pending` and `failed` rows may carry nothing, which is the state of a call that has not answered yet.
- `payment_attempts_status_outcome_check` — the stored state and the verdict cannot disagree: `captured` carries `approved`, `declined` carries `declined`, and a `pending` row carries no verdict requirement.

Four guards make the impossible cases impossible, and each one is a database constraint rather than a service check, because two concurrent checkouts must be decided by one of them losing:

1. **A reservation is paid at most once.** The partial unique index `payment_attempts_captured_per_reservation` on `(reservation_id) WHERE status = 'captured'` allows one captured attempt per reservation. Declined and failed attempts are history, not payments, so they are outside the predicate.
2. **A provider reference is charged once.** The partial unique index `payment_attempts_provider_reference` on `(provider, provider_reference) WHERE provider_reference IS NOT NULL` means a retried provider callback cannot insert the same provider object twice, while two pending rows that have no reference yet do not collide.
3. **A reservation has at most one open payment attempt** (`0006_one_open_payment_attempt`). The partial unique index `payment_attempts_open_per_reservation` on `(reservation_id) WHERE status = 'pending'` is the in-flight guard, and it is the reason there is no timed window anywhere in the checkout: an open attempt means the provider may already hold the money for that reservation under a reference only that attempt can settle, so a second pending attempt would be a second charge. Only the attempt that holds the client `Idempotency-Key` can settle the open one, which is why the buyer resumes by resending the **same** key, and why persisting that key across a page reload is a client requirement rather than a nicety. A decided attempt (`captured` or `declined`) leaves the index, so paying again after a decline is still allowed — that is also why the indexed column is the reservation and not the key.
4. **A seat is sold once, in its own event.** `UNIQUE (event_seat_id)` on `cinema.tickets` means one seat can never carry two tickets, and both of the ticket's references carry `event_id` — `(reservation_id, event_id)` to `cinema.reservations` and `(event_seat_id, event_id)` to `cinema.event_seats`, the same composite-key pattern `cinema.reservation_seats` uses — so a ticket can never join a reservation to a seat of **another** event.

The replay key is scoped to its reservation: `UNIQUE (reservation_id, idempotency_key)`, so **T3 must look the attempt up by reservation *and* key** — a key another buyer used can never answer for this reservation, and `request_fingerprint` stored next to it makes a reused key with a different payload detectable instead of silently answered. `tickets_reservation` and `payment_attempts_reservation` are plain indexes for the confirmation read, and `outbox_unpublished` on `(created_at) WHERE published_at IS NULL` is the relay's working set.

**Applying `0006` to a database that already ran the checkout.** `CREATE UNIQUE INDEX`
refuses to build over data that violates it, so a database that carries two open attempts
for one reservation cannot take this version. That is exactly the state the pre-`0006`
checkout could leave behind — a payment whose phase 3 never ran, followed by a retry with a
different key — so it is worth looking before migrating rather than discovering it halfway
through a deploy:

```sql
SELECT reservation_id, count(*)
FROM cinema.payment_attempts
WHERE status = 'pending'
GROUP BY reservation_id
HAVING count(*) > 1;
```

Every reservation this returns is money in doubt: a pending attempt means the provider may
already hold a charge under `payment_attempt:<attempt id>`. Resolve each one **with the
provider, before the migration**: if that reference was charged, record the verdict on the
attempt (`captured`, which writes the sale) and close the others; if it was not, the
attempt can be closed as `failed` or `declined`. Marking every duplicate `failed` without
asking the provider is faster and deletes the only record that the money may have left. A
fresh database has no attempts at all, so this concerns only environments that served
payments before this version existed.

**What the checkout transaction relies on.** The attempt is written before the provider is called, then captured in the same transaction that marks the reservation paid, sells the seats, issues one ticket per seat and writes the outbox row. T3 can therefore rely on the database to refuse a second open attempt, a second capture, a duplicated provider reference, a duplicate ticket and a ticket of another event, and only has to map the failures: `unique_violation` on `payment_attempts_open_per_reservation` is "a payment is already in flight" (refused at any age of the open attempt), on the captured index it is "already paid" (answer from the stored attempt), on the provider pair it is a duplicate callback, on the per-reservation idempotency key it is a replay race that is retried once and then answered from the stored row, and on the ticket seat it is "seat already sold". `check_violation` means an amount that is not positive, a state outside the enumerated values, an incoherent status/outcome pair, or a decided attempt without its reference or verdict. `foreign_key_violation` means an unknown reservation or seat, or a seat of another event. The outbox row is written in the same transaction, so a crashed relay retries delivery and never loses or duplicates the sale; nothing about delivery correctness depends on the relay running.

**What T3 still has to enforce itself**, because the schema cannot:

- **The amount is `price × seats`.** The database only knows the amount is positive; the checkout computes it from the event price and the number of tickets it issues and must not trust a client-supplied amount.
- **The provider reference is trimmed before it is stored.** The check rejects a blank reference, but normalisation is the adapter's job.
- **The seats are the ones the reservation holds.** The composite key proves the ticket's reservation and seat are in the same event; it does not prove that this reservation held that seat. T3 must issue tickets from the reservation's own `cinema.reservation_seats`.
- **Tickets are linked to the capture only by the transaction.** Nothing in the schema ties a ticket row to a captured attempt; atomicity plus "one captured attempt per reservation" is the guarantee, so the ticket and the capture must be written in the same transaction.

The in-flight rule is deliberately **not** on that list: nothing about it is left to the application. There is no window, no read of other attempts and no configuration knob; a second key is refused by `payment_attempts_open_per_reservation` and the checkout only maps the violation to `409 payment_in_flight`.

**Deliberately not enforced in v1** (explicit follow-ups, not oversights):

- A voided ticket does not free its seat. `status = 'void'` keeps the seat's `UNIQUE` row, so re-selling it needs a policy (and M4 decides it).
- The outbox has no deduplication key. A relay that publishes and then crashes before marking the row published delivers the notification twice, which consumers must tolerate.
- There is no currency column: `amount_cents` and `price_cents` are minor units of one currency for v1.

`tests/migrations/m3_checkout.sql` pins every one of these rules: a second captured attempt for one reservation, a duplicated `(provider, provider_reference)`, a second ticket for one event seat, a ticket of another event, a negative price, a non-positive amount, a decided attempt without its reference or verdict, and a captured/declined attempt whose verdict disagrees with its state are all refused and persist nothing; a pending attempt without a reference or an outcome, two such pending attempts (on different reservations, because 0006 allows one open attempt per reservation) without a collision on the provider-reference index, the same idempotency key on two reservations and a ticket for the reservation's own seat are accepted; a `paid` reservation is markable and is never an expiry candidate; and an outbox row is inserted and stays unpublished. `tests/migrations/m3_one_open_attempt.sql` pins the 0006 invariant on the real schema: a second `pending` attempt for a reservation that already has an aged one is refused with `unique_violation` on `payment_attempts_open_per_reservation` and the first row survives, a `pending` attempt for another reservation is accepted, a reservation whose attempt was `captured` or `declined` accepts a new `pending` attempt (and refuses the next open one), and decided attempts remain storable as history. `scripts/migrations/verify_migrations.sh` rolls 0006 back and reapplies it, and asserts that the rollback removes only the index while the checkout tables, the price column and the 0005 capture guard stay.

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

To apply or roll back the M3 checkout schema:

```sh
MIGRATION_VERSION=0005_checkout docker compose --profile tools run --rm migrate
MIGRATION_VERSION=0006_one_open_payment_attempt docker compose --profile tools run --rm migrate
MIGRATION_VERSION=0006_one_open_payment_attempt MIGRATION_DIRECTION=down docker compose --profile tools run --rm migrate
MIGRATION_VERSION=0005_checkout MIGRATION_DIRECTION=down docker compose --profile tools run --rm migrate
```

The development seed sets a price for both seeded sessions (`12.50` EUR and `11.50` EUR), chosen so that `price × seats` never ends in the `13` minor units the simulated provider declines, and it repairs the price of an already seeded event on every run.

Verify the M0, M1, M2, M3 checkout and M3 one-open-attempt up/down/up lifecycles and schema assertions against an isolated temporary database:

```sh
docker compose --profile tools run --rm migration-verify
```

The verification command drops only a database whose name begins with `cinema_m0_verify`; it does not remove named volumes or touch the development database.

Verify the M2 end-to-end contract: the accepted concurrency race, the loser conflicts, availability after the race, the running expiry worker, and the killed-worker recovery evidence.

```sh
make verify-m2
```

That script creates a disposable database named `cinema_m2_smoke_<pid>`, applies every migration to it, runs the gated PostgreSQL tests against it (`M2_RECOVERY_DATABASE_URL`), starts the API binary on port 18081 with local administration, the public reservation surface and a 60s hold window, asserts that the public availability read serves the seeded irregular layout projection (row labels in row-position order, seat numbers, kinds, and each row's own seat count), runs `go test -tags acceptance_m2 ./tests/concurrency/` with the seeded `M2_EVENT_ID` and `M2_SEAT_IDS`, then drops the database, flushes only the dedicated Redis database it used, and stops its own process. The default `go test ./apps/api/...` suite needs no database: the PostgreSQL-only tests skip unless `M2_RECOVERY_DATABASE_URL` is set.
