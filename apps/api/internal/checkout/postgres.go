package checkout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// errAttemptRaced reports that another transaction inserted the attempt for this
// reservation and key between our lookup and our insert. The whole phase 1
// transaction is retried once, so the committed attempt answers as a replay
// instead of surfacing a unique violation to the buyer.
var errAttemptRaced = errors.New("checkout: a concurrent request stored this attempt")

// prepareAttemptAttempts bounds the retry of phase 1. One retry is enough: the
// first attempt either stored the row (this one is a replay) or committed nothing.
const prepareAttemptAttempts = 2

// The constraint names the checkout classifies its unique violations by. Naming
// them is not cosmetic: each one means something different to the buyer, and the
// open-attempt index must never be retried or reported as a reused key.
const (
	// openAttemptIndex is the partial unique index of 0006: at most one pending
	// payment attempt per reservation.
	openAttemptIndex = "payment_attempts_open_per_reservation"

	// attemptKeyConstraint is UNIQUE (reservation_id, idempotency_key), the replay
	// lookup of the flow.
	attemptKeyConstraint = "payment_attempts_reservation_id_idempotency_key_key"

	// capturedAttemptIndex is the partial unique index of 0005: at most one captured
	// attempt per reservation.
	capturedAttemptIndex = "payment_attempts_captured_per_reservation"

	// providerReferenceIndex is the partial unique index of 0005: one provider object
	// is recorded once.
	providerReferenceIndex = "payment_attempts_provider_reference"

	// ticketSeatConstraint is UNIQUE (event_seat_id) on cinema.tickets: one seat is
	// sold once.
	ticketSeatConstraint = "tickets_event_seat_id_key"
)

