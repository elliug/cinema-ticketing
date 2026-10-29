package reservation_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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

// fakeClock is the authoritative deadline source the sweeper is given. It proves
// the sweep deadline comes from PostgreSQL (the only production implementation)
// and not from the process clock, without needing a live database.
type fakeClock struct {
	now   time.Time
	err   error
	calls int
}

func (c *fakeClock) DatabaseNow(context.Context) (time.Time, error) {
	c.calls++
	return c.now, c.err
}

// sweepLogger discards structured log output; the recovery test captures it.
func sweepLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testSweeperConfig is a fast tick: a short interval, a small batch, a generous
// per-sweep deadline and a bounded batch count.
func testSweeperConfig() reservation.SweeperConfig {
	return reservation.SweeperConfig{
		Interval:   5 * time.Millisecond,
		Batch:      10,
		Timeout:    time.Second,
		MaxBatches: 3,
		Logger:     sweepLogger(),
	}
}

// Behaviour 4: every sweep compares against the database clock. The process
// clock is never used, so a skewed API host cannot expire live holds early.
func TestSweeperDerivesTheDeadlineFromTheDatabaseClock(t *testing.T) {
	clockTime := time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := &fakeClock{now: clockTime}
	store := newFakeStore()
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)
	sweeper, err := reservation.NewSweeper(service, clock, testSweeperConfig())
	if err != nil {
		t.Fatalf("NewSweeper returned error: %v", err)
	}

	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if clock.calls != 1 {
		t.Fatalf("database clock read %d times, want exactly 1 per sweep", clock.calls)
	}
	if !store.expireNow.Equal(clockTime) {
		t.Fatalf("sweep deadline = %s, want the database clock %s (never the process clock)", store.expireNow, clockTime)
	}
	if store.expireNow.Equal(time.Now()) {
		t.Fatalf("sweep deadline = %s, want a value the process clock did not produce", store.expireNow)
	}
}

// Behaviour 4, fail-closed half: a deadline that cannot be read is a failed
// sweep, so the durable store is never asked to sweep with a guessed clock.
func TestSweeperFailsWhenTheDatabaseClockCannotBeRead(t *testing.T) {
	clock := &fakeClock{err: errors.New("read tcp 127.0.0.1:5432: connection reset by peer")}
	store := newFakeStore()
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)
	sweeper, err := reservation.NewSweeper(service, clock, testSweeperConfig())
	if err != nil {
		t.Fatalf("NewSweeper returned error: %v", err)
	}

	if err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep succeeded without a database clock, want a failure")
	}
	if store.expireCalls != 0 {
		t.Fatalf("sweep without a deadline reached the durable store %d times, want 0", store.expireCalls)
	}
}

// Behaviour 4: each sweep is bounded by its own deadline, so a stalled database
// cannot wedge the worker forever.
func TestSweeperBoundsEachSweepWithATimeout(t *testing.T) {
	store := newFakeStore()
	store.expireFunc = func(ctx context.Context) (reservation.ExpiryResult, error) {
		<-ctx.Done()
		return reservation.ExpiryResult{}, ctx.Err()
	}
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)
	cfg := testSweeperConfig()
	cfg.Timeout = 50 * time.Millisecond
	sweeper, err := reservation.NewSweeper(service, &fakeClock{now: sweepNow}, cfg)
	if err != nil {
		t.Fatalf("NewSweeper returned error: %v", err)
	}

	started := time.Now()
	err = sweeper.Sweep(context.Background())
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Sweep succeeded over a stalled store, want the deadline to abort it")
	}
	if elapsed < cfg.Timeout {
		t.Fatalf("stalled sweep returned after %s, want it to wait at least the %s deadline", elapsed, cfg.Timeout)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("stalled sweep returned after %s, want the deadline to bound it", elapsed)
	}
}

