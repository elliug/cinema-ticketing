// Behaviour tests for the cancel service: the application half of
// POST /api/v1/reservations/{reservationId}/cancel (S10).
//
// They need no database: the durable layer is a double and the coordination layer is
// the real Lua scripts against miniredis, so the two facts only a real Redis proves —
// a compare-and-delete that leaves another owner's seat alone, and an absent key
// being a successful no-op — are exercised for real.
package reservation_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

// cancelReservationID is the fixture hold. It is both the reservation id and the
// owner value its seats carry in Redis, which is what makes the release its own.
const cancelReservationID = "00000000-0000-0000-0000-00000000d0f1"

// cancelledHold is the durable outcome the store reports for a request that cancelled
// the hold and released these seats.
func cancelledHold(eventSeatIDs ...int64) reservation.Cancellation {
	return reservation.Cancellation{
		ReservationID: cancelReservationID,
		Status:        "cancelled",
		SeatIDs:       eventSeatIDs,
	}
}

// S10: cancelling a valid, unpaid hold releases its seats — in the durable layer and,
// after it commits, in Redis — and answers the reservation's new state.
func TestCancelReleasesTheHeldSeats(t *testing.T) {
	store := newFakeStore()
	store.cancelResult = cancelledHold(21, 22)
	service, coordinator, server := newTestService(t, store)
	mustAcquire(t, coordinator, cancelReservationID, testHoldTTL, 21, 22)

	cancellation, err := service.Cancel(context.Background(), cancelReservationID)
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if cancellation.Status != "cancelled" {
		t.Fatalf("status = %q, want the stored cancelled state", cancellation.Status)
	}
	if store.cancelCalls != 1 || store.cancelID != cancelReservationID {
		t.Fatalf("durable cancel called %d times for %q, want once for %q",
			store.cancelCalls, store.cancelID, cancelReservationID)
	}

	// The seats stop being held: no key of the hold survives, so the seats are
	// selectable again.
	if held := mustHeld(t, coordinator, 21, 22); held[0] || held[1] {
		t.Fatalf("held after cancel = %v, want both seats free", held)
	}
	if keys := holdKeys(server); len(keys) != 0 {
		t.Fatalf("coordination keys after cancel = %v, want none", keys)
	}
}

// S10: cancelling twice is not an error. The second request answers the same
// cancelled reservation and releases nothing — the stored transition is the truth, and
// the seats of someone else's hold must survive a late release untouched.
func TestCancelAnswersTheSameHoldTwiceWithoutReleasingAgain(t *testing.T) {
	store := newFakeStore()
	store.cancelResult = cancelledHold(21, 22)
	service, coordinator, _ := newTestService(t, store)
	mustAcquire(t, coordinator, cancelReservationID, testHoldTTL, 21, 22)

	first, err := service.Cancel(context.Background(), cancelReservationID)
	if err != nil {
		t.Fatalf("first Cancel returned error: %v", err)
	}

	// The durable layer answers a replay with the same cancelled state and no seat it
	// released this time, which is also what a lost Redis key looks like: the expiry
	// sweep may already have cleared it, and that must not turn a cancel into a failure.
	store.cancelResult = cancelledHold()
	second, err := service.Cancel(context.Background(), cancelReservationID)
	if err != nil {
		t.Fatalf("second Cancel returned error, want the same cancelled state: %v", err)
	}
	if first.Status != "cancelled" || second.Status != first.Status {
		t.Fatalf("statuses = %q then %q, want cancelled both times", first.Status, second.Status)
	}
	if store.cancelCalls != 2 {
		t.Fatalf("durable cancel called %d times, want twice", store.cancelCalls)
	}

	// A seat re-acquired by a different hold — the state a Redis key that outlived its
	// use leaves behind — must survive the second cancel: a request that released
	// nothing must touch nothing.
	mustAcquire(t, coordinator, "00000000-0000-0000-0000-00000000d0f2", testHoldTTL, 21, 22)
	if held := mustHeld(t, coordinator, 21, 22); !held[0] || !held[1] {
		t.Fatalf("held after the second cancel = %v, want both seats still held by the other hold", held)
	}
}

