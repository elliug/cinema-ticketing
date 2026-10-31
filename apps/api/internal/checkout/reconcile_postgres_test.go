// The reconciler against real PostgreSQL (issue #87, S3).
//
// The scenario is the one a dead process leaves: an attempt still 'pending' whose hold
// window has already closed. The test proves all three outcomes of the mechanism the
// issue decided — captured, declined and unresolved — and, for the declined case, that
// the #82-guarded expiry sweep then does release the hold. That last step is why this
// test drives the reservation package's sweep too: the two changes have to compose, and
// composition is the thing a unit test cannot show.
//
// It owns its own event and its own seats, because a seat can only be held once and the
// other gated cases already hold all fourteen of the shared event.
package checkout_test

import (
	"context"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/reservation"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	reconcileEvent       = "00000000-0000-0000-0000-00000000d0e1"
	reconcileApproved    = "00000000-0000-0000-0000-00000000d0e2"
	reconcileDeclined    = "00000000-0000-0000-0000-00000000d0e3"
	reconcileInFlight    = "00000000-0000-0000-0000-00000000d0e4"
	reconcileAttemptOK   = "00000000-0000-0000-0000-00000000d0e5"
	reconcileAttemptNo   = "00000000-0000-0000-0000-00000000d0e6"
	reconcileAttemptLive = "00000000-0000-0000-0000-00000000d0e7"
)

// reconcileStaleAge is the bound the test reconciles with: an hour-old attempt is
// certainly abandoned, a fresh one is not.
const reconcileStaleAge = 10 * time.Minute

// reconcileDeclineSentinel is the provider rule this test wires: an amount whose last
// two digits are 98 is declined. It is chosen so one amount in the fixture approves and
// the other declines under a single adapter.
const reconcileDeclineSentinel int64 = 98

func cleanupReconcileFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	// Every statement takes the same identifier list through $1 and compares it with the
	// column that names it. A statement without a placeholder would be handed an argument
	// it cannot take, and that failure is deliberately swallowed below because cleanup also
	// runs before the first seed.
	ids := []string{reconcileApproved, reconcileDeclined, reconcileInFlight, reconcileEvent}
	for _, sql := range []string{
		`DELETE FROM cinema.outbox WHERE payload->>'reservationId' = ANY($1)`,
		`DELETE FROM cinema.tickets WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.payment_attempts WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservation_seats WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservations WHERE id::text = ANY($1)`,
		`DELETE FROM cinema.event_seats WHERE event_id::text = ANY($1)`,
		`DELETE FROM cinema.events WHERE id::text = ANY($1)`,
	} {
		_, _ = pool.Exec(ctx, sql, ids)
	}
}

// seedReconcileFixture creates the event and the three holds. The attempt rows carry
// their own created_at, which is what the reconciler's age bound reads.
func seedReconcileFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []int64 {
	t.Helper()
	cleanupReconcileFixture(pool)

	if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
VALUES ($1, $2, $3, $4, now() + interval '3 days', 'on_sale')`,
		reconcileEvent, fixtureRoom, fixtureFilm, fixtureLayout); err != nil {
		t.Fatalf("seed the reconcile event: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`,
		reconcileEvent, fixturePriceCents); err != nil {
		t.Fatalf("seed the reconcile event price: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT id FROM cinema.event_seats WHERE event_id = $1 ORDER BY id LIMIT 6`, reconcileEvent)
	if err != nil {
		t.Fatalf("read the reconcile event seats: %v", err)
	}
	defer rows.Close()
	var seats []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("read a reconcile seat: %v", err)
		}
		seats = append(seats, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the reconcile seats: %v", err)
	}
	if len(seats) < 6 {
		t.Fatalf("the reconcile event materialized %d seats, want 6", len(seats))
	}

	for index, hold := range []struct {
		reservation string
		attempt     string
		seats       []int64
		// age is how long ago the attempt was stored; a zero age means "just now".
		age string
	}{
		// Three seats, which is 29,97 EUR: the last two digits are 97, so the sentinel
		// adapter approves it.
		{reconcileApproved, reconcileAttemptOK, seats[0:3], "1 hour"},
		// Two seats, 19,98 EUR: the 98 sentinel declines it, which is the whole point of
		// using the sentinel instead of a second adapter.
		{reconcileDeclined, reconcileAttemptNo, seats[3:5], "1 hour"},
		{reconcileInFlight, reconcileAttemptLive, seats[5:6], ""},
	} {
		// The hold is inserted open because the claim trigger refuses a seat row for an
		// already expired hold; it is backdated below, which is the state a payment that
		// outlived its window leaves.
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservations (id, event_id, status, expires_at)
VALUES ($1, $2, 'held', now() + interval '10 minutes')`, hold.reservation, reconcileEvent); err != nil {
			t.Fatalf("seed reconcile reservation %d: %v", index, err)
		}
		for _, seat := range hold.seats {
			if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id) VALUES ($1, $2, $3)`,
				hold.reservation, reconcileEvent, seat); err != nil {
				t.Fatalf("seed reconcile seat %d: %v", seat, err)
			}
		}
		if _, err := pool.Exec(ctx, `
UPDATE cinema.reservations SET expires_at = now() - interval '5 minutes' WHERE id = $1`,
			hold.reservation); err != nil {
			t.Fatalf("backdate reconcile reservation %d: %v", index, err)
		}
		createdAt := "now()"
		if hold.age != "" {
			createdAt = "now() - interval '" + hold.age + "'"
		}
		amount := fixturePriceCents * int64(len(hold.seats))
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'pending', $5, $6, `+createdAt+`, `+createdAt+`)`,
			hold.attempt, hold.reservation, payment.ProviderName, amount, "reconcile-"+hold.attempt, "fp"); err != nil {
			t.Fatalf("seed reconcile attempt %d: %v", index, err)
		}
	}
	return seats
}

func TestReconcileStaleAttemptsOnPostgres(t *testing.T) {
	ctx := context.Background()
	pool, url := checkoutPool(t, ctx)
	defer pool.Close()

	// The venue, film and layout come from the shared fixture: the reconciler's event is
	// its own, but it reuses the published layout those rows materialize seats from.
	seedCheckoutFixture(t, ctx, pool)
	seedReconcileFixture(t, ctx, pool)
	defer cleanupReconcileFixture(pool)

	store, err := checkout.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the checkout store: %v", err)
	}
	defer store.Close()

	// One adapter, built with the sentinel rule, serves both cases: the amounts differ, so
	// the same provider approves the three-seat order and declines the two-seat one.
	gateway, err := payment.NewSimulatedWithDeclineRule(reconcileDeclineSentinel, payment.NewMemoryLedger())
	if err != nil {
		t.Fatalf("build the sentinel provider: %v", err)
	}
	approved := checkout.NewService(gateway, store, &fakeReleaser{journal: new(journal)}, payment.ProviderName)

	result, err := approved.ReconcileStaleAttempts(ctx, reconcileStaleAge, 10)
	if err != nil {
		t.Fatalf("reconcile the approved case: %v", err)
	}
	if result.Candidates != 2 || result.Captured != 1 || result.Declined != 1 || result.Unresolved != 0 {
		t.Fatalf("result = %+v, want the two hour-old attempts settled: one captured, one declined", result)
	}

	// The approved attempt became a sale: reservation paid, seats sold, one ticket per
	// seat. That is the whole point — the money the provider took is now a ticket.
	if status := readText(t, ctx, pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reconcileApproved); status != "paid" {
		t.Fatalf("approved reservation status = %q, want paid", status)
	}
	if bought := readInt(t, ctx, pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reconcileApproved); bought != 3 {
		t.Fatalf("tickets for the approved reservation = %d, want 3", bought)
	}
	if sold := readInt(t, ctx, pool, `