// Durable statements. Each one belongs to exactly one phase, and the phase 1 read
// takes a row lock on the reservation so the status and the window a checkout
// decides on cannot change between the read and the insert.
const (
	// lockReservationQuery reads the order a checkout pays. `live` is decided by the
	// database clock, the same clock the hold trigger and the availability read use,
	// so a hold is never payable inside PostgreSQL and expired outside it.
	lockReservationQuery = `
SELECT r.id::text, r.event_id::text, r.status, r.expires_at, r.expires_at > now() AS live, e.price_cents
FROM cinema.reservations r
JOIN cinema.events e ON e.id = r.event_id
WHERE r.id = $1
FOR UPDATE OF r`

	// heldSeatsQuery reads the seats the reservation still holds, which is the seat
	// set both the amount and the tickets are derived from.
	heldSeatsQuery = `
SELECT event_seat_id
FROM cinema.reservation_seats
WHERE reservation_id = $1 AND released_at IS NULL
ORDER BY event_seat_id`

	// lookupAttemptQuery reads the attempt a client key already names inside its
	// reservation, which is why the key is looked up with the reservation and never
	// on its own.
	lookupAttemptQuery = `
SELECT id::text, reservation_id::text, provider, COALESCE(provider_reference, ''), amount_cents,
       COALESCE(outcome, ''), status, idempotency_key, request_fingerprint
FROM cinema.payment_attempts
WHERE reservation_id = $1 AND idempotency_key = $2`

	// lockAttemptQuery reads one attempt and locks its row, so a concurrent request
	// that reached the provider with the same attempt waits here and then answers
	// with the stored verdict.
	lockAttemptQuery = `
SELECT id::text, reservation_id::text, provider, COALESCE(provider_reference, ''), amount_cents,
       COALESCE(outcome, ''), status, idempotency_key, request_fingerprint
FROM cinema.payment_attempts
WHERE id = $1
FOR UPDATE`

	// insertPendingAttemptQuery stores the attempt before the provider is called.
	// provider_reference and outcome stay NULL: the provider has not answered yet,
	// and the decided-state constraint requires both as soon as it does.
	//
	// This insert is where the double-charge guard lives, and it is the schema that
	// enforces it: a second open attempt of the same reservation violates
	// payment_attempts_open_per_reservation (0006) instead of being stored, so no
	// second request can reach the provider for money the first one may already have
	// taken. There is deliberately no in-flight read next to it: two guards measuring
	// the same thing differently is a hazard, and a clock cannot make this invariant
	// hold. The reservation row lock the phase takes still serializes the read and this
	// insert, so of two concurrent checkouts exactly one commits.
	insertPendingAttemptQuery = `
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
VALUES ($1, $2, $3, $4, 'pending', $5, $6)`

	// decideAttemptQuery stores the verdict on the attempt. The status predicate is
	// the guard: an attempt that already carries a verdict is never overwritten.
	decideAttemptQuery = `
UPDATE cinema.payment_attempts
   SET status = $2, outcome = $3, provider_reference = $4, updated_at = now()
 WHERE id = $1 AND status IN ('pending', 'failed')`

	// markPaidQuery is the sale of the order. The status predicate is what makes a
	// concurrent second capture lose: only a reservation that is still held can be
	// paid.
	markPaidQuery = `
UPDATE cinema.reservations SET status = 'paid' WHERE id = $1 AND status = 'held'`

	reservationStatusQuery = `SELECT status FROM cinema.reservations WHERE id = $1`

	sellSeatsQuery = `
UPDATE cinema.event_seats SET state = 'sold' WHERE event_id = $1 AND id = ANY($2::bigint[])`

	insertTicketQuery = `
INSERT INTO cinema.tickets (id, reservation_id, event_id, event_seat_id)
VALUES ($1, $2, $3, $4)`

	insertOutboxQuery = `
INSERT INTO cinema.outbox (id, topic, payload) VALUES ($1, $2, $3::jsonb)`

	ticketIDsQuery = `
SELECT id::text FROM cinema.tickets WHERE reservation_id = $1 ORDER BY event_seat_id`

	// listStaleAttemptsQuery selects the attempts a dead process left pending, oldest
	// first, each with the reservation it is paying for.
	//
	// The age bound is applied by the **database** clock, not by the caller: `now()` here
	// is the same clock the hold trigger and the expiry sweep use, so a reconciler in Go
	// and a sweep in PostgreSQL can never disagree about what "older than the hold
	// window" means. Passing a deadline computed in the process would make the two
	// disagree during clock skew, which is how a hold gets released before the payment
	// question is answered.
	//
	// The seats are the reservation's still-active rows, ascending, which is the seat set
	// the tickets are issued from: a reconciliation may release nothing, but it may issue.
	listStaleAttemptsQuery = `
SELECT a.id::text, a.reservation_id::text, a.provider, COALESCE(a.provider_reference, ''),
       a.amount_cents, COALESCE(a.outcome, ''), a.status, a.idempotency_key, a.request_fingerprint,
       r.event_id::text, r.status, r.expires_at, e.price_cents,
       ARRAY(SELECT rs.event_seat_id
             FROM cinema.reservation_seats rs
             WHERE rs.reservation_id = a.reservation_id AND rs.released_at IS NULL
             ORDER BY rs.event_seat_id) AS seats
FROM cinema.payment_attempts a
JOIN cinema.reservations r ON r.id = a.reservation_id
JOIN cinema.events e ON e.id = r.event_id
WHERE a.status = 'pending'
  AND a.created_at <= now() - ($1::bigint * interval '1 microsecond')
ORDER BY a.created_at, a.id
LIMIT $2`

	// checkoutSchemaQuery is the closed-surface check: the payment route is only
	// mounted against a database that actually carries the checkout objects, and the
	// guard that makes a second charge impossible. The 0006 open-attempt index is part
	// of the check on purpose: the checkout no longer carries a clock of its own, so a
	// database with the tables but without the index would advertise a route that can
	// still charge a buyer twice after a crash.
	checkoutSchemaQuery = `
SELECT to_regclass('cinema.payment_attempts') IS NOT NULL
   AND to_regclass('cinema.tickets') IS NOT NULL
   AND to_regclass('cinema.outbox') IS NOT NULL
   AND to_regclass('cinema.payment_attempts_open_per_reservation') IS NOT NULL
   AND EXISTS (
     SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'events' AND column_name = 'price_cents')`
)

// PostgresStore persists payment attempts, tickets and outbox rows in PostgreSQL,
// the authoritative source of truth for the sale.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore opens a pooled connection to the given database URL.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open checkout database: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases pooled connections.
func (s *PostgresStore) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

// CheckoutSchemaPresent reports whether this database carries the checkout
// objects. The process uses it to decide whether the payment route belongs in this
// deployment: mounting it against an older schema would advertise a route that
// cannot serve a single request.
func (s *PostgresStore) CheckoutSchemaPresent(ctx context.Context) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("checkout database is not configured")
	}
	var present bool
	if err := s.pool.QueryRow(ctx, checkoutSchemaQuery).Scan(&present); err != nil {
		return false, fmt.Errorf("read the checkout schema state: %w", err)
	}
	return present, nil
}

