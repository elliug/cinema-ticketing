package reservation

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ExpiryResult is what one durable expiry sweep changed. Every count is read
// from the rows the transaction actually touched, never derived from the number
// of candidates it selected, so a partially released hold or a seat a re-claim
// takeover already freed is reported as it really is.
type ExpiryResult struct {
	// ReservationsExpired is how many holds were moved to status 'expired'.
	ReservationsExpired int
	// SeatsReleased is how many reservation_seats rows got a released_at.
	SeatsReleased int
	// IdempotencyPruned is how many cinema.idempotency_keys rows were deleted
	// because their retention deadline passed.
	IdempotencyPruned int
	// More reports that the batch was exhausted, so the caller should sweep
	// again. It is a hint, not a promise: a caller loop that stops when it is
	// false still converges, and one spurious extra sweep costs one empty
	// transaction when it is true.
	More bool
}

// ExpiryStore is the durable half of the expiry sweep. PostgreSQL implements it;
// the interface is what keeps the sweep testable without a live database.
type ExpiryStore interface {
	ExpireHolds(ctx context.Context, now time.Time, batch int) (ExpiryResult, error)
}

// expireHoldsAttempts bounds the retry of a sweep that lost a deadlock or a
// serialization conflict to a concurrent claim. Retrying is safe because the
// whole batch is one transaction: a rolled back attempt changed nothing, and a
// committed one is already gone from the next candidate selection.
const expireHoldsAttempts = 3

// expireHoldsPrealloc bounds the slice reservation for the locked hold ids. A
// misconfigured batch must not reserve memory for rows that may never arrive.
const expireHoldsPrealloc = 64

// Sweep statements. The sweep is four statements inside one transaction, and it
// takes the same tables in the order a claim takes them (idempotency record,
// reservation, seat row). The table order matches, but the row order does not:
// a claim locks its seats in ascending event_seat_id order, while the seat update
// below locks the rows matching its reservation ids in plan order. A collision
// with a multi-seat claim can therefore deadlock, and the bounded retry in
// expireHolds is what makes the sweep safe, not a lock-order argument.
const (
	// pruneIdempotencyQuery deletes at most one batch of expired idempotency
	// records. expires_at <= $1 is the retention deadline; hold_expires_at <= $1
	// is the second half of the rule and is checked separately, so a record is
	// never pruned while the hold it replays is still live, whatever the
	// retention deadline says. FOR UPDATE SKIP LOCKED keeps two worker
	// instances from deleting, and waiting on, the same rows.
	pruneIdempotencyQuery = `
WITH prunable AS (
  SELECT idempotency_key
  FROM cinema.idempotency_keys
  WHERE expires_at <= $1
    AND hold_expires_at <= $1
  ORDER BY expires_at, idempotency_key
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
DELETE FROM cinema.idempotency_keys k
USING prunable p
WHERE k.idempotency_key = p.idempotency_key`

	// lockExpiredHoldsQuery selects at most one batch of holds whose window has
	// elapsed, oldest deadline first, and locks exactly those rows.
	//
	//   - status IN ('held','payment_pending') is what keeps a cancelled hold out
	//     of the sweep: only a live hold can become expired.
	//   - expires_at <= $1 is what keeps a hold whose window has not elapsed out
	//     of it, whatever the caller passes as the deadline.
	//   - FOR NO KEY UPDATE is the weakest lock that still excludes a second
	//     sweep, and unlike FOR UPDATE it does not block the KEY SHARE lock a
	//     claim takes on the reservation its idempotency record references.
	//   - SKIP LOCKED makes a second worker instance walk past the rows this one
	//     already owns instead of waiting for them, which is what stops two
	//     processes from double-expiring or deadlocking.
	//   - NOT EXISTS (pending payment attempt) is the payment guard, and it is why
	//     this sweep cannot take the seats a capture is paying for. An attempt lives
	//     in status 'pending' for the whole provider call, which is seconds outside
	//     any transaction, so a hold whose window closes in that gap must not be a
	//     candidate: the capture's markPaidQuery still finds the reservation 'held'
	//     and the money the provider took becomes a sale. The guard only ever
	//     defers work, never cancels it — once the attempt settles (captured or
	//     failed) it stops being pending and the next sweep expires the hold if it
	//     is still live. Expiring it here instead is money taken and nothing sold,
	//     which is the defect issue #82 records. The predicate is served by
	//     payment_attempts_open_per_reservation, the partial unique index over the
	//     pending rows.
	lockExpiredHoldsQuery = `
SELECT r.id::text
FROM cinema.reservations r
WHERE r.status IN ('held', 'payment_pending')
  AND r.expires_at <= $1
  AND NOT EXISTS (
    SELECT 1 FROM cinema.payment_attempts p
    WHERE p.reservation_id = r.id AND p.status = 'pending')
ORDER BY r.expires_at, r.id
LIMIT $2
FOR NO KEY UPDATE SKIP LOCKED`

	// releaseExpiredSeatsQuery frees the seat rows of the holds locked above.
	// released_at IS NULL is what makes a partially released hold safe: a seat a
	// re-claim takeover already freed keeps its original released_at, and the
	// reservation still transitions to expired below.
	//
	// The payment predicate repeats the candidate rule of lockExpiredHoldsQuery, and
	// it is not redundant: FOR NO KEY UPDATE does not conflict with the KEY SHARE an
	// attempt's foreign key takes, so an attempt can be stored after the candidate
	// selection. Re-checking here is what keeps the three statements of one batch
	// agreeing with each other — without it a hold could keep its status while its
	// seat was freed to somebody else, which is the same defect wearing the other
	// half of its clothes.
	releaseExpiredSeatsQuery = `
UPDATE cinema.reservation_seats rs
SET released_at = $1
WHERE rs.released_at IS NULL
  AND rs.reservation_id = ANY($2::uuid[])
  AND NOT EXISTS (
    SELECT 1 FROM cinema.payment_attempts p
    WHERE p.reservation_id = rs.reservation_id AND p.status = 'pending')`

	// expireHoldsQuery closes the holds themselves. The status predicate repeats
	// the candidate rule so only a live hold can become expired, and re-checking
	// it here means the sweep can never rewrite a status it did not select. The
	// payment predicate is repeated for the same reason as in the seat release: a
	// hold a payment is paying for keeps its status, so the sale can still be
	// recorded.
	expireHoldsQuery = `
UPDATE cinema.reservations r
SET status = 'expired'
WHERE r.id = ANY($1::uuid[])
  AND r.status IN ('held', 'payment_pending')
  AND NOT EXISTS (
    SELECT 1 FROM cinema.payment_attempts p
    WHERE p.reservation_id = r.id AND p.status = 'pending')`
)

