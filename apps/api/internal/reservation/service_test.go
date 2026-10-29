package reservation_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

const testHoldTTL = 10 * time.Minute

// fakeStore is the durable layer double. Records and holds are exposed so a
// test can prove what a request wrote, or that it wrote nothing at all.
type fakeStore struct {
	records     map[string]reservation.IdempotencyRecord
	holds       map[string]reservation.Hold
	seats       []reservation.SeatAvailability
	seatEventID string

	lookupErr error
	recordErr error
	seatErr   error

	recordCalls  int
	lookupMisses int

	// Expiry sweep double: the canned result or failure the durable layer
	// reports, and the arguments it was asked to sweep with.
	expireResult reservation.ExpiryResult
	expireErr    error
	expireCalls  int
	expireNow    time.Time
	expireBatch  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		records: map[string]reservation.IdempotencyRecord{},
		holds:   map[string]reservation.Hold{},
	}
}

func (f *fakeStore) LookupIdempotency(_ context.Context, key string) (reservation.IdempotencyRecord, bool, error) {
	if f.lookupErr != nil {
		return reservation.IdempotencyRecord{}, false, f.lookupErr
	}
	if f.lookupMisses > 0 {
		// Simulates a record that was committed by a concurrent request after
		// this request read the store.
		f.lookupMisses--
		return reservation.IdempotencyRecord{}, false, nil
	}
	record, found := f.records[key]
	return record, found, nil
}

func (f *fakeStore) RecordHold(_ context.Context, hold reservation.Hold, record reservation.IdempotencyRecord) error {
	f.recordCalls++
	if f.recordErr != nil {
		return f.recordErr
	}
	if _, exists := f.records[record.Key]; exists {
		return reservation.ErrIdempotencyKeyTaken
	}
	f.records[record.Key] = record
	f.holds[hold.ID] = hold
	return nil
}

func (f *fakeStore) ExpireHolds(_ context.Context, now time.Time, batch int) (reservation.ExpiryResult, error) {
	f.expireCalls++
	f.expireNow = now
	f.expireBatch = batch
	if f.expireErr != nil {
		return reservation.ExpiryResult{}, f.expireErr
	}
	return f.expireResult, nil
}

func (f *fakeStore) ListSeatAvailability(_ context.Context, eventID string) ([]reservation.SeatAvailability, error) {
	f.seatEventID = eventID
	if f.seatErr != nil {
		return nil, f.seatErr
	}
	return f.seats, nil
}

// failingLocker is a coordination layer that cannot be reached.
type failingLocker struct{ err error }

func (f failingLocker) Acquire(context.Context, string, []int64, time.Duration) error { return f.err }
func (f failingLocker) Release(context.Context, string, []int64) error                { return nil }

func newTestService(t *testing.T, store reservation.Store) (*reservation.Service, *reservation.Coordinator, *miniredis.Miniredis) {
	t.Helper()
	coordinator, server := newTestCoordinator(t)
	return reservation.NewService(coordinator, store, testHoldTTL), coordinator, server
}

func seatKey(eventSeatID int64) string {
	return holdKeyPrefix + strconv.FormatInt(eventSeatID, 10)
}

func wantSeatIDs(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hold seat ids = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("hold seat ids = %v, want %v", got, want)
		}
	}
}

