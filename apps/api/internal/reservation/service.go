package reservation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// Errors reported by the hold application service and its durable store.
var (
	// ErrHoldWindowExpired reports that a claim whose own hold window had
	// already closed was rejected by the durable schema (SQLSTATE 23514 from
	// the release_expired_seat_hold trigger). It is caller error, not a seat
	// conflict: the seats may well be free.
	ErrHoldWindowExpired = errors.New("reservation: hold window already expired")

	// ErrIdempotencyKeyReused reports that an idempotency key was replayed with
	// a different payload. It is a conflict, never a silent second hold.
	ErrIdempotencyKeyReused = errors.New("reservation: idempotency key reused with a different payload")

	// ErrDuplicateClaim reports a self-conflict inside one claim, such as the
	// same reservation claiming one seat twice. It is distinct from
	// ErrSeatsUnavailable, which means another hold owns the seat.
	ErrDuplicateClaim = errors.New("reservation: duplicate seat claim")

	// ErrEventNotFound reports an availability read of an unknown event.
	ErrEventNotFound = errors.New("reservation: event not found")

	// ErrUnavailable reports that the coordination or durable layer could not
	// be reached. Callers must surface it as a retryable failure (503) and
	// never as a success or a fail-open.
	ErrUnavailable = errors.New("reservation: reservation service unavailable")

	// ErrIdempotencyKeyTaken reports that the durable store already holds a
	// record for the key. The store cannot tell a replay from a reuse on its
	// own, so the service resolves it by reading the stored record.
	ErrIdempotencyKeyTaken = errors.New("reservation: idempotency key already recorded")
)

const (
	// statusHoldCreated is the outcome status recorded for a successful hold.
	statusHoldCreated = 201

	// idempotencyRetention is how long a stored request keeps answering
	// replays. It is deliberately longer than any hold window, so a replay of
	// a live hold is always answered from the record; records past this
	// deadline may be pruned by the durable expiry sweep.
	idempotencyRetention = 24 * time.Hour

	// maxIdempotencyKeyLength bounds the client supplied key so a record stays
	// a bounded row.
	maxIdempotencyKeyLength = 200
)

// Hold is the durable outcome of a hold request: one reservation row plus one
// reservation_seats row per claimed seat.
type Hold struct {
	// ID is the reservation identifier, also the owner value of the Redis seats.
	ID string
	// EventID is the event whose seats are held.
	EventID string
	// SeatIDs is the claimed event seat set in ascending order, the order the
	// durable rows are written in and the order that avoids multi-claim deadlocks.
	SeatIDs []int64
	// ExpiresAt is the durable hold deadline, derived from the configured HoldTTL.
	ExpiresAt time.Time
	// Status is the outcome status stored with an idempotency record, so a
	// replay answers with the same status as the original request.
	Status int
}

// IdempotencyRecord is the durable record of one hold request, keyed by the
// client supplied Idempotency-Key.
type IdempotencyRecord struct {
	// Key is the client supplied idempotency key.
	Key string
	// Fingerprint identifies the payload the key was first used with.
	Fingerprint string
	// Status is the outcome status of the original request.
	Status int
	// HoldID is the reservation created by the original request.
	HoldID string
	// HoldExpiresAt is the deadline of that reservation, replayed verbatim so a
	// replay can never extend the hold window.
	HoldExpiresAt time.Time
	// ExpiresAt is when this record may be pruned.
	ExpiresAt time.Time
}

// SeatAvailability is one event seat with its current occupancy.
type SeatAvailability struct {
	// ID is the event seat identifier.
	ID int64
	// LayoutSeatID is the physical seat it materializes.
	LayoutSeatID string
	// State is the stored cinema.event_seats.state value.
	State string
	// Held reports whether a live hold currently owns the seat.
	Held bool
}

// Locker is the expiring coordination layer, satisfied by Coordinator.
type Locker interface {
	Acquire(ctx context.Context, holdID string, eventSeatIDs []int64, ttl time.Duration) error
	Release(ctx context.Context, holdID string, eventSeatIDs []int64) error
}