// PrepareAttempt runs phase 1 in one transaction and commits before returning.
func (s *PostgresStore) PrepareAttempt(ctx context.Context, request AttemptRequest) (PreparedAttempt, error) {
	var err error
	for attempt := 0; attempt < prepareAttemptAttempts; attempt++ {
		var prepared PreparedAttempt
		prepared, err = s.prepareAttempt(ctx, request)
		if err == nil {
			return prepared, nil
		}
		// Only the race with a concurrent request for the same key is retried: the
		// second run finds the committed attempt and answers as a replay.
		if !errors.Is(err, errAttemptRaced) {
			return PreparedAttempt{}, err
		}
	}
	return PreparedAttempt{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func (s *PostgresStore) prepareAttempt(ctx context.Context, request AttemptRequest) (PreparedAttempt, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PreparedAttempt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	reservation, live, err := lockReservation(ctx, tx, request.ReservationID)
	if err != nil {
		return PreparedAttempt{}, err
	}

	// The replay lookup comes before every status check: a replayed key answers from
	// its own row even though the reservation is already paid by now.
	stored, found, err := lookupAttempt(ctx, tx, request.ReservationID, request.IdempotencyKey)
	if err != nil {
		return PreparedAttempt{}, err
	}
	if found {
		if stored.Status == StatusCaptured {
			// The tickets of a captured attempt are what the confirmation surface
			// links to, so a replay carries them.
			tickets, err := lookupTicketIDs(ctx, tx, request.ReservationID)
			if err != nil {
				return PreparedAttempt{}, err
			}
			stored.TicketIDs = tickets
		}
		return PreparedAttempt{Reservation: reservation, Attempt: stored}, nil
	}

	if err := requirePayable(reservation, live); err != nil {
		return PreparedAttempt{}, err
	}

	// Nothing checks whether another attempt of this reservation is open: the schema
	// does, and it does it without a clock. Two client keys of one reservation cannot
	// both be open, because payment_attempts_open_per_reservation is a partial unique
	// index over the pending rows, so the second insert below is refused (and classified
	// as ErrPaymentInFlight) instead of opening a second charge. Only the attempt that
	// holds the client key can settle it, which is why the buyer resumes with the same
	// key rather than a fresh one.
	amount, err := reservation.AmountCents()
	if err != nil {
		return PreparedAttempt{}, err
	}

	attempt := Attempt{
		ID:             venue.NewID(),
		ReservationID:  reservation.ID,
		Provider:       request.Provider,
		AmountCents:    amount,
		Status:         StatusPending,
		IdempotencyKey: request.IdempotencyKey,
		Fingerprint:    request.Fingerprint,
	}
	if _, err := tx.Exec(ctx, insertPendingAttemptQuery, attempt.ID, attempt.ReservationID,
		attempt.Provider, attempt.AmountCents, attempt.IdempotencyKey, attempt.Fingerprint); err != nil {
		// The violation is classified by constraint, never retried blindly: a second
		// open attempt of the reservation is a refusal (ErrPaymentInFlight) while a
		// duplicate of this very key is the concurrent race the retry turns into a
		// replay.
		return PreparedAttempt{}, classifyCheckoutError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PreparedAttempt{}, err
	}
	return PreparedAttempt{Reservation: reservation, Attempt: attempt, Created: true}, nil
}

// RecordOutcome runs phase 3 in one transaction and returns the attempt as stored.
func (s *PostgresStore) RecordOutcome(ctx context.Context, outcome AttemptOutcome) (Attempt, error) {
	switch outcome.Status {
	case StatusCaptured, StatusDeclined:
	default:
		return Attempt{}, fmt.Errorf("%w: %q is not a stored payment verdict", ErrInvalidInput, outcome.Status)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Attempt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The attempt row is locked first: a concurrent request that reached the provider
	// with the same attempt waits here and then answers with the stored verdict
	// instead of writing a second sale.
	stored, found, err := lockAttempt(ctx, tx, outcome.AttemptID)
	if err != nil {
		return Attempt{}, err
	}
	if !found {
		return Attempt{}, fmt.Errorf("%w: payment attempt %s is not stored", ErrUnavailable, outcome.AttemptID)
	}
	if stored.AmountCents != outcome.AmountCents {
		return Attempt{}, fmt.Errorf("%w: attempt %s holds %d minor units, this answer charges %d",
			ErrAmountMismatch, stored.ID, stored.AmountCents, outcome.AmountCents)
	}
	if stored.Decided() {
		if stored.Status == StatusCaptured {
			tickets, err := lookupTicketIDs(ctx, tx, stored.ReservationID)
			if err != nil {
				return Attempt{}, err
			}
			stored.TicketIDs = tickets
		}
		return stored, nil
	}

	if _, err := tx.Exec(ctx, decideAttemptQuery, stored.ID, outcome.Status, string(outcome.Outcome),
		outcome.ProviderReference); err != nil {
		return Attempt{}, classifyCheckoutError(err)
	}
	stored.Status = outcome.Status
	stored.Outcome = string(outcome.Outcome)
	stored.ProviderReference = outcome.ProviderReference

	if outcome.Status == StatusCaptured {
		recorded, err := s.capture(ctx, tx, outcome, stored)
		if err != nil {
			return Attempt{}, err
		}
		stored = recorded
	}

	if err := tx.Commit(ctx); err != nil {
		return Attempt{}, err
	}
	return stored, nil
}

// ListStaleAttempts reads the attempts still pending after olderThan, with the
// reservation each one is paying for. It locks nothing: the caller then calls the
// provider, which is I/O, and holding a row lock across that call would serialize the
// reconciliation of unrelated payments. `RecordOutcome` takes the lock that matters.
func (s *PostgresStore) ListStaleAttempts(ctx context.Context, olderThan time.Duration, batch int) ([]PreparedAttempt, error) {
	if olderThan < minReconcileAge {
		return nil, fmt.Errorf("%w: reconciliation age bound must be at least one microsecond (%s), got %s",
			ErrInvalidInput, minReconcileAge, olderThan)
	}
	if batch <= 0 {
		return nil, fmt.Errorf("%w: reconciliation batch must be positive, got %d", ErrInvalidInput, batch)
	}

	rows, err := s.pool.Query(ctx, listStaleAttemptsQuery, olderThan.Microseconds(), batch)
	if err != nil {
		return nil, classifyCheckoutError(err)
	}
	defer rows.Close()

	stale := make([]PreparedAttempt, 0, batch)
	for rows.Next() {
		var prepared PreparedAttempt
		if err := rows.Scan(
			&prepared.Attempt.ID, &prepared.Attempt.ReservationID, &prepared.Attempt.Provider,
			&prepared.Attempt.ProviderReference, &prepared.Attempt.AmountCents, &prepared.Attempt.Outcome,
			&prepared.Attempt.Status, &prepared.Attempt.IdempotencyKey, &prepared.Attempt.Fingerprint,
			&prepared.Reservation.EventID, &prepared.Reservation.Status, &prepared.Reservation.ExpiresAt,
			&prepared.Reservation.PriceCents, &prepared.Reservation.Seats,
		); err != nil {
			return nil, classifyCheckoutError(err)
		}
		prepared.Reservation.ID = prepared.Attempt.ReservationID
		stale = append(stale, prepared)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyCheckoutError(err)
	}
	return stale, nil
}

// capture writes the sale: the paid reservation, the sold seats, one ticket per
// seat and the outbox row. Every write is in the caller's transaction, so a sale
// is either entirely recorded or not recorded at all.
func (s *PostgresStore) capture(ctx context.Context, tx pgx.Tx, outcome AttemptOutcome, stored Attempt) (Attempt, error) {
	// Defence in depth: the verdict must belong to the reservation being paid. The
	// service derives both from the same prepared attempt, so a disagreement here means
	// the caller assembled a capture by hand, and paying one reservation's seats with
	// another attempt's verdict must never reach the database.
	if outcome.ReservationID != stored.ReservationID {
		return Attempt{}, fmt.Errorf("%w: attempt %s belongs to reservation %s, but the capture pays %s",
			ErrUnavailable, stored.ID, stored.ReservationID, outcome.ReservationID)
	}

	tag, err := tx.Exec(ctx, markPaidQuery, outcome.ReservationID)
	if err != nil {
		return Attempt{}, classifyCheckoutError(err)
	}
	if tag.RowsAffected() == 0 {
		// Another attempt captured this reservation first, or the hold stopped being
		// payable. Either way the money of this attempt must not be recorded as a
		// sale, so the transaction is rolled back and the buyer is told which case it
		// was.
		var status string
		if err := tx.QueryRow(ctx, reservationStatusQuery, outcome.ReservationID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Attempt{}, fmt.Errorf("%w: %s", ErrReservationNotFound, outcome.ReservationID)
			}
			return Attempt{}, classifyCheckoutError(err)
		}
		if status == paidStatus {
			return Attempt{}, fmt.Errorf("%w: %s", ErrAlreadyPaid, outcome.ReservationID)
		}
		return Attempt{}, fmt.Errorf("%w: %s is %s", ErrReservationNotPayable, outcome.ReservationID, status)
	}

	seats := make([]int64, len(outcome.Seats))
	copy(seats, outcome.Seats)
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })
	soldTag, err := tx.Exec(ctx, sellSeatsQuery, outcome.EventID, seats)
	if err != nil {
		return Attempt{}, classifyCheckoutError(err)
	}
	// Every seat a ticket is about to be issued for must have been marked sold. A seat
	// of another event, or one that is no longer in this event, is silently absent from
	// this UPDATE, and issuing its ticket anyway would sell a seat nobody sold.
	if sold := soldTag.RowsAffected(); sold != int64(len(outcome.Seats)) {
		return Attempt{}, fmt.Errorf(
			"%w: %d of %d seats of %s in event %s were marked sold, so no ticket is issued for the rest",
			ErrUnavailable, sold, len(outcome.Seats), outcome.ReservationID, outcome.EventID)
	}

	ticketIDs := make([]string, 0, len(outcome.Tickets))
	for _, ticket := range outcome.Tickets {
		if _, err := tx.Exec(ctx, insertTicketQuery, ticket.ID, outcome.ReservationID, outcome.EventID, ticket.EventSeatID); err != nil {
			return Attempt{}, classifyCheckoutError(err)
		}
		ticketIDs = append(ticketIDs, ticket.ID)
	}

	if _, err := tx.Exec(ctx, insertOutboxQuery, outcome.Outbox.ID, outcome.Outbox.Topic, string(outcome.Outbox.Payload)); err != nil {
		return Attempt{}, classifyCheckoutError(err)
	}

	stored.TicketIDs = ticketIDs
	return stored, nil
}

