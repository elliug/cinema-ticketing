# Cinema Ticketing — Architecture Baseline

**Decision:** build the first release as a modular Go backend, a Next.js/TypeScript web application, PostgreSQL as the durable source of truth, and Redis as the short-lived coordination layer for seat holds. This design preserves seat-layout flexibility and prevents concurrent double booking without implementing a virtual waiting room in v1.

**Audience:** product, engineering, and operations. Read [Quick path](#quick-path) first; implementation requirements and invariants follow.

## Quick path

1. Model every reusable physical room layout independently from each screening event.
2. Create event-specific seats from that layout; availability is always scoped to an event.
3. Hold a selection atomically in Redis for a fixed TTL, then persist the reservation conditionally in PostgreSQL.
4. Confirm payment exactly once through an idempotent payment gateway interface.
5. Publish seat-state changes to connected clients and expire abandoned holds safely.

## Scope and boundaries

| Included in v1 | Deferred beyond v1 |
|---|---|
| Multiple cinemas, rooms, layouts, rows, and seats | Virtual waiting room / admission queue |
| Rows with independently sized seat counts | Extreme premiere-scale capacity planning |
| Public seat selection and checkout | Live Stripe/PayPal integration |
| Authenticated users and guest checkout | Native mobile applications |
| Admin, cashier, and reporting surfaces | Dynamic yield-pricing engine |
| Simulated payment provider behind an interface | Loyalty program and promotions |
| Hold window, expiry, cancellation, refund model | Multi-region active-active deployment |

## System overview

```text
Browser (Next.js PWA)
  ├─ Public purchase flow: browse → choose seats → hold → simulated payment → ticket
  ├─ Cashier flow: assisted sale / lookup / simulated payment
  └─ Admin flow: cinemas, rooms, layouts, events, pricing, reports
          │ HTTPS + authenticated API requests / Server-Sent Events
          ▼
Go API (modular monolith)
  ├─ Identity and authorization
  ├─ Catalog and scheduling
  ├─ Layout administration
  ├─ Reservation orchestration
  ├─ Payment adapter
  ├─ Ticketing and reporting
  └─ Event publication
       │                       │
       ▼                       ▼
Redis                         PostgreSQL
short-lived seat holds        durable transactional truth
TTL + atomic Lua scripts      layouts, events, reservations, payments, tickets
```

A modular monolith is intentional for v1: one deployable Go service keeps reservation transactions, expiry, payment confirmation, and authorization observable and consistent. Modules communicate through Go interfaces and domain events; they may be extracted later only when measured load or team boundaries justify it.

## Technology decisions

| Area | Decision | Why |
|---|---|---|
| Backend | Go HTTP API | Predictable concurrency, compact deployment, and a strong fit for reservation coordination. |
| Web application | Next.js + TypeScript PWA | Browser-first Chrome delivery with typed UI and installable experience. |
| Durable storage | PostgreSQL | ACID transactions, constraints, reporting queries, and reliable audit history. |
| Ephemeral coordination | Redis | Low-latency TTL holds and atomic multi-seat claims. It is not the final authority. |
| Updates | Server-Sent Events (SSE) initially | One-way availability updates are simpler than WebSockets; add WebSockets only for a demonstrated bidirectional need. |
| Authentication | OIDC-capable identity module + guests | Enables accounts while allowing purchase with verified email or phone. |
| Payments | `PaymentGateway` interface + simulator | Lets v1 exercise payment lifecycle without binding business logic to a provider. |
| Observability | Structured logs, metrics, traces | Reservation conflicts, hold expiry, payment idempotency, and latency require operational visibility. |

## Domain model

### Layout is not availability

A **layout** describes reusable physical geometry. An **event** references one layout through its room, and owns its own sellable seat state. The same physical seat can therefore be sold for one screening and available for another.

```text
Cinema 1 ──< Room 1 ──1 LayoutVersion 1 ──< LayoutRow ──< LayoutSeat
                     │
                     └──< Event ──< EventSeat >── ReservationSeat >── Reservation
                                                   │
                                                   └── Ticket
Reservation ──< PaymentAttempt
User / GuestCustomer ──< Reservation
```

### Core entities

| Entity | Responsibility | Important fields |
|---|---|---|
| `cinemas` | Venue identity and locale | `id`, `name`, `timezone`, `address` |
| `rooms` | A physical screening room | `id`, `cinema_id`, `name`, `active_layout_version_id` |
| `layout_versions` | Immutable, reusable room geometry | `id`, `room_id`, `version`, `status` |
| `layout_rows` | Ordered row in one layout | `id`, `layout_version_id`, `label`, `position` |
| `layout_seats` | Physical seat position in row | `id`, `layout_row_id`, `number`, `position`, `kind`, `enabled` |
| `films` | Film metadata | `id`, `title`, `runtime_minutes`, `rating` |
| `events` | One screening at a scheduled time | `id`, `room_id`, `film_id`, `starts_at`, `layout_version_id`, `status` |
| `event_seats` | Sale state of one seat for one event | `id`, `event_id`, `layout_seat_id`, `state`, `reservation_id`, `hold_expires_at`, `price_snapshot` |
| `reservations` | Purchase intent and lifecycle | `id`, `event_id`, `customer_id`, `status`, `expires_at`, `currency`, `total_amount` |
| `reservation_seats` | Seats in a reservation | `reservation_id`, `event_seat_id`, `unit_price` |
| `payment_attempts` | Provider operation and idempotency ledger | `id`, `reservation_id`, `provider`, `provider_reference`, `status`, `idempotency_key` |
| `tickets` | Final right of entry | `id`, `reservation_seat_id`, `barcode`, `issued_at`, `status` |
| `users` | Registered identities | `id`, `email`, `phone`, `role`, `status` |
| `guest_customers` | PII for guest purchase/contact | `id`, `email`, `phone`, `verified_at` |
| `audit_log` | Operator and security trace | actor, action, aggregate, before/after metadata, timestamp |

### Layout rules

- A row has its own ordered collection of seats; there is no global fixed seat count per row.
- `label` is display-oriented (for example, `A`, `B`, `VIP-1`); `position` determines ordering.
- `number` is unique only within its row and layout version.
- A published layout version is immutable. Editing geometry creates a new draft version.
- An event snapshots the chosen `layout_version_id`; changing a room's active layout cannot change an already scheduled event.
- Creating an event materializes exactly one `event_seat` per enabled layout seat within one database transaction.

## State machines

### Event and event seat

```text
Event: draft → published → on_sale → started → completed
                    └──────→ cancelled

EventSeat: available → held → sold
                 │        │
                 │        └──→ available  (hold expiry, cancellation, failed payment)
                 └──────────→ unavailable (operator block)
```

`EventSeat.state` is a projection of the durable reservation lifecycle. It is never changed without a state-transition reason and transaction.

### Reservation

```text
created → held → payment_pending → paid → ticketed
   │         │          │             └──→ refunded
   │         │          └──→ failed → released
   │         └──→ expired / cancelled → released
   └──→ rejected
```

| State | Meaning | Allowed next states |
|---|---|---|
| `created` | Request accepted but seats not yet held | `held`, `rejected` |
| `held` | All requested seats are exclusively reserved until `expires_at` | `payment_pending`, `expired`, `cancelled` |
| `payment_pending` | A payment command is being processed | `paid`, `failed`, `expired` |
| `paid` | Payment was accepted; seats are durable sold | `ticketed`, `refunded` |
| `ticketed` | Tickets issued | `refunded` under controlled policy |
| `expired` / `cancelled` / `failed` | Terminal sale attempt, seats released | none |

### Payment attempt

```text
created → processing → authorized → captured
                      └→ declined / failed / cancelled
```

The simulated provider may transition directly to `captured`, but the domain supports the fuller lifecycle required by real providers. A provider webhook or callback is always deduplicated by provider event/reference and processed idempotently.

## Reservation invariants

The following are non-negotiable correctness rules:

1. **A seat may be sold once per event.** PostgreSQL enforces this with a partial unique constraint on the sold association, plus state checks in the transaction.
2. **A reservation either holds all requested seats or none.** Partial holds are never returned as success.
3. **Only a currently valid hold can begin payment.** The authoritative expiry is checked in PostgreSQL, not only Redis.
4. **A successful payment creates durable sold seats and tickets atomically.** It cannot leave a captured payment with only some tickets.
5. **Repeated client requests do not create duplicate reservations or payments.** The API accepts idempotency keys and persists their result.
6. **The client is advisory.** Seat IDs, prices, totals, and hold expiry are calculated and authorized server-side.
7. **Redis accelerates exclusion; PostgreSQL resolves final truth.** Redis loss may create an extra conflict or require reconciliation, but may never create double sales.

## Concurrency protocol

### Why two layers

Redis prevents high-contention users from repeatedly colliding at the database and supports automatic hold TTL. PostgreSQL guarantees correctness under Redis restarts, races, retries, and operational recovery. No success is final until the PostgreSQL transaction commits.

### Hold operation: all seats or none

**Input:** `eventId`, ordered unique `eventSeatIds`, authenticated user or guest identity, and an API idempotency key.

1. Validate the event is on sale and every requested seat belongs to it.
2. Create or replay the idempotent request result. Reject duplicate seat IDs.
3. Run a Redis Lua script over keys `hold:{eventId}:{eventSeatId}`:
   - if any key exists, return its conflicting seat identifiers;
   - otherwise set every key to the same `reservationId` using `PX holdTTL`;
   - return success only after every key was set.
4. Begin a PostgreSQL transaction. Lock the affected `event_seats` in stable sorted ID order (`FOR UPDATE`).
5. Verify every row is `available` and belongs to the event, calculate authoritative prices, create the reservation, insert reservation seats, and transition rows to `held` with the same expiry.
6. Commit. If the transaction fails, delete only Redis keys whose value matches this reservation ID using a compare-and-delete Lua script.
7. Publish one `seat_hold_created` availability event after commit.

If the database detects a conflict after Redis claimed the keys, it wins. The API releases its Redis keys, returns `409 Conflict`, and emits no successful hold.

### Payment confirmation

1. The client submits a payment command with an idempotency key and reservation ID.
2. In one PostgreSQL transaction, lock the reservation and all its event seats. Ensure the reservation is `held` or recoverably `payment_pending`, and `expires_at > now()`.
3. Create/replay the payment attempt with a unique idempotency record.
4. Invoke the provider using its own idempotency reference. The simulated provider returns a deterministic approved/declined outcome configured for test scenarios.
5. On approved callback, in one transaction transition payment to `captured`, reservation to `paid`, all its event seats to `sold`, and create tickets. Commit before emitting `reservation_paid`.
6. Remove matching Redis hold keys and publish availability change.

A real external payment cannot be enclosed in a database transaction. The design therefore uses a durable outbox and idempotent provider references. A recovery worker reconciles payment attempts left in `processing` before treating them as failed.

### Expiration and reconciliation

Redis TTL only makes a key disappear. A background worker is responsible for durable release:

1. Periodically select expired `held`/`payment_pending` reservations with `FOR UPDATE SKIP LOCKED`.
2. If payment capture cannot be proven, mark the reservation `expired`, return associated `event_seats` to `available`, and enqueue an outbox event.
3. Compare-and-delete any remaining Redis keys belonging to that reservation.
4. Publish `seat_hold_expired`.

The worker must be safe to run concurrently and repeatedly. Metrics alert when the oldest expired hold exceeds a bounded lag.

## API contract baseline

The external API is versioned under `/api/v1`. Error responses use RFC 9457 problem-details JSON with a stable `code`, a human-safe `detail`, and a request correlation ID.

The tables below are the **target contract for v1**, not a description of the current binary. What exists today and is gated by CI:

- `POST /api/v1/reservations/holds` — requires `Idempotency-Key`; answers `201` with `id`, `eventId`, `seatIds` and `expiresAt`, or `409 seat_unavailable`, `400 hold_window_expired`, `400 invalid_input`, `503` when the coordination layer or the database is unreachable. Replaying the same key returns the stored result and never extends the window.
- `GET /api/v1/events/{eventId}/seats` — occupancy plus the layout projection (`rowLabel`, `seatNumber`, `kind`, `held`) in row and seat order.

Everything else in these tables is planned. M3 adds the payment and ticket routes plus the PDF download.

### Public customer API

| Method | Route | Purpose |
|---|---|---|
| `GET` | `/films` | Browse films and screening availability. |
| `GET` | `/events/{eventId}` | Get event, price bands, and seat layout projection. |
| `GET` | `/events/{eventId}/seats` | Snapshot event-seat availability. |
| `GET` | `/events/{eventId}/stream` | SSE stream of availability updates. |
| `POST` | `/reservations/holds` | Atomically hold a complete seat selection. |
| `GET` | `/reservations/{reservationId}` | Read reservation and expiry state. |
| `POST` | `/reservations/{reservationId}/payment` | Start/replay payment confirmation. |
| `POST` | `/reservations/{reservationId}/cancel` | Cancel a valid, unpaid hold. |
| `GET` | `/tickets/{ticketId}` | Retrieve ticket presentation data for its owner. |
| `GET` | `/tickets/{ticketId}/pdf` | Download the stored ticket PDF through a short-lived pre-signed link. |
| `GET` | `/tickets/{ticketId}/qr.svg` | Render the ticket's signed QR code as an image for the page and the printout. |
| `POST` | `/tickets/verify` | Verify a presented ticket code against the stored ticket. |

`POST /reservations/holds` requires `Idempotency-Key`, a customer session/guest verification token, and this body shape:

```json
{
  "eventId": "evt_01H...",
  "seatIds": ["eseat_01H...", "eseat_01I..."]
}
```

Success includes `reservationId`, canonical seat labels, server-calculated `totalAmount`, `currency`, and ISO-8601 `expiresAt`. A conflict includes only the requested seat labels that were unavailable; it never exposes another buyer's identity or reservation.

### Administrative and cashier API

| Route group | Role | Responsibilities |
|---|---|---|
| `/admin/cinemas`, `/admin/rooms`, `/admin/layouts` | `admin` | Venue and immutable layout-version management. |
| `/admin/films`, `/admin/events`, `/admin/prices` | `admin` | Catalog, scheduling, on-sale state, price rules. |
| `/cashier/reservations` | `cashier` | Assisted holds, payment simulation, print/reissue under policy. |
| `/admin/reports` | `admin`, `analyst` | Sales, occupancy, hold expiry, refunds, and reconciliation data. |

Every mutating administrative or cashier operation is audited with actor, role, correlation ID, target IDs, and before/after metadata excluding payment secrets and unnecessary PII.

## Frontend architecture

### Public application

- **Discovery:** film and event list filtered by cinema/date.
- **Seat map:** renders from layout rows and seats; no assumption of a rectangular grid or uniform row size.
- **Selection:** local optimistic indication only; server hold response is authoritative.
- **Countdown:** displays server `expiresAt`; on expiry or SSE update, returns the UI to selection.
- **Checkout:** signed-in account path and guest path with email/phone verification; uses the same reservation API.
- **Ticket wallet:** lists purchased tickets and barcode/QR presentation data.

### Operations application

An authenticated role-aware route group exposes:

- **Admin:** cinemas, rooms, layout versions, films, events, pricing, user roles, and reports.
- **Cashier:** event search, assisted seat selection, guest lookup, simulated payment, and ticket issue/reprint under audit.
- **Reports:** sales volume/value, occupancy, conversion from holds, expiry rate, payment outcomes, refunds, and operational latency.

The UI must treat a seat update during selection as normal: clear it from selection, display a neutral availability message, and never promise a seat before a hold succeeds.

## Security and privacy baseline

- Enforce role-based authorization server-side: customer, cashier, admin, and analyst are distinct roles.
- Use short-lived secure sessions/tokens and hashed passwords only if local passwords are implemented; prefer an external OIDC provider when deployed.
- Guests prove control of email or phone before accessing/resuming a reservation; do not make phone/email searchable to arbitrary users.
- Rate-limit anonymous and authentication endpoints by IP and account/guest identifier. This is v1 protection, not a virtual waiting room.
- Validate every identifier and enforce event ownership for every event seat supplied by a client.
- Store money as integer minor units plus ISO currency, never floating-point.
- Encrypt transport, use managed secret storage, redact authorization and payment fields from logs, and minimize retained guest PII.
- Make refund, reissue, schedule change, and manual seat-block actions auditable.

## Reliability, operations, and observability

### Required metrics

| Metric | Purpose |
|---|---|
| hold attempts / successes / conflicts | Measure contention and conversion. |
| hold expiration count and expiry-worker lag | Detect abandoned-checkout behavior and stuck cleanup. |
| reservation and payment idempotency replay count | Detect client retry and provider behavior. |
| seat-state transition failures | Detect invariant or data issues. |
| payment outcome and reconciliation age | Detect payment-provider or recovery faults. |
| PostgreSQL lock wait and transaction latency | Detect database contention. |
| Redis Lua latency/error and key count | Detect hold-layer degradation. |
| SSE active connections and delivery delay | Monitor real-time experience. |

### Failure posture

| Failure | Expected behavior |
|---|---|
| Redis unavailable | Do not accept new holds until a documented degraded transactional fallback is implemented and tested; preserve correctness over conversion. Existing durable reservations remain readable. |
| PostgreSQL unavailable | Reject mutation safely; never claim a successful hold or payment. |
| API process restart | Recover durable holds/payments through PostgreSQL and workers; Redis keys expire naturally. |
| Duplicate request/webhook | Return the original idempotent result; do not duplicate tickets or charges. |
| SSE disconnect | Client refetches seat snapshot and reconnects with backoff. |
| Expiry worker delay | Availability may be temporarily conservative; sold seats never become available. |

## Testing strategy

Tests prove observable business rules through module boundaries and HTTP interfaces where practical.

| Layer | Required proof |
|---|---|
| Unit | State-transition guards, price calculation, layout validation, authorization rules. |
| PostgreSQL integration | Constraints, transaction locks, materialization of event seats, expiry worker idempotence. |
| Redis integration | Lua all-or-none semantics, value-checked release, TTL behavior. |
| API integration | Exact success/error contracts, idempotency replay, guest and role authorization. |
| Concurrency | N parallel hold requests for the same seats yield at most one successful reservation; no partial selection success. |
| Payment recovery | Duplicate callback, late callback after expiry, restart during `processing`, and reconciler behavior. |
| Browser end-to-end | Irregular seat map, expiration countdown, seat lost during selection, guest and signed-in checkout. |
| Load | Moderate concurrent traffic representative of v1; document headroom before adding the queue feature. |

The first implementation milestone must begin with a deterministic red integration test that launches concurrent hold requests for overlapping seats and proves exactly one succeeds.

## Delivery milestones

| Milestone | Status | Outcome | Exit evidence |
|---|---|---|---|
| M0 — Foundation | Delivered | Repository, local dependencies, migrations, module boundaries, CI baseline | Service and web app run locally; health checks and migration rollback verified. Delivered by `make verify-m0`, which runs in CI. |
| M1 — Venue & layout | Delivered | Admin can create immutable irregular layouts and materialize event seats | Tests prove rows may differ in seat count and published layouts do not mutate. Delivered by `make verify-m1`, which runs in CI. |
| M2 — Reservation core | Delivered | Atomic hold, TTL expiry, and double-booking protection | Concurrent integration test and worker recovery test pass. Delivered by `make verify-m2`, which runs in CI; the record is `odd/tasks/m2-reservation-core.md`. |
| M3 — Checkout & tickets | Delivered | Account/guest purchase through simulated payments | Payment idempotency and ticket issuance flow pass end to end. Delivered by `make verify-m3`, which runs in CI as its own job: it holds seats, pays, fetches the ticket, verifies the signed code, downloads the PDF through its pre-signed redirect and proves the artefact survives a Floci restart and a container recreation. The record is `odd/tasks/m3-checkout-tickets.md` (T0–T8, logs L4–L15). |
| M4 — Operations | Planned | Cashier workflows, reporting, audit trail | Role/authorization and reporting accuracy tests pass. |
| M5 — Hardening | Planned | Monitoring, backups, load characterization, security review | Runbook and measured moderate-load baseline approved. |
| M6 — Future scale | Planned | Virtual waiting room and real payment adapter | Separate architecture decision based on measured bottlenecks. |

A milestone is *delivered* when its verification command runs in CI on the default branch; the per-milestone record lives in `odd/tasks/`.

## Implementation acceptance checklist

- [ ] Every event seat is linked to an immutable layout version and only to one event.
- [ ] A row may contain a different number of seats than any other row.
- [ ] A multi-seat hold is atomic: all selected seats are held, or none are.
- [ ] Concurrent requests cannot sell or hold the same seat successfully at the same time.
- [ ] Redis holds have a bounded TTL and PostgreSQL has durable expiry cleanup.
- [ ] Payment requests and callbacks are idempotent.
- [ ] Guest checkout works without creating a permanent account.
- [ ] Administrative, cashier, and reporting routes enforce distinct roles.
- [ ] The UI receives or refetches availability changes and handles conflicts calmly.
- [ ] No real payment credentials or live payment flow exist in v1.
- [ ] The virtual waiting room remains absent until explicitly scoped as a later milestone.

## Implementation decisions

### Decided

1. Hold duration — ten minutes by default, configurable down to seconds through `API_HOLD_TTL`. Decided in M2; recorded in `odd/tasks/m2-reservation-core.md` (S3). Cashier-assisted sales may adjust it in M4.
2. Pricing-rule complexity for v1 — one price per event, so the order amount is price × seats. Decided before M3 and recorded in `odd/tasks/m3-checkout-tickets.md`; the `cinema.events.price_cents` column itself arrives with M3's checkout schema (T2), so it does not exist yet on the default branch.

### Open

These do not change the architecture, but must be selected before their milestone starts.

3. Identity provider and guest email/phone verification vendor.
4. Initial currency, tax, refund, and ticket-validation policies for the deployment jurisdiction.
5. Deployment target, backup retention, and operational ownership.

## References

- Feature tracking specification: [`odd/tasks/cinema-ticketing-design.md`](odd/tasks/cinema-ticketing-design.md)
- Next implementation work begins at **M0**, then **M1/M2** in that order. M2 is the first correctness-critical release slice.
