// PostgreSQL-only cancel tests: the durable half of
// POST /api/v1/reservations/{reservationId}/cancel (S10).
//
// They need a migrated isolated database and are gated on M3_CHECKOUT_DATABASE_URL, the
// same throwaway M3 database the venue suite uses, because the paid fixture needs the
// status and the captured attempt that arrived with 0005_checkout. The default unit suite
// skips them, and every test refuses a database whose name does not say it is disposable.
//
// What only PostgreSQL can prove is here: that the cancel transition is one guarded
// statement whose winner is decided by the row lock, that the seat rows are released (so
// the partial unique index stops guarding them and the seats are selectable again), that
// a paid order is refused with its seats untouched, that a hold whose window closed is
// answered as expired without writing anything, and that of two racing cancels exactly one
// releases.
package reservation_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

// cancelDatabaseURLEnv names the isolated database these tests use.
const cancelDatabaseURLEnv = "M3_CHECKOUT_DATABASE_URL"

// Fixture identifiers are fixed so a failed run can be cleaned up by the next one. The
// venue is created once and reused: M1 makes a published layout immutable, so it can
// never be deleted.
const (
	cancelCinema  = "00000000-0000-0000-0000-00000000e0a1"
	cancelRoom    = "00000000-0000-0000-0000-00000000e0a2"
	cancelLayout  = "00000000-0000-0000-0000-00000000e0a3"
	cancelRow     = "00000000-0000-0000-0000-00000000e0a4"
	cancelSeat1   = "00000000-0000-0000-0000-00000000e0a5"
	cancelSeat2   = "00000000-0000-0000-0000-00000000e0a6"
	cancelSeat3   = "00000000-0000-0000-0000-00000000e0a7"
	cancelSeat4   = "00000000-0000-0000-0000-00000000e0a8"
	cancelFilm    = "00000000-0000-0000-0000-00000000e0b1"
	cancelEvent   = "00000000-0000-0000-0000-00000000e0b2"
	cancelHold    = "00000000-0000-0000-0000-00000000e0c1"
	cancelPaid    = "00000000-0000-0000-0000-00000000e0c2"
	cancelLapsed  = "00000000-0000-0000-0000-00000000e0c3"
	cancelAbsent  = "00000000-0000-0000-0000-00000000e0cf"
	cancelAttempt = "00000000-0000-0000-0000-00000000e0d1"
	cancelTicket  = "00000000-0000-0000-0000-00000000e0d2"
	// cancelReclaim is the second hold the tests try to place on seats a cancel released.
	cancelReclaim = "00000000-0000-0000-0000-00000000e0c4"
)

// TestCancelReleasesTheSeatsSoTheyCanBeHeldAgain is the first required behaviour: a
// valid, unpaid hold becomes cancelled and its seats are selectable again, which the
// partial unique index proves only if a new hold can claim them.
func TestCancelReleasesTheSeatsSoTheyCanBeHeldAgain(t *testing.T) {
	ctx := context.Background()
	pool, url := cancelPool(t, ctx)
	defer pool.Close()

	seedCancelFixture(t, ctx, pool)
	defer cleanupCancelFixture(ctx, t, pool)

	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open reservation store: %v", err)
	}
	defer store.Close()

	cancellation, err := store.Cancel(ctx, cancelHold)
	if err != nil {
		t.Fatalf("Cancel(live hold) = unexpected error %v", err)
	}
	if cancellation.Status != "cancelled" {
		t.Fatalf("status = %q, want the stored cancelled state", cancellation.Status)
	}
	if len(cancellation.SeatIDs) != 2 {
		t.Fatalf("released seats = %v, want the hold's two seats", cancellation.SeatIDs)
	}

	if status := cancelStatus(t, ctx, pool, cancelHold); status != "cancelled" {
		t.Fatalf("stored status = %q, want cancelled", status)
	}
	if live := activeCancelSeats(t, ctx, pool, cancelHold); live != 0 {
		t.Fatalf("%d seat row(s) of the cancelled hold are still active, want none", live)
	}

	// The seats are selectable again: a new hold claims them, which the partial unique
	// index would refuse while the cancelled hold still owned them.
	claimCancelSeats(t, ctx, pool, cancelReclaim, cancelHold)
	if live := activeCancelSeats(t, ctx, pool, cancelReclaim); live != 2 {
		t.Fatalf("the re-claimed hold owns %d seat row(s), want 2", live)
	}
}