// Store is the durable layer. PostgreSQL is the source of truth; the interface
// keeps the service testable without a live database.
type Store interface {
	// LookupIdempotency reads the record stored for key. The second result is
	// false when no record exists.
	LookupIdempotency(ctx context.Context, key string) (IdempotencyRecord, bool, error)
	// RecordHold writes the reservation, its seats, and the idempotency record
	// in one transaction, so a hold and the record that replays it can never
	// disagree.
	RecordHold(ctx context.Context, hold Hold, record IdempotencyRecord) error
	// ListSeatAvailability reads every seat of one event with its occupancy.
	ListSeatAvailability(ctx context.Context, eventID string) ([]SeatAvailability, error)
	// ExpireHolds runs one durable expiry sweep: it releases the seat rows of at
	// most batch expired holds, marks those holds expired, and prunes the
	// idempotency records whose retention deadline passed, all in one
	// transaction.
	ExpireHolds(ctx context.Context, now time.Time, batch int) (ExpiryResult, error)
}

// Service creates holds and reports seat availability.
type Service struct {
	locker Locker
	store  Store
	ttl    time.Duration
}

// NewService builds the hold service over a coordination layer, a durable
// store, and the configured hold window.
func NewService(locker Locker, store Store, ttl time.Duration) *Service {
	return &Service{locker: locker, store: store, ttl: ttl}
}

// CreateHold claims the whole seat set and stores the durable hold.
//
// The order is fixed, and each step has one reason:
//
//  1. Validate the request, so a rejected call never touches Redis or PostgreSQL.
//  2. Read the durable idempotency record, so a replay is answered from the
//     stored result without re-acquiring seats or extending the window.
//  3. Acquire the full seat set in Redis, which fails fast on contention
//     without writing anything durable.
//  4. Write the reservation, its seats, and the idempotency record in one
//     PostgreSQL transaction.
//  5. On any durable failure, release the Redis seats: Redis must never hold a
//     seat that PostgreSQL does not know about.
func (s *Service) CreateHold(ctx context.Context, idempotencyKey, eventID string, eventSeatIDs []int64) (Hold, error) {
	key, err := validateIdempotencyKey(idempotencyKey)
	if err != nil {
		return Hold{}, err
	}
	event, err := validateEventID(eventID)
	if err != nil {
		return Hold{}, err
	}
	seats, err := claimedSeats(eventSeatIDs)
	if err != nil {
		return Hold{}, err
	}
	if s.ttl < minTTL {
		return Hold{}, fmt.Errorf("%w: hold window must be at least %s, got %s", ErrInvalidInput, minTTL, s.ttl)
	}

	fingerprint := requestFingerprint(event, seats)
	if stored, found, err := s.storedResult(ctx, key, fingerprint, event, seats); err != nil || found {
		return stored, err
	}

	hold := Hold{
		ID:        venue.NewID(),
		EventID:   event,
		SeatIDs:   seats,
		ExpiresAt: time.Now().Add(s.ttl),
		Status:    statusHoldCreated,
	}
	if err := s.locker.Acquire(ctx, hold.ID, seats, s.ttl); err != nil {
		return Hold{}, mapCoordinationError(err)
	}

	record := IdempotencyRecord{
		Key:           key,
		Fingerprint:   fingerprint,
		Status:        hold.Status,
		HoldID:        hold.ID,
		HoldExpiresAt: hold.ExpiresAt,
		ExpiresAt:     time.Now().Add(idempotencyRetention),
	}
	if err := s.store.RecordHold(ctx, hold, record); err != nil {
		releaseErr := s.locker.Release(ctx, hold.ID, seats)
		if errors.Is(err, ErrIdempotencyKeyTaken) {
			// A concurrent request with the same key committed first: its
			// stored result answers this one, and the seats this request
			// claimed are already released above.
			stored, found, lookupErr := s.storedResult(ctx, key, fingerprint, event, seats)
			switch {
			case lookupErr != nil:
				return Hold{}, lookupErr
			case found:
				return stored, nil
			default:
				return Hold{}, fmt.Errorf("%w: %q", ErrIdempotencyKeyReused, key)
			}
		}
		mapped := mapDurableError(err)
		if releaseErr != nil {
			return Hold{}, fmt.Errorf("%w (releasing the claimed seats also failed: %v)", mapped, releaseErr)
		}
		return Hold{}, mapped
	}
	return hold, nil
}

