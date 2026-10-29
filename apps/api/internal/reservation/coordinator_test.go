package reservation_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

// holdKeyPrefix is the documented Redis key namespace: one key per event seat,
// holding the id of the hold that currently owns it.
const holdKeyPrefix = "reservation:{hold}:seat:"

func newTestCoordinator(t *testing.T) (*reservation.Coordinator, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return reservation.NewCoordinator(client), server
}

// holdKeys exposes the raw coordination keys so a test can prove that a failed
// acquire left nothing behind.
func holdKeys(server *miniredis.Miniredis) []string {
	keys := make([]string, 0, len(server.Keys()))
	for _, key := range server.Keys() {
		if strings.HasPrefix(key, holdKeyPrefix) {
			keys = append(keys, key)
		}
	}
	return keys
}

func mustAcquire(t *testing.T, coordinator *reservation.Coordinator, holdID string, ttl time.Duration, eventSeatIDs ...int64) {
	t.Helper()
	if err := coordinator.Acquire(context.Background(), holdID, eventSeatIDs, ttl); err != nil {
		t.Fatalf("Acquire(%q, %v) returned error: %v", holdID, eventSeatIDs, err)
	}
}

func mustHeld(t *testing.T, coordinator *reservation.Coordinator, eventSeatIDs ...int64) []bool {
	t.Helper()
	held, err := coordinator.Held(context.Background(), eventSeatIDs)
	if err != nil {
		t.Fatalf("Held(%v) returned error: %v", eventSeatIDs, err)
	}
	if len(held) != len(eventSeatIDs) {
		t.Fatalf("Held(%v) returned %d results, want %d", eventSeatIDs, len(held), len(eventSeatIDs))
	}
	return held
}

func wantAll(t *testing.T, held []bool, want bool, context string) {
	t.Helper()
	for index, value := range held {
		if value != want {
			t.Fatalf("%s: seat at index %d held = %t, want %t (all results %v)", context, index, value, want, held)
		}
	}
}

// Behaviour 1 and 5: a multi-seat acquire holds every seat for the whole TTL or
// holds none, and a partial overlap leaves no key from the losing hold behind.
func TestAcquireHoldsEverySeatOrNone(t *testing.T) {
	ctx := context.Background()
	coordinator, server := newTestCoordinator(t)

	mustAcquire(t, coordinator, "hold-a", time.Minute, 11, 12, 13)
	wantAll(t, mustHeld(t, coordinator, 11, 12, 13), true, "after a successful acquire")

	// Re-acquiring a subset with the owning hold is the idempotent replay path.
	if err := coordinator.Acquire(ctx, "hold-a", []int64{12}, time.Minute); err != nil {
		t.Fatalf("re-acquire by the owning hold returned error: %v", err)
	}

	// Seat 13 is already held and seat 21 is free: the whole request must fail.
	err := coordinator.Acquire(ctx, "hold-b", []int64{13, 21}, time.Minute)
	if !errors.Is(err, reservation.ErrSeatsUnavailable) {
		t.Fatalf("Acquire over a held seat returned %v, want ErrSeatsUnavailable", err)
	}

	held := mustHeld(t, coordinator, 13, 21)
	if !held[0] {
		t.Fatalf("failed acquire dropped the conflicting seat 13 that hold-a still owns")
	}
	if held[1] {
		t.Fatalf("seat 21 stayed held after a failed multi-seat acquire")
	}
	if keys := holdKeys(server); len(keys) != 3 {
		t.Fatalf("expected exactly hold-a's 3 keys after the failed acquire, found %v", keys)
	}
	if server.Exists(holdKeyPrefix + "21") {
		t.Fatalf("failed acquire left key %q behind", holdKeyPrefix+"21")
	}
}

// Behaviour 2: 16 contenders for one seat produce exactly one winner, mirroring
// the accepted contract in tests/concurrency/hold_concurrency_test.go.
func TestConcurrentAcquireOfOneSeatHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	coordinator, server := newTestCoordinator(t)

	const contenders = 16
	results := make([]error, contenders)
	start := make(chan struct{})
	var group sync.WaitGroup
	for contender := 0; contender < contenders; contender++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index] = coordinator.Acquire(ctx, fmt.Sprintf("hold-%02d", index), []int64{7}, time.Minute)
		}(contender)
	}
	close(start)
	group.Wait()

	var winners int
	for index, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, reservation.ErrSeatsUnavailable):
		default:
			t.Fatalf("contender %02d returned an unexpected error: %v", index, err)
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner out of %d contenders, got %d", contenders, winners)
	}
	if keys := holdKeys(server); len(keys) != 1 {
		t.Fatalf("expected exactly one coordination key for the single seat, found %v", keys)
	}
}

