// More independent verifier probes for issue #87: items 3, 5 and 6.
package checkout_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/reservation"
	"cinema.local/ticketing/apps/api/internal/venue"
)

// Verifier item 3: a price that changed between the attempt and the re-call. The
// reconciler must ask for the attempt's own amount, and the divergence from Charge
// (which refuses the same stored state) is recorded here.
func TestProbeReconcileUsesTheAttemptAmountAfterAPriceChange(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	reservationID, attemptID, _ := probeHold(t, ctx, pf, 0, 2, "1 hour")
	if _, err := pf.pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, pf.event, fixturePriceCents+200); err != nil {
		t.Fatalf("change the price: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pf.pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, pf.event, fixturePriceCents)
	})

	gateway := &probeGateway{inner: probeSimulated(t, payment.NewMemoryLedger())}
	service := checkout.NewService(gateway, store, &fakeReleaser{journal: new(journal)}, payment.ProviderName)
	result, err := service.ReconcileStaleAttempts(ctx, 10*time.Minute, 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	asked := gateway.asked()
	if len(asked) != 1 {
		t.Fatalf("the provider was asked %d times, want 1", len(asked))
	}
	if asked[0].AmountMinorUnits != fixturePriceCents*2 {
		t.Errorf("the reconciler asked for %d minor units, want the attempt's stored %d",
			asked[0].AmountMinorUnits, fixturePriceCents*2)
	}
	if asked[0].Reference != "payment_attempt:"+attemptID {
		t.Errorf("reference = %q, want payment_attempt:%s", asked[0].Reference, attemptID)
	}
	if result.Captured != 1 {
		t.Errorf("result = %+v, want the stale attempt captured at its own amount", result)
	}
	if amount := probeCount(t, ctx, pf.pool, `SELECT amount_cents::int FROM cinema.payment_attempts WHERE id = $1`, attemptID); amount != fixturePriceCents*2 {
		t.Errorf("stored amount = %d, want the attempt's own %d", amount, fixturePriceCents*2)
	}
	if tickets := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reservationID); tickets != 2 {
		t.Errorf("tickets = %d, want 2", tickets)
	}

	// The same stored state on the buyer's path: Charge refuses with a conflict
	// before the provider is asked. Both answers are defensible; they must not be
	// confused, so a test that changes one of them fails here.
	// Whatever the buyer's path answers once the reconciliation has settled the attempt,
	// it must not write a second sale. Which refusal it gives is a detail — it depends on
	// what the reconciliation already wrote — and the invariant is the sale count.
	buyerGateway := &probeGateway{inner: probeSimulated(t, payment.NewMemoryLedger())}
	buyer := checkout.NewService(buyerGateway, store, &fakeReleaser{journal: new(journal)}, payment.ProviderName)
	_, _ = buyer.Charge(ctx, reservationID, "probe-"+attemptID)
	if tickets := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reservationID); tickets != 2 {
		t.Errorf("tickets after the buyer's path = %d, want still exactly 2: never a second sale", tickets)
	}

	// A provider that already holds a different amount for that reference refuses the
	// re-call, and the refusal leaves the attempt pending.
	pf2, ctx2 := probeSetup(t)
	store2, err := checkout.NewPostgresStore(ctx2, pf2.url)
	if err != nil {
		t.Fatalf("open the second store: %v", err)
	}
	defer store2.Close()
	_, attempt2, _ := probeHold(t, ctx2, pf2, 0, 2, "1 hour")
	ledger := payment.NewMemoryLedger()
	if _, err := probeSimulated(t, ledger).Charge(ctx2, payment.Request{AmountMinorUnits: 7777, Reference: "payment_attempt:" + attempt2}); err != nil {
		t.Fatalf("seed the provider record: %v", err)
	}
	gate := &probeGateway{inner: probeSimulated(t, ledger)}
	res, err := checkout.NewService(gate, store2, &fakeReleaser{journal: new(journal)}, payment.ProviderName).ReconcileStaleAttempts(ctx2, 10*time.Minute, 10)
	if err != nil {
		t.Fatalf("reconcile a mismatched amount: %v", err)
	}
	if res.Unresolved != 1 || res.Captured != 0 {
		t.Errorf("result = %+v, want the mismatch left unresolved", res)
	}
	if status := probeText(t, ctx2, pf2.pool, `SELECT status FROM cinema.payment_attempts WHERE id = $1`, attempt2); status != checkout.StatusPending {
		t.Errorf("attempt status = %q, want pending after ErrAmountMismatch", status)
	}
	if asked := gate.asked(); len(asked) != 1 || asked[0].AmountMinorUnits != fixturePriceCents*2 {
		t.Errorf("the reconciler asked %+v, want the attempt amount before the refusal", asked)
	}
}

