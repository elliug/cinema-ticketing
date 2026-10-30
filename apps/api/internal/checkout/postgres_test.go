// PostgreSQL-only checkout store tests. They need a migrated isolated database
// and are gated on M3_CHECKOUT_DATABASE_URL, so the default unit suite needs no
// database at all.
//
// The unit tests in service_test.go decide the service's behaviour over fakes. What
// only PostgreSQL can prove is here: that phase 1 commits the attempt before the
// provider is called, that phase 2 is never reached for a second client key while a
// first payment is still open (at any age, because the guard is the partial unique
// index payment_attempts_open_per_reservation and not a clock), that phase 3 records
// the whole sale in one transaction — the captured attempt, the paid reservation, the
// sold seats, one ticket per seat and the outbox row — that a replay answers from
// those rows without a second sale, and that the schema's guards refuse an already
// paid reservation, an expired hold and an attempt charged a different amount. It also
// pins the two defence-in-depth guards of the capture itself: the verdict must belong
// to the reservation being paid, and every seat a ticket is issued for must actually
// have been marked sold.
package checkout_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
)

// checkoutDatabaseURLEnv names the isolated database these tests use. The default
// unit suite skips them; `make verify-m3` points it at a throwaway database.
const checkoutDatabaseURLEnv = "M3_CHECKOUT_DATABASE_URL"

// Fixture identifiers are fixed so a failed run can be cleaned up by the next one.
// The venue (cinema, room, published layout) is inserted once and reused: M1 makes
// a published layout immutable, so it can never be deleted and re-inserted.
const (
	fixtureCinema = "00000000-0000-0000-0000-00000000d0a1"
	fixtureRoom   = "00000000-0000-0000-0000-00000000d0a2"
	fixtureLayout = "00000000-0000-0000-0000-00000000d0a3"
	fixtureRow    = "00000000-0000-0000-0000-00000000d0a4"
	fixtureSeat1  = "00000000-0000-0000-0000-00000000d0a5"
	fixtureSeat2  = "00000000-0000-0000-0000-00000000d0a6"
	fixtureSeat3  = "00000000-0000-0000-0000-00000000d0a7"
	fixtureSeat4  = "00000000-0000-0000-0000-00000000d0a8"
	fixtureSeat5  = "00000000-0000-0000-0000-00000000d0b2"
	fixtureSeat6  = "00000000-0000-0000-0000-00000000d0b3"
	fixtureSeat7  = "00000000-0000-0000-0000-00000000d0b4"
	fixtureSeat8  = "00000000-0000-0000-0000-00000000d0b5"
	fixtureSeat9  = "00000000-0000-0000-0000-00000000d0b6"
	fixtureSeat10 = "00000000-0000-0000-0000-00000000d0b7"
	fixtureSeat11 = "00000000-0000-0000-0000-00000000d0b8"
	fixtureSeat12 = "00000000-0000-0000-0000-00000000d0b9"
	fixtureSeat13 = "00000000-0000-0000-0000-00000000d0ba"
	fixtureSeat14 = "00000000-0000-0000-0000-00000000d0bb"
	fixtureFilm   = "00000000-0000-0000-0000-00000000d0a9"
	fixtureEvent  = "00000000-0000-0000-0000-00000000d0b1"

	approvedReservation    = "00000000-0000-0000-0000-00000000d0c1"
	declinedReservation    = "00000000-0000-0000-0000-00000000d0c2"
	expiredReservation     = "00000000-0000-0000-0000-00000000d0c3"
	mismatchReservation    = "00000000-0000-0000-0000-00000000d0c4"
	fingerprintReservation = "00000000-0000-0000-0000-00000000d0c5"
	inFlightReservation    = "00000000-0000-0000-0000-00000000d0c6"
	guardReservation       = "00000000-0000-0000-0000-00000000d0c7"
)

// fixtureReservations is every reservation the fixture seeds, in request order.
// Each one holds two seats of its own and cleanup removes all of them.
var fixtureReservations = []string{
	approvedReservation,
	declinedReservation,
	expiredReservation,
	mismatchReservation,
	fingerprintReservation,
	inFlightReservation,
	guardReservation,
}

// fixturePriceCents is the price of the fixture event: two seats sell for 19,98
// EUR, an amount ending in 98, which is the sentinel the declining adapter below
// is built with.
const fixturePriceCents = 999

// postgresReleaser records the coordination releases the service asked for, so the
// ordering rule "release only after the commit" is observable here too.
type postgresReleaser struct {
	holdIDs []string
	seats   [][]int64
}

func (r *postgresReleaser) Release(_ context.Context, holdID string, eventSeatIDs []int64) error {
	r.holdIDs = append(r.holdIDs, holdID)
	r.seats = append(r.seats, append([]int64(nil), eventSeatIDs...))
	return nil
}

// failingGateway refuses every charge, which is the honest way to leave a payment
// attempt stored `pending`: phase 1 committed it and phase 2 never answered, exactly
// the state a crashed or unreachable provider leaves behind.
type failingGateway struct {
	calls int
}

func (g *failingGateway) Charge(_ context.Context, _ payment.Request) (payment.Result, error) {
	g.calls++
	return payment.Result{}, errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
}

// countingGateway wraps the payment boundary and counts the calls that reached it,
// so "no money was taken" is observable instead of inferred.
type countingGateway struct {
	inner payment.PaymentGateway
	calls int
}

func (g *countingGateway) Charge(ctx context.Context, request payment.Request) (payment.Result, error) {
	g.calls++
	return g.inner.Charge(ctx, request)
}