// ListAvailability reports every seat of one event with its occupancy.
//
// Occupancy is decided by PostgreSQL alone: a seat is held only while its
// reservation_seats row is active and its reservation has not expired, so a
// hold whose window elapsed is reported free even before the expiry sweep
// releases it. Redis is deliberately not consulted here; it is the fast
// admission layer, not the source of truth.
func (s *Service) ListAvailability(ctx context.Context, eventID string) ([]SeatAvailability, error) {
	event, err := validateEventID(eventID)
	if err != nil {
		return nil, err
	}
	seats, err := s.store.ListSeatAvailability(ctx, event)
	if err != nil {
		return nil, mapDurableError(err)
	}
	return seats, nil
}

// ExpireHolds runs one durable expiry sweep over the durable store.
//
// The sweep itself keeps no state between calls: the cadence, the batch size and
// the loop that keeps sweeping while More is reported belong to the process
// wiring. A caller may stop after any batch and a restarted process resumes from
// the durable rows alone, which is what makes the sweep restart-safe.
func (s *Service) ExpireHolds(ctx context.Context, now time.Time, batch int) (ExpiryResult, error) {
	if err := validateSweepParams(now, batch); err != nil {
		return ExpiryResult{}, err
	}
	result, err := s.store.ExpireHolds(ctx, now, batch)
	if err != nil {
		return ExpiryResult{}, mapDurableError(err)
	}
	return result, nil
}

// storedResult answers a request from its durable idempotency record. It
// reports found=false when the key was never used, and a conflict when it was
// used for a different payload.
func (s *Service) storedResult(ctx context.Context, key, fingerprint, eventID string, seats []int64) (Hold, bool, error) {
	record, found, err := s.store.LookupIdempotency(ctx, key)
	if err != nil {
		return Hold{}, false, mapDurableError(err)
	}
	if !found {
		return Hold{}, false, nil
	}
	if record.Fingerprint != fingerprint {
		return Hold{}, false, fmt.Errorf("%w: %q was already used for a different seat selection", ErrIdempotencyKeyReused, key)
	}
	return Hold{
		ID:        record.HoldID,
		EventID:   eventID,
		SeatIDs:   seats,
		ExpiresAt: record.HoldExpiresAt,
		Status:    record.Status,
	}, true, nil
}

// validateIdempotencyKey normalizes the client supplied key. A blank key is
// rejected instead of being treated as "no idempotency", because that would
// silently allow a replay to create a second hold.
func validateIdempotencyKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" {
		return "", fmt.Errorf("%w: Idempotency-Key header is required", ErrInvalidInput)
	}
	if len(key) > maxIdempotencyKeyLength {
		return "", fmt.Errorf("%w: Idempotency-Key must be at most %d characters", ErrInvalidInput, maxIdempotencyKeyLength)
	}
	return key, nil
}

// validateEventID normalizes the event identifier.
func validateEventID(value string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" {
		return "", fmt.Errorf("%w: eventId must not be empty", ErrInvalidInput)
	}
	return id, nil
}

