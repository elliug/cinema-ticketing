// The cancel transition, POST /api/v1/reservations/{reservationId}/cancel (S10).
//
// A hold that nobody is going to pay for used to wait out its window before its seats
// came back. Cancelling ends it now: one guarded write decides who takes the hold out of
// the live set, the seat rows are released in the same transaction, and the coordination
// key is cleared afterwards, best effort.
//
// The stored transition is the truth and Redis is only the fast admission layer, which is
// what fixes the order of the steps and their failure behaviour: the durable write
// commits first, and nothing about a Redis answer — including an absent key — can turn a
// stored cancel into a failure.

package reservation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Errors reported by the cancel path. They are distinct sentinels because each one is a
// different thing a client can do: select seats again, stop trying, or retry.
var (
	// ErrReservationNotFound reports an identifier that names no reservation — and one
	// that could not name a reservation, which gets the same answer on purpose. The
	// identifier is the buyer's capability, so a wrong guess and an order that never
	// existed must be indistinguishable.
	ErrReservationNotFound = errors.New("reservation: reservation not found")

	// ErrAlreadyPaid reports a reservation whose money was already taken. It is a
	// conflict, never a retryable failure: waiting does not change it, and releasing its
	// seats would sell them twice.
	ErrAlreadyPaid = errors.New("reservation: the reservation is already paid")
)

// statuses of cinema.reservations.status, as 0003_reservation and 0005_checkout define
// them. Only the values the cancel path names are listed here.
const (
	statusCancelled = "cancelled"
	statusExpired   = "expired"
	statusPaid      = "paid"
)

// cancelReleaseTimeout bounds the post-commit coordination release, which runs on a
// context detached from the request so a client hanging up cannot leave the Redis hold
// behind. It matches the checkout's own bound for the same release.
const cancelReleaseTimeout = 5 * time.Second

// Cancellation is what one cancel request established, read from the durable rows.
type Cancellation struct {
	// ReservationID is the order the request named, and the owner value of its Redis
	// seats.
	ReservationID string
	// Status is the reservation's state after the request: "cancelled" when this request
	// cancelled the hold or found it already cancelled, and "expired" when the hold
	// window had already closed — stored by the expiry sweep, or not yet, because the
	// window decides it and a buyer has no use for the difference.
	Status string
	// SeatIDs is the seat set this request released, ascending. It is empty whenever the
	// request released nothing: an already cancelled hold, an expired one, a paid one,
	// or a lost race. That emptiness is what keeps a second cancel from releasing a seat
	// someone else holds by then.
	SeatIDs []int64
}

// Cancel ends one live hold and answers its new state.
//
// The identifier is parsed before the durable layer is asked anything, so a value that
// cannot name a reservation never becomes a query — the same rule the confirmation read
// follows, and the same answer an identifier that never existed gets.
func (s *Service) Cancel(ctx context.Context, reservationID string) (Cancellation, error) {
	id, err := validateReservationID(reservationID)
	if err != nil {
		return Cancellation{}, err
	}
	cancellation, err := s.store.Cancel(ctx, id)
	if err != nil {
		return Cancellation{}, mapDurableError(err)
	}
	if len(cancellation.SeatIDs) == 0 {
		// Nothing was released by this request, so there is nothing to release in Redis
		// either: releasing here is exactly the double release a second cancel must not
		// perform.
		return cancellation, nil
	}

	// The durable transition is committed, so the coordination release is best effort. A
	// key that is already gone is a successful no-op in Release (the hold's TTL or the
	// expiry sweep may have cleared it), and a Redis failure must not turn a stored
	// cancel into an error the buyer would retry: the seats are free in the only layer
	// that decides that.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelReleaseTimeout)
	defer cancel()
	_ = s.locker.Release(releaseCtx, cancellation.ReservationID, cancellation.SeatIDs)
	return cancellation, nil
}

// validateReservationID normalizes the reservation identifier. The parse is the whole
// validation: a value that is not a UUID cannot name a stored row, so the durable layer is
// never asked about it and the caller gets the not-found answer a wrong guess gets.
func validateReservationID(value string) (string, error) {
	id := value
	var parsed pgtype.UUID
	if err := parsed.Scan(id); err != nil || !parsed.Valid {
		return "", fmt.Errorf("%w: %q is not a reservation identifier", ErrReservationNotFound, id)
	}
	return id, nil
}

