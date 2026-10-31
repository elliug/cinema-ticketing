// Independent verifier probes for issue #87 (feat/reconcile-stale-payments).
// Scratch-copy probes: they exercise the same exported surface as the gated suite
// and assert the spec-derived properties the writer's tests do not cover.
package checkout_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/reservation"
	"cinema.local/ticketing/apps/api/internal/venue"

	"github.com/jackc/pgx/v5/pgxpool"
)

type probeFixture struct {
	pool  *pgxpool.Pool
	url   string
	event string
	seats []int64
}

func probeSetup(t *testing.T) (*probeFixture, context.Context) {
	t.Helper()
	ctx := context.Background()
	pool, url := checkoutPool(t, ctx)
	seedCheckoutFixture(t, ctx, pool)

	pf := &probeFixture{pool: pool, url: url, event: venue.NewID()}
	if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
VALUES ($1, $2, $3, $4, now() + interval '3 days', 'on_sale')`,
		pf.event, fixtureRoom, fixtureFilm, fixtureLayout); err != nil {
		t.Fatalf("seed probe event: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`,
		pf.event, fixturePriceCents); err != nil {
		t.Fatalf("seed probe price: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT id FROM cinema.event_seats WHERE event_id = $1 ORDER BY id`, pf.event)
	if err != nil {
		t.Fatalf("read probe seats: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("read probe seat: %v", err)
		}
		pf.seats = append(pf.seats, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read probe seats: %v", err)
	}
	if len(pf.seats) < 8 {
		t.Fatalf("probe event materialized %d seats, want at least 8", len(pf.seats))
	}
	t.Cleanup(func() { probeCleanup(pool, pf.event) })
	return pf, ctx
}

func probeCleanup(pool *pgxpool.Pool, event string) {
	ctx := context.Background()
	// The outbox is matched by the event **or** by the reservation it names: one probe
	// records an outcome whose payload carries only `reservationId`, so a cleanup keyed on
	// `eventId` alone left a row behind. The gate asserts counts over the whole database, so
	// a leftover row here is not cosmetic — it published alongside the gate's own sale and
	// failed `verify-m3` with "published:unpublished = 2:0".
	_, _ = pool.Exec(ctx, `
DELETE FROM cinema.outbox
WHERE payload->>'eventId' = $1
   OR payload->>'reservationId' IN (SELECT id::text FROM cinema.reservations WHERE event_id::text = $1)`, event)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.tickets WHERE event_id::text = $1`, event)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.payment_attempts WHERE reservation_id IN (SELECT id FROM cinema.reservations WHERE event_id::text = $1)`, event)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.reservation_seats WHERE event_id::text = $1`, event)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.reservations WHERE event_id::text = $1`, event)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.event_seats WHERE event_id::text = $1`, event)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.events WHERE id::text = $1`, event)
}

// probeHold creates a held reservation whose window has already closed plus one
// pending attempt of the given age. It returns the reservation, the attempt and
// the seat set.
func probeHold(t *testing.T, ctx context.Context, pf *probeFixture, seatOffset, seatCount int, attemptAge string) (string, string, []int64) {
	t.Helper()
	return probeHoldStatus(t, ctx, pf, seatOffset, seatCount, attemptAge, "held", fixturePriceCents*int64(seatCount))
}

func probeHoldStatus(t *testing.T, ctx context.Context, pf *probeFixture, seatOffset, seatCount int, attemptAge, status string, amount int64) (string, string, []int64) {
	t.Helper()
	if seatOffset+seatCount > len(pf.seats) {
		t.Fatalf("probe fixture has %d seats, want %d", len(pf.seats), seatOffset+seatCount)
	}
	reservationID, attemptID := venue.NewID(), venue.NewID()
	seats := append([]int64(nil), pf.seats[seatOffset:seatOffset+seatCount]...)

	if _, err := pf.pool.Exec(ctx, `
INSERT INTO cinema.reservations (id, event_id, status, expires_at)
VALUES ($1, $2, $3, now() + interval '10 minutes')`, reservationID, pf.event, status); err != nil {
		t.Fatalf("seed probe hold: %v", err)
	}
	for _, seat := range seats {
		if _, err := pf.pool.Exec(ctx, `
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id) VALUES ($1, $2, $3)`,
			reservationID, pf.event, seat); err != nil {
			t.Fatalf("seed probe seat: %v", err)
		}
	}
	if _, err := pf.pool.Exec(ctx, `
UPDATE cinema.reservations SET expires_at = now() - interval '5 minutes' WHERE id = $1`, reservationID); err != nil {
		t.Fatalf("backdate probe hold: %v", err)
	}
	key := "probe-" + attemptID
	if _, err := pf.pool.Exec(ctx, `
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'pending', $5, $6, now() - $7::interval, now() - $7::interval)`,
		attemptID, reservationID, payment.ProviderName, amount, key, probeFingerprint(reservationID), attemptAge); err != nil {
		t.Fatalf("seed probe attempt: %v", err)
	}
	return reservationID, attemptID, seats
}