// Behaviour 1: a successful hold claims the whole selection in Redis for the
// configured window and stores one durable hold plus its idempotency record.
func TestCreateHoldClaimsTheWholeSelectionDurably(t *testing.T) {
	store := newFakeStore()
	service, _, server := newTestService(t, store)

	before := time.Now()
	hold, err := service.CreateHold(context.Background(), "key-success", "evt-1", []int64{22, 21})
	if err != nil {
		t.Fatalf("CreateHold returned error: %v", err)
	}
	if hold.ID == "" {
		t.Fatalf("CreateHold returned an empty hold id")
	}
	if hold.EventID != "evt-1" {
		t.Fatalf("hold event id = %q, want evt-1", hold.EventID)
	}
	wantSeatIDs(t, hold.SeatIDs, []int64{21, 22})
	if hold.Status != 201 {
		t.Fatalf("hold status = %d, want 201", hold.Status)
	}
	if want := before.Add(testHoldTTL); hold.ExpiresAt.Before(want) || hold.ExpiresAt.After(want.Add(time.Second)) {
		t.Fatalf("hold expires at %s, want about %s (the configured HoldTTL)", hold.ExpiresAt, want)
	}

	for _, eventSeatID := range []int64{21, 22} {
		key := seatKey(eventSeatID)
		owner, err := server.Get(key)
		if err != nil {
			t.Fatalf("seat %d is not held in Redis: %v", eventSeatID, err)
		}
		if owner != hold.ID {
			t.Fatalf("seat %d is owned by %q, want hold %q", eventSeatID, owner, hold.ID)
		}
		if ttl := server.TTL(key); ttl != testHoldTTL {
			t.Fatalf("seat %d TTL = %s, want %s (the configured HoldTTL)", eventSeatID, ttl, testHoldTTL)
		}
	}

	stored, ok := store.holds[hold.ID]
	if !ok {
		t.Fatalf("no durable hold was recorded for %q", hold.ID)
	}
	if !stored.ExpiresAt.Equal(hold.ExpiresAt) {
		t.Fatalf("durable expires_at = %s, want %s (same as the Redis window)", stored.ExpiresAt, hold.ExpiresAt)
	}
	record, ok := store.records["key-success"]
	if !ok {
		t.Fatalf("no idempotency record was stored for key-success")
	}
	if record.HoldID != hold.ID || record.Status != 201 {
		t.Fatalf("idempotency record = %+v, want the stored hold id and status 201", record)
	}
	if !record.HoldExpiresAt.Equal(hold.ExpiresAt) {
		t.Fatalf("idempotency hold expiry = %s, want %s", record.HoldExpiresAt, hold.ExpiresAt)
	}
	if !record.ExpiresAt.After(hold.ExpiresAt) {
		t.Fatalf("idempotency record expires at %s, want a retention deadline past the hold window %s", record.ExpiresAt, hold.ExpiresAt)
	}
	if record.Fingerprint == "" {
		t.Fatalf("idempotency record has no payload fingerprint")
	}
}

// Behaviour 2: a seat already held by another hold is a conflict that writes
// nothing and leaves the other hold's seat untouched.
func TestCreateHoldRejectsASeatOwnedByAnotherHold(t *testing.T) {
	store := newFakeStore()
	service, coordinator, server := newTestService(t, store)
	mustAcquire(t, coordinator, "other-hold", time.Minute, 31)

	hold, err := service.CreateHold(context.Background(), "key-conflict", "evt-1", []int64{31, 32})
	if !errors.Is(err, reservation.ErrSeatsUnavailable) {
		t.Fatalf("CreateHold error = %v, want ErrSeatsUnavailable", err)
	}
	if hold.ID != "" {
		t.Fatalf("rejected hold returned id %q, want none", hold.ID)
	}
	if store.recordCalls != 0 {
		t.Fatalf("rejected hold reached the durable store %d times, want 0", store.recordCalls)
	}
	if len(store.holds) != 0 || len(store.records) != 0 {
		t.Fatalf("rejected hold persisted data: holds %v, records %v", store.holds, store.records)
	}
	if server.Exists(seatKey(32)) {
		t.Fatalf("failed multi-seat hold left seat 32 held in Redis")
	}
	if owner, err := server.Get(seatKey(31)); err != nil || owner != "other-hold" {
		t.Fatalf("seat 31 = %q (err %v), want it still owned by other-hold", owner, err)
	}
}