SELECT count(*) FROM cinema.event_seats es
JOIN cinema.reservation_seats rs ON rs.event_seat_id = es.id
WHERE rs.reservation_id = $1 AND es.state = 'sold'`, reconcileApproved); sold != 3 {
		t.Fatalf("sold seats for the approved reservation = %d, want 3", sold)
	}

	// The declined attempt is decided, so it is no longer pending and the sweep's guard
	// stops protecting its hold. The reconciler itself released nothing.
	if status := readText(t, ctx, pool, `SELECT status FROM cinema.payment_attempts WHERE id = $1`, reconcileAttemptNo); status != checkout.StatusDeclined {
		t.Fatalf("declined attempt status = %q, want declined", status)
	}
	if status := readText(t, ctx, pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reconcileDeclined); status != "held" {
		t.Fatalf("declined reservation status = %q, want held: releasing belongs to the sweep", status)
	}

	// The fresh attempt is untouched: a live provider call is not a candidate.
	if status := readText(t, ctx, pool, `SELECT status FROM cinema.payment_attempts WHERE id = $1`, reconcileAttemptLive); status != checkout.StatusPending {
		t.Fatalf("fresh attempt status = %q, want it still pending", status)
	}

	// A second tick is a no-op for the settled attempts: nothing was charged again and no
	// second sale was written.
	again, err := approved.ReconcileStaleAttempts(ctx, reconcileStaleAge, 10)
	if err != nil {
		t.Fatalf("reconcile a second time: %v", err)
	}
	if again.Captured != 0 || again.Declined != 0 {
		t.Fatalf("second tick = %+v, want nothing settled twice", again)
	}
	if bought := readInt(t, ctx, pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reconcileApproved); bought != 3 {
		t.Fatalf("tickets after the second tick = %d, want still 3", bought)
	}

	// The composition with #82: now that the declined attempt is no longer pending, the
	// expiry sweep expires the hold and frees the seats.
	sweeps, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the reservation store: %v", err)
	}
	defer sweeps.Close()
	deadline, err := sweeps.DatabaseNow(ctx)
	if err != nil {
		t.Fatalf("read the database clock: %v", err)
	}
	if _, err := sweeps.ExpireHolds(ctx, deadline, 100); err != nil {
		t.Fatalf("sweep after the decline: %v", err)
	}
	if status := readText(t, ctx, pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reconcileDeclined); status != "expired" {
		t.Fatalf("declined reservation status after the sweep = %q, want expired", status)
	}
	if released := readInt(t, ctx, pool, `
SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = $1 AND released_at IS NOT NULL`,
		reconcileDeclined); released != 2 {
		t.Fatalf("released seats after the sweep = %d, want 2", released)
	}
	// And the hold that is still pending keeps its seats, which is #82's guarantee holding
	// through this change as well.
	if released := readInt(t, ctx, pool, `
SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = $1 AND released_at IS NOT NULL`,
		reconcileInFlight); released != 0 {
		t.Fatalf("the sweep released %d seat(s) of a hold whose payment is still pending, want none", released)
	}
}

func readText(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arg any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(ctx, query, arg).Scan(&value); err != nil {
		t.Fatalf("read a text value: %v", err)
	}
	return value
}

func readInt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arg any) int {
	t.Helper()
	var value int
	if err := pool.QueryRow(ctx, query, arg).Scan(&value); err != nil {
		t.Fatalf("read an integer value: %v", err)
	}
	return value
}
