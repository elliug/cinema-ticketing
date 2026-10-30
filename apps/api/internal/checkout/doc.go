// Package checkout is the M3 checkout: it turns a held reservation into a paid
// one, sells its seats, issues one ticket per seat and records the sale in the
// transactional outbox.
//
// The reservation is the order, so there is no separate order object: the row
// that holds the seats is the row that is paid, and `cinema.events.price_cents`
// times the number of seats the reservation holds is the amount. The buyer never
// sends an amount, and nothing here trusts one.
//
// # The three phases
//
// One checkout is three steps, and the order is the whole design:
//
//  1. Before the provider is called, one transaction loads the reservation
//     (locking its row), requires it to be *held* and inside its window, computes
//     the amount, and inserts a `pending` `cinema.payment_attempts` row carrying
//     the client Idempotency-Key, a request fingerprint and the amount. Then it
//     **commits**. That committed row is what closes the double-charge window: a
//     crash after this point leaves a durable record of what was about to be
//     charged, so a retry finds it instead of charging again.
//  2. The provider is called with the amount and a reference derived from that
//     attempt's own identity (`payment_attempt:<attempt id>`), never from anything
//     the client sent. A retry of the same attempt therefore re-sends the same
//     reference, which is the provider's own idempotency key.
//  3. After the answer, one transaction stores the verdict and the provider
//     reference on the attempt and, when the payment was approved, marks the
//     reservation `paid`, sets the event seats to `sold`, inserts one
//     `cinema.tickets` row per held seat (each with its `event_id`) and inserts
//     the `cinema.outbox` row. It ensures the verdict belongs to the reservation
//     being paid, and that every seat a ticket is issued for was actually marked
//     sold, before anything is written. Then it **commits**. Only after that commit
//     is the Redis hold of those seats released, with the compare-and-delete Release
//     the reservation core already implements, so the coordination layer is never
//     told a seat is free before PostgreSQL knows it was sold.
//
// # Idempotency
//
// The client Idempotency-Key names the attempt inside its own reservation
// (`UNIQUE (reservation_id, idempotency_key)`), so a key another buyer used can
// never answer here. A replay with the same key and the same fingerprint returns
// the stored result without calling the provider again — even after the
// reservation became `paid`, because a completed sale is exactly what a replay
// must be able to answer. The same key with a different fingerprint is a conflict
// (`ErrIdempotencyKeyReused`), never a silent second charge. A `pending` attempt
// found on retry resumes from the provider call under the attempt's own reference
// instead of creating a second attempt, and a stored attempt is never charged an
// amount other than the one it was created with: a price that changed underneath
// a pending attempt is `ErrAmountMismatch`, not a different charge.
//
// A client key is not the only guard, because one buyer can send two keys. While
// another attempt of the same reservation is **open** (`pending`), a request
// carrying a *different* key is refused with `ErrPaymentInFlight` and never reaches
// the provider. Without that guarantee the second key would insert its own pending
// attempt while the first payment may already have taken the money, and the loser of
// the capture race would be left with money charged by the provider and a verdict
// recorded nowhere.
//
// The guarantee is **structural, not timed**: the partial unique index
// `payment_attempts_open_per_reservation` (0006) allows at most one open attempt per
// reservation, so the database refuses the second one and no request has to decide
// it. There is deliberately no in-flight query and no window next to the index: two
// guards that measure the same thing differently would be a hazard, and a clock
// cannot make an invariant hold — an attempt whose request crashed between the phase
// 1 commit and the provider call may already hold the buyer's money, however old the
// row is. That row keeps blocking a second key until the hold itself expires, because
// nothing else can reconcile the charge.
//
// Only the attempt that holds the client Idempotency-Key can settle the open attempt,
// so the buyer resumes by sending the **same** key. This makes persisting the key
// across a page reload a requirement of the client, not a nicety: a browser that
// invents a fresh key on refresh is answered `409 payment_in_flight`, which is the
// truthful answer — a payment is already under way. The web surface of T7 has to
// honour that contract.
//
// # Declines
//
// A declined charge is a successful request whose body says so
// (`paymentStatus: "declined"`). The attempt is stored as `declined`, and nothing
// else happens: the reservation stays `held`, no ticket exists, no outbox row is
// written and the Redis hold is left alone, because the buyer may still pay
// inside the hold window with a new key. Declining is a provider answer, never an
// error; a declined attempt is history, not a payment, which is why the schema's
// double-payment guard only covers `captured` attempts.
//
// # Error mapping
//
// Each failure keeps its own meaning so the route can answer with its own status:
// validation errors are caller error, a disagreement about the amount, a reused
// key, a payment already in flight, an expired hold, an already paid reservation, a
// seat already sold and a taken provider reference are conflicts, an unknown
// reservation is not found, and a provider or storage transport failure is
// retryable. Nothing is reported as a success unless the sale is committed.
//
// # What the rows carry
//
// The ticket rows are the durable seat sale: one row per held event seat, with
// its `reservation_id`, `event_id` and `event_seat_id`. The confirmation surface
// and the PDF read them by reservation. The outbox row (`ticket.issued`) carries
// the reservation, the event, the attempt, the amount and every ticket id, so a
// relay that publishes it can drive the later delivery work without reading the
// sale again.
package checkout