// Behaviour 3 and 6: every durable write failure releases the Redis seats, so
// Redis never holds a seat PostgreSQL does not know about, and each failure is
// mapped to its own error.
func TestCreateHoldReleasesSeatsWhenTheDurableWriteFails(t *testing.T) {
	cases := []struct {
		name     string
		writeErr error
		want     error
	}{
		{"seat taken by a concurrent claim", reservation.ErrSeatsUnavailable, reservation.ErrSeatsUnavailable},
		{"own hold window already expired", reservation.ErrHoldWindowExpired, reservation.ErrHoldWindowExpired},
		{"self conflicting claim", reservation.ErrDuplicateClaim, reservation.ErrDuplicateClaim},
		{"database transport failure", errors.New("write tcp 127.0.0.1:5432: connection reset by peer"), reservation.ErrUnavailable},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newFakeStore()
			store.recordErr = testCase.writeErr
			service, _, server := newTestService(t, store)

			if _, err := service.CreateHold(context.Background(), "key-db", "evt-1", []int64{41, 42}); !errors.Is(err, testCase.want) {
				t.Fatalf("CreateHold error = %v, want %v", err, testCase.want)
			}
			for _, eventSeatID := range []int64{41, 42} {
				if server.Exists(seatKey(eventSeatID)) {
					t.Fatalf("seat %d stayed held in Redis after the durable write failed", eventSeatID)
				}
			}
			if store.recordCalls != 1 {
				t.Fatalf("durable store called %d times, want exactly 1", store.recordCalls)
			}
			if len(store.records) != 0 {
				t.Fatalf("failed hold left an idempotency record: %v", store.records)
			}
		})
	}
}

// Behaviour 4: a replayed key answers with the stored hold and never extends
// the window, in either layer.
func TestCreateHoldReplayReturnsTheStoredHoldWithoutExtendingTheWindow(t *testing.T) {
	store := newFakeStore()
	service, _, server := newTestService(t, store)

	first, err := service.CreateHold(context.Background(), "key-replay", "evt-1", []int64{51, 52})
	if err != nil {
		t.Fatalf("CreateHold returned error: %v", err)
	}
	server.FastForward(time.Minute)
	ttlBefore := server.TTL(seatKey(51))

	// The same payload in another order is the same request.
	replay, err := service.CreateHold(context.Background(), "key-replay", "evt-1", []int64{52, 51})
	if err != nil {
		t.Fatalf("replayed CreateHold returned error: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay returned hold id %q, want the stored %q", replay.ID, first.ID)
	}
	if replay.Status != first.Status || replay.Status != 201 {
		t.Fatalf("replay status = %d, want the stored %d", replay.Status, first.Status)
	}
	if !replay.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("replay expires at %s, want the stored %s (a replay must not extend the hold)", replay.ExpiresAt, first.ExpiresAt)
	}
	wantSeatIDs(t, replay.SeatIDs, []int64{51, 52})
	if store.recordCalls != 1 {
		t.Fatalf("replay reached the durable store %d times, want 1", store.recordCalls)
	}
	if ttlAfter := server.TTL(seatKey(51)); ttlAfter != ttlBefore {
		t.Fatalf("replay changed the Redis window of seat 51 from %s to %s", ttlBefore, ttlAfter)
	}
}

// Behaviour 5: reusing a key for a different payload is a conflict, not a
// second hold.
func TestCreateHoldRejectsAnIdempotencyKeyReusedForADifferentSelection(t *testing.T) {
	store := newFakeStore()
	service, _, server := newTestService(t, store)

	first, err := service.CreateHold(context.Background(), "key-reuse", "evt-1", []int64{61})
	if err != nil {
		t.Fatalf("CreateHold returned error: %v", err)
	}

	cases := []struct {
		name         string
		eventID      string
		eventSeatIDs []int64
	}{
		{"different event", "evt-2", []int64{61}},
		{"different seat set", "evt-1", []int64{61, 62}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := service.CreateHold(context.Background(), "key-reuse", testCase.eventID, testCase.eventSeatIDs); !errors.Is(err, reservation.ErrIdempotencyKeyReused) {
				t.Fatalf("CreateHold error = %v, want ErrIdempotencyKeyReused", err)
			}
			if store.recordCalls != 1 {
				t.Fatalf("conflicting reuse reached the durable store: %d calls, want 1", store.recordCalls)
			}
			if len(store.holds) != 1 {
				t.Fatalf("conflicting reuse created %d holds, want 1", len(store.holds))
			}
			if server.Exists(seatKey(62)) {
				t.Fatalf("conflicting reuse claimed seat 62 in Redis")
			}
		})
	}

	if stored := store.holds[first.ID]; !stored.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("conflicting reuse changed the original hold expiry from %s to %s", first.ExpiresAt, stored.ExpiresAt)
	}
}