func probeFingerprint(reservationID string) string {
	sum := sha256.Sum256([]byte("payment|" + reservationID))
	return hex.EncodeToString(sum[:])
}

// probeGateway wraps a gateway (or answers directly) and records every request.
type probeGateway struct {
	mu       sync.Mutex
	requests []payment.Request
	inner    payment.PaymentGateway
	err      error
	outcome  payment.Outcome
	blankRef bool
}

func (g *probeGateway) Charge(ctx context.Context, request payment.Request) (payment.Result, error) {
	g.mu.Lock()
	g.requests = append(g.requests, request)
	inner, err, outcome, blank := g.inner, g.err, g.outcome, g.blankRef
	g.mu.Unlock()
	if err != nil {
		return payment.Result{}, err
	}
	if inner != nil {
		return inner.Charge(ctx, request)
	}
	if blank {
		return payment.Result{Outcome: outcome, Reference: "   "}, nil
	}
	return payment.Result{Outcome: outcome, Reference: "probe_ref:" + request.Reference}, nil
}

func (g *probeGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

func (g *probeGateway) asked() []payment.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]payment.Request(nil), g.requests...)
}

func probeSimulated(t *testing.T, ledger *payment.MemoryLedger) payment.PaymentGateway {
	t.Helper()
	if ledger == nil {
		return payment.NewSimulated()
	}
	adapter, err := payment.NewSimulatedWithDeclineRule(13, ledger)
	if err != nil {
		t.Fatalf("build the sentinel provider: %v", err)
	}
	return adapter
}

// S2/S1: the age bound is the database clock's predicate, and an in-flight
// attempt is never a candidate.
func TestProbeAgeBoundIsTheDatabaseClock(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if _, _, _ = probeHold(t, ctx, pf, 0, 2, "1 hour"); err != nil {
		t.Fatal(err)
	}
	liveReservation, liveAttempt, _ := probeHold(t, ctx, pf, 2, 2, "0 seconds")

	for _, age := range []time.Duration{time.Hour, 10 * time.Minute, time.Second, time.Microsecond} {
		got, err := store.ListStaleAttempts(ctx, age, 10)
		if err != nil {
			t.Fatalf("ListStaleAttempts(%v): %v", age, err)
		}
		_ = liveReservation
		var want int
		if err := pf.pool.QueryRow(ctx, `
SELECT count(*)
FROM cinema.payment_attempts a
JOIN cinema.reservations r ON r.id = a.reservation_id
WHERE a.status = 'pending' AND r.event_id = $2::uuid
  AND a.created_at <= now() - ($1::bigint * interval '1 microsecond')`,
			age.Microseconds(), pf.event).Scan(&want); err != nil {
			t.Fatalf("read the SQL predicate: %v", err)
		}
		if len(got) != want {
			t.Errorf("age %v: store returned %d candidates, the database predicate selects %d", age, len(got), want)
		}
		for _, prepared := range got {
			if prepared.Attempt.ID == liveAttempt && age >= time.Second {
				t.Errorf("age %v: the attempt created %s ago was a candidate; a live provider call must never be", age, "just now")
			}
			if prepared.Attempt.Status != checkout.StatusPending {
				t.Errorf("candidate %s has status %q, want pending", prepared.Attempt.ID, prepared.Attempt.Status)
			}
		}
	}

	if _, err := store.ListStaleAttempts(ctx, 0, 10); !errors.Is(err, checkout.ErrInvalidInput) {
		t.Errorf("ListStaleAttempts(0) error = %v, want the named refusal", err)
	}
	if _, err := store.ListStaleAttempts(ctx, time.Hour, 0); !errors.Is(err, checkout.ErrInvalidInput) {
		t.Errorf("ListStaleAttempts(batch 0) error = %v, want the named refusal", err)
	}

	// Duration.Microseconds() truncates, so an age below one microsecond would make the
	// bound read as now() and hand back an attempt created moments ago — a live provider
	// call exposed as abandoned. The bound is refused below that resolution instead of
	// silently becoming "no bound at all".
	if under, err := store.ListStaleAttempts(ctx, 999*time.Nanosecond, 10); !errors.Is(err, checkout.ErrInvalidInput) {
		t.Errorf("ListStaleAttempts(999ns) = %d candidate(s), err %v; want the named refusal", len(under), err)
	}
}

