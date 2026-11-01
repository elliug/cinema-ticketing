// Behaviour tests for the reconciler that closes issue #87: an attempt left
// `pending` must not strand its hold for ever.
//
// The rule these tests pin is the one the issue decided before any code was
// written: a stale attempt is settled by **asking the provider again** with the
// amount and the reference it already carries — never by assigning a verdict. A
// reconciler that wrote `failed` on its own would let the sweep release the seats
// of a buyer the provider had already charged, which is the defect #82 closed.
//
// They are process-only, like the rest of this package's unit tests: the fakes from
// service_test.go stand in for the store, the provider and the coordination layer.
package checkout_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
)

// testAttemptID is the attempt the fixtures store, so the expected provider
// reference can be written out in full instead of derived from the code.
const testAttemptID = "attempt-1"

// testStaleAge is the bound every test reconciles with unless it says otherwise.
const testStaleAge = 10 * time.Minute

// staleFixture wires a service over a held two-seat reservation whose attempt is
// still pending: exactly what a process that died during the provider call leaves.
func staleFixture(gateway *fakeGateway) *fixture {
	f := newFixture(payableReservation(), gateway)
	f.store.seed(checkout.Attempt{
		ID:          testAttemptID,
		AmountCents: 2426,
		Status:      checkout.StatusPending,
	})
	return f
}

func steps(f *fixture) []string { return []string(f.journal) }

func containsStep(f *fixture, want string) bool {
	for _, step := range steps(f) {
		if step == want {
			return true
		}
	}
	return false
}