// TestCancelOfAPaidOrderIsRefusedAndReleasesNothing is the third required behaviour: a
// paid order is a conflict, told apart from every other failure, and the seats of the sale
// stay exactly as the capture left them.
func TestCancelOfAPaidOrderIsRefusedAndReleasesNothing(t *testing.T) {
	ctx := context.Background()
	pool, url := cancelPool(t, ctx)
	defer pool.Close()

	seedCancelFixture(t, ctx, pool)
	defer cleanupCancelFixture(ctx, t, pool)

	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open reservation store: %v", err)
	}
	defer store.Close()

	_, err = store.Cancel(ctx, cancelPaid)
	if !errors.Is(err, reservation.ErrAlreadyPaid) {
		t.Fatalf("Cancel(paid order) = %v, want ErrAlreadyPaid", err)
	}
	if errors.Is(err, reservation.ErrUnavailable) {
		t.Fatalf("Cancel(paid order) = %v, want the paid refusal kept apart from a retryable failure", err)
	}
	if status := cancelStatus(t, ctx, pool, cancelPaid); status != "paid" {
		t.Fatalf("stored status = %q, want the paid state untouched", status)
	}
	if live := activeCancelSeats(t, ctx, pool, cancelPaid); live != 1 {
		t.Fatalf("the paid order owns %d active seat row(s), want its sold seat untouched", live)
	}
}

// TestCancelOfALapsedHoldAnswersExpiredAndWritesNothing is the fourth required behaviour:
// a hold whose window closed but that the expiry sweep has not rewritten yet is reported
// as what it is, and the cancel writes nothing — the expiry transition, with the seat-row
// release that belongs to it, stays the sweep's.
func TestCancelOfALapsedHoldAnswersExpiredAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	pool, url := cancelPool(t, ctx)
	defer pool.Close()

	seedCancelFixture(t, ctx, pool)
	defer cleanupCancelFixture(ctx, t, pool)

	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open reservation store: %v", err)
	}
	defer store.Close()

	cancellation, err := store.Cancel(ctx, cancelLapsed)
	if err != nil {
		t.Fatalf("Cancel(lapsed hold) = unexpected error %v", err)
	}
	if cancellation.Status != "expired" {
		t.Fatalf("status = %q, want expired", cancellation.Status)
	}
	if len(cancellation.SeatIDs) != 0 {
		t.Fatalf("released seats = %v, want none: an expired hold releases nothing", cancellation.SeatIDs)
	}
	if status := cancelStatus(t, ctx, pool, cancelLapsed); status != "held" {
		t.Fatalf("stored status = %q, want the sweep's transition left alone", status)
	}
	if live := activeCancelSeats(t, ctx, pool, cancelLapsed); live != 1 {
		t.Fatalf("a lapsed hold left %d active seat row(s), want its row untouched by the cancel", live)
	}
}

// TestCancelOfAnUnknownReservationIsNotFound is the fifth required behaviour's durable
// half: an identifier that names no reservation is not found, never an invented success.
func TestCancelOfAnUnknownReservationIsNotFound(t *testing.T) {
	ctx := context.Background()
	pool, url := cancelPool(t, ctx)
	defer pool.Close()

	seedCancelFixture(t, ctx, pool)
	defer cleanupCancelFixture(ctx, t, pool)

	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open reservation store: %v", err)
	}
	defer store.Close()

	if _, err := store.Cancel(ctx, cancelAbsent); !errors.Is(err, reservation.ErrReservationNotFound) {
		t.Fatalf("Cancel(unknown) = %v, want ErrReservationNotFound", err)
	}
}

// TestTwoRacingCancelsReleaseOnce is the sixth required behaviour: of two concurrent
// cancels of the same hold, exactly one wins and releases the seats; the other answers the
// stored state and releases nothing. The property asserted does not depend on which one
// wins, and each released seat is reported by exactly one of the two answers.
func TestTwoRacingCancelsReleaseOnce(t *testing.T) {
	ctx := context.Background()
	pool, url := cancelPool(t, ctx)
	defer pool.Close()

	seedCancelFixture(t, ctx, pool)
	defer cleanupCancelFixture(ctx, t, pool)

	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open reservation store: %v", err)
	}
	defer store.Close()

	results := make([]reservation.Cancellation, 2)
	errs := make([]error, 2)
	var start, done sync.WaitGroup
	start.Add(1)
	for index := range results {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			results[index], errs[index] = store.Cancel(ctx, cancelHold)
		}(index)
	}
	start.Done()
	done.Wait()

	released := map[int64]int{}
	for index, result := range results {
		if errs[index] != nil {
			t.Fatalf("cancel %d = unexpected error %v", index, errs[index])
		}
		if result.Status != "cancelled" {
			t.Fatalf("cancel %d status = %q, want cancelled", index, result.Status)
		}
		for _, seat := range result.SeatIDs {
			released[seat]++
		}
	}
	if len(released) != 2 {
		t.Fatalf("seats released across both cancels = %v, want the hold's two seats", released)
	}
	for seat, count := range released {
		if count != 1 {
			t.Fatalf("seat %d was released by %d requests, want exactly one", seat, count)
		}
	}
	if status := cancelStatus(t, ctx, pool, cancelHold); status != "cancelled" {
		t.Fatalf("stored status = %q, want cancelled", status)
	}
	if live := activeCancelSeats(t, ctx, pool, cancelHold); live != 0 {
		t.Fatalf("%d seat row(s) of the cancelled hold are still active, want none", live)
	}
}

