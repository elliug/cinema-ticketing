package reservation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

// sweepNow is the deadline the expiry sweep is measured against. Injection is
// what makes the sweep reproducible: the durable statements compare expires_at
// against this value, never against the database clock.
var sweepNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// Behaviour 1: one sweep reports what it actually changed in the durable store —
// expired reservations, released seat rows, pruned idempotency records — and the
// caller receives the durable result unchanged instead of a re-derived one.
func TestExpireHoldsReportsTheDurableSweepResult(t *testing.T) {
	store := newFakeStore()
	store.expireResult = reservation.ExpiryResult{
		ReservationsExpired: 3,
		SeatsReleased:       5,
		IdempotencyPruned:   2,
	}
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)

	result, err := service.ExpireHolds(context.Background(), sweepNow, 10)
	if err != nil {
		t.Fatalf("ExpireHolds returned error: %v", err)
	}
	if store.expireCalls != 1 {
		t.Fatalf("durable sweep ran %d times, want exactly 1", store.expireCalls)
	}
	if result.ReservationsExpired != 3 {
		t.Fatalf("reservations expired = %d, want 3", result.ReservationsExpired)
	}
	if result.SeatsReleased != 5 {
		t.Fatalf("seat rows released = %d, want 5", result.SeatsReleased)
	}
	if result.IdempotencyPruned != 2 {
		t.Fatalf("idempotency records pruned = %d, want 2", result.IdempotencyPruned)
	}
	if result.More {
		t.Fatalf("More = true, want false when the store reported no remaining work")
	}
}

// Behaviour 2: each call sweeps at most the requested batch and tells the caller
// whether more work likely remains, so a caller loop can drive the sweep without
// the sweep keeping any in-memory state.
func TestExpireHoldsPassesTheBatchAndReportsWhetherWorkRemains(t *testing.T) {
	for _, more := range []bool{false, true} {
		name := "no further work"
		if more {
			name = "work remains"
		}
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			store.expireResult = reservation.ExpiryResult{ReservationsExpired: 25, SeatsReleased: 25, More: more}
			service := reservation.NewService(failingLocker{}, store, testHoldTTL)

			result, err := service.ExpireHolds(context.Background(), sweepNow, 25)
			if err != nil {
				t.Fatalf("ExpireHolds returned error: %v", err)
			}
			if store.expireBatch != 25 {
				t.Fatalf("durable sweep batch = %d, want the requested 25", store.expireBatch)
			}
			if !store.expireNow.Equal(sweepNow) {
				t.Fatalf("durable sweep deadline = %s, want the requested %s", store.expireNow, sweepNow)
			}
			if result.More != more {
				t.Fatalf("More = %v, want %v", result.More, more)
			}
			if result.ReservationsExpired != 25 {
				t.Fatalf("reservations expired = %d, want the bounded 25", result.ReservationsExpired)
			}
		})
	}
}

// Behaviour 2, guarded input: a sweep that could never make progress is caller
// error and is rejected before the durable store is touched. A non-positive
// batch would spin a caller loop forever and a zero deadline compares against
// year one, so neither may reach the database.
func TestExpireHoldsRejectsInvalidSweepInput(t *testing.T) {
	cases := []struct {
		name  string
		now   time.Time
		batch int
	}{
		{"zero batch", sweepNow, 0},
		{"negative batch", sweepNow, -5},
		{"zero deadline", time.Time{}, 10},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newFakeStore()
			service := reservation.NewService(failingLocker{}, store, testHoldTTL)

			if _, err := service.ExpireHolds(context.Background(), testCase.now, testCase.batch); !errors.Is(err, reservation.ErrInvalidInput) {
				t.Fatalf("ExpireHolds error = %v, want ErrInvalidInput", err)
			}
			if store.expireCalls != 0 {
				t.Fatalf("rejected sweep reached the durable store %d times, want 0", store.expireCalls)
			}
		})
	}
}

// Behaviour 2, durable half: the PostgreSQL store validates on its own, before
// it opens a transaction. The zero value has no pool, so a sweep that touched
// the database would panic instead of returning the documented error.
func TestPostgresStoreExpireHoldsValidatesBeforeAnyQuery(t *testing.T) {
	var store reservation.PostgresStore

	if _, err := store.ExpireHolds(context.Background(), sweepNow, 0); !errors.Is(err, reservation.ErrInvalidInput) {
		t.Fatalf("ExpireHolds(batch 0) error = %v, want ErrInvalidInput", err)
	}
	if _, err := store.ExpireHolds(context.Background(), time.Time{}, 10); !errors.Is(err, reservation.ErrInvalidInput) {
		t.Fatalf("ExpireHolds(zero deadline) error = %v, want ErrInvalidInput", err)
	}
}

// Behaviour 6, fail-closed half: a sweep that cannot reach PostgreSQL is a
// retryable failure and reports no counts, so a caller can never mistake a
// failed sweep for a completed one and stop looping.
func TestExpireHoldsFailsClosedWhenTheDurableSweepFails(t *testing.T) {
	store := newFakeStore()
	store.expireErr = errors.New("write tcp 127.0.0.1:5432: connection reset by peer")
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)

	result, err := service.ExpireHolds(context.Background(), sweepNow, 10)
	if !errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("ExpireHolds error = %v, want ErrUnavailable", err)
	}
	if result != (reservation.ExpiryResult{}) {
		t.Fatalf("failed sweep reported %+v, want the zero result", result)
	}
}