// TestReconcileStaleAttemptsAsksTheProviderAgain is the core of #87: the attempt is
// settled by the provider's own answer, asked with the attempt's stored amount and
// its deterministic reference, and the sale is written through the same outcome path
// a normal charge uses.
func TestReconcileStaleAttemptsAsksTheProviderAgain(t *testing.T) {
	ctx := context.Background()
	f := staleFixture(nil)

	result, err := f.service.ReconcileStaleAttempts(ctx, testStaleAge, 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result != (checkout.ReconcileResult{Candidates: 1, Captured: 1}) {
		t.Fatalf("result = %+v, want one candidate captured and nothing unresolved", result)
	}

	if len(f.gateway.requests) != 1 {
		t.Fatalf("the provider was asked %d time(s), want exactly one", len(f.gateway.requests))
	}
	request := f.gateway.requests[0]
	if request.AmountMinorUnits != 2426 {
		t.Fatalf("charged %d minor units, want the 2426 the attempt stored, never today's price",
			request.AmountMinorUnits)
	}
	if want := "payment_attempt:" + testAttemptID; request.Reference != want {
		t.Fatalf("reference = %q, want %q so the provider replays instead of charging twice",
			request.Reference, want)
	}

	if len(f.store.outcomes) != 1 {
		t.Fatalf("recorded %d outcome(s), want exactly one", len(f.store.outcomes))
	}
	outcome := f.store.outcomes[0]
	if outcome.Status != checkout.StatusCaptured || outcome.Outcome != payment.OutcomeApproved {
		t.Fatalf("recorded status %q / outcome %q, want captured/approved from the provider's answer",
			outcome.Status, outcome.Outcome)
	}
	if !reflect.DeepEqual(outcome.Seats, []int64{21, 22}) {
		t.Fatalf("recorded seats %v, want the reservation's held seats [21 22]", outcome.Seats)
	}
	if outcome.AttemptID != testAttemptID || outcome.AmountCents != 2426 {
		t.Fatalf("recorded attempt %q for %d minor units, want %q for 2426",
			outcome.AttemptID, outcome.AmountCents, testAttemptID)
	}

	// A reconciler settles an attempt that is already stored. Preparing a second
	// attempt for the same hold would be a second charge attempt, which is the one
	// thing the open-attempt index exists to prevent.
	if containsStep(f, "prepare") {
		t.Fatalf("the reconciler prepared an attempt (%s): it must settle the stored one", f.journal)
	}
	// Releasing the coordination hold belongs to the expiry sweep, after the verdict
	// is durable. A reconciler that released here would free seats before knowing
	// whether the money moved.
	if containsStep(f, "release") {
		t.Fatalf("the reconciler released a hold (%s): the sweep releases after the verdict", f.journal)
	}
}

// TestReconcilerRecordsADeclineAndReleasesNothing: a declined reconciliation records
// the provider's refusal and stops there. The hold is released by the sweep, once the
// attempt is no longer pending — never by the reconciler itself.
func TestReconcilerRecordsADeclineAndReleasesNothing(t *testing.T) {
	ctx := context.Background()
	gateway := &fakeGateway{outcome: payment.OutcomeDeclined}
	f := staleFixture(gateway)

	result, err := f.service.ReconcileStaleAttempts(ctx, testStaleAge, 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result != (checkout.ReconcileResult{Candidates: 1, Declined: 1}) {
		t.Fatalf("result = %+v, want one candidate declined and nothing captured", result)
	}
	if len(f.store.outcomes) != 1 || f.store.outcomes[0].Status != checkout.StatusDeclined {
		t.Fatalf("recorded %+v, want one declined outcome", f.store.outcomes)
	}
	if len(f.store.outcomes[0].Tickets) != 0 {
		t.Fatalf("a decline issued %d ticket(s), want none", len(f.store.outcomes[0].Tickets))
	}
	if containsStep(f, "release") {
		t.Fatalf("the reconciler released the hold itself (%s): the sweep does that after the verdict", f.journal)
	}
}

// TestReconcilerLeavesAnUnansweredAttemptPending is the promise that makes this
// design safe: when the provider does not answer, nothing is recorded and the
// attempt stays pending, so the seats stay held instead of being released on a
// guess. The tick reports it rather than failing the whole run.
func TestReconcilerLeavesAnUnansweredAttemptPending(t *testing.T) {
	ctx := context.Background()
	gateway := &fakeGateway{err: errors.New("provider unreachable")}
	f := staleFixture(gateway)

	result, err := f.service.ReconcileStaleAttempts(ctx, testStaleAge, 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result != (checkout.ReconcileResult{Candidates: 1, Unresolved: 1}) {
		t.Fatalf("result = %+v, want one candidate left unresolved", result)
	}
	if len(f.store.outcomes) != 0 {
		t.Fatalf("recorded %+v after the provider failed, want nothing recorded", f.store.outcomes)
	}
	if f.store.attempt.Status != checkout.StatusPending {
		t.Fatalf("attempt status = %q, want it still pending: an unanswered call is not a verdict",
			f.store.attempt.Status)
	}
}

// TestReconcilerNeverRechargesADecidedAttempt: the guard for a race between the
// reconciler and the buyer's own slow request. A decided attempt is never sent to
// the provider again.
func TestReconcilerNeverRechargesADecidedAttempt(t *testing.T) {
	ctx := context.Background()
	f := staleFixture(nil)
	// The buyer's request won the race and settled the attempt first.
	f.store.attempt.Status = checkout.StatusCaptured

	result, err := f.service.ReconcileStaleAttempts(ctx, testStaleAge, 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(f.gateway.requests) != 0 {
		t.Fatalf("the provider was asked %d time(s) for an attempt that already carried a verdict, want none",
			len(f.gateway.requests))
	}
	if result.Captured != 0 || result.Unresolved != 0 {
		t.Fatalf("result = %+v, want the decided attempt counted as nothing at all", result)
	}
}

// TestReconcilerRefusesAnImpossibleBatch pins the two caller errors, so a
// misconfigured worker cannot spin or sweep with no bound.
func TestReconcilerRefusesAnImpossibleBatch(t *testing.T) {
	ctx := context.Background()
	f := staleFixture(nil)

	for _, tc := range []struct {
		name      string
		olderThan time.Duration
		batch     int
	}{
		{name: "zero age", olderThan: 0, batch: 10},
		{name: "negative age", olderThan: -time.Minute, batch: 10},
		// Duration.Microseconds() truncates, so this bound would mean "now" and expose a
		// live call as abandoned. It is refused instead of silently rounded away.
		{name: "sub-microsecond age", olderThan: 999 * time.Nanosecond, batch: 10},
		{name: "zero batch", olderThan: testStaleAge, batch: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.service.ReconcileStaleAttempts(ctx, tc.olderThan, tc.batch); err == nil {
				t.Fatalf("reconcile(%v, %d) returned no error, want a refusal", tc.olderThan, tc.batch)
			}
		})
	}
	if len(f.gateway.requests) != 0 {
		t.Fatalf("a refused batch asked the provider %d time(s), want none", len(f.gateway.requests))
	}
}

// TestReconcilerClassifiesAStoreFailure keeps one unresolvable row from stopping the
// tick, while a durable failure still surfaces: a row the database refuses to settle
// is counted and reported, and the run is not silently declared healthy.
func TestReconcilerClassifiesAStoreFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("a row the store refuses is counted, not fatal", func(t *testing.T) {
		f := staleFixture(nil)
		f.store.recordErr = checkout.ErrReservationNotPayable

		result, err := f.service.ReconcileStaleAttempts(ctx, testStaleAge, 10)
		if err != nil {
			t.Fatalf("reconcile: %v, want the row counted instead of failing the tick", err)
		}
		if result.Unresolved != 1 || result.Captured != 0 {
			t.Fatalf("result = %+v, want the refused row counted as unresolved", result)
		}
	})

	t.Run("an unavailable store still surfaces", func(t *testing.T) {
		f := staleFixture(nil)
		f.store.recordErr = checkout.ErrUnavailable

		if _, err := f.service.ReconcileStaleAttempts(ctx, testStaleAge, 10); err == nil {
			t.Fatal("reconcile returned no error for an unavailable store, want the failure surfaced")
		}
	})
}