// claimedSeats validates the seat set and returns it in ascending order. The
// order matters twice: the durable rows are written in it, which keeps two
// overlapping multi-seat claimers from deadlocking, and the request fingerprint
// is computed from it, so the same selection in another order is the same
// request.
func claimedSeats(eventSeatIDs []int64) ([]int64, error) {
	if len(eventSeatIDs) == 0 {
		return nil, fmt.Errorf("%w: at least one event seat must be selected", ErrInvalidInput)
	}
	seen := make(map[int64]struct{}, len(eventSeatIDs))
	seats := make([]int64, 0, len(eventSeatIDs))
	for _, eventSeatID := range eventSeatIDs {
		if eventSeatID <= 0 {
			return nil, fmt.Errorf("%w: event seat id %d must be positive", ErrInvalidInput, eventSeatID)
		}
		if _, duplicate := seen[eventSeatID]; duplicate {
			return nil, fmt.Errorf("%w: event seat id %d appears more than once", ErrInvalidInput, eventSeatID)
		}
		seen[eventSeatID] = struct{}{}
		seats = append(seats, eventSeatID)
	}
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })
	return seats, nil
}

// requestFingerprint identifies the payload an idempotency key was used with.
// The seat set must already be canonical (ascending) so the same selection is
// the same fingerprint.
func requestFingerprint(eventID string, eventSeatIDs []int64) string {
	ids := make([]string, 0, len(eventSeatIDs))
	for _, eventSeatID := range eventSeatIDs {
		ids = append(ids, strconv.FormatInt(eventSeatID, 10))
	}
	sum := sha256.Sum256([]byte(eventID + "|" + strings.Join(ids, ",")))
	return hex.EncodeToString(sum[:])
}