// Cancel statements. The transition and its race guard are one statement, and that is the
// whole concurrency design of this route:
//
//   - `status IN ('held','payment_pending')` makes the transition possible exactly once,
//     because a row can only leave that set by leaving it. Of two concurrent cancels, the
//     first holds the row lock and writes; the second re-evaluates the predicate against
//     the committed row and matches nothing. A read-then-write in Go would let both read
//     "held" and both release. The set is the one the expiry sweep treats as a live hold
//     (expiry.go) and the one the confirmation read counts as holding seats, so cancel and
//     sweep cannot disagree about what is still cancellable; no code path writes
//     'payment_pending' yet, and when one does it inherits the same rule instead of a new
//     one decided here.
//   - `expires_at > now()` makes the deadline part of the same decision, in the database's
//     clock, so a hold whose window has closed cannot be cancelled into a state that
//     implies it was still live when the request arrived.
//   - `RETURNING` says which request made the transition: a row means this one won and
//     owes the seat release and the Redis release; no row means someone else already did
//     it, and this request must touch nothing.
//
// 'paid' is deliberately absent from the set. It is the status the checkout writes in the
// same transaction as the captured payment attempt and the tickets (see
// checkout.PostgresStore.capture), so reading the status is this codebase's own test for
// "the money was taken" — and a paid reservation therefore falls through to the read
// below, which refuses it by name.
const (
	cancelHoldQuery = `
UPDATE cinema.reservations
SET status = 'cancelled'
WHERE id = $1
  AND status IN ('held', 'payment_pending')
  AND expires_at > now()
RETURNING status`

	// releaseCancelledSeatsQuery frees the seat rows of the hold the statement above
	// cancelled, in the same transaction, and returns them in seat order. released_at is
	// what the partial unique index `reservation_seats_active_hold` guards, so releasing
	// these rows is what makes the seats selectable again; the availability read's
	// expires_at predicate alone would not.
	releaseCancelledSeatsQuery = `
WITH released AS (
  UPDATE cinema.reservation_seats
  SET released_at = now()
  WHERE reservation_id = $1
    AND released_at IS NULL
  RETURNING event_seat_id
)
SELECT event_seat_id FROM released ORDER BY event_seat_id`

	// reservationCancelStateQuery answers what a reservation is when the transition
	// matched nothing. `live` is the same deadline comparison the transition makes, in the
	// same clock, so the two statements cannot disagree about a window.
	reservationCancelStateQuery = `
SELECT status, expires_at > now()
FROM cinema.reservations
WHERE id = $1`
)

// Cancel performs the durable half of one cancel request: the guarded transition, the
// seat release it won, or the stored state that explains why there was nothing to release.
func (s *PostgresStore) Cancel(ctx context.Context, reservationID string) (Cancellation, error) {
	if s == nil || s.pool == nil {
		return Cancellation{}, fmt.Errorf("%w: the reservation store is not configured", ErrUnavailable)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Cancellation{}, classifyDurableError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cancellation := Cancellation{ReservationID: reservationID, Status: statusCancelled}
	err = tx.QueryRow(ctx, cancelHoldQuery, reservationID).Scan(&cancellation.Status)
	switch {
	case err == nil:
		seatIDs, err := releaseCancelledSeats(ctx, tx, reservationID)
		if err != nil {
			return Cancellation{}, err
		}
		cancellation.SeatIDs = seatIDs
		if err := tx.Commit(ctx); err != nil {
			return Cancellation{}, classifyDurableError(err)
		}
		return cancellation, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Cancellation{}, classifyDurableError(err)
	}

	// The guard matched nothing, so this request changed nothing and must release nothing.
	// What is left is to answer what the reservation is; the read stays in this
	// transaction, and the deferred rollback ends it because there is nothing to commit.
	var (
		storedStatus string
		live         bool
	)
	err = tx.QueryRow(ctx, reservationCancelStateQuery, reservationID).Scan(&storedStatus, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cancellation{}, fmt.Errorf("%w: %s", ErrReservationNotFound, reservationID)
	}
	if err != nil {
		return Cancellation{}, classifyDurableError(err)
	}

	switch storedStatus {
	case statusCancelled:
		// Cancelling twice is not an error: the stored transition is the truth, the first
		// request already released the seats, and this one answers the same reservation.
		cancellation.Status = statusCancelled
	case statusPaid:
		return Cancellation{}, fmt.Errorf("%w: %s", ErrAlreadyPaid, reservationID)
	case statusExpired:
		cancellation.Status = statusExpired
	default:
		if live {
			// Unreachable while the guard, the status CHECK and this read agree, because a
			// live hold in a cancellable status is exactly what the transition matches. A
			// silent success here would claim a cancel that did not happen.
			return Cancellation{}, fmt.Errorf(
				"%w: reservation %s is %q and live, which the cancel transition did not match",
				ErrUnavailable, reservationID, storedStatus)
		}
		// A hold whose window closed but that the expiry sweep has not rewritten yet is
		// reported as what it is. This deliberately does not write the expiry transition:
		// that transition owns the seat-row release and belongs to the sweep (expiry.go),
		// and a second writer of one transition is the hazard this package avoids
		// everywhere else. The seats are already free — the availability read and the
		// claim trigger both compare the deadline — so reporting is all that is owed.
		cancellation.Status = statusExpired
	}
	return cancellation, nil
}

// releaseCancelledSeats frees the seat rows of a cancelled hold and returns the freed seat
// set in ascending order.
func releaseCancelledSeats(ctx context.Context, tx pgx.Tx, reservationID string) ([]int64, error) {
	rows, err := tx.Query(ctx, releaseCancelledSeatsQuery, reservationID)
	if err != nil {
		return nil, classifyDurableError(err)
	}
	defer rows.Close()

	seatIDs := []int64{}
	for rows.Next() {
		var eventSeatID int64
		if err := rows.Scan(&eventSeatID); err != nil {
			return nil, classifyDurableError(err)
		}
		seatIDs = append(seatIDs, eventSeatID)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDurableError(err)
	}
	return seatIDs, nil
}