// Behaviour 3: the TTL bounds the hold, and the seat becomes acquirable again
// only after the window has elapsed.
func TestAcquireTTLBoundsTheHold(t *testing.T) {
	coordinator, server := newTestCoordinator(t)

	const ttl = 30 * time.Second
	mustAcquire(t, coordinator, "hold-a", ttl, 5)

	server.FastForward(ttl - time.Second)
	if !mustHeld(t, coordinator, 5)[0] {
		t.Fatalf("seat 5 expired before its TTL elapsed")
	}

	server.FastForward(2 * time.Second)
	if mustHeld(t, coordinator, 5)[0] {
		t.Fatalf("seat 5 is still held after its TTL elapsed")
	}
	if err := coordinator.Acquire(context.Background(), "hold-b", []int64{5}, time.Minute); err != nil {
		t.Fatalf("Acquire after the previous hold expired returned error: %v", err)
	}
}

// Behaviour 4: Release frees only the keys owned by the given hold.
func TestReleaseFreesOnlyItsOwnSeats(t *testing.T) {
	ctx := context.Background()
	coordinator, _ := newTestCoordinator(t)

	mustAcquire(t, coordinator, "hold-a", time.Minute, 31)
	mustAcquire(t, coordinator, "hold-b", time.Minute, 32)

	if err := coordinator.Release(ctx, "hold-a", []int64{31}); err != nil {
		t.Fatalf("Release returned error: %v", err)
	}
	held := mustHeld(t, coordinator, 31, 32)
	if held[0] || !held[1] {
		t.Fatalf("after releasing 31: held(31,32) = %v, want [false true]", held)
	}

	// Seat 31 is re-acquired by hold-b; a late Release from hold-a must not
	// delete the new owner's key, and hold-a must not touch seat 32 either.
	mustAcquire(t, coordinator, "hold-b", time.Minute, 31)
	if err := coordinator.Release(ctx, "hold-a", []int64{31, 32}); err != nil {
		t.Fatalf("Release returned error: %v", err)
	}
	held = mustHeld(t, coordinator, 31, 32)
	if !held[0] || !held[1] {
		t.Fatalf("late Release deleted another hold's key: held(31,32) = %v, want [true true]", held)
	}
}

// Behaviour 4, expiry case: releasing a hold whose window already elapsed is a
// documented no-op that succeeds and leaves the new holder untouched.
func TestReleaseAfterExpiryIsANoOp(t *testing.T) {
	ctx := context.Background()
	coordinator, server := newTestCoordinator(t)

	mustAcquire(t, coordinator, "hold-a", 30*time.Second, 41)
	server.FastForward(31 * time.Second)
	mustAcquire(t, coordinator, "hold-b", time.Minute, 41)

	if err := coordinator.Release(ctx, "hold-a", []int64{41}); err != nil {
		t.Fatalf("Release of an expired hold returned error: %v", err)
	}
	if !mustHeld(t, coordinator, 41)[0] {
		t.Fatalf("Release of an expired hold freed the seat of the current holder")
	}
	if err := coordinator.Release(ctx, "hold-unknown", []int64{41}); err != nil {
		t.Fatalf("Release of an unknown hold returned error: %v", err)
	}
	if !mustHeld(t, coordinator, 41)[0] {
		t.Fatalf("Release of an unknown hold freed another hold's seat")
	}
}

// Behaviour 5: Held reports occupancy in request order and mutates nothing.
func TestHeldReportsOccupancyWithoutMutating(t *testing.T) {
	coordinator, server := newTestCoordinator(t)

	mustAcquire(t, coordinator, "hold-a", 30*time.Second, 51)
	key := holdKeyPrefix + "51"
	keysBefore := holdKeys(server)
	ttlBefore := server.TTL(key)

	held := mustHeld(t, coordinator, 52, 51, 52)
	want := []bool{false, true, false}
	for index := range want {
		if held[index] != want[index] {
			t.Fatalf("Held(52,51,52) = %v, want %v (request order preserved)", held, want)
		}
	}

	if keysAfter := holdKeys(server); len(keysAfter) != len(keysBefore) || !strings.Contains(strings.Join(keysAfter, ","), "51") {
		t.Fatalf("Held changed the key set: before %v, after %v", keysBefore, keysAfter)
	}
	if ttlAfter := server.TTL(key); ttlAfter != ttlBefore {
		t.Fatalf("Held changed the TTL of %q: before %v, after %v", key, ttlBefore, ttlAfter)
	}
}