// lockReservation reads the order a checkout pays, locking its row so the status
// and the window it decided on cannot change underneath it.
func lockReservation(ctx context.Context, tx pgx.Tx, reservationID string) (Reservation, bool, error) {
	var reservation Reservation
	var live bool
	err := tx.QueryRow(ctx, lockReservationQuery, reservationID).
		Scan(&reservation.ID, &reservation.EventID, &reservation.Status, &reservation.ExpiresAt, &live, &reservation.PriceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, fmt.Errorf("%w: %s", ErrReservationNotFound, reservationID)
	}
	if err != nil {
		return Reservation{}, false, classifyCheckoutError(err)
	}

	rows, err := tx.Query(ctx, heldSeatsQuery, reservation.ID)
	if err != nil {
		return Reservation{}, false, classifyCheckoutError(err)
	}
	defer rows.Close()
	seats := []int64{}
	for rows.Next() {
		var eventSeatID int64
		if err := rows.Scan(&eventSeatID); err != nil {
			return Reservation{}, false, classifyCheckoutError(err)
		}
		seats = append(seats, eventSeatID)
	}
	if err := rows.Err(); err != nil {
		return Reservation{}, false, classifyCheckoutError(err)
	}
	reservation.Seats = seats
	return reservation, live, nil
}

// requirePayable refuses a reservation that cannot be paid right now. A paid
// reservation is reported apart from any other non-payable state, because it means
// the money was already taken; a hold whose window closed is reported as expired
// whether or not the sweep has already stored that status, because for the buyer
// the two are the same situation.
func requirePayable(reservation Reservation, live bool) error {
	switch reservation.Status {
	case paidStatus:
		return fmt.Errorf("%w: %s", ErrAlreadyPaid, reservation.ID)
	case heldStatus:
		if !live {
			return fmt.Errorf("%w: %s", ErrHoldExpired, reservation.ID)
		}
		return nil
	case "expired":
		return fmt.Errorf("%w: %s is expired", ErrHoldExpired, reservation.ID)
	default:
		return fmt.Errorf("%w: %s is %s", ErrReservationNotPayable, reservation.ID, reservation.Status)
	}
}

