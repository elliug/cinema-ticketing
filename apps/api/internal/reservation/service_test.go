package reservation_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
	lookupCalls  int
	lookupMisses int

	// Expiry sweep double: the canned result or failure the durable layer
	// reports, and the arguments it was asked to sweep with.
	expireResult reservation.ExpiryResult
	expireErr    error
	expireFunc   func(ctx context.Context) (reservation.ExpiryResult, error)
	expireCalls  int
	expireNow    time.Time
	expireBatch  int

	// Cancel double: the state the durable layer reports for one reservation and
	// the seats it says this request released.
	cancelResult reservation.Cancellation
	cancelErr    error
	cancelCalls  int
	cancelID     string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		records: map[string]reservation.IdempotencyRecord{},
		holds:   map[string]reservation.Hold{},
	}
}

func (f *fakeStore) LookupIdempotency(_ context.Context, key string) (reservation.IdempotencyRecord, bool, error) {
	f.lookupCalls++
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

func (f *fakeStore) ExpireHolds(ctx context.Context, now time.Time, batch int) (reservation.ExpiryResult, error) {
	f.expireCalls++
	f.expireNow = now
	f.expireBatch = batch
	if f.expireFunc != nil {
		return f.expireFunc(ctx)
	}
	if f.expireErr != nil {
		return reservation.ExpiryResult{}, f.expireErr
	}
	return f.expireResult, nil
}

func (f *fakeStore) Cancel(_ context.Context, reservationID string) (reservation.Cancellation, error) {
	f.cancelCalls++
	f.cancelID = reservationID
	if f.cancelErr != nil {
		return reservation.Cancellation{}, f.cancelErr
	}
	result := f.cancelResult
	if result.ReservationID == "" {
		result.ReservationID = reservationID
	}
	return result, nil
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

func (f failingLocker) Acquire(context.Context, string, string, []int64, time.Duration) error {
	return f.err
}
func (f failingLocker) Release(context.Context, string, string, []int64) error { return nil }

func newTestService(t *testing.T, store reservation.Store) (*reservation.Service, *reservation.Coordinator, *miniredis.Miniredis) {
	t.Helper()
	coordinator, server := newTestCoordinator(t)
	return reservation.NewService(coordinator, store, testHoldTTL), coordinator, server
}

// seatKey is the coordination key of one seat of the screening every CreateHold
// test below uses.
func seatKey(eventSeatID int64) string {
	return seatKeyOf("evt-1", eventSeatID)
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
	mustAcquire(t, coordinator, "evt-1", "other-hold", time.Minute, 31)

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

// Behaviour 11 (S9): the availability read carries the layout projection the
// browser needs. The service must not reshape what the durable store read, so a
// projected row label, seat number and seat kind reach the transport intact.
func TestListAvailabilityCarriesTheLayoutProjection(t *testing.T) {
	store := newFakeStore()
	store.seats = []reservation.SeatAvailability{
		{ID: 1, LayoutSeatID: "seat-a1", State: "available", Held: false, RowLabel: "A", SeatNumber: "1", Kind: "standard"},
		{ID: 2, LayoutSeatID: "seat-a2", State: "available", Held: true, RowLabel: "A", SeatNumber: "2", Kind: "accessible"},
		{ID: 3, LayoutSeatID: "seat-d1", State: "available", Held: false, RowLabel: "D", SeatNumber: "1", Kind: "companion"},
	}
	service := reservation.NewService(failingLocker{}, store, testHoldTTL)

	seats, err := service.ListAvailability(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("ListAvailability returned error: %v", err)
	}
	if len(seats) != len(store.seats) {
		t.Fatalf("ListAvailability returned %d seats, want %d", len(seats), len(store.seats))
	}
	for index, want := range store.seats {
		if seats[index] != want {
			t.Fatalf("seat %d = %+v, want %+v (the projection must reach the transport intact)", index, seats[index], want)
		}
	}
}

// Behaviour 11 (S9), query shape without a database: the availability statement
// is the one place that decides what the seat map can render, so its joins and
// its ordering are pinned here as text. The statement is executed for real by
// TestListSeatAvailabilityProjectsTheSeededLayout against PostgreSQL.
func TestSeatAvailabilityQueryProjectsAndOrdersTheLayout(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("read service.go: %v", err)
	}
	query := queryConstant(string(source), "listSeatAvailabilityQuery")
	if query == "" {
		t.Fatalf("listSeatAvailabilityQuery is not declared in service.go")
	}

	for _, want := range []struct {
		what string
		sql  string
	}{
		{"the row label", "lsr.label"},
		{"the seat number", "ls.number"},
		{"the seat kind", "ls.kind"},
		{"the layout seat join", "JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id"},
		{"the layout row join", "JOIN cinema.layout_rows lsr ON lsr.id = ls.layout_row_id"},
		// The L5 predicate is load-bearing: expiry is lazy, so a stored active row
		// whose window elapsed is not a live hold and must not be reported as one.
		{"the unchanged occupancy predicate", "released_at IS NULL"},
		{"the unchanged deadline predicate", "r.expires_at > now()"},
		// Deterministic ordering: rows by their position, seats by their own.
		{"row-position ordering", "ORDER BY lsr.position, ls.position"},
	} {
		if !strings.Contains(query, want.sql) {
			t.Errorf("availability query does not project %s (%q):\n%s", want.what, want.sql, query)
		}
	}
}

// queryConstant extracts the raw string assigned to one constant, so a shape test
// only ever reads the statement it names.
func queryConstant(source, name string) string {
	start := strings.Index(source, name+" = `")
	if start < 0 {
		return ""
	}
	body := source[start+len(name)+len(" = `"):]
	end := strings.Index(body, "`")
	if end < 0 {
		return ""
	}
	return body[:end]
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

func (c *countingLocker) Acquire(context.Context, string, string, []int64, time.Duration) error {
	c.calls++
	return nil
}

func (c *countingLocker) Release(context.Context, string, string, []int64) error { return nil }

func (c *countingLocker) Held(context.Context, string, []int64) ([]bool, error) { return nil, nil }

// Advisory (T4 review): an unreasonable seat count is rejected before any I/O,
// so a caller cannot make the service build an unbounded Redis key list or an
// unbounded durable transaction.
func TestCreateHoldRejectsAnUnreasonableSeatCountBeforeAnyIO(t *testing.T) {
	store := newFakeStore()
	locker := &countingLocker{}
	service := reservation.NewService(locker, store, testHoldTTL)

	seats := make([]int64, 0, 21)
	for seat := int64(1); seat <= 21; seat++ {
		seats = append(seats, seat)
	}

	_, err := service.CreateHold(context.Background(), "key-too-many", "evt-1", seats)
	if !errors.Is(err, reservation.ErrInvalidInput) {
		t.Fatalf("CreateHold error = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "seats") {
		t.Fatalf("error = %q, want it to name the seat limit", err)
	}
	if locker.calls != 0 || store.recordCalls != 0 || store.lookupCalls != 0 {
		t.Fatalf("rejected seat count reached I/O: locker=%d lookups=%d durable writes=%d",
			locker.calls, store.lookupCalls, store.recordCalls)
	}
}

// Advisory (T4 review): a reference the durable store does not know is caller
// error, but it is reported under its own sentinel because the PostgreSQL
// message behind it names tables and constraint values and must never be echoed
// to a client as the request detail.
func TestMapDurableErrorKeepsAnUnknownReferenceDistinct(t *testing.T) {
	// The reference error a claim carries up from the store also carries the
	// driver message, which the transport must not echo.
	reference := fmt.Errorf("%w: %s", reservation.ErrUnknownReference,
		`insert or update on table "reservation_seats" violates foreign key constraint "reservation_seats_event_id_fkey"`)

	store := newFakeStore()
	store.recordErr = reference
	service, _, server := newTestService(t, store)

	_, err := service.CreateHold(context.Background(), "key-unknown-reference", "evt-1", []int64{41})
	if !errors.Is(err, reservation.ErrUnknownReference) {
		t.Fatalf("CreateHold error = %v, want ErrUnknownReference", err)
	}
	if errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("CreateHold error = %v, want caller error rather than a retryable failure", err)
	}
	if server.Exists(seatKey(41)) {
		t.Fatal("the rejected claim left seat 41 held in Redis")
	}
}

// The limit is a boundary, not an approximation: exactly twenty seats is a valid
// hold and must reach the coordination layer once.
func TestCreateHoldAcceptsTheSeatLimitBoundary(t *testing.T) {
	store := newFakeStore()
	locker := &countingLocker{}
	service := reservation.NewService(locker, store, testHoldTTL)

	seats := make([]int64, 0, 20)
	for seat := int64(1); seat <= 20; seat++ {
		seats = append(seats, seat)
	}

	if _, err := service.CreateHold(context.Background(), "key-at-limit", "evt-1", seats); err != nil {
		t.Fatalf("CreateHold at the seat limit returned error: %v", err)
	}
	if locker.calls != 1 {
		t.Fatalf("coordination calls = %d, want exactly 1 acquire", locker.calls)
	}
}