// S6/verifier item 2: the reconciler racing the buyer's own request must write
// exactly one sale.
func TestProbeReconcileRacingChargeWritesOneSale(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ledger := payment.NewMemoryLedger()
	reservationID, attemptID, seats := probeHold(t, ctx, pf, 0, 2, "1 hour")

	services := make([]*checkout.Service, 4)
	gateways := make([]*probeGateway, 4)
	for i := range services {
		gateways[i] = &probeGateway{inner: probeSimulated(t, ledger)}
		services[i] = checkout.NewService(gateways[i], store, &fakeReleaser{journal: new(journal)}, payment.ProviderName)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var buyerResult checkout.Result
	var buyerErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		buyerResult, buyerErr = services[0].Charge(ctx, reservationID, "probe-"+attemptID)
	}()
	results := make([]checkout.ReconcileResult, 3)
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = services[i+1].ReconcileStaleAttempts(ctx, 10*time.Minute, 10)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Logf("reconcile tick %d returned %v", i, err)
		}
	}
	if buyerErr != nil {
		t.Errorf("the buyer's Charge failed: %v", buyerErr)
	}
	if buyerResult.PaymentStatus != checkout.PaymentApproved || len(buyerResult.TicketIDs) != 2 {
		t.Errorf("buyer answered %+v, want approved with 2 tickets", buyerResult)
	}
	if tickets := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reservationID); tickets != 2 {
		t.Errorf("tickets for the reservation = %d, want exactly 2 (one sale per attempt)", tickets)
	}
	if rows := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id = $1`, reservationID); rows != 1 {
		t.Errorf("payment_attempts rows = %d, want 1", rows)
	}
	if status := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reservationID); status != "paid" {
		t.Errorf("reservation status = %q, want paid", status)
	}
	if outbox := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.outbox WHERE payload->>'reservationId' = $1`, reservationID); outbox != 1 {
		t.Errorf("outbox rows = %d, want exactly 1", outbox)
	}
	if captured := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id = $1 AND status = 'captured'`, reservationID); captured != 1 {
		t.Errorf("captured attempts = %d, want exactly 1", captured)
	}
	var totalCharges int
	for _, gateway := range gateways {
		totalCharges += gateway.count()
	}
	t.Logf("provider calls made by the four racers: %d (a shared ledger replays them)", totalCharges)
	_ = seats
}

// Verifier item 3: a provider whose replay record is per process (two ledgers) can
// be asked to charge the same reference twice; the durable layer must still sell
// once.
func TestProbeTwoProviderLedgersStillSellOnce(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	reservationID, attemptID, _ := probeHold(t, ctx, pf, 0, 2, "1 hour")
	reference := "payment_attempt:" + attemptID

	firstLedger := payment.NewMemoryLedger()
	deadProcessAdapter, err := payment.NewSimulatedWithDeclineRule(13, firstLedger)
	if err != nil {
		t.Fatalf("build the dead process's provider: %v", err)
	}
	first, err := deadProcessAdapter.Charge(ctx, payment.Request{AmountMinorUnits: fixturePriceCents * 2, Reference: reference})
	if err != nil || first.Outcome != payment.OutcomeApproved {
		t.Fatalf("the dead process's charge: %+v %v", first, err)
	}
	// The dead process never recorded the verdict: the attempt is still pending.
	secondLedger := payment.NewMemoryLedger()
	secondGateway := &probeGateway{inner: probeSimulated(t, secondLedger)}
	second := checkout.NewService(secondGateway, store, &fakeReleaser{journal: new(journal)}, payment.ProviderName)
	result, err := second.ReconcileStaleAttempts(ctx, 10*time.Minute, 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Captured != 1 {
		t.Errorf("result = %+v, want the stale attempt captured", result)
	}
	// Proof that the second ledger recorded its own charge under the same reference:
	// a replay answers, a different amount for that reference is refused.
	if _, err := probeSimulated(t, secondLedger).Charge(ctx, payment.Request{AmountMinorUnits: 77, Reference: reference}); err == nil ||
		!errors.Is(err, payment.ErrAmountMismatch) {
		t.Errorf("second ledger replay = %v, want ErrAmountMismatch so we know it holds its own charge", err)
	}
	if tickets := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reservationID); tickets != 2 {
		t.Errorf("tickets = %d, want exactly 2: a second provider charge must not become a second sale", tickets)
	}
}

// S1/verifier item 1: an unanswered call must leave the attempt pending and the
// seats held, through the real #82-guarded sweep.
func TestProbeUnansweredCallKeepsSeatsHeld(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	reservationID, _, _ := probeHold(t, ctx, pf, 0, 2, "1 hour")
	liveReservation, _, _ := probeHold(t, ctx, pf, 2, 2, "0 seconds")

	for _, tc := range []struct {
		name string
		gate *probeGateway
	}{
		{name: "provider error", gate: &probeGateway{err: errors.New("connection refused")}},
		{name: "blank reference", gate: &probeGateway{outcome: payment.OutcomeApproved, blankRef: true}},
		{name: "unclassifiable answer", gate: &probeGateway{outcome: payment.Outcome("maybe")}},
	} {
		service := checkout.NewService(tc.gate, store, &fakeReleaser{journal: new(journal)}, payment.ProviderName)
		result, err := service.ReconcileStaleAttempts(ctx, 10*time.Minute, 10)
		if err != nil {
			t.Fatalf("%s: reconcile: %v", tc.name, err)
		}
		if result.Captured != 0 || result.Declined != 0 {
			t.Errorf("%s: result = %+v, want nothing settled", tc.name, result)
		}
		if status := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.payment_attempts WHERE reservation_id = $1`, reservationID); status != checkout.StatusPending {
			t.Errorf("%s: attempt status = %q, want pending", tc.name, status)
		}
		if tickets := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reservationID); tickets != 0 {
			t.Errorf("%s: tickets = %d, want 0 (nothing was sold)", tc.name, tickets)
		}

		probeSweep(t, ctx, pf.url)
		if released := probeCount(t, ctx, pf.pool,
			`SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = $1 AND released_at IS NOT NULL`, reservationID); released != 0 {
			t.Errorf("%s: the sweep released %d seat(s) of a hold whose provider never answered, want 0", tc.name, released)
		}
		if status := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reservationID); status != "held" {
			t.Errorf("%s: reservation status after the sweep = %q, want held", tc.name, status)
		}
		if released := probeCount(t, ctx, pf.pool,
			`SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = $1 AND released_at IS NOT NULL`, liveReservation); released != 0 {
			t.Errorf("%s: the sweep released %d seat(s) of a hold whose attempt is in flight, want 0", tc.name, released)
		}
	}
}