// TestCheckoutSaleIsDurableIdempotentAndGuarded walks the whole checkout against a
// real database: the approved sale, its replay, the decline, the expired hold and
// the disagreeing amount.
func TestCheckoutSaleIsDurableAndGuarded(t *testing.T) {
	ctx := context.Background()
	pool, url := checkoutPool(t, ctx)
	seats := seedCheckoutFixture(t, ctx, pool)
	t.Cleanup(func() {
		cleanupCheckoutFixture(pool)
		pool.Close()
	})

	store, err := checkout.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the checkout store: %v", err)
	}
	defer store.Close()

	present, err := store.CheckoutSchemaPresent(ctx)
	if err != nil {
		t.Fatalf("check the checkout schema: %v", err)
	}
	if !present {
		t.Fatalf("%s is not migrated with 0005_checkout", checkoutDatabaseURLEnv)
	}

	releaser := &postgresReleaser{}
	service := checkout.NewService(payment.NewSimulated(), store, releaser, payment.ProviderName)

	t.Run("an approved payment captures the sale in one transaction", func(t *testing.T) {
		result, err := service.Charge(ctx, approvedReservation, "key-approved")
		if err != nil {
			t.Fatalf("Charge() = unexpected error %v", err)
		}
		if result.PaymentStatus != checkout.PaymentApproved || result.AttemptStatus != checkout.StatusCaptured {
			t.Fatalf("result = %+v, want an approved, captured payment", result)
		}
		if result.AmountCents != fixturePriceCents*2 {
			t.Fatalf("amount = %d, want %d (two seats at 9,99 EUR)", result.AmountCents, fixturePriceCents*2)
		}
		if len(result.TicketIDs) != 2 {
			t.Fatalf("ticket ids = %v, want one per seat", result.TicketIDs)
		}

		// The attempt carries the client key, the fingerprint, the amount and the
		// provider reference derived from its own id.
		var status, outcome, reference, provider string
		var amount int64
		if err := pool.QueryRow(ctx, `
SELECT status, outcome, provider_reference, provider, amount_cents
FROM cinema.payment_attempts WHERE id = $1`, result.AttemptID).
			Scan(&status, &outcome, &reference, &provider, &amount); err != nil {
			t.Fatalf("read the stored attempt: %v", err)
		}
		if status != checkout.StatusCaptured || outcome != string(payment.OutcomeApproved) {
			t.Fatalf("stored attempt = %s/%s, want captured/approved", status, outcome)
		}
		if reference != "payment_attempt:"+result.AttemptID {
			t.Fatalf("stored provider reference = %q, want the attempt derived reference", reference)
		}
		if provider != payment.ProviderName {
			t.Fatalf("stored provider = %q, want %q", provider, payment.ProviderName)
		}
		if amount != fixturePriceCents*2 {
			t.Fatalf("stored amount = %d, want %d", amount, fixturePriceCents*2)
		}

		// The reservation is paid and the seats are sold.
		var reservationStatus string
		if err := pool.QueryRow(ctx, `SELECT status FROM cinema.reservations WHERE id = $1`, approvedReservation).
			Scan(&reservationStatus); err != nil {
			t.Fatalf("read the reservation: %v", err)
		}
		if reservationStatus != "paid" {
			t.Fatalf("reservation status = %q, want paid", reservationStatus)
		}
		for _, seat := range seats[:2] {
			var state string
			if err := pool.QueryRow(ctx, `SELECT state FROM cinema.event_seats WHERE id = $1`, seat).Scan(&state); err != nil {
				t.Fatalf("read seat %d: %v", seat, err)
			}
			if state != "sold" {
				t.Fatalf("seat %d state = %q, want sold", seat, state)
			}
		}

		// One ticket per seat, each carrying the reservation, the event and its seat.
		rows, err := pool.Query(ctx, `
SELECT id::text, event_id::text, event_seat_id FROM cinema.tickets
WHERE reservation_id = $1 ORDER BY event_seat_id`, approvedReservation)
		if err != nil {
			t.Fatalf("read the tickets: %v", err)
		}
		defer rows.Close()
		type ticketRow struct {
			id      string
			eventID string
			seatID  int64
		}
		var tickets []ticketRow
		for rows.Next() {
			var row ticketRow
			if err := rows.Scan(&row.id, &row.eventID, &row.seatID); err != nil {
				t.Fatalf("scan a ticket: %v", err)
			}
			tickets = append(tickets, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read the tickets: %v", err)
		}
		if len(tickets) != 2 {
			t.Fatalf("stored %d tickets, want 2", len(tickets))
		}
		for index, ticket := range tickets {
			if ticket.id != result.TicketIDs[index] {
				t.Fatalf("ticket %d = %q, want the reported %q", index, ticket.id, result.TicketIDs[index])
			}
			if ticket.eventID != fixtureEvent {
				t.Fatalf("ticket %d event = %q, want %q", index, ticket.eventID, fixtureEvent)
			}
			if ticket.seatID != seats[index] {
				t.Fatalf("ticket %d seat = %d, want %d", index, ticket.seatID, seats[index])
			}
		}

		// The outbox row is written in the same transaction and stays unpublished.
		var topic, payloadReservation string
		var published *time.Time
		if err := pool.QueryRow(ctx, `
SELECT topic, payload->>'reservationId', published_at FROM cinema.outbox
WHERE payload->>'reservationId' = $1`, approvedReservation).
			Scan(&topic, &payloadReservation, &published); err != nil {
			t.Fatalf("read the outbox row: %v", err)
		}
		if topic != checkout.OutboxTopicTicketIssued {
			t.Fatalf("outbox topic = %q, want %q", topic, checkout.OutboxTopicTicketIssued)
		}
		if payloadReservation != approvedReservation {
			t.Fatalf("outbox payload reservation = %q, want %q", payloadReservation, approvedReservation)
		}
		if published != nil {
			t.Fatalf("outbox row was already published at %v", published)
		}

		// The coordination hold was released after the commit, with the sold seats.
		if len(releaser.holdIDs) != 1 || releaser.holdIDs[0] != approvedReservation {
			t.Fatalf("released holds = %v, want the paid reservation", releaser.holdIDs)
		}
		if len(releaser.seats) != 1 || len(releaser.seats[0]) != 2 {
			t.Fatalf("released seats = %v, want the two sold seats", releaser.seats)
		}
	})

	t.Run("a replay answers from the stored sale and charges nothing", func(t *testing.T) {
		first, err := service.Charge(ctx, approvedReservation, "key-approved")
		if err != nil {
			t.Fatalf("Charge() = unexpected error %v", err)
		}
		replayed, err := service.Charge(ctx, approvedReservation, "key-approved")
		if err != nil {
			t.Fatalf("the replayed Charge() = unexpected error %v", err)
		}
		if replayed.AttemptID != first.AttemptID || replayed.PaymentStatus != checkout.PaymentApproved {
			t.Fatalf("replay = %+v, want the stored approval of attempt %q", replayed, first.AttemptID)
		}
		if len(replayed.TicketIDs) != len(first.TicketIDs) {
			t.Fatalf("replay ticket ids = %v, want the stored %v", replayed.TicketIDs, first.TicketIDs)
		}

		var tickets int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, approvedReservation).
			Scan(&tickets); err != nil {
			t.Fatalf("count the tickets: %v", err)
		}
		if tickets != 2 {
			t.Fatalf("the replay issued tickets: %d rows, want 2", tickets)
		}
		var outboxRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.outbox WHERE payload->>'reservationId' = $1`, approvedReservation).
			Scan(&outboxRows); err != nil {
			t.Fatalf("count the outbox rows: %v", err)
		}
		if outboxRows != 1 {
			t.Fatalf("the replay wrote the outbox %d times, want 1", outboxRows)
		}
	})

	t.Run("a new key on a paid reservation is already paid", func(t *testing.T) {
		_, err := service.Charge(ctx, approvedReservation, "key-second-attempt")
		if !errors.Is(err, checkout.ErrAlreadyPaid) {
			t.Fatalf("Charge() error = %v, want ErrAlreadyPaid", err)
		}
		var attempts int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id = $1`, approvedReservation).
			Scan(&attempts); err != nil {
			t.Fatalf("count the attempts: %v", err)
		}
		if attempts != 1 {
			t.Fatalf("a refused second checkout stored %d attempts, want the original 1", attempts)
		}
	})

	t.Run("a declined payment keeps the hold and issues nothing", func(t *testing.T) {
		declining, err := payment.NewSimulatedWithDeclineRule(98, payment.NewMemoryLedger())
		if err != nil {
			t.Fatalf("build the declining provider: %v", err)
		}
		decliningService := checkout.NewService(declining, store, releaser, payment.ProviderName)

		result, err := decliningService.Charge(ctx, declinedReservation, "key-declined")
		if err != nil {
			t.Fatalf("Charge() = unexpected error %v", err)
		}
		if result.PaymentStatus != checkout.PaymentDeclined || result.AttemptStatus != checkout.StatusDeclined {
			t.Fatalf("result = %+v, want a declined payment", result)
		}
		if len(result.TicketIDs) != 0 {
			t.Fatalf("a declined payment reported tickets %v, want none", result.TicketIDs)
		}

		var reservationStatus, status, outcome, reference string
		if err := pool.QueryRow(ctx, `SELECT status FROM cinema.reservations WHERE id = $1`, declinedReservation).
			Scan(&reservationStatus); err != nil {
			t.Fatalf("read the declined reservation: %v", err)
		}
		if reservationStatus != "held" {
			t.Fatalf("declined reservation status = %q, want held", reservationStatus)
		}
		if err := pool.QueryRow(ctx, `
SELECT status, outcome, provider_reference FROM cinema.payment_attempts WHERE id = $1`, result.AttemptID).
			Scan(&status, &outcome, &reference); err != nil {
			t.Fatalf("read the declined attempt: %v", err)
		}
		if status != checkout.StatusDeclined || outcome != string(payment.OutcomeDeclined) {
			t.Fatalf("stored attempt = %s/%s, want declined/declined", status, outcome)
		}
		if reference != "payment_attempt:"+result.AttemptID {
			t.Fatalf("stored provider reference = %q, want the attempt derived reference", reference)
		}

		var tickets, outboxRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, declinedReservation).
			Scan(&tickets); err != nil {
			t.Fatalf("count the declined tickets: %v", err)
		}
		if tickets != 0 {
			t.Fatalf("a declined payment stored %d tickets, want none", tickets)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.outbox WHERE payload->>'reservationId' = $1`, declinedReservation).
			Scan(&outboxRows); err != nil {
			t.Fatalf("count the declined outbox rows: %v", err)
		}
		if outboxRows != 0 {
			t.Fatalf("a declined payment stored %d outbox rows, want none", outboxRows)
		}
	})

	t.Run("an expired hold is a conflict and stores no attempt", func(t *testing.T) {
		_, err := service.Charge(ctx, expiredReservation, "key-expired")
		if !errors.Is(err, checkout.ErrHoldExpired) {
			t.Fatalf("Charge() error = %v, want ErrHoldExpired", err)
		}
		var attempts int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id = $1`, expiredReservation).
			Scan(&attempts); err != nil {
			t.Fatalf("count the expired attempts: %v", err)
		}
		if attempts != 0 {
			t.Fatalf("an expired hold stored %d attempts, want none", attempts)
		}
	})

	t.Run("an attempt charged a different amount is a conflict", func(t *testing.T) {
		// The attempt is left pending by a provider failure, so it carries the amount of
		// the moment it was created: this is the honest state a retry after a crash finds,
		// not a hand-written row.
		failing := &failingGateway{}
		failingService := checkout.NewService(failing, store, releaser, payment.ProviderName)
		_, err := failingService.Charge(ctx, mismatchReservation, "key-mismatch")
		if !errors.Is(err, checkout.ErrProviderUnavailable) {
			t.Fatalf("the failing Charge() error = %v, want ErrProviderUnavailable", err)
		}
		if failing.calls != 1 {
			t.Fatalf("provider calls = %d, want the one that failed", failing.calls)
		}

		stored := readAttempts(t, ctx, pool, mismatchReservation)
		if stored.Count != 1 || stored.Status != checkout.StatusPending || stored.Amount != fixturePriceCents*2 {
			t.Fatalf("stored attempts = %d, newest = %s/%d, want 1 pending / %d",
				stored.Count, stored.Status, stored.Amount, fixturePriceCents*2)
		}
		attemptID := stored.ID

		// The price changes underneath the pending attempt, so a retry of the same key
		// sells for another amount than the one the attempt was created with.
		raisedPrice := int64(fixturePriceCents + 200)
		if _, err := pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, fixtureEvent, raisedPrice); err != nil {
			t.Fatalf("change the event price: %v", err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(context.Background(),
				`UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, fixtureEvent, fixturePriceCents); err != nil {
				t.Errorf("restore the event price: %v", err)
			}
		})

		retryReleaser := &postgresReleaser{}
		retryGateway := &countingGateway{inner: payment.NewSimulated()}
		retryService := checkout.NewService(retryGateway, store, retryReleaser, payment.ProviderName)
		_, err = retryService.Charge(ctx, mismatchReservation, "key-mismatch")
		if !errors.Is(err, checkout.ErrAmountMismatch) {
			t.Fatalf("Charge() error = %v, want ErrAmountMismatch", err)
		}
		if errors.Is(err, checkout.ErrIdempotencyKeyReused) {
			t.Fatalf("Charge() error = %v, want the amount conflict and not the key conflict", err)
		}
		if retryGateway.calls != 0 {
			t.Fatalf("the provider was charged %d times for a disagreeing amount, want none", retryGateway.calls)
		}
		if len(retryReleaser.holdIDs) != 0 {
			t.Fatalf("a refused retry released the hold %v, want it kept", retryReleaser.holdIDs)
		}

		// The attempt is untouched: still pending, still at its original amount, still
		// without a provider reference.
		still := readAttempts(t, ctx, pool, mismatchReservation)
		if still.Count != 1 || still.ID != attemptID || still.Status != checkout.StatusPending ||
			still.Amount != fixturePriceCents*2 {
			t.Fatalf("stored attempts = %d, newest = %s/%s/%d, want 1 %s pending / %d",
				still.Count, still.ID, still.Status, still.Amount, attemptID, fixturePriceCents*2)
		}
		var reference *string
		if err := pool.QueryRow(ctx, `SELECT provider_reference FROM cinema.payment_attempts WHERE id = $1`, attemptID).
			Scan(&reference); err != nil {
			t.Fatalf("read the provider reference: %v", err)
		}
		if reference != nil {
			t.Fatalf("provider reference = %q, want the attempt left without one", *reference)
		}
	})

	// The fingerprint conflict is built through the same store the service uses: an
	// attempt whose stored fingerprint belongs to a genuinely different request.
	t.Run("a reused key with a different request is a conflict", func(t *testing.T) {
		prepared, err := store.PrepareAttempt(ctx, checkout.AttemptRequest{
			ReservationID:  fingerprintReservation,
			IdempotencyKey: "key-fingerprint",
			Fingerprint:    "a-genuinely-different-payment-request",
			Provider:       payment.ProviderName,
		})
		if err != nil {
			t.Fatalf("PrepareAttempt() = unexpected error %v", err)
		}
		if !prepared.Created {
			t.Fatal("PrepareAttempt() reused an attempt, want the first one stored")
		}

		conflictReleaser := &postgresReleaser{}
		conflictGateway := &countingGateway{inner: payment.NewSimulated()}
		conflictService := checkout.NewService(conflictGateway, store, conflictReleaser, payment.ProviderName)
		_, err = conflictService.Charge(ctx, fingerprintReservation, "key-fingerprint")
		if !errors.Is(err, checkout.ErrIdempotencyKeyReused) {
			t.Fatalf("Charge() error = %v, want ErrIdempotencyKeyReused", err)
		}
		if errors.Is(err, checkout.ErrAmountMismatch) {
			t.Fatalf("Charge() error = %v, want the key conflict and not the amount conflict", err)
		}
		if conflictGateway.calls != 0 {
			t.Fatalf("the provider was charged %d times for a reused key, want none", conflictGateway.calls)
		}
		if len(conflictReleaser.holdIDs) != 0 {
			t.Fatalf("a refused key released the hold %v, want it kept", conflictReleaser.holdIDs)
		}
		stored := readAttempts(t, ctx, pool, fingerprintReservation)
		if stored.Count != 1 || stored.ID != prepared.Attempt.ID ||
			stored.Status != checkout.StatusPending || stored.Amount != fixturePriceCents*2 {
			t.Fatalf("stored attempts = %d, newest = %s/%s/%d, want 1 %s pending / %d",
				stored.Count, stored.ID, stored.Status, stored.Amount, prepared.Attempt.ID, fixturePriceCents*2)
		}
	})

	// A second client key must never reach the provider while the first payment is
	// still open: the money of the first attempt may already be taken, and recording it
	// is nobody's job until that attempt answers.
	//
	// The guard is the partial unique index payment_attempts_open_per_reservation, so
	// it does not decay. The attempt below is aged past the two minute window the
	// service used to read and the second key is still refused, which is the crash
	// window this table exists to close: a request that died between the phase 1 commit
	// and the provider call.
	t.Run("a second key while a payment is open is a conflict at any age", func(t *testing.T) {
		failing := &failingGateway{}
		failingService := checkout.NewService(failing, store, releaser, payment.ProviderName)
		_, err := failingService.Charge(ctx, inFlightReservation, "key-in-flight-a")
		if !errors.Is(err, checkout.ErrProviderUnavailable) {
			t.Fatalf("the failing Charge() error = %v, want ErrProviderUnavailable", err)
		}
		if failing.calls != 1 {
			t.Fatalf("provider calls = %d, want the one that failed", failing.calls)
		}

		first := readAttempts(t, ctx, pool, inFlightReservation)
		if first.Count != 1 || first.Status != checkout.StatusPending || first.Amount != fixturePriceCents*2 {
			t.Fatalf("stored attempts = %d, newest = %s/%d, want 1 pending / %d",
				first.Count, first.Status, first.Amount, fixturePriceCents*2)
		}
		firstID := first.ID

		// Age the open attempt well past the removed window. Nothing in the schema looks
		// at created_at, so this changes no outcome: it only proves that the refusal is
		// structural rather than timed, and it reproduces the state a crashed request
		// leaves behind long before its hold expires.
		if _, err := pool.Exec(ctx, `
UPDATE cinema.payment_attempts SET created_at = now() - interval '3 minutes' WHERE id = $1`, firstID); err != nil {
			t.Fatalf("age the open attempt: %v", err)
		}
		if status := readAttempts(t, ctx, pool, inFlightReservation); status.Status != checkout.StatusPending {
			t.Fatalf("the aged attempt status = %q, want it still pending", status.Status)
		}

		secondReleaser := &postgresReleaser{}
		secondGateway := &countingGateway{inner: payment.NewSimulated()}
		secondService := checkout.NewService(secondGateway, store, secondReleaser, payment.ProviderName)
		_, err = secondService.Charge(ctx, inFlightReservation, "key-in-flight-b")
		if !errors.Is(err, checkout.ErrPaymentInFlight) {
			t.Fatalf("Charge() with a second key error = %v, want ErrPaymentInFlight", err)
		}
		if errors.Is(err, checkout.ErrUnavailable) {
			t.Fatalf("Charge() error = %v, want the in-flight conflict and not a retryable failure", err)
		}
		if secondGateway.calls != 0 {
			t.Fatalf("the second key reached the provider %d times, want no money taken for it", secondGateway.calls)
		}
		if len(secondReleaser.holdIDs) != 0 {
			t.Fatalf("a refused second key released the hold %v, want it kept", secondReleaser.holdIDs)
		}

		// Exactly one attempt exists: the first payment's, still pending. Nothing was
		// recorded for the second key, neither an attempt nor money.
		if stored := readAttempts(t, ctx, pool, inFlightReservation); stored.Count != 1 {
			t.Fatalf("the refused second key stored %d attempts, want the original 1", stored.Count)
		}
		if stored := readAttempts(t, ctx, pool, inFlightReservation); stored.ID != firstID || stored.Key != "key-in-flight-a" || stored.Status != checkout.StatusPending {
			t.Fatalf("stored attempt = %s/%s/%s, want %s/key-in-flight-a/pending",
				stored.ID, stored.Key, stored.Status, firstID)
		}
		refused := countRows(t, ctx, pool,
			`SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id = $1 AND idempotency_key = $2`,
			inFlightReservation, "key-in-flight-b")
		if refused != 0 {
			t.Fatalf("the refused second key stored %d attempts, want none", refused)
		}

		// Retrying with the first key resumes its own attempt instead of conflicting,
		// which is what a buyer whose request died has to do: only that key can settle
		// the attempt the provider may already hold money for.
		resuming, err := secondService.Charge(ctx, inFlightReservation, "key-in-flight-a")
		if err != nil {
			t.Fatalf("the resuming Charge() = unexpected error %v", err)
		}
		if resuming.PaymentStatus != checkout.PaymentApproved || resuming.AttemptID != firstID {
			t.Fatalf("the resume answered %+v, want the approval of attempt %s", resuming, firstID)
		}
		if secondGateway.calls != 1 {
			t.Fatalf("provider calls = %d, want the resume to charge the first attempt once", secondGateway.calls)
		}
		if len(resuming.TicketIDs) != 2 {
			t.Fatalf("the resume issued ticket ids %v, want one per seat", resuming.TicketIDs)
		}
		if status := reservationStatus(t, ctx, pool, inFlightReservation); status != "paid" {
			t.Fatalf("reservation status = %q, want paid after the resumed capture", status)
		}
	})

	// The capture's two defence-in-depth guards are only reachable from a hand-assembled
	// verdict, so they are exercised by calling the store directly: the verdict must
	// belong to the reservation the attempt belongs to, and every seat a ticket is about
	// to be issued for must actually have been marked sold.
	t.Run("a hand-assembled capture is refused by the capture guards", func(t *testing.T) {
		// One pending attempt, created the honest way: phase 1 committed, the provider
		// failed, so nothing has been decided yet.
		failing := &failingGateway{}
		failingService := checkout.NewService(failing, store, releaser, payment.ProviderName)
		if _, err := failingService.Charge(ctx, guardReservation, "key-guard"); !errors.Is(err, checkout.ErrProviderUnavailable) {
			t.Fatalf("the failing Charge() error = %v, want ErrProviderUnavailable", err)
		}
		pending := readAttempts(t, ctx, pool, guardReservation)
		if pending.Count != 1 || pending.Status != checkout.StatusPending {
			t.Fatalf("stored attempts = %d, newest = %s, want one pending", pending.Count, pending.Status)
		}

		capture := checkout.AttemptOutcome{
			AttemptID:         pending.ID,
			ReservationID:     guardReservation,
			EventID:           fixtureEvent,
			AmountCents:       pending.Amount,
			ProviderReference: "payment_attempt:" + pending.ID,
			Status:            checkout.StatusCaptured,
			Outcome:           payment.OutcomeApproved,
			Outbox: checkout.Outbox{
				ID:      "00000000-0000-0000-0000-00000000d0f5",
				Topic:   checkout.OutboxTopicTicketIssued,
				Payload: []byte(`{"reservationId":"` + guardReservation + `"}`),
			},
			Tickets: []checkout.Ticket{
				{ID: "00000000-0000-0000-0000-00000000d0f1", EventSeatID: seats[12]},
				{ID: "00000000-0000-0000-0000-00000000d0f2", EventSeatID: seats[13]},
			},
		}

		// Case 1: the verdict belongs to another reservation, whose seats are still
		// unsold, so an unguarded capture would pay that reservation with this attempt.
		// Every other part of the outcome is well formed, so only the guard can refuse it.
		forged := capture
		forged.ReservationID = declinedReservation
		forged.Seats = []int64{seats[2], seats[3]}
		forged.Outbox.Payload = []byte(`{"reservationId":"` + declinedReservation + `"}`)
		forged.Tickets = []checkout.Ticket{
			{ID: "00000000-0000-0000-0000-00000000d0f3", EventSeatID: seats[2]},
			{ID: "00000000-0000-0000-0000-00000000d0f4", EventSeatID: seats[3]},
		}
		if _, err := store.RecordOutcome(ctx, forged); !errors.Is(err, checkout.ErrUnavailable) {
			t.Fatalf("RecordOutcome() with another reservation's verdict error = %v, want ErrUnavailable", err)
		}
		if status := reservationStatus(t, ctx, pool, declinedReservation); status != "held" {
			t.Fatalf("the other reservation status = %q, want it left held", status)
		}
		if tickets := countRows(t, ctx, pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, declinedReservation); tickets != 0 {
			t.Fatalf("the refused capture issued %d tickets for the other reservation, want none", tickets)
		}

		// Case 2: one of the seats is not a seat of this event, so it is never marked
		// sold and no ticket may be issued for it.
		absent := capture
		absent.Seats = []int64{seats[12], 999999}
		if _, err := store.RecordOutcome(ctx, absent); !errors.Is(err, checkout.ErrUnavailable) {
			t.Fatalf("RecordOutcome() with a seat outside the event error = %v, want ErrUnavailable", err)
		}

		// Neither refused capture committed anything: the attempt is still pending and
		// without a reference, the reservation is still held, no ticket exists and no
		// seat was marked sold.
		still := readAttempts(t, ctx, pool, guardReservation)
		if still.Count != 1 || still.Status != checkout.StatusPending || still.Amount != pending.Amount {
			t.Fatalf("stored attempts = %d, newest = %s/%d, want the untouched pending / %d",
				still.Count, still.Status, still.Amount, pending.Amount)
		}
		var reference *string
		if err := pool.QueryRow(ctx, `SELECT provider_reference FROM cinema.payment_attempts WHERE id = $1`, pending.ID).
			Scan(&reference); err != nil {
			t.Fatalf("read the guard attempt: %v", err)
		}
		if reference != nil {
			t.Fatalf("the refused capture stored provider reference %q", *reference)
		}
		if status := reservationStatus(t, ctx, pool, guardReservation); status != "held" {
			t.Fatalf("guard reservation status = %q, want it left held", status)
		}
		if tickets := countRows(t, ctx, pool, `SELECT count(*) FROM cinema.tickets WHERE reservation_id = $1`, guardReservation); tickets != 0 {
			t.Fatalf("the refused capture issued %d tickets, want none", tickets)
		}
		if outbox := countRows(t, ctx, pool, `SELECT count(*) FROM cinema.outbox WHERE payload->>'reservationId' = $1`, guardReservation); outbox != 0 {
			t.Fatalf("the refused capture wrote %d outbox rows, want none", outbox)
		}
		if sold := countRows(t, ctx, pool, `SELECT count(*) FROM cinema.event_seats WHERE id = ANY($1) AND state = 'sold'`, seats[12:14]); sold != 0 {
			t.Fatalf("the refused capture marked %d seats sold, want none", sold)
		}
	})
}

// The payment route is mounted only against a database that carries the guard which
// makes a second charge impossible, so the closed-surface probe has to refuse a schema
// without the 0006 index. The service no longer keeps a clock of its own, so this probe
// is the only thing standing between a 0005-only database and a route that could charge
// a buyer twice: it is checked here by removing the index from a migrated database and
// putting it back.
func TestCheckoutSchemaRequiresTheOpenAttemptIndex(t *testing.T) {
	ctx := context.Background()
	pool, url := checkoutPool(t, ctx)
	defer pool.Close()

	store, err := checkout.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the checkout store: %v", err)
	}
	defer store.Close()

	present, err := store.CheckoutSchemaPresent(ctx)
	if err != nil {
		t.Fatalf("check the migrated checkout schema: %v", err)
	}
	if !present {
		t.Fatalf("%s is not migrated with 0006_one_open_payment_attempt", checkoutDatabaseURLEnv)
	}

	const recreateIndex = `
CREATE UNIQUE INDEX payment_attempts_open_per_reservation
  ON cinema.payment_attempts (reservation_id) WHERE status = 'pending'`
	restored := false
	// Cleanup restores the index even when an assertion below fails, so a broken run
	// cannot leave the fixture without the guard the other cases rely on.
	t.Cleanup(func() {
		if restored {
			return
		}
		if _, err := pool.Exec(context.Background(), recreateIndex); err != nil {
			t.Errorf("restore the open-attempt index: %v", err)
		}
	})

	if _, err := pool.Exec(ctx, `DROP INDEX cinema.payment_attempts_open_per_reservation`); err != nil {
		t.Fatalf("drop the open-attempt index: %v", err)
	}
	present, err = store.CheckoutSchemaPresent(ctx)
	if err != nil {
		t.Fatalf("check the checkout schema without the index: %v", err)
	}
	if present {
		t.Fatal("a database without payment_attempts_open_per_reservation must not advertise the checkout schema")
	}

	if _, err := pool.Exec(ctx, recreateIndex); err != nil {
		t.Fatalf("restore the open-attempt index: %v", err)
	}
	restored = true
	present, err = store.CheckoutSchemaPresent(ctx)
	if err != nil {
		t.Fatalf("check the restored checkout schema: %v", err)
	}
	if !present {
		t.Fatal("the restored index must make the checkout schema present again")
	}
}

// reservationStatus reads one reservation's stored status.
func reservationStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reservationID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM cinema.reservations WHERE id = $1`, reservationID).
		Scan(&status); err != nil {
		t.Fatalf("read the status of %s: %v", reservationID, err)
	}
	return status
}

// countRows runs one counting query and returns its single number.
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

// attemptRows is what one reservation's stored attempts look like: the number of
// them, and the only row when there is exactly one. "No second attempt was stored"
// is asserted on Count, so a refused request can never be mistaken for a stored one.
type attemptRows struct {
	Count  int
	ID     string
	Key    string
	Status string
	Amount int64
}

// readAttempts reads every attempt of one reservation in creation order. Count is how
// many exist, and the fields besides it describe the newest one — the attempt a first
// checkout stored.
func readAttempts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reservationID string) attemptRows {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT id::text, idempotency_key, status, amount_cents FROM cinema.payment_attempts
WHERE reservation_id = $1 ORDER BY created_at, id`, reservationID)
	if err != nil {
		t.Fatalf("read the attempts of %s: %v", reservationID, err)
	}
	defer rows.Close()
	stored := attemptRows{}
	for rows.Next() {
		var id, key, status string
		var amount int64
		if err := rows.Scan(&id, &key, &status, &amount); err != nil {
			t.Fatalf("scan an attempt of %s: %v", reservationID, err)
		}
		stored.Count++
		stored.ID, stored.Key, stored.Status, stored.Amount = id, key, status, amount
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the attempts of %s: %v", reservationID, err)
	}
	return stored
}

// checkoutPool opens the gated isolated database and refuses to run anywhere the
// fixtures could collide with someone else's data.
func checkoutPool(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(checkoutDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set: this test needs a migrated isolated database (make verify-m3 sets it)", checkoutDatabaseURLEnv)
	}
	requireIsolatedCheckoutDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open %s pool: %v", checkoutDatabaseURLEnv, err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Fatalf("%s is not reachable: %v", checkoutDatabaseURLEnv, err)
	}
	return pool, url
}

// requireIsolatedCheckoutDatabase refuses the development database. The test
// writes real rows, so the name has to say it is disposable.
func requireIsolatedCheckoutDatabase(t *testing.T, url string) {
	t.Helper()
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if name == "cinema" {
		t.Fatalf("%s must never point at the development database", checkoutDatabaseURLEnv)
	}
	if !strings.HasPrefix(name, "cinema_m3") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m3, got %q", checkoutDatabaseURLEnv, name)
	}
}

// seedCheckoutFixture creates the venue, film and event once, then the reservations
// each of the gated subtests pays: a live hold per case — an approval, a decline, an
// expired window, a changed price, a reused key, an in-flight payment and the capture
// guards — all of them left in the honest state their case needs by the test itself.
func seedCheckoutFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []int64 {
	t.Helper()
	cleanupCheckoutFixture(pool)

	var layoutExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, fixtureLayout).
		Scan(&layoutExists); err != nil {
		t.Fatalf("inspect the checkout venue fixture: %v", err)
	}
	if !layoutExists {
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + fixtureCinema + `','Cine Checkout','UTC')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + fixtureRoom + `','` + fixtureCinema + `','Sala Checkout')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + fixtureLayout + `','` + fixtureRoom + `',1)`},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('` + fixtureRow + `','` + fixtureLayout + `','A',1)`},
			{"seats", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
			 ('` + fixtureSeat1 + `','` + fixtureRow + `','1',1,'standard',true),
			 ('` + fixtureSeat2 + `','` + fixtureRow + `','2',2,'standard',true),
			 ('` + fixtureSeat3 + `','` + fixtureRow + `','3',3,'standard',true),
			 ('` + fixtureSeat4 + `','` + fixtureRow + `','4',4,'standard',true),
			 ('` + fixtureSeat5 + `','` + fixtureRow + `','5',5,'standard',true),
			 ('` + fixtureSeat6 + `','` + fixtureRow + `','6',6,'standard',true),
			 ('` + fixtureSeat7 + `','` + fixtureRow + `','7',7,'standard',true),
			 ('` + fixtureSeat8 + `','` + fixtureRow + `','8',8,'standard',true),
			 ('` + fixtureSeat9 + `','` + fixtureRow + `','9',9,'standard',true),
			 ('` + fixtureSeat10 + `','` + fixtureRow + `','10',10,'standard',true),
			 ('` + fixtureSeat11 + `','` + fixtureRow + `','11',11,'standard',true),
			 ('` + fixtureSeat12 + `','` + fixtureRow + `','12',12,'standard',true),
			 ('` + fixtureSeat13 + `','` + fixtureRow + `','13',13,'standard',true),
			 ('` + fixtureSeat14 + `','` + fixtureRow + `','14',14,'standard',true)`},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = '` + fixtureLayout + `'`},
		} {
			if _, err := pool.Exec(ctx, statement.sql); err != nil {
				t.Fatalf("seed %s: %v", statement.what, err)
			}
		}
	}

	var eventExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.events WHERE id = $1)`, fixtureEvent).
		Scan(&eventExists); err != nil {
		t.Fatalf("inspect the checkout event fixture: %v", err)
	}
	if !eventExists {
		if _, err := pool.Exec(ctx,
			`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1,'Checkout Fixture',90,'PG')`,
			fixtureFilm); err != nil {
			t.Fatalf("seed film: %v", err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
VALUES ($1, $2, $3, $4, now() + interval '2 days', 'on_sale')`,
			fixtureEvent, fixtureRoom, fixtureFilm, fixtureLayout); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, fixtureEvent, fixturePriceCents); err != nil {
		t.Fatalf("seed the event price: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT id FROM cinema.event_seats WHERE event_id = $1 ORDER BY id LIMIT 14`, fixtureEvent)
	if err != nil {
		t.Fatalf("read the event seats: %v", err)
	}
	defer rows.Close()
	var seats []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan an event seat: %v", err)
		}
		seats = append(seats, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the event seats: %v", err)
	}
	if len(seats) < 14 {
		t.Fatalf("the event materialized %d seats, want 14", len(seats))
	}

	// Every reservation holds two seats of its own — a seat can only be held once, so
	// the fixtures cannot share them — of the same event. The amount is therefore
	// 19,98 EUR in every case, and the declining adapter declines it by its 98
	// sentinel instead of by a special price.
	holds := []struct {
		reservation string
		seats       []int64
	}{
		{approvedReservation, seats[0:2]},
		{declinedReservation, seats[2:4]},
		{expiredReservation, seats[4:6]},
		{mismatchReservation, seats[6:8]},
		{fingerprintReservation, seats[8:10]},
		{inFlightReservation, seats[10:12]},
		{guardReservation, seats[12:14]},
	}
	for _, hold := range holds {
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservations (id, event_id, status, expires_at)
VALUES ($1, $2, 'held', now() + interval '10 minutes')`, hold.reservation, fixtureEvent); err != nil {
			t.Fatalf("seed reservation %s: %v", hold.reservation, err)
		}
		for _, seat := range hold.seats {
			if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id) VALUES ($1, $2, $3)`,
				hold.reservation, fixtureEvent, seat); err != nil {
				t.Fatalf("seed reservation seat %d: %v", seat, err)
			}
		}
	}

	// The expired hold keeps its seat rows and loses its window, which is the state a
	// sweep has not reached yet: the checkout must refuse it anyway.
	if _, err := pool.Exec(ctx,
		`UPDATE cinema.reservations SET expires_at = now() - interval '1 minute' WHERE id = $1`, expiredReservation); err != nil {
		t.Fatalf("expire the fixture hold: %v", err)
	}

	return seats
}

// cleanupCheckoutFixture removes the rows the test creates. The venue and the event
// stay: M1 makes a published layout immutable and the event is reusable.
func cleanupCheckoutFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	reservations := fixtureReservations
	for _, sql := range []string{
		`DELETE FROM cinema.outbox WHERE payload->>'reservationId' = ANY($1)`,
		`DELETE FROM cinema.tickets WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.payment_attempts WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservation_seats WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservations WHERE id::text = ANY($1)`,
	} {
		if _, err := pool.Exec(ctx, sql, reservations); err != nil && !strings.Contains(err.Error(), "does not exist") {
			// Cleanup runs before seeding too, where a failure is only diagnostic.
			continue
		}
	}
}