// cancelPool opens the isolated database and refuses anything that does not say it is
// disposable. The development database is named cinema, which fails the prefix rule.
func cancelPool(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(cancelDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set: these tests need a migrated isolated database (make verify-m3 sets it)", cancelDatabaseURLEnv)
	}
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if !strings.HasPrefix(name, "cinema_m3") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m3, got %q", cancelDatabaseURLEnv, name)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open %s pool: %v", cancelDatabaseURLEnv, err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Fatalf("%s is not reachable: %v", cancelDatabaseURLEnv, err)
	}
	return pool, url
}

// seedCancelFixture inserts the venue, the event, and the three orders the tests need: a
// live two-seat hold, a paid one-seat order with its captured attempt and ticket, and a
// one-seat hold whose window already closed. It is idempotent; the venue part is created
// only once because a published layout is immutable.
func seedCancelFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	cleanupCancelFixture(ctx, t, pool)

	var venueExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, cancelLayout).Scan(&venueExists); err != nil {
		t.Fatalf("inspect the cancel venue fixture: %v", err)
	}
	if !venueExists {
		// A layout may only be inserted as a draft and published once.
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + cancelCinema + `','Cine Cancel','UTC')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + cancelRoom + `','` + cancelCinema + `','Sala Cancel')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + cancelLayout + `','` + cancelRoom + `',1)`},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('` + cancelRow + `','` + cancelLayout + `','A',1)`},
			{"seats", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
			 ('` + cancelSeat1 + `','` + cancelRow + `','1',1,'standard',true),
			 ('` + cancelSeat2 + `','` + cancelRow + `','2',2,'standard',true),
			 ('` + cancelSeat3 + `','` + cancelRow + `','3',3,'standard',true),
			 ('` + cancelSeat4 + `','` + cancelRow + `','4',4,'standard',true)`},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = '` + cancelLayout + `'`},
		} {
			if _, err := pool.Exec(ctx, statement.sql); err != nil {
				t.Fatalf("seed venue %s: %v", statement.what, err)
			}
		}
	}

	for _, statement := range []struct {
		what string
		sql  string
	}{
		{"film", `INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ('` + cancelFilm + `','Cancel Film',100,'PG')`},
		{"event", `INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status, price_cents)
		 VALUES ('` + cancelEvent + `','` + cancelRoom + `','` + cancelFilm + `','` + cancelLayout + `',now(),'on_sale',1250)`},
		// The two holds are inserted with open windows because the claim trigger rejects a
		// seat row for a hold whose window already closed; the lapsed one is backdated
		// below, which is exactly the state the sweep exists to clean up.
		{"live hold", `INSERT INTO cinema.reservations (id, event_id, status, expires_at)
		 VALUES ('` + cancelHold + `','` + cancelEvent + `','held', now() + interval '1 hour')`},
		{"live hold seats", `INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
		 SELECT '` + cancelHold + `', event_id, id FROM cinema.event_seats
		 WHERE event_id = '` + cancelEvent + `' ORDER BY id LIMIT 2`},
		{"lapsed hold", `INSERT INTO cinema.reservations (id, event_id, status, expires_at)
		 VALUES ('` + cancelLapsed + `','` + cancelEvent + `','held', now() + interval '1 hour')`},
		{"lapsed hold seat", `INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
		 SELECT '` + cancelLapsed + `', event_id, id FROM cinema.event_seats
		 WHERE event_id = '` + cancelEvent + `' ORDER BY id OFFSET 3 LIMIT 1`},
		{"lapsed", `UPDATE cinema.reservations SET expires_at = now() - interval '1 hour' WHERE id = '` + cancelLapsed + `'`},
		// The paid order is written the way the capture writes it: the reservation paid, the
		// seat sold, the attempt captured and the ticket issued, all in one shape. Its window
		// is opened first, because the claim trigger refuses a seat row for a hold that
		// already elapsed, and closed at the end, because a paid order keeps the deadline it
		// was held with.
		{"paid reservation", `INSERT INTO cinema.reservations (id, event_id, status, expires_at)
		 VALUES ('` + cancelPaid + `','` + cancelEvent + `','paid', now() + interval '1 hour')`},
		{"paid seat", `INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
		 SELECT '` + cancelPaid + `', event_id, id FROM cinema.event_seats
		 WHERE event_id = '` + cancelEvent + `' ORDER BY id OFFSET 2 LIMIT 1`},
		{"sold seat", `UPDATE cinema.event_seats SET state = 'sold'
		 WHERE event_id = '` + cancelEvent + `'
		   AND id IN (SELECT event_seat_id FROM cinema.reservation_seats WHERE reservation_id = '` + cancelPaid + `')`},
		{"captured attempt", `INSERT INTO cinema.payment_attempts
		 (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
		 VALUES ('` + cancelAttempt + `','` + cancelPaid + `','simulated','payment_attempt:` + cancelAttempt + `',1250,'approved','captured','cancel-paid-key','fp')`},
		{"ticket", `INSERT INTO cinema.tickets (id, reservation_id, event_id, event_seat_id)
		 SELECT '` + cancelTicket + `', '` + cancelPaid + `', event_id, event_seat_id
		 FROM cinema.reservation_seats WHERE reservation_id = '` + cancelPaid + `'`},
		{"paid deadline", `UPDATE cinema.reservations SET expires_at = now() - interval '1 hour' WHERE id = '` + cancelPaid + `'`},
	} {
		if _, err := pool.Exec(ctx, statement.sql); err != nil {
			t.Fatalf("seed %s: %v", statement.what, err)
		}
	}
}