// Behaviour 4, race case: when a concurrent request commits the same key first,
// the store reports the taken key, the seats are released, and the stored
// outcome answers the request.
func TestCreateHoldTreatsAConcurrentSameKeyCommitAsAReplay(t *testing.T) {
	store := newFakeStore()
	service, _, server := newTestService(t, store)

	first, err := service.CreateHold(context.Background(), "key-race", "evt-1", []int64{71})
	if err != nil {
		t.Fatalf("CreateHold returned error: %v", err)
	}
	// The first hold's Redis window elapsed while its durable rows live on.
	server.FastForward(testHoldTTL + time.Minute)

	t.Run("same payload replays the stored hold", func(t *testing.T) {
		store.lookupMisses = 1
		store.recordErr = reservation.ErrIdempotencyKeyTaken

		replay, err := service.CreateHold(context.Background(), "key-race", "evt-1", []int64{71})
		if err != nil {
			t.Fatalf("CreateHold returned error: %v", err)
		}
		if replay.ID != first.ID {
			t.Fatalf("replay returned hold id %q, want the stored %q", replay.ID, first.ID)
		}
		if server.Exists(seatKey(71)) {
			t.Fatalf("the racing claim left seat 71 held in Redis after losing the key")
		}
	})

	t.Run("different payload is a conflict", func(t *testing.T) {
		store.lookupMisses = 1
		store.recordErr = reservation.ErrIdempotencyKeyTaken

		if _, err := service.CreateHold(context.Background(), "key-race", "evt-1", []int64{72}); !errors.Is(err, reservation.ErrIdempotencyKeyReused) {
			t.Fatalf("CreateHold error = %v, want ErrIdempotencyKeyReused", err)
		}
		if server.Exists(seatKey(72)) {
			t.Fatalf("the losing claim left seat 72 held in Redis")
		}
		if len(store.holds) != 1 {
			t.Fatalf("the losing claim created a second hold: %v", store.holds)
		}
	})
}

// Behaviour 7: caller input is rejected before Redis or PostgreSQL is touched.
func TestCreateHoldRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		eventID string
		seats   []int64
	}{
		{"missing idempotency key", "", "evt-1", []int64{81}},
		{"blank idempotency key", "   ", "evt-1", []int64{81}},
		{"missing event id", "key-invalid", "", []int64{81}},
		{"no seats", "key-invalid", "evt-1", nil},
		{"empty seat set", "key-invalid", "evt-1", []int64{}},
		{"non-positive seat", "key-invalid", "evt-1", []int64{0}},
		{"duplicate seat", "key-invalid", "evt-1", []int64{81, 81}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newFakeStore()
			service, _, server := newTestService(t, store)

			if _, err := service.CreateHold(context.Background(), testCase.key, testCase.eventID, testCase.seats); !errors.Is(err, reservation.ErrInvalidInput) {
				t.Fatalf("CreateHold error = %v, want ErrInvalidInput", err)
			}
			if store.recordCalls != 0 {
				t.Fatalf("rejected input reached the durable store %d times", store.recordCalls)
			}
			if keys := holdKeys(server); len(keys) != 0 {
				t.Fatalf("rejected input wrote Redis keys: %v", keys)
			}
		})
	}
}