// validateSweepParams rejects a sweep that could never make progress, before any
// transaction is opened. A non-positive batch would spin a caller loop forever
// and a zero deadline compares against year one, so both are caller errors.
func validateSweepParams(now time.Time, batch int) error {
	if now.IsZero() {
		return fmt.Errorf("%w: expiry sweep needs the current time", ErrInvalidInput)
	}
	if batch <= 0 {
		return fmt.Errorf("%w: expiry sweep batch must be positive, got %d", ErrInvalidInput, batch)
	}
	return nil
}

// ExpireHolds runs one durable expiry sweep: it releases the seat rows of at most
// batch expired holds, marks those holds expired, and prunes the idempotency
// records whose retention deadline passed, all inside one transaction.
//
// Correctness does not depend on any in-memory state or on running exactly once:
//
//   - One transaction per batch. A process killed between batches leaves the
//     previous batches committed and nothing half-done, so a restarted process
//     resumes from the durable rows alone.
//   - Idempotent by predicate, not by memory. A second sweep on the same data
//     selects nothing, because the first one already moved those rows out of the
//     held set; a repeated call on partially swept data releases only the rows
//     that are still active.
//   - Bounded and non-blocking. Each call locks at most batch holds with SKIP
//     LOCKED, so two instances running the sweep at the same time divide the
//     work instead of duplicating it, and neither waits on the other.
func (s *PostgresStore) ExpireHolds(ctx context.Context, now time.Time, batch int) (ExpiryResult, error) {
	if err := validateSweepParams(now, batch); err != nil {
		return ExpiryResult{}, err
	}
	var (
		result ExpiryResult
		err    error
	)
	for attempt := 1; attempt <= expireHoldsAttempts; attempt++ {
		if result, err = s.expireHolds(ctx, now, batch); err == nil {
			return result, nil
		}
		if !isRetryableClaimError(err) {
			return ExpiryResult{}, err
		}
	}
	return ExpiryResult{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func (s *PostgresStore) expireHolds(ctx context.Context, now time.Time, batch int) (ExpiryResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ExpiryResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The prune goes first so this transaction takes its locks in the same order
	// a claim does: idempotency record, then reservation, then seat row.
	pruned, err := tx.Exec(ctx, pruneIdempotencyQuery, now, batch)
	if err != nil {
		return ExpiryResult{}, classifyDurableError(err)
	}
	result := ExpiryResult{IdempotencyPruned: int(pruned.RowsAffected())}

	rows, err := tx.Query(ctx, lockExpiredHoldsQuery, now, batch)
	if err != nil {
		return ExpiryResult{}, classifyDurableError(err)
	}
	holdIDs := make([]string, 0, min(batch, expireHoldsPrealloc))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return ExpiryResult{}, classifyDurableError(err)
		}
		holdIDs = append(holdIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ExpiryResult{}, classifyDurableError(err)
	}
	rows.Close()

	result.More = result.IdempotencyPruned >= batch
	if len(holdIDs) > 0 {
		released, err := tx.Exec(ctx, releaseExpiredSeatsQuery, now, holdIDs)
		if err != nil {
			return ExpiryResult{}, classifyDurableError(err)
		}
		expired, err := tx.Exec(ctx, expireHoldsQuery, holdIDs)
		if err != nil {
			return ExpiryResult{}, classifyDurableError(err)
		}
		result.SeatsReleased = int(released.RowsAffected())
		result.ReservationsExpired = int(expired.RowsAffected())
		result.More = result.More || len(holdIDs) >= batch
	}

	if err := tx.Commit(ctx); err != nil {
		return ExpiryResult{}, classifyDurableError(err)
	}
	return result, nil
}

// DatabaseClock reads the authoritative deadline for a sweep. PostgreSQL
// implements it; a fake is what lets the worker be tested without a database.
//
// The sweep compares against this value instead of now() so the deadline is
// explicit and reproducible. The caller therefore owes the sweep a deadline taken
// from the database clock: a value ahead of it would expire live holds early and
// write a future-dated released_at.
type DatabaseClock interface {
	DatabaseNow(ctx context.Context) (time.Time, error)
}

// databaseNowQuery reads the PostgreSQL clock. It is the single source of truth
// for the sweep deadline, so a skewed API host cannot expire live holds early.
const databaseNowQuery = `SELECT now()`

// DatabaseNow returns the PostgreSQL clock.
func (s *PostgresStore) DatabaseNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := s.pool.QueryRow(ctx, databaseNowQuery).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("reservation: read the database clock: %w", classifyDurableError(err))
	}
	return now, nil
}

