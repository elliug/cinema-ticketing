// PostgreSQL-only reservation store tests. They need a migrated isolated
// database and are gated on M2_RECOVERY_DATABASE_URL, so the default unit suite
// needs no database at all.
//
// The file carries the committed S8 worker-recovery evidence T5 left unproven:
// a sweep killed mid-transaction leaves no partial state, and the next sweep
// completes the work from the durable rows alone. It also pins the durable claim
// classification for a reference PostgreSQL does not know, which is the only way
// to exercise a real SQLSTATE.
package reservation_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

// recoveryDatabaseURLEnv names the isolated database these tests use. The default
// unit suite skips them, and `make verify-m2` points it at a throwaway database.
const recoveryDatabaseURLEnv = "M2_RECOVERY_DATABASE_URL"

// Fixture identifiers are fixed so a failed run can be cleaned up by the next
// one. The venue fixture is inserted once and reused: a published layout is
// immutable by design, so it can never be deleted.
const (
	recoveryCinema = "00000000-0000-0000-0000-00000000c0a1"
	recoveryRoom   = "00000000-0000-0000-0000-00000000c0a2"
	recoveryLayout = "00000000-0000-0000-0000-00000000c0a3"
	recoveryRow    = "00000000-0000-0000-0000-00000000c0a4"
	recoverySeat1  = "00000000-0000-0000-0000-00000000c0a5"
	recoverySeat2  = "00000000-0000-0000-0000-00000000c0a6"
	recoveryFilm   = "00000000-0000-0000-0000-00000000c0a7"
	recoveryEvent  = "00000000-0000-0000-0000-00000000c0b1"
	recoveryHold   = "00000000-0000-0000-0000-00000000c0c1"
	recoveryKey    = "m2-recovery-key"
)

// TestExpirySweepRecoversFromAKilledWorker is the S8 worker-recovery evidence:
// one sweep is killed while its transaction is mid-flight, and the durable rows
// prove nothing was half-done before the next sweep finishes the work.
//
// The kill is deterministic rather than timing-based. A second transaction holds
// a row lock on the expired hold's seat row, so the sweep runs its prune (which
// deletes the retained idempotency record) and its candidate lock, then blocks at
// the seat release. Cancelling the sweep context there aborts the whole
// transaction, so the prune must be rolled back with everything else.
func TestExpirySweepRecoversFromAKilledWorker(t *testing.T) {
	ctx := context.Background()
	pool, url := recoveryPool(t, ctx)
	defer pool.Close()

	seedRecoveryFixture(t, ctx, pool)
	defer cleanupRecoveryFixture(ctx, t, pool)

	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open reservation store: %v", err)
	}
	defer store.Close()

	// The deadline is taken from the database clock, exactly as the process
	// wiring does: it must never be a process clock value.
	deadline, err := store.DatabaseNow(ctx)
	if err != nil {
		t.Fatalf("read the database clock: %v", err)
	}

	// Block the sweep at its third statement.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the blocking transaction: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx,
		`SELECT 1 FROM cinema.reservation_seats WHERE reservation_id = $1 FOR UPDATE`, recoveryHold); err != nil {
		t.Fatalf("lock the seat row of the expired hold: %v", err)
	}

	sweepCtx, cancelSweep := context.WithCancel(ctx)
	defer cancelSweep()
	sweepErr := make(chan error, 1)
	go func() {
		_, err := store.ExpireHolds(sweepCtx, deadline, 10)
		sweepErr <- err
	}()

	waitForBlockedSweep(t, ctx, pool)
	cancelSweep()

	select {
	case err := <-sweepErr:
		if err == nil {
			t.Fatal("the killed sweep reported success, want the cancellation to abort it")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the killed sweep never returned after its context was cancelled")
	}

	// Release the blocker and prove the killed sweep holds no lock of its own, so
	// the state below is the committed state and not a snapshot of an open
	// transaction.
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release the blocking transaction: %v", err)
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, 5*time.Second)
	defer cancelLock()
	prover, err := pool.Begin(lockCtx)
	if err != nil {
		t.Fatalf("begin the lock probe: %v", err)
	}
	if _, err := prover.Exec(lockCtx,
		`SELECT 1 FROM cinema.reservation_seats WHERE reservation_id = $1 FOR UPDATE`, recoveryHold); err != nil {
		t.Fatalf("the killed sweep still holds the seat row: %v", err)
	}
	_ = prover.Rollback(lockCtx)

	// No partial state: the hold is still held, its seat is still active, and the
	// idempotency record the killed transaction had deleted is still stored.
	if status := holdStatus(t, ctx, pool); status != "held" {
		t.Fatalf("hold status after the killed sweep = %q, want held (the transaction must roll back entirely)", status)
	}
	if released := releasedSeats(t, ctx, pool); released != 0 {
		t.Fatalf("the killed sweep released %d seat row(s), want none", released)
	}
	if records := idempotencyRecords(t, ctx, pool); records != 1 {
		t.Fatalf("the killed sweep pruned down to %d idempotency record(s), want the record still stored", records)
	}

	// Completion on the next sweep, from the durable rows alone.
	result, err := store.ExpireHolds(ctx, deadline, 10)
	if err != nil {
		t.Fatalf("the recovering sweep returned error: %v", err)
	}
	if result.ReservationsExpired != 1 || result.SeatsReleased != 1 || result.IdempotencyPruned != 1 {
		t.Fatalf("recovering sweep = %+v, want exactly 1 expired hold, 1 released seat and 1 pruned record", result)
	}
	if status := holdStatus(t, ctx, pool); status != "expired" {
		t.Fatalf("hold status after the recovering sweep = %q, want expired", status)
	}
	if released := releasedSeats(t, ctx, pool); released != 1 {
		t.Fatalf("released seat rows after the recovering sweep = %d, want 1", released)
	}
	if records := idempotencyRecords(t, ctx, pool); records != 0 {
		t.Fatalf("idempotency records after the recovering sweep = %d, want the retained record pruned", records)
	}

	// A third sweep is a full no-op, so the recovery did not duplicate work.
	again, err := store.ExpireHolds(ctx, deadline, 10)
	if err != nil {
		t.Fatalf("the third sweep returned error: %v", err)
	}
	if again != (reservation.ExpiryResult{}) {
		t.Fatalf("third sweep = %+v, want a no-op", again)
	}
}