// Behaviour 8: an unreachable coordination layer is a retryable failure, never
// a success and never a durable hold.
func TestCreateHoldFailsClosedWhenCoordinationIsUnavailable(t *testing.T) {
	store := newFakeStore()
	service := reservation.NewService(
		failingLocker{err: errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")},
		store,
		testHoldTTL,
	)

	if _, err := service.CreateHold(context.Background(), "key-503", "evt-1", []int64{91}); !errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("CreateHold error = %v, want ErrUnavailable", err)
	}
	if store.recordCalls != 0 {
		t.Fatalf("unreachable coordination layer still wrote %d durable rows", store.recordCalls)
	}
}

// Behaviour 9: a durable read failure while replaying is retryable, not a
// silent new hold.
func TestCreateHoldFailsClosedWhenTheIdempotencyLookupFails(t *testing.T) {
	store := newFakeStore()
	store.lookupErr = errors.New("read tcp 127.0.0.1:5432: connection reset by peer")
	service, _, server := newTestService(t, store)

	if _, err := service.CreateHold(context.Background(), "key-503", "evt-1", []int64{95}); !errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("CreateHold error = %v, want ErrUnavailable", err)
	}
	if keys := holdKeys(server); len(keys) != 0 {
		t.Fatalf("failed lookup claimed Redis keys: %v", keys)
	}
}

// Behaviour 10: availability is read for the requested event and its errors are
// mapped, so the read path can never report availability it did not observe.
// The held/not-held predicate over an expired hold is pinned by
// tests/migrations/m2_idempotency.sql, which runs the same query.
func TestListAvailabilityReportsSeatsAndMapsErrors(t *testing.T) {
	store := newFakeStore()
	store.seats = []reservation.SeatAvailability{
		{ID: 1, LayoutSeatID: "seat-a", State: "available", Held: true},
		{ID: 2, LayoutSeatID: "seat-b", State: "available", Held: false},
	}
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)

	seats, err := service.ListAvailability(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("ListAvailability returned error: %v", err)
	}
	if store.seatEventID != "evt-1" {
		t.Fatalf("availability read for %q, want evt-1", store.seatEventID)
	}
	if len(seats) != 2 || !seats[0].Held || seats[1].Held {
		t.Fatalf("ListAvailability = %+v, want the held flag of each seat", seats)
	}

	if _, err := service.ListAvailability(context.Background(), "  "); !errors.Is(err, reservation.ErrInvalidInput) {
		t.Fatalf("ListAvailability error = %v, want ErrInvalidInput", err)
	}

	store.seatErr = reservation.ErrEventNotFound
	if _, err := service.ListAvailability(context.Background(), "evt-missing"); !errors.Is(err, reservation.ErrEventNotFound) {
		t.Fatalf("ListAvailability error = %v, want ErrEventNotFound", err)
	}

	store.seatErr = errors.New("read tcp 127.0.0.1:5432: connection reset by peer")
	if _, err := service.ListAvailability(context.Background(), "evt-1"); !errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("ListAvailability error = %v, want ErrUnavailable", err)
	}
}

// The service refuses a window shorter than Redis can represent before any I/O,
// so a misconfigured hold window can never create a durable hold.
func TestCreateHoldRejectsWindowBelowOneMillisecond(t *testing.T) {
	store := &fakeStore{}
	locker := &countingLocker{}
	service := reservation.NewService(locker, store, 500*time.Microsecond)

	_, err := service.CreateHold(context.Background(), "idem-short-ttl", "evt", []int64{1})
	if !errors.Is(err, reservation.ErrInvalidInput) {
		t.Fatalf("CreateHold error = %v, want ErrInvalidInput", err)
	}
	if locker.calls != 0 || store.recordCalls != 0 {
		t.Fatalf("rejected call reached I/O: locker=%d recordCalls=%d", locker.calls, store.recordCalls)
	}
}

// countingLocker records whether the coordination layer was touched at all.
type countingLocker struct{ calls int }

func (c *countingLocker) Acquire(context.Context, string, []int64, time.Duration) error {
	c.calls++
	return nil
}

func (c *countingLocker) Release(context.Context, string, []int64) error { return nil }

func (c *countingLocker) Held(context.Context, []int64) ([]bool, error) { return nil, nil }