// Behaviour 4: one tick sweeps at most MaxBatches batches, so a backlog cannot
// monopolise the worker, and it stops as soon as the store reports no more work.
func TestSweeperBoundsTheBatchesPerTick(t *testing.T) {
	t.Run("backlog is bounded per tick", func(t *testing.T) {
		store := newFakeStore()
		store.expireResult = reservation.ExpiryResult{More: true}
		service := reservation.NewService(failingLocker{}, store, testHoldTTL)
		cfg := testSweeperConfig()
		cfg.MaxBatches = 3
		sweeper, err := reservation.NewSweeper(service, &fakeClock{now: sweepNow}, cfg)
		if err != nil {
			t.Fatalf("NewSweeper returned error: %v", err)
		}

		if err := sweeper.Sweep(context.Background()); err != nil {
			t.Fatalf("Sweep returned error: %v", err)
		}
		if store.expireCalls != cfg.MaxBatches {
			t.Fatalf("durable sweep ran %d times in one tick, want the %d-batch bound", store.expireCalls, cfg.MaxBatches)
		}
	})

	t.Run("an exhausted batch ends the tick", func(t *testing.T) {
		store := newFakeStore()
		store.expireResult = reservation.ExpiryResult{ReservationsExpired: 2, SeatsReleased: 2}
		service := reservation.NewService(failingLocker{}, store, testHoldTTL)
		sweeper, err := reservation.NewSweeper(service, &fakeClock{now: sweepNow}, testSweeperConfig())
		if err != nil {
			t.Fatalf("NewSweeper returned error: %v", err)
		}

		if err := sweeper.Sweep(context.Background()); err != nil {
			t.Fatalf("Sweep returned error: %v", err)
		}
		if store.expireCalls != 1 {
			t.Fatalf("durable sweep ran %d times, want exactly 1 when no work remains", store.expireCalls)
		}
	})
}

// Behaviour 4: a failed tick is logged and the worker continues on the next
// tick, so one transient database failure never stops expiry.
func TestSweeperLogsAFailureAndContinuesOnTheNextTick(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	store := newFakeStore()
	succeeded := make(chan struct{}, 1)
	calls := 0
	store.expireFunc = func(context.Context) (reservation.ExpiryResult, error) {
		calls++
		if calls == 1 {
			return reservation.ExpiryResult{}, errors.New("write tcp 127.0.0.1:5432: connection reset by peer")
		}
		select {
		case succeeded <- struct{}{}:
		default:
		}
		return reservation.ExpiryResult{}, nil
	}
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)
	cfg := testSweeperConfig()
	cfg.Logger = logger
	sweeper, err := reservation.NewSweeper(service, &fakeClock{now: sweepNow}, cfg)
	if err != nil {
		t.Fatalf("NewSweeper returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweeper.Run(ctx)
	}()

	select {
	case <-succeeded:
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("the sweep never recovered after a failed tick")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop on shutdown")
	}

	if !strings.Contains(logs.String(), "expiry sweep failed") {
		t.Fatalf("logs = %q, want the failed tick reported", logs.String())
	}
}

// Behaviour 4: shutdown stops the worker before it starts another sweep.
func TestSweeperStopsCleanlyOnShutdown(t *testing.T) {
	store := newFakeStore()
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)
	sweeper, err := reservation.NewSweeper(service, &fakeClock{now: sweepNow}, testSweeperConfig())
	if err != nil {
		t.Fatalf("NewSweeper returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweeper.Run(ctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the process context was cancelled")
	}
	if store.expireCalls != 0 {
		t.Fatalf("shut-down worker swept %d times, want none", store.expireCalls)
	}
}

// Behaviour 4, guarded configuration: a sweeper that cannot make progress is
// refused before it is started, instead of spinning or sweeping with no bound.
func TestNewSweeperRejectsInvalidConfiguration(t *testing.T) {
	service := reservation.NewService(failingLocker{}, newFakeStore(), testHoldTTL)
	cases := []struct {
		name   string
		svc    *reservation.Service
		clock  reservation.DatabaseClock
		mutate func(*reservation.SweeperConfig)
	}{
		{"missing service", nil, &fakeClock{now: sweepNow}, nil},
		{"missing clock", service, nil, nil},
		{"zero interval", service, &fakeClock{now: sweepNow}, func(c *reservation.SweeperConfig) { c.Interval = 0 }},
		{"negative interval", service, &fakeClock{now: sweepNow}, func(c *reservation.SweeperConfig) { c.Interval = -time.Second }},
		{"zero batch", service, &fakeClock{now: sweepNow}, func(c *reservation.SweeperConfig) { c.Batch = 0 }},
		{"zero timeout", service, &fakeClock{now: sweepNow}, func(c *reservation.SweeperConfig) { c.Timeout = 0 }},
		{"zero batch bound", service, &fakeClock{now: sweepNow}, func(c *reservation.SweeperConfig) { c.MaxBatches = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testSweeperConfig()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			if _, err := reservation.NewSweeper(tc.svc, tc.clock, cfg); !errors.Is(err, reservation.ErrInvalidInput) {
				t.Fatalf("NewSweeper error = %v, want ErrInvalidInput", err)
			}
		})
	}
}