// TestPostgresStoreClassifiesAnUnknownReference pins the durable classification
// the T4 advisory is about: a foreign key failure is caller error, and its driver
// message stays available to operators without ever being the client's detail.
func TestPostgresStoreClassifiesAnUnknownReference(t *testing.T) {
	ctx := context.Background()
	pool, url := recoveryPool(t, ctx)
	defer pool.Close()

	seedRecoveryFixture(t, ctx, pool)
	defer cleanupRecoveryFixture(ctx, t, pool)

	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open reservation store: %v", err)
	}
	defer store.Close()

	deadline, err := store.DatabaseNow(ctx)
	if err != nil {
		t.Fatalf("read the database clock: %v", err)
	}

	// A hold for an event seat that does not exist: the reservation row is valid,
	// the seat row violates its foreign key.
	hold := reservation.Hold{
		ID:        "00000000-0000-0000-0000-00000000c0d1",
		EventID:   recoveryEvent,
		SeatIDs:   []int64{9223372036854775807},
		ExpiresAt: deadline.Add(time.Minute),
		Status:    201,
	}
	err = store.RecordHold(ctx, hold, reservation.IdempotencyRecord{
		Key:           "m2-unknown-reference",
		Fingerprint:   "fp",
		Status:        201,
		HoldID:        hold.ID,
		HoldExpiresAt: hold.ExpiresAt,
		ExpiresAt:     deadline.Add(time.Hour),
	})
	if !errors.Is(err, reservation.ErrUnknownReference) {
		t.Fatalf("RecordHold error = %v, want ErrUnknownReference", err)
	}
	if !strings.Contains(err.Error(), "violates foreign key constraint") {
		t.Fatalf("error = %q, want the driver message kept for operators", err)
	}

	// The same classification covers a cast failure on a read path, so a
	// malformed event id is caller error and never a retryable outage.
	if _, err := store.ListSeatAvailability(ctx, "not-an-event-uuid"); !errors.Is(err, reservation.ErrUnknownReference) {
		t.Fatalf("ListSeatAvailability error = %v, want ErrUnknownReference", err)
	}
}

// recoveryPool opens the gated isolated database and refuses to run anywhere the
// test fixtures could collide with someone else's data.
func recoveryPool(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(recoveryDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set: these tests need a migrated isolated database (make verify-m2 sets it)", recoveryDatabaseURLEnv)
	}
	requireIsolatedDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open %s pool: %v", recoveryDatabaseURLEnv, err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Fatalf("%s is not reachable: %v", recoveryDatabaseURLEnv, err)
	}
	return pool, url
}

// requireIsolatedDatabase refuses the development database. The tests write real
// rows, so the name has to say it is disposable.
func requireIsolatedDatabase(t *testing.T, url string) {
	t.Helper()
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if !strings.HasPrefix(name, "cinema_m2") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m2, got %q", recoveryDatabaseURLEnv, name)
	}
	if name == "cinema" {
		t.Fatalf("%s must never point at the development database", recoveryDatabaseURLEnv)
	}
}

