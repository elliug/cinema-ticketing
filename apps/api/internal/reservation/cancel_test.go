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
	"os"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

// cancelReservationID is the fixture hold. It is both the reservation id and the
// owner value its seats carry in Redis, which is what makes the release its own.
const cancelReservationID = "00000000-0000-0000-0000-00000000d0f1"

// cancelEventID is the screening the fixture hold's seats belong to. It is the tag of
// the coordination keys the acquire wrote, so a release that addresses any other
// event leaves the keys behind.
const cancelEventID = "00000000-0000-0000-0000-00000000d0e1"

// cancelledHold is the durable outcome the store reports for a request that cancelled
// the hold and released these seats.
func cancelledHold(eventSeatIDs ...int64) reservation.Cancellation {
	return reservation.Cancellation{
		ReservationID: cancelReservationID,
		EventID:       cancelEventID,
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
	mustAcquire(t, coordinator, cancelEventID, cancelReservationID, testHoldTTL, 21, 22)

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
	if held := mustHeld(t, coordinator, cancelEventID, 21, 22); held[0] || held[1] {
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
	mustAcquire(t, coordinator, cancelEventID, cancelReservationID, testHoldTTL, 21, 22)

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
	mustAcquire(t, coordinator, cancelEventID, "00000000-0000-0000-0000-00000000d0f2", testHoldTTL, 21, 22)
	if held := mustHeld(t, coordinator, cancelEventID, 21, 22); !held[0] || !held[1] {
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
	mustAcquire(t, coordinator, cancelEventID, cancelReservationID, testHoldTTL, 31)

	_, err := service.Cancel(context.Background(), cancelReservationID)
	if !errors.Is(err, reservation.ErrAlreadyPaid) {
		t.Fatalf("Cancel error = %v, want ErrAlreadyPaid", err)
	}
	// A refusal is a conflict and never a retryable failure, so a client is not invited
	// to send the same request again.
	if errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("Cancel error = %v, want the paid refusal kept apart from the retryable failure", err)
	}
	if held := mustHeld(t, coordinator, cancelEventID, 31); !held[0] {
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
	mustAcquire(t, coordinator, cancelEventID, "00000000-0000-0000-0000-00000000d0f3", testHoldTTL, 41)

	cancellation, err := service.Cancel(context.Background(), cancelReservationID)
	if err != nil {
		t.Fatalf("Cancel of an expired hold returned error: %v", err)
	}
	if cancellation.Status != "expired" {
		t.Fatalf("status = %q, want expired", cancellation.Status)
	}
	if held := mustHeld(t, coordinator, cancelEventID, 41); !held[0] {
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

// S15, query shape without a database: the durable cancel has to carry the event id,
// because that is the hash tag of the coordination keys the acquire wrote. It is loaded
// by the statement that already decides the transition, not by a second round trip. The
// value is proved against PostgreSQL by TestCancelReleasesTheSeatsSoTheyCanBeHeldAgain.
func TestCancelQueriesLoadTheEventIDWithTheStatementTheyAlreadyRun(t *testing.T) {
	source, err := os.ReadFile("cancel.go")
	if err != nil {
		t.Fatalf("read cancel.go: %v", err)
	}
	for _, name := range []string{"cancelHoldQuery", "reservationCancelStateQuery"} {
		query := queryConstant(string(source), name)
		if query == "" {
			t.Fatalf("%s is not declared in cancel.go", name)
		}
		if !strings.Contains(query, "event_id") {
			t.Errorf("%s does not load event_id, so the release would have no tag:\n%s", name, query)
		}
	}
}

// S16, query shape without a database: a cancel must not release a hold whose payment is
// in flight, so the transition carries that refusal as a predicate of its own and the
// read that decides takes the row lock. Both halves are proved against PostgreSQL, by
// TestCancelRefusesWhileAPaymentAttemptIsPending and
// TestCancelRacingACaptureNeverReleasesTheSeatsTheCaptureIsPayingFor; this test only pins
// that the statements were not written without them, the way the event-id shape test
// above pins the coordination key.
func TestCancelQueriesLockTheRowAndRefuseAnOpenPaymentAttempt(t *testing.T) {
	source, err := os.ReadFile("cancel.go")
	if err != nil {
		t.Fatalf("read cancel.go: %v", err)
	}

	hold := queryConstant(string(source), "cancelHoldQuery")
	if hold == "" {
		t.Fatal("cancelHoldQuery is not declared in cancel.go")
	}
	if !strings.Contains(hold, "payment_attempts") || !strings.Contains(hold, "NOT EXISTS") {
		t.Errorf("cancelHoldQuery does not refuse a reservation with an open payment attempt:\n%s", hold)
	}
	if !strings.Contains(hold, "'pending'") {
		t.Errorf("cancelHoldQuery does not name the pending attempt status:\n%s", hold)
	}

	state := queryConstant(string(source), "reservationCancelStateQuery")
	if state == "" {
		t.Fatal("reservationCancelStateQuery is not declared in cancel.go")
	}
	if !strings.Contains(state, "FOR UPDATE") {
		t.Errorf("reservationCancelStateQuery does not lock the reservation row, so the decision can be overtaken between the read and the write:\n%s", state)
	}
	if !strings.Contains(state, "payment_attempts") {
		t.Errorf("reservationCancelStateQuery does not report an open payment attempt, so a lost cancel could not say what refused it:\n%s", state)
	}
}

// S16 (#79): a reservation whose payment is in flight is refused, and the refusal is not
// flattened into a retryable failure: the durable layer's own answer reaches the caller
// unchanged, and nothing is released in Redis — releasing there is what would free seats
// the capture is paying for. The verdict itself needs a database (the cancel decision is
// SQL), which is where the gated tests prove it.
func TestCancelRefusesAPaymentInFlightWithoutReleasingItsSeats(t *testing.T) {
	store := newFakeStore()
	store.cancelErr = fmt.Errorf("%w: %s", reservation.ErrPaymentInFlight, cancelReservationID)
	service, coordinator, _ := newTestService(t, store)
	mustAcquire(t, coordinator, cancelEventID, cancelReservationID, testHoldTTL, 31)

	cancellation, err := service.Cancel(context.Background(), cancelReservationID)
	if !errors.Is(err, reservation.ErrPaymentInFlight) {
		t.Fatalf("Cancel error = %v, want ErrPaymentInFlight", err)
	}
	// A conflict, never a retryable failure: waiting does not end the capture, and the
	// captured order will not become cancellable afterwards.
	if errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("Cancel error = %v, want the in-flight refusal kept apart from the retryable failure", err)
	}
	// Nor is it the paid answer: the sale has not been decided yet, and a caller told
	// "already paid" would stop asking about a payment that may still decline.
	if errors.Is(err, reservation.ErrAlreadyPaid) {
		t.Fatalf("Cancel error = %v, want the in-flight refusal kept apart from the paid one", err)
	}
	if len(cancellation.SeatIDs) != 0 {
		t.Fatalf("released seats = %v, want none", cancellation.SeatIDs)
	}
	if held := mustHeld(t, coordinator, cancelEventID, 31); !held[0] {
		t.Fatal("the in-flight refusal released a seat the capture is paying for")
	}
}

// S10: a durable layer that cannot be reached is a retryable failure, never a success
// the buyer would read as "the seats are free".
func TestCancelReportsAnUnreachableStore(t *testing.T) {
	store := newFakeStore()
	store.cancelErr = errors.New("connection refused")
	service, coordinator, _ := newTestService(t, store)
	mustAcquire(t, coordinator, cancelEventID, cancelReservationID, testHoldTTL, 21)

	_, err := service.Cancel(context.Background(), cancelReservationID)
	if !errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("Cancel error = %v, want ErrUnavailable", err)
	}
	if held := mustHeld(t, coordinator, cancelEventID, 21); !held[0] {
		t.Fatal("a failed cancel released a seat although nothing was stored")
	}
}