// Behaviour 6: empty input and non-positive TTLs are rejected instead of
// silently succeeding, and a rejected call writes nothing.
func TestRejectsEmptySeatSetAndNonPositiveTTL(t *testing.T) {
	ctx := context.Background()
	coordinator, server := newTestCoordinator(t)

	cases := []struct {
		name string
		call func() error
	}{
		{"acquire without seats", func() error {
			return coordinator.Acquire(ctx, "hold-a", nil, time.Minute)
		}},
		{"acquire with an empty seat set", func() error {
			return coordinator.Acquire(ctx, "hold-a", []int64{}, time.Minute)
		}},
		{"acquire with zero ttl", func() error {
			return coordinator.Acquire(ctx, "hold-a", []int64{61}, 0)
		}},
		{"acquire with negative ttl", func() error {
			return coordinator.Acquire(ctx, "hold-a", []int64{61}, -time.Second)
		}},
		{"acquire with a sub-millisecond ttl", func() error {
			return coordinator.Acquire(ctx, "hold-a", []int64{61}, 500*time.Microsecond)
		}},
		{"acquire without a hold id", func() error {
			return coordinator.Acquire(ctx, "", []int64{61}, time.Minute)
		}},
		{"acquire with a non-positive seat id", func() error {
			return coordinator.Acquire(ctx, "hold-a", []int64{0}, time.Minute)
		}},
		{"release without seats", func() error {
			return coordinator.Release(ctx, "hold-a", nil)
		}},
		{"release without a hold id", func() error {
			return coordinator.Release(ctx, "", []int64{61})
		}},
		{"held without seats", func() error {
			_, err := coordinator.Held(ctx, nil)
			return err
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.call()
			if !errors.Is(err, reservation.ErrInvalidInput) {
				t.Fatalf("%s returned %v, want ErrInvalidInput", testCase.name, err)
			}
		})
	}

	if keys := holdKeys(server); len(keys) != 0 {
		t.Fatalf("rejected calls wrote keys: %v", keys)
	}
	if mustHeld(t, coordinator, 61)[0] {
		t.Fatalf("rejected calls left seat 61 held")
	}
}

// TestAcquireStoresTheOwningHoldID pins ownership at the value level: the stored
// value is the holding id, and a release by a different hold leaves both key and
// value untouched. Counting keys alone would not catch an owner mix-up.
func TestAcquireStoresTheOwningHoldID(t *testing.T) {
	coordinator, server := newTestCoordinator(t)
	ctx := context.Background()

	if err := coordinator.Acquire(ctx, "hold-owner", []int64{41, 42}, time.Minute); err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	for _, seat := range []int64{41, 42} {
		value, err := server.Get(holdKeyPrefix + strconv.FormatInt(seat, 10))
		if err != nil {
			t.Fatalf("reading seat %d: %v", seat, err)
		}
		if value != "hold-owner" {
			t.Errorf("seat %d stores owner %q, want hold-owner", seat, value)
		}
	}

	if err := coordinator.Release(ctx, "hold-intruder", []int64{41, 42}); err != nil {
		t.Fatalf("Release by a non-owner returned error: %v", err)
	}
	held, err := coordinator.Held(ctx, []int64{41, 42})
	if err != nil {
		t.Fatalf("Held returned error: %v", err)
	}
	if !held[0] || !held[1] {
		t.Fatalf("Held = %v, want both seats still held by the owner", held)
	}
}

// TestDuplicateSeatIDsRejected pins that a seat listed twice is caller error and
// writes nothing, instead of being silently acquired twice.
func TestDuplicateSeatIDsRejected(t *testing.T) {
	coordinator, server := newTestCoordinator(t)
	ctx := context.Background()

	err := coordinator.Acquire(ctx, "hold-dup", []int64{7, 7}, time.Minute)
	if !errors.Is(err, reservation.ErrInvalidInput) {
		t.Fatalf("Acquire error = %v, want ErrInvalidInput", err)
	}
	if keys := holdKeys(server); len(keys) != 0 {
		t.Fatalf("rejected call wrote keys: %v", keys)
	}
}
