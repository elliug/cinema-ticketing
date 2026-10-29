package reservation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Errors reported by the coordination layer.
var (
	// ErrSeatsUnavailable reports that at least one requested seat is currently
	// held by a different hold. A failed acquire writes nothing at all, so the
	// caller's seat set is untouched.
	ErrSeatsUnavailable = errors.New("reservation: seat held by another hold")

	// ErrInvalidInput reports caller input rejected before Redis is touched.
	ErrInvalidInput = errors.New("reservation: invalid input")
)

// keyPrefix is the documented hold key namespace; see the package documentation.
// One key per event seat, holding the id of the owning hold.
const keyPrefix = "reservation:{hold}:seat:"

// minTTL is the shortest hold window Redis can represent: PX takes milliseconds.
const minTTL = time.Millisecond

// acquireScript checks every requested seat and then claims all of them inside a
// single server-side execution, so the check-then-set cannot interleave with
// another acquirer. KEYS are the seat keys, ARGV[1] is the hold id (owner) and
// ARGV[2] is the window in milliseconds. It returns 1 when the whole set is
// held by the caller and 0 when it wrote nothing because another hold owns at
// least one seat; re-acquiring a seat it already owns refreshes that window.
const acquireScript = `
local owner = ARGV[1]
local window = ARGV[2]
for index = 1, #KEYS do
  local current = redis.call('GET', KEYS[index])
  if current and current ~= owner then
    return 0
  end
end
for index = 1, #KEYS do
  redis.call('SET', KEYS[index], owner, 'PX', window)
end
return 1
`

// releaseScript frees only the seats that the given hold still owns: the GET and
// the DEL of one key happen inside the same atomic execution, and a key owned by
// anyone else is left untouched. ARGV[1] is the hold id. It returns the number of
// seats released, which is zero when the hold already expired or never owned the
// seat, and that case is a successful no-op.
const releaseScript = `
local owner = ARGV[1]
local released = 0
for index = 1, #KEYS do
  if redis.call('GET', KEYS[index]) == owner then
    redis.call('DEL', KEYS[index])
    released = released + 1
  end
end
return released
`

// Coordinator owns the expiring per-seat holds in Redis. It is safe for
// concurrent use; all of its state lives in Redis, not in the process.
type Coordinator struct {
	client  redis.UniversalClient
	acquire *redis.Script
	release *redis.Script
}

// NewCoordinator builds the coordination layer on top of an existing Redis
// client, so connection, TLS, and pooling policy stay with the caller.
func NewCoordinator(client redis.UniversalClient) *Coordinator {
	return &Coordinator{
		client:  client,
		acquire: redis.NewScript(acquireScript),
		release: redis.NewScript(releaseScript),
	}
}

// Acquire holds every given event seat for the whole TTL or holds none of them.
// It returns ErrSeatsUnavailable when another hold owns any seat of the set, and
// in that case no seat of this request is claimed. Re-acquiring a set the same
// hold already owns succeeds and restarts the window.
func (c *Coordinator) Acquire(ctx context.Context, holdID string, eventSeatIDs []int64, ttl time.Duration) error {
	owner, err := validateHoldID(holdID)
	if err != nil {
		return err
	}
	if ttl < minTTL {
		return fmt.Errorf("%w: hold window must be at least %s, got %s", ErrInvalidInput, minTTL, ttl)
	}
	keys, err := seatKeys(eventSeatIDs)
	if err != nil {
		return err
	}
	if err := requireDistinctSeats(eventSeatIDs); err != nil {
		return err
	}

	acquired, err := c.acquire.Run(ctx, c.client, keys, owner, strconv.FormatInt(ttl.Milliseconds(), 10)).Int64()
	if err != nil {
		return fmt.Errorf("reservation: acquire %d seat(s) for hold %q: %w", len(keys), owner, err)
	}
	if acquired != 1 {
		return ErrSeatsUnavailable
	}
	return nil
}

// Release frees the given event seats that are still owned by holdID. It never
// deletes a seat owned by a different hold, so a late release cannot steal a seat
// that someone else re-acquired. Releasing a hold whose window already elapsed,
// or that never owned the seats, is a successful no-op: the seats are already
// free of that hold.
func (c *Coordinator) Release(ctx context.Context, holdID string, eventSeatIDs []int64) error {
	owner, err := validateHoldID(holdID)
	if err != nil {
		return err
	}
	keys, err := seatKeys(eventSeatIDs)
	if err != nil {
		return err
	}

	if _, err := c.release.Run(ctx, c.client, keys, owner).Int64(); err != nil {
		return fmt.Errorf("reservation: release %d seat(s) for hold %q: %w", len(keys), owner, err)
	}
	return nil
}

// Held reports whether each given event seat is currently held, in request
// order. It is a single read-only lookup per set and changes no key, value, or
// TTL; an elapsed hold window reads as not held.
func (c *Coordinator) Held(ctx context.Context, eventSeatIDs []int64) ([]bool, error) {
	keys, err := seatKeys(eventSeatIDs)
	if err != nil {
		return nil, err
	}

	values, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("reservation: read occupancy of %d seat(s): %w", len(keys), err)
	}
	held := make([]bool, len(values))
	for index, value := range values {
		held[index] = value != nil
	}
	return held, nil
}

// validateHoldID normalizes the hold identifier used as the seat key's owner.
func validateHoldID(holdID string) (string, error) {
	owner := strings.TrimSpace(holdID)
	if owner == "" {
		return "", fmt.Errorf("%w: hold id must not be empty", ErrInvalidInput)
	}
	return owner, nil
}

// seatKeys validates the seat set and maps it to coordination keys in request
// order. An empty set is rejected instead of trivially succeeding. Duplicates are
// allowed here because Held reports occupancy in request order; Acquire rejects
// them separately, where a repeated seat can only mean a caller defect.
func seatKeys(eventSeatIDs []int64) ([]string, error) {
	if len(eventSeatIDs) == 0 {
		return nil, fmt.Errorf("%w: at least one event seat is required", ErrInvalidInput)
	}
	keys := make([]string, 0, len(eventSeatIDs))
	for _, eventSeatID := range eventSeatIDs {
		if eventSeatID <= 0 {
			return nil, fmt.Errorf("%w: event seat id %d must be positive", ErrInvalidInput, eventSeatID)
		}
		keys = append(keys, keyPrefix+strconv.FormatInt(eventSeatID, 10))
	}
	return keys, nil
}

// requireDistinctSeats rejects a seat listed twice in one acquire, which can only
// be a caller defect: the same seat cannot be held twice for one hold.
func requireDistinctSeats(eventSeatIDs []int64) error {
	seen := make(map[int64]struct{}, len(eventSeatIDs))
	for _, eventSeatID := range eventSeatIDs {
		if _, duplicate := seen[eventSeatID]; duplicate {
			return fmt.Errorf("%w: event seat id %d appears more than once", ErrInvalidInput, eventSeatID)
		}
		seen[eventSeatID] = struct{}{}
	}
	return nil
}