// claimCancelSeats places a new hold on the seats of the given reservation, which only
// succeeds once those seats are free.
func claimCancelSeats(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holdID, releasedHoldID string) {
	t.Helper()
	statements := []struct {
		what string
		sql  string
		args []any
	}{
		{"hold", `INSERT INTO cinema.reservations (id, event_id, status, expires_at)
		 VALUES ($1, '` + cancelEvent + `', 'held', now() + interval '1 hour')`, []any{holdID}},
		{"seats", `INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
		 SELECT $1, event_id, event_seat_id FROM cinema.reservation_seats WHERE reservation_id = $2`, []any{holdID, releasedHoldID}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("re-claim %s: %v", statement.what, err)
		}
	}
}

// cleanupCancelFixture removes the rows the tests create. The cinema, room and published
// layout stay: M1 makes a published layout immutable. A cleanup failure is reported, never
// fatal, because cleanup also runs from a defer and before the seed.
func cleanupCancelFixture(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, sql := range []string{
		`DELETE FROM cinema.tickets WHERE reservation_id = '` + cancelPaid + `'`,
		`DELETE FROM cinema.payment_attempts WHERE reservation_id = '` + cancelPaid + `'`,
		`DELETE FROM cinema.reservation_seats WHERE reservation_id = '` + cancelReclaim + `'`,
		`DELETE FROM cinema.reservations WHERE id = '` + cancelReclaim + `'`,
		`DELETE FROM cinema.reservation_seats WHERE reservation_id IN
		   ('` + cancelHold + `','` + cancelPaid + `','` + cancelLapsed + `')`,
		`DELETE FROM cinema.reservations WHERE id IN
		   ('` + cancelHold + `','` + cancelPaid + `','` + cancelLapsed + `')`,
		`DELETE FROM cinema.event_seats WHERE event_id = '` + cancelEvent + `'`,
		`DELETE FROM cinema.events WHERE id = '` + cancelEvent + `'`,
		`DELETE FROM cinema.films WHERE id = '` + cancelFilm + `'`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			if !strings.Contains(err.Error(), "does not exist") {
				t.Logf("clean the cancel fixture: %v", err)
			}
		}
	}
}

// cancelStatus reads the stored status of one reservation.
func cancelStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reservationID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM cinema.reservations WHERE id = $1`, reservationID).Scan(&status); err != nil {
		t.Fatalf("read the status of %s: %v", reservationID, err)
	}
	return status
}

// activeCancelSeats counts the seat rows of one reservation that are still active, which
// is what the partial unique index guards.
func activeCancelSeats(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reservationID string) int {
	t.Helper()
	var live int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = $1 AND released_at IS NULL`,
		reservationID).Scan(&live); err != nil {
		t.Fatalf("count the active seats of %s: %v", reservationID, err)
	}
	return live
}