// S10: an already paid reservation is refused explicitly and its seats are not
// released. Paid means the state the checkout writes with the captured attempt and its
// tickets, so undoing the hold here would sell a seat twice.
func TestCancelRefusesAPaidReservationWithoutReleasingItsSeats(t *testing.T) {
	store := newFakeStore()
	store.cancelErr = fmt.Errorf("%w: %s", reservation.ErrAlreadyPaid, cancelReservationID)
	service, coordinator, _ := newTestService(t, store)
	mustAcquire(t, coordinator, cancelReservationID, testHoldTTL, 31)

	_, err := service.Cancel(context.Background(), cancelReservationID)
	if !errors.Is(err, reservation.ErrAlreadyPaid) {
		t.Fatalf("Cancel error = %v, want ErrAlreadyPaid", err)
	}
	// A refusal is a conflict and never a retryable failure, so a client is not invited
	// to send the same request again.
	if errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("Cancel error = %v, want the paid refusal kept apart from the retryable failure", err)
	}
	if held := mustHeld(t, coordinator, 31); !held[0] {
		t.Fatal("the paid refusal released a seat that belongs to a sold order")
	}
}

// S10: an expired hold answers honestly with what it is and releases nothing. It is not
// a failure: the hold holds no seat any more, which is exactly what the caller asked
// for.
func TestCancelReportsAnExpiredHoldAndReleasesNothing(t *testing.T) {
	store := newFakeStore()
	store.cancelResult = reservation.Cancellation{ReservationID: cancelReservationID, Status: "expired"}
	service, coordinator, _ := newTestService(t, store)
	// A key that still exists is the hardest case: an expired hold must not delete
	// anybody's seat.
	mustAcquire(t, coordinator, "00000000-0000-0000-0000-00000000d0f3", testHoldTTL, 41)

	cancellation, err := service.Cancel(context.Background(), cancelReservationID)
	if err != nil {
		t.Fatalf("Cancel of an expired hold returned error: %v", err)
	}
	if cancellation.Status != "expired" {
		t.Fatalf("status = %q, want expired", cancellation.Status)
	}
	if held := mustHeld(t, coordinator, 41); !held[0] {
		t.Fatal("cancelling an expired hold released a seat")
	}
}

// S10: an identifier that cannot name a reservation is refused before the durable layer
// is asked anything, the way the confirmation read refuses one — and the same parsed
// identifier does reach it, so the guard is the parse and not a blanket refusal.
func TestCancelRefusesAnImpossibleIdentifierBeforeAnyQuery(t *testing.T) {
	for _, reservationID := range []string{"", "   ", "not-a-reservation-id", "1234", "00000000-0000-0000-0000-00000000d0f"} {
		store := newFakeStore()
		service, _, _ := newTestService(t, store)

		_, err := service.Cancel(context.Background(), reservationID)
		if !errors.Is(err, reservation.ErrReservationNotFound) {
			t.Fatalf("Cancel(%q) error = %v, want ErrReservationNotFound", reservationID, err)
		}
		if store.cancelCalls != 0 {
			t.Fatalf("Cancel(%q) asked the durable layer %d times, want none", reservationID, store.cancelCalls)
		}
	}

	store := newFakeStore()
	store.cancelResult = cancelledHold(21)
	service, _, _ := newTestService(t, store)
	if _, err := service.Cancel(context.Background(), cancelReservationID); err != nil {
		t.Fatalf("Cancel of a well-formed identifier returned error: %v", err)
	}
	if store.cancelCalls != 1 || store.cancelID != cancelReservationID {
		t.Fatalf("durable cancel = %d calls for %q, want one for %q", store.cancelCalls, store.cancelID, cancelReservationID)
	}
}

// S10: a durable layer that cannot be reached is a retryable failure, never a success
// the buyer would read as "the seats are free".
func TestCancelReportsAnUnreachableStore(t *testing.T) {
	store := newFakeStore()
	store.cancelErr = errors.New("connection refused")
	service, coordinator, _ := newTestService(t, store)
	mustAcquire(t, coordinator, cancelReservationID, testHoldTTL, 21)

	_, err := service.Cancel(context.Background(), cancelReservationID)
	if !errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("Cancel error = %v, want ErrUnavailable", err)
	}
	if held := mustHeld(t, coordinator, 21); !held[0] {
		t.Fatal("a failed cancel released a seat although nothing was stored")
	}
}