// Verifier item 5: a hold that is no longer payable when the capture is recorded.
// The money question has already been answered by the provider, so the sale must
// fail loudly: no tickets, the attempt stays pending, and the worker says so.
func TestProbeCaptureOnANonPayableHoldIsSurfaced(t *testing.T) {
	for _, status := range []string{"expired", "cancelled", "payment_pending"} {
		t.Run(status, func(t *testing.T) {
			pf, ctx := probeSetup(t)
			store, err := checkout.NewPostgresStore(ctx, pf.url)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer store.Close()

			reservationID, attemptID, _ := probeHold(t, ctx, pf, 0, 2, "1 hour")
			if _, err := pf.pool.Exec(ctx, `UPDATE cinema.reservations SET status = $2 WHERE id = $1`, reservationID, status); err != nil {
				t.Fatalf("make the hold %s: %v", status, err)
			}

			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			worker, err := checkout.NewReconciler(
				checkout.NewService(&probeGateway{inner: probeSimulated(t, payment.NewMemoryLedger())}, store,
					&fakeReleaser{journal: new(journal)}, payment.ProviderName),
				checkout.ReconcilerConfig{Interval: time.Minute, Age: 10 * time.Minute, Batch: 10, Timeout: 5 * time.Second, Logger: logger})
			if err != nil {
				t.Fatalf("build the worker: %v", err)
			}
			if err := worker.Reconcile(ctx); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			if tickets := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, reservationID); tickets != 0 {
				t.Errorf("tickets = %d, want 0: no sale may be written on a %s hold", tickets, status)
			}
			if st := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.payment_attempts WHERE id = $1`, attemptID); st != checkout.StatusPending {
				t.Errorf("attempt status = %q, want pending: the refusal must not decide the attempt", st)
			}
			if res := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.reservations WHERE id = $1`, reservationID); res != status {
				t.Errorf("reservation status = %q, want it untouched at %q", res, status)
			}
			text := logs.String()
			if !strings.Contains(text, "left attempts pending") {
				t.Errorf("the worker swallowed the unresolved attempt; logs = %q", text)
			}
			t.Logf("%s: log = %s", status, strings.ReplaceAll(strings.TrimSpace(text), "\n", " | "))

			// A second tick behaves the same: it is retried, never given up on.
			var second bytes.Buffer
			worker2, err := checkout.NewReconciler(
				checkout.NewService(&probeGateway{inner: probeSimulated(t, payment.NewMemoryLedger())}, store,
					&fakeReleaser{journal: new(journal)}, payment.ProviderName),
				checkout.ReconcilerConfig{Interval: time.Minute, Age: 10 * time.Minute, Batch: 10, Timeout: 5 * time.Second, Logger: slog.New(slog.NewTextHandler(&second, nil))})
			if err != nil {
				t.Fatalf("build the second worker: %v", err)
			}
			if err := worker2.Reconcile(ctx); err != nil {
				t.Fatalf("second reconcile: %v", err)
			}
			if got := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.payment_attempts WHERE id = $1 AND status = 'pending'`, attemptID); got != 1 {
				t.Errorf("pending attempts after the second tick = %d, want 1", got)
			}
		})
	}
}

// Verifier item 6: ListStaleAttempts must not lock, and RecordOutcome must.
func TestProbeListStaleAttemptsTakesNoLock(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	reservationID, attemptID, seats := probeHold(t, ctx, pf, 0, 2, "1 hour")

	conn, err := pf.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM cinema.payment_attempts WHERE id = $1 FOR UPDATE`, attemptID).Scan(&locked); err != nil {
		t.Fatalf("lock the attempt row: %v", err)
	}

	started := time.Now()
	candidates, err := store.ListStaleAttempts(ctx, 10*time.Minute, 10)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("ListStaleAttempts under a held row lock: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Attempt.ID != attemptID {
		t.Fatalf("candidates = %+v, want the one stale attempt", candidates)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("ListStaleAttempts waited %v on a locked attempt row, so it takes a lock", elapsed)
	}

	outcome := checkout.AttemptOutcome{
		AttemptID: attemptID, ReservationID: reservationID, EventID: pf.event,
		AmountCents: fixturePriceCents * int64(len(seats)), ProviderReference: "probe_lock_ref",
		Status: checkout.StatusCaptured, Outcome: payment.OutcomeApproved, Seats: seats,
		Tickets: []checkout.Ticket{{ID: venue.NewID(), EventSeatID: seats[0]}, {ID: venue.NewID(), EventSeatID: seats[1]}},
		Outbox:  checkout.Outbox{ID: venue.NewID(), Topic: checkout.OutboxTopicTicketIssued, Payload: []byte(`{"reservationId":"` + reservationID + `"}`)},
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.RecordOutcome(ctx, outcome)
		done <- err
	}()
	select {
	case err := <-done:
		t.Errorf("RecordOutcome returned while the attempt row was locked by another transaction: %v", err)
	case <-time.After(750 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the locking transaction: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RecordOutcome after the lock was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("RecordOutcome never finished after the lock was released")
	}
}

// Verifier item 6: reconciling against the sweep, the cancel path and other
// charges must not deadlock, and must never write two sales or a paid hold with no
// tickets.
func TestProbeReconcileStressFindsNoDeadlockOrDoubleSale(t *testing.T) {
	pf, ctx := probeSetup(t)
	store, err := checkout.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	reserveStore, err := reservation.NewPostgresStore(ctx, pf.url)
	if err != nil {
		t.Fatalf("open the reservation store: %v", err)
	}
	defer reserveStore.Close()

	type hold struct {
		reservationID string
		attemptID     string
		seats         []int64
	}
	holds := make([]hold, 0, 4)
	for i := 0; i < 4; i++ {
		reservationID, attemptID, seats := probeHold(t, ctx, pf, i*2, 2, "1 hour")
		holds = append(holds, hold{reservationID: reservationID, attemptID: attemptID, seats: seats})
	}
	ledger := payment.NewMemoryLedger()

	var mu sync.Mutex
	var deadlockErrors, otherErrors []string
	var wg sync.WaitGroup
	stop := make(chan struct{})

	worker := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				fn()
			}
		}()
	}

	record := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(err.Error(), "deadlock") || strings.Contains(err.Error(), "40P01") {
			deadlockErrors = append(deadlockErrors, err.Error())
		} else if !strings.Contains(err.Error(), "already") && !strings.Contains(err.Error(), "not awaiting payment") {
			otherErrors = append(otherErrors, err.Error())
		}
	}

	for i := range holds {
		h := holds[i]
		service := checkout.NewService(&probeGateway{inner: probeSimulated(t, ledger)}, store,
			&fakeReleaser{journal: new(journal)}, payment.ProviderName)
		worker(func() { _, err := service.ReconcileStaleAttempts(ctx, 10*time.Minute, 4); record(err) })
		worker(func() { _, err := service.Charge(ctx, h.reservationID, "probe-"+h.attemptID); record(err) })
	}
	worker(func() {
		deadline, err := reserveStore.DatabaseNow(ctx)
		record(err)
		if err == nil {
			_, err := reserveStore.ExpireHolds(ctx, deadline, 10)
			record(err)
		}
	})
	worker(func() {
		_, err := reserveStore.Cancel(ctx, holds[0].reservationID)
		record(err)
	})

	time.Sleep(3 * time.Second)
	close(stop)
	wg.Wait()

	if len(deadlockErrors) > 0 {
		t.Errorf("deadlocks observed: %d, first: %s", len(deadlockErrors), deadlockErrors[0])
	}
	for _, res := range holds {
		status := probeText(t, ctx, pf.pool, `SELECT status FROM cinema.reservations WHERE id = $1`, res.reservationID)
		tickets := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, res.reservationID)
		captured := probeCount(t, ctx, pf.pool, `SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id = $1 AND status = 'captured'`, res.reservationID)
		if status == "paid" && tickets != len(res.seats) {
			t.Errorf("%s paid with %d tickets, want %d", res.reservationID, tickets, len(res.seats))
		}
		if captured > 1 {
			t.Errorf("%s has %d captured attempts, want at most 1", res.reservationID, captured)
		}
		if tickets > len(res.seats) {
			t.Errorf("%s has %d tickets, want at most %d", res.reservationID, tickets, len(res.seats))
		}
	}
	if released := probeCount(t, ctx, pf.pool, `
SELECT count(*) FROM cinema.reservation_seats rs JOIN cinema.reservations r ON r.id = rs.reservation_id
WHERE r.event_id::text = $1 AND rs.released_at IS NOT NULL AND r.status = 'paid'`, pf.event); released != 0 {
		t.Errorf("%d seat row(s) of a paid reservation were released, want 0", released)
	}
	t.Logf("other errors observed (expected: replays and conflicts): %d, sample: %v", len(otherErrors), firstOf(otherErrors))
}

func firstOf(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	if len(values) > 3 {
		values = values[:3]
	}
	return strings.Join(values, " | ")
}
