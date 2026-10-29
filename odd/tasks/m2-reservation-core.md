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