// seedRecoveryFixture inserts the venue, event, expired hold and retained
// idempotency record the recovery test needs. It is idempotent: M1 freezes a
// published layout, so its rows can never be deleted or re-inserted and the venue
// part is created once. The event and the hold are recreated on every run.
func seedRecoveryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	cleanupRecoveryFixture(ctx, t, pool)

	var venueExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, recoveryLayout).Scan(&venueExists); err != nil {
		t.Fatalf("inspect the recovery venue fixture: %v", err)
	}
	if !venueExists {
		// A layout may only be inserted as a draft and published once.
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + recoveryCinema + `','Cine Recovery','UTC')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + recoveryRoom + `','` + recoveryCinema + `','Sala Recovery')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + recoveryLayout + `','` + recoveryRoom + `',1)`},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('` + recoveryRow + `','` + recoveryLayout + `','A',1)`},
			{"seats", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
			 ('` + recoverySeat1 + `','` + recoveryRow + `','1',1,'standard',true),
			 ('` + recoverySeat2 + `','` + recoveryRow + `','2',2,'standard',true)`},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = '` + recoveryLayout + `'`},
		} {
			if _, err := pool.Exec(ctx, statement.sql); err != nil {
				t.Fatalf("seed venue %s: %v", statement.what, err)
			}
		}
	}

	statements := []struct {
		what string
		sql  string
	}{
		{"film", `INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ('` + recoveryFilm + `','Recovery Film',100,'PG')`},
		{"event", `INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
		 VALUES ('` + recoveryEvent + `','` + recoveryRoom + `','` + recoveryFilm + `','` + recoveryLayout + `',now(),'on_sale')`},
		// The hold is inserted with an open window because the claim trigger
		// rejects a seat row for an already expired hold; it is backdated below,
		// which is exactly the state the sweep exists to clean up.
		{"hold", `INSERT INTO cinema.reservations (id, event_id, status, expires_at)
		 VALUES ('` + recoveryHold + `','` + recoveryEvent + `','held', now() + interval '1 hour')`},
		{"held seat", `INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
		 SELECT '` + recoveryHold + `', event_id, id FROM cinema.event_seats
		 WHERE event_id = '` + recoveryEvent + `' ORDER BY id LIMIT 1`},
		{"record", `INSERT INTO cinema.idempotency_keys
		 (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
		 VALUES ('` + recoveryKey + `','fp',201,'` + recoveryHold + `', now() - interval '1 hour', now() - interval '1 hour')`},
		{"backdate", `UPDATE cinema.reservations SET expires_at = now() - interval '1 hour' WHERE id = '` + recoveryHold + `'`},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql); err != nil {
			t.Fatalf("seed %s: %v", statement.what, err)
		}
	}
}

// cleanupRecoveryFixture removes the rows the tests create: the hold, its record
// and its event. The cinema, room and published layout are left in place because
// M1 makes a published layout immutable. A cleanup failure is reported, never
// fatal, because cleanup also runs from a defer.
func cleanupRecoveryFixture(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, sql := range []string{
		`DELETE FROM cinema.idempotency_keys WHERE hold_id = '` + recoveryHold + `'`,
		`DELETE FROM cinema.reservation_seats WHERE reservation_id = '` + recoveryHold + `'`,
		`DELETE FROM cinema.reservations WHERE id = '` + recoveryHold + `'`,
		`DELETE FROM cinema.event_seats WHERE event_id = '` + recoveryEvent + `'`,
		`DELETE FROM cinema.events WHERE id = '` + recoveryEvent + `'`,
		`DELETE FROM cinema.films WHERE id = '` + recoveryFilm + `'`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			// Cleanup also runs before the seed, where the tables may legitimately
			// be empty of these rows; only a real failure is reported.
			if !strings.Contains(err.Error(), "does not exist") {
				t.Logf("clean the recovery fixture: %v", err)
			}
		}
	}
}

// waitForBlockedSweep waits until a backend is visibly waiting on the seat row
// lock, which is the only statement of the sweep that can block. Without this
// wait the cancellation could land before the transaction had done any work, and
// the test would prove nothing.
func waitForBlockedSweep(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var blocked int
		err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_stat_activity
WHERE datname = current_database()
  AND pid <> pg_backend_pid()
  AND wait_event_type = 'Lock'
  AND query LIKE '%reservation_seats%'`).Scan(&blocked)
		if err != nil {
			t.Fatalf("inspect the sweep from pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep never blocked on the seat row lock, so the kill could not be placed mid-transaction")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func holdStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM cinema.reservations WHERE id = $1`, recoveryHold).Scan(&status); err != nil {
		t.Fatalf("read the hold status: %v", err)
	}
	return status
}

func releasedSeats(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var released int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = $1 AND released_at IS NOT NULL`,
		recoveryHold).Scan(&released); err != nil {
		t.Fatalf("count the released seat rows: %v", err)
	}
	return released
}

func idempotencyRecords(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var records int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cinema.idempotency_keys WHERE idempotency_key = $1`, recoveryKey).Scan(&records); err != nil {
		t.Fatalf("count the idempotency records: %v", err)
	}
	return records
}
