# M2 — Reservation core

Atomic all-or-nothing holds for a set of event seats, a bounded TTL with durable
PostgreSQL expiry, and protection against double booking under concurrency —
plus the web seat-selection surface that shows availability, counts down and
handles a seat lost mid-selection.
Builds on the integrated M1 stack on `main`.

## Specs

1. S1 — "dos usuarios no pueden reservar el mismo asiento simultáneamente" (design S3).
2. S2 — "Reservation uses a hold window with TTL-based automatic release before payment." (design S4).
3. S3 — "Quiero la opción de 10 minutos pero que pueda ser configurablen para por ejemplo, en pruebas, poder bajar a segundos." (user, hold duration).
4. S4 — "N parallel hold requests for the same seats yield at most one successful reservation; no partial selection success." (ARCHITECTURE.md:333).
5. S5 — "A multi-seat hold is atomic: all selected seats are held, or none are." (ARCHITECTURE.md:362).
6. S6 — "Redis holds have a bounded TTL and PostgreSQL has durable expiry cleanup." (ARCHITECTURE.md:364).
7. S7 — the accepted contract in `tests/concurrency/hold_concurrency_test.go`: `POST /api/v1/reservations/holds` with an `Idempotency-Key` header, 16 concurrent contenders for one seat produce exactly one `201` and 15 `409`.
8. S8 — "Concurrency | N parallel hold requests for the same seats yield at most one successful reservation; no partial selection success." plus worker recovery evidence required by `ARCHITECTURE.md:333` (M2 exit evidence).
9. S9 — "Browser end-to-end | Irregular seat map, expiration countdown, seat lost during selection, guest and signed-in checkout." — M2 delivers the first three; checkout belongs to M3.
10. S10 — "The UI receives or refetches availability changes and handles conflicts calmly." (ARCHITECTURE.md:365).
11. S11 — "Backend + UI de selección" (user, M2 scope).

## Tasks

- T1 — S1, S5, S6, S7 — reversible `0003` reservation schema with per-seat uniqueness and durable expiry columns, SQL tests — pending
- T2 — S3 — `API_HOLD_TTL` duration configuration (default `10m`, seconds allowed) with validation — pending
- T3 — S1, S4, S5, S6 — Redis coordination: atomic multi-seat acquire with TTL and full rollback, release path, idempotency — pending
- T4 — S4, S7, S10 — hold API (201/409, `Idempotency-Key`) and event availability read endpoint — pending
- T5 — S6, S8 — durable PostgreSQL expiry worker: idempotent sweep, restart-safe recovery — pending
- T6 — S7, S8 — `acceptance_m2` concurrency test green against a real server, plus `make verify-m2` — pending
- T7 — S9, S10, S11 — web seat selection: irregular map, countdown, conflict handling and availability refetch — pending

## Log

- L1 — "perfecto, continuamos con M2?"
- L2 — "Quiero la opción de 10 minutos pero que pueda ser configurablen para por ejemplo, en pruebas, poder bajar a segundos." (hold TTL decision)
- L3 — "Backend + UI de selección" (M2 scope decision)
- L4 — T1 delivered a reversible `0003_reservation` migration: `cinema.reservations` (durable `expires_at`, status lifecycle) and `cinema.reservation_seats` (active hold per seat, history kept through `released_at`), the partial unique index `reservation_seats_active_hold` on `(event_seat_id) WHERE released_at IS NULL`, the `release_expired_seat_hold` trigger that frees the claimed seat at claim time and rejects a claim whose own window already closed, and composite foreign keys that keep a hold inside its event. RED was observed first (`missing table cinema.reservations`) and GREEN after the migration, both for the SQL test and for `make verify-migrations`. Independent verification reproduced the RED/GREEN, confirmed that exclusivity comes from the index rather than from the trigger or the isolation level (the deadlock DETAIL names `reservation_seats_active_hold`), that a takeover of an expired hold yields exactly one active row, that `0003_reservation.down.sql` leaves `platform` and the eight M1 tables byte-identical, and that the M0/M1 assertions in `verify_migrations.sh` were preserved and still run first.
- L5 — Decisions this slice pins, and what T4/T5 must inherit. The active hold lives only in `reservation_seats`, so `event_seats` gets no projection columns even though `ARCHITECTURE.md` describes them; `event_seats.state` stays untouched by `0003`, so T4/T5 must either sync it inside the claim transaction or read availability exclusively from `reservation_seats`. Availability must filter `released_at IS NULL AND reservations.expires_at > now()`, because expiry is lazy: an expired hold that nobody re-claimed is still stored as active. The primary key `(reservation_id, event_seat_id)` means a released seat can only be re-held by a *new* reservation, so an idempotent replay must return the stored result instead of re-inserting. The trigger releases only the claimed seat, so T5 must tolerate partially released reservations and move `status` and `released_at` together. `23514` (own window expired) must be reported apart from `23505` on the active-hold index (seat taken) and from `23505` on the primary key (replay or self-conflict). Multi-seat claimers must insert in sorted `event_seat_id` order and retry `40P01`/`40001`, since reverse order deadlocks by construction.
- L6 — Slice boundary and delivery strategy. T1 closed at `b4d2eb6` (460 authored lines over two commits on `main`), past the advisory 400-line budget, so the delivery strategy was fixed before the next commit: M2 ships stacked to `main` with one PR per task, and T1 was published as PR #17 (`Refs #16`). T2 continues on `feat/m2-hold-ttl` from `b4d2eb6`, adding `API_HOLD_TTL` with a ten-minute default and a positive-duration rule so tests can run in seconds.
- L7 — T3 delivered the Redis coordination layer in `apps/api/internal/reservation`: one key per event seat (`reservation:{hold}:seat:<id>`, value = owning hold, TTL = hold window), an acquire that checks and claims the whole seat set inside one Lua script, a release that compare-and-deletes per key so it can never steal a seat re-acquired by another hold, a read-only `Held`, and validation that returns before any Redis call. Tests run against miniredis: seven behaviour tests (including 16 contenders for one seat with exactly one winner and a failed multi-seat acquire that leaves no key behind) plus twelve validation subtests. Independent verification reproduced the non-atomic failure path (a client-side GET-all/SET-all fake fails the suite 5/5), ran its own 32-contender two-seat race with a single owner per key, and confirmed `-count=5 -race`, the whole `apps/api/...` suite and `go vet` are green.
- L8 — Decisions and inherited constraints from T3. The window is written as whole milliseconds, so the stored deadline is at most one millisecond shorter than requested; renewal restarts the deadline of the keys in the request, so a caller that renews must send the hold's complete seat set or the hold ends up owning a mix of live and expired seats. Duplicate seat ids are rejected by `Acquire` only, because `Held` deliberately reports occupancy in request order and a test pins that. The literal `{hold}` hash tag keeps every seat key in one Redis Cluster slot, which is what makes the multi-key scripts valid but concentrates all hold traffic in a single shard, a load concern for M5. New dependencies are `github.com/redis/go-redis/v9` and, for tests, `github.com/alicebob/miniredis/v2`; `golang.org/x/sys v0.48.0` arrives as a new indirect requirement of go-redis, not as an upgrade. T4 must map transport errors to 503 or a retry rather than failing open, must not extend a hold indefinitely on idempotent replay, and must decide whether `event_seats.state` is synchronised or ignored.
