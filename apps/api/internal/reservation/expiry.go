package reservation

import (
	"context"
	"fmt"
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
	lockExpiredHoldsQuery = `
SELECT r.id::text
FROM cinema.reservations r
WHERE r.status IN ('held', 'payment_pending')
  AND r.expires_at <= $1
ORDER BY r.expires_at, r.id
LIMIT $2
FOR NO KEY UPDATE SKIP LOCKED`

	// releaseExpiredSeatsQuery frees the seat rows of the holds locked above.
	// released_at IS NULL is what makes a partially released hold safe: a seat a
	// re-claim takeover already freed keeps its original released_at, and the
	// reservation still transitions to expired below.
	releaseExpiredSeatsQuery = `
UPDATE cinema.reservation_seats
SET released_at = $1
WHERE released_at IS NULL
  AND reservation_id = ANY($2::uuid[])`

	// expireHoldsQuery closes the holds themselves. The status predicate repeats
	// the candidate rule so only a live hold can become expired, and re-checking
	// it here means the sweep can never rewrite a status it did not select.
	expireHoldsQuery = `
UPDATE cinema.reservations
SET status = 'expired'
WHERE id = ANY($1::uuid[])
  AND status IN ('held', 'payment_pending')`
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