// S1/verifier item 1: a decline is the one answer that lets the sweep release, and
// it releases only after the verdict is durable.
func TestProbeDeclineLetsTheSweepRelease(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	reservationID, _, _ := probeHold(t, ctx, pf, 0, 2, "1 hour")
	service := checkout.NewService(&probeGateway{outcome: payment.OutcomeDeclined}, store,
		&fakeReleaser{journal: new(journal)}, payment.ProviderName)
	result, err := service.ReconcileStaleAttempts(ctx, 10*time.Minute, 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Declined != 1 || result.Unresolved != 0 {
		t.Fatalf("result = %+v, want one declined", result)
	}
	if status := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reservationID); status != "held" {
		t.Errorf("reservation status = %q after the decline, want held: the reconciler releases nothing itself", status)
	}
	probeSweep(t, ctx, pf.url)
	if status := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reservationID); status != "expired" {
		t.Errorf("reservation status after the sweep = %q, want expired", status)
	}
	if released := probeCount(t, ctx, pf.pool,
		`SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = $1 AND released_at IS NOT NULL`, reservationID); released != 2 {
		t.Errorf("released seats = %d, want 2", released)
	}
}

func probeSweep(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	store, err := reservation.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the reservation store: %v", err)
	}
	defer store.Close()
	deadline, err := store.DatabaseNow(ctx)
	if err != nil {
		t.Fatalf("read the database clock: %v", err)
	}
	if _, err := store.ExpireHolds(ctx, deadline, 100); err != nil {
		t.Fatalf("sweep: %v", err)
	}
}

func probeCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arg any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, query, arg).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func probeText(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arg any) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(ctx, query, arg).Scan(&value); err != nil {
		t.Fatalf("text: %v", err)
	}
	return value
}