// lookupAttempt reads the attempt a client key already names inside its
// reservation.
func lookupAttempt(ctx context.Context, tx pgx.Tx, reservationID, idempotencyKey string) (Attempt, bool, error) {
	return scanAttempt(tx.QueryRow(ctx, lookupAttemptQuery, reservationID, idempotencyKey))
}

// lockAttempt reads and locks one attempt row.
func lockAttempt(ctx context.Context, tx pgx.Tx, attemptID string) (Attempt, bool, error) {
	return scanAttempt(tx.QueryRow(ctx, lockAttemptQuery, attemptID))
}

// scanAttempt reads one attempt row. The nullable provider reference and verdict
// are coalesced to an empty string in SQL, so a pending attempt is representable
// without a pointer dance.
func scanAttempt(row pgx.Row) (Attempt, bool, error) {
	var attempt Attempt
	err := row.Scan(&attempt.ID, &attempt.ReservationID, &attempt.Provider, &attempt.ProviderReference,
		&attempt.AmountCents, &attempt.Outcome, &attempt.Status, &attempt.IdempotencyKey, &attempt.Fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, false, nil
	}
	if err != nil {
		return Attempt{}, false, classifyCheckoutError(err)
	}
	return attempt, true, nil
}

// lookupTicketIDs reads the tickets of one reservation in seat order, which is what
// a replayed captured attempt answers with.
func lookupTicketIDs(ctx context.Context, tx pgx.Tx, reservationID string) ([]string, error) {
	rows, err := tx.Query(ctx, ticketIDsQuery, reservationID)
	if err != nil {
		return nil, classifyCheckoutError(err)
	}
	defer rows.Close()
	tickets := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, classifyCheckoutError(err)
		}
		tickets = append(tickets, id)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyCheckoutError(err)
	}
	return tickets, nil
}