// mapCoordinationError keeps the errors that already carry a meaning and turns
// every other coordination failure into a retryable one.
func mapCoordinationError(err error) error {
	if errors.Is(err, ErrSeatsUnavailable) || errors.Is(err, ErrInvalidInput) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// mapDurableError keeps every distinct durable outcome distinct and reports
// anything else as a retryable failure, never as a success.
func mapDurableError(err error) error {
	for _, sentinel := range []error{
		ErrSeatsUnavailable,
		ErrHoldWindowExpired,
		ErrDuplicateClaim,
		ErrIdempotencyKeyReused,
		ErrEventNotFound,
		ErrInvalidInput,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// Durable statements. The availability query is the single place that decides
// whether a seat is held: an expired hold that nobody has re-claimed is still
// stored as active until the trigger or the expiry sweep releases it, so the
// predicate has to check the deadline as well as released_at.
const (
	lookupIdempotencyQuery = `
SELECT idempotency_key, request_fingerprint, status, hold_id::text, hold_expires_at, expires_at
FROM cinema.idempotency_keys
WHERE idempotency_key = $1`

	insertIdempotencyQuery = `
INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)`

	insertReservationQuery = `
INSERT INTO cinema.reservations (id, event_id, status, expires_at)
VALUES ($1, $2, 'held', $3)`

	insertReservationSeatQuery = `
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
VALUES ($1, $2, $3)`

	listSeatAvailabilityQuery = `
SELECT es.id, es.layout_seat_id, es.state,
       EXISTS (
         SELECT 1
         FROM cinema.reservation_seats rs
         JOIN cinema.reservations r ON r.id = rs.reservation_id
         WHERE rs.event_seat_id = es.id
           AND rs.released_at IS NULL
           AND r.expires_at > now()
       ) AS held
FROM cinema.event_seats es
WHERE es.event_id = $1
ORDER BY es.id`

	eventExistsQuery = `SELECT 1 FROM cinema.events WHERE id = $1`
)

// recordHoldAttempts bounds the retry of a claim that lost a deadlock or a
// serialization conflict. Overlapping multi-seat claimers can still collide
// when a re-claim takes over expired seats, so one bounded retry re-runs the
// whole transaction instead of reporting a transient conflict as caller error.
const recordHoldAttempts = 3

// PostgresStore persists holds and idempotency records in PostgreSQL, the
// authoritative source of truth for reservations.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore opens a pooled connection to the given database URL.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open reservation database: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases pooled connections.
func (s *PostgresStore) Close() { s.pool.Close() }

// LookupIdempotency reads the record stored for key.
func (s *PostgresStore) LookupIdempotency(ctx context.Context, key string) (IdempotencyRecord, bool, error) {
	var record IdempotencyRecord
	err := s.pool.QueryRow(ctx, lookupIdempotencyQuery, key).
		Scan(&record.Key, &record.Fingerprint, &record.Status, &record.HoldID, &record.HoldExpiresAt, &record.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, classifyDurableError(err)
	}
	return record, true, nil
}

// RecordHold writes the reservation, its seats, and the idempotency record in
// one transaction. The seats are written in ascending event_seat_id order so
// two overlapping claimers walk the same order and cannot deadlock by
// construction; a residual deadlock or serialization failure is retried.
func (s *PostgresStore) RecordHold(ctx context.Context, hold Hold, record IdempotencyRecord) error {
	var err error
	for attempt := 1; attempt <= recordHoldAttempts; attempt++ {
		if err = s.recordHold(ctx, hold, record); err == nil {
			return nil
		}
		if !isRetryableClaimError(err) {
			return err
		}
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func (s *PostgresStore) recordHold(ctx context.Context, hold Hold, record IdempotencyRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The record goes first: a replay that lost the race fails here without
	// touching the reservation tables at all.
	if _, err := tx.Exec(ctx, insertIdempotencyQuery,
		record.Key, record.Fingerprint, record.Status, record.HoldID, record.HoldExpiresAt, record.ExpiresAt); err != nil {
		return classifyDurableError(err)
	}
	if _, err := tx.Exec(ctx, insertReservationQuery, hold.ID, hold.EventID, hold.ExpiresAt); err != nil {
		return classifyDurableError(err)
	}
	seats := make([]int64, len(hold.SeatIDs))
	copy(seats, hold.SeatIDs)
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })
	for _, eventSeatID := range seats {
		if _, err := tx.Exec(ctx, insertReservationSeatQuery, hold.ID, hold.EventID, eventSeatID); err != nil {
			return classifyDurableError(err)
		}
	}
	return classifyDurableError(tx.Commit(ctx))
}

// ListSeatAvailability reads every seat of one event with its occupancy.
func (s *PostgresStore) ListSeatAvailability(ctx context.Context, eventID string) ([]SeatAvailability, error) {
	rows, err := s.pool.Query(ctx, listSeatAvailabilityQuery, eventID)
	if err != nil {
		return nil, classifyDurableError(err)
	}
	defer rows.Close()
	seats := []SeatAvailability{}
	for rows.Next() {
		var seat SeatAvailability
		if err := rows.Scan(&seat.ID, &seat.LayoutSeatID, &seat.State, &seat.Held); err != nil {
			return nil, classifyDurableError(err)
		}
		seats = append(seats, seat)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyDurableError(err)
	}
	if len(seats) > 0 {
		return seats, nil
	}
	// An event whose seats were never materialized is still an event; a bad
	// event identifier is not found instead of an empty availability list.
	var found int
	if err := s.pool.QueryRow(ctx, eventExistsQuery, eventID).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrEventNotFound, eventID)
		}
		return nil, classifyDurableError(err)
	}
	return seats, nil
}

// classifyDurableError maps SQLSTATE codes to the documented outcomes. The
// distinction between the two 23505 cases and the 23514 case is the difference
// between a taken seat, a self-conflict or replay, and a claim whose own window
// closed; conflating them would report a retryable conflict where the caller
// has to redo the request.
func classifyDurableError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			switch pgErr.ConstraintName {
			case "idempotency_keys_pkey":
				return fmt.Errorf("%w: %s", ErrIdempotencyKeyTaken, pgErr.Message)
			case "reservation_seats_active_hold":
				return fmt.Errorf("%w: %s", ErrSeatsUnavailable, pgErr.Message)
			default:
				return fmt.Errorf("%w: %s", ErrDuplicateClaim, pgErr.Message)
			}
		case "23514":
			return fmt.Errorf("%w: %s", ErrHoldWindowExpired, pgErr.Message)
		case "23503", "22P02":
			return fmt.Errorf("%w: %s", ErrInvalidInput, pgErr.Message)
		}
	}
	return err
}

// isRetryableClaimError reports the transient conflicts worth one more attempt.
func isRetryableClaimError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40P01" || pgErr.Code == "40001"
}
