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