// SweeperConfig is the process wiring of the durable expiry sweep: how often it
// runs, how much one batch locks, and how long any single sweep may take.
type SweeperConfig struct {
	// Interval is the cadence between ticks. It must be positive and the process
	// configuration keeps it at or below half the hold window.
	Interval time.Duration
	// Batch is how many expired holds one sweep transaction locks.
	Batch int
	// Timeout bounds one sweep, including the clock read, so a stalled database
	// cannot wedge the worker.
	Timeout time.Duration
	// MaxBatches bounds how many batches one tick may run, so a backlog cannot
	// monopolise the worker; the next tick continues from where this one stopped.
	MaxBatches int
	// Logger receives the start/stop notices and every failed tick. A sweep
	// failure is logged and retried on the next tick, never fatal.
	Logger *slog.Logger
}

// Sweeper runs the durable expiry sweep on a configured cadence. It keeps no
// state between ticks: correctness lives in the durable rows, so a killed worker
// resumes from them and two workers divide the work instead of duplicating it.
type Sweeper struct {
	service *Service
	clock   DatabaseClock
	cfg     SweeperConfig
}

// NewSweeper builds a sweep worker. It refuses a configuration that could not
// make progress (a zero cadence, batch, batch bound, or deadline) instead of
// starting a loop that spins or sweeps with no bound.
func NewSweeper(service *Service, clock DatabaseClock, cfg SweeperConfig) (*Sweeper, error) {
	if service == nil {
		return nil, fmt.Errorf("%w: expiry sweep needs a reservation service", ErrInvalidInput)
	}
	if clock == nil {
		return nil, fmt.Errorf("%w: expiry sweep needs a database clock", ErrInvalidInput)
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("%w: expiry sweep interval must be positive, got %s", ErrInvalidInput, cfg.Interval)
	}
	if cfg.Batch <= 0 {
		return nil, fmt.Errorf("%w: expiry sweep batch must be positive, got %d", ErrInvalidInput, cfg.Batch)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("%w: expiry sweep timeout must be positive, got %s", ErrInvalidInput, cfg.Timeout)
	}
	if cfg.MaxBatches <= 0 {
		return nil, fmt.Errorf("%w: expiry sweep batch bound must be positive, got %d", ErrInvalidInput, cfg.MaxBatches)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Sweeper{service: service, clock: clock, cfg: cfg}, nil
}

// Run sweeps immediately and then on every interval until the process context is
// cancelled. A failed tick is logged and the next tick tries again, so a
// transient database failure never stops expiry; shutdown stops the worker before
// it starts another sweep.
func (s *Sweeper) Run(ctx context.Context) {
	s.cfg.Logger.Info("durable expiry sweep started",
		"interval", s.cfg.Interval, "batch", s.cfg.Batch, "timeout", s.cfg.Timeout, "max_batches", s.cfg.MaxBatches)
	defer s.cfg.Logger.Info("durable expiry sweep stopped")

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.Sweep(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.cfg.Logger.Error("expiry sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep runs one tick: at most MaxBatches batches, each bounded by Timeout and by
// the process context, each comparing against a deadline read from the database
// clock. It stops as soon as the durable store reports no more work.
func (s *Sweeper) Sweep(ctx context.Context) error {
	for batch := 1; batch <= s.cfg.MaxBatches; batch++ {
		result, err := s.sweepBatch(ctx)
		if err != nil {
			return err
		}
		if !result.More {
			return nil
		}
	}
	return nil
}

// sweepBatch is one bounded sweep attempt: read the database clock, then sweep.
func (s *Sweeper) sweepBatch(ctx context.Context) (ExpiryResult, error) {
	sweepCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	now, err := s.clock.DatabaseNow(sweepCtx)
	if err != nil {
		return ExpiryResult{}, err
	}
	return s.service.ExpireHolds(sweepCtx, now, s.cfg.Batch)
}