// classifyCheckoutError maps SQLSTATE codes to the documented outcomes. The 23505
// cases are named by constraint, because the unique guards of this flow mean
// different things to the buyer: this reservation is already open (a second key), a
// concurrent request stored this very attempt (a replay), this reservation is already
// paid, this provider object was already recorded, this seat already carries a
// ticket. Conflating them would report the wrong conflict, or retry a request that
// must be refused.
func classifyCheckoutError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			switch pgErr.ConstraintName {
			case openAttemptIndex:
				// One open attempt per reservation. Nothing was charged and nothing was
				// stored; a retry would only violate the index again, so this is a straight
				// refusal and never errAttemptRaced.
				return fmt.Errorf("%w: %s", ErrPaymentInFlight, pgErr.Message)
			case attemptKeyConstraint:
				// The same reservation and key already exist, so another request won the
				// race between the lookup and the insert and its row is the replay source.
				return fmt.Errorf("%w: %s", errAttemptRaced, pgErr.Message)
			case capturedAttemptIndex:
				return fmt.Errorf("%w: %s", ErrAlreadyPaid, pgErr.Message)
			case providerReferenceIndex:
				return fmt.Errorf("%w: %s", ErrProviderReferenceTaken, pgErr.Message)
			case ticketSeatConstraint:
				return fmt.Errorf("%w: %s", ErrSeatAlreadySold, pgErr.Message)
			default:
				return fmt.Errorf("%w: %s", ErrIdempotencyKeyReused, pgErr.Message)
			}
		case "23514", "23502":
			// A check violation means the write would have stored an incoherent state:
			// a non-positive amount, a status/outcome pair that cannot agree, or a
			// decided attempt without its reference. It is caller error, never a sale.
			return fmt.Errorf("%w: %s", ErrInvalidInput, pgErr.Message)
		case "23503", "22P02", "22003":
			// A foreign key or cast failure means the request named a reference the
			// store does not know. The driver message names tables, constraints and
			// values, so it is kept for the operator's log and never used as a client's
			// response detail.
			return fmt.Errorf("%w: %s", ErrInvalidInput, pgErr.Message)
		}
	}
	return err
}

// compile-time proof that the store satisfies the contract the service runs on.
var _ Store = (*PostgresStore)(nil)
