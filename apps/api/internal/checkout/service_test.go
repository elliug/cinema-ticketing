// Behaviour tests for the checkout application service. They need no Docker, no
// network and no database: the store, the coordination layer and the provider are
// all fakes, so the phase order, the idempotency rules, the decline semantics and
// the error mapping are decided in process.
//
// The tests are written against the service contract only, which is what the HTTP
// route and the durable store both have to honour. They pin the observable
// effects of each rule: the provider request, the transaction the service asked
// for, whether the coordination hold was released, and the result the buyer sees.
package checkout_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
)

// journal records the order in which the three collaborators were called, so a
// test can assert the phase order instead of only the final state. The order is
// the whole point of the design: the attempt is durable before the provider is
// called, and the hold is released only after the sale is committed.
type journal []string

func (j *journal) add(step string) { *j = append(*j, step) }

func (j journal) String() string { return strings.Join(j, ",") }

// fakeStore is an in-memory implementation of the durable contract. It is
// deliberately faithful rather than canned: the first call stores a pending
// attempt whose amount is the reservation's own amount, every later call answers
// from the stored row, and the capture writes the outcome back — exactly the
// shape the PostgreSQL store has.
type fakeStore struct {
	journal *journal

	reservation checkout.Reservation
	// prepareErr replaces the phase 1 answer with a failure.
	prepareErr error
	// recordErr replaces the phase 3 answer with a failure.
	recordErr error
	// attempt is the stored row; stored reports whether one exists.
	attempt checkout.Attempt
	stored  bool

	prepareCalls []checkout.AttemptRequest
	outcomes     []checkout.AttemptOutcome
}

func newFakeStore(j *journal, reservation checkout.Reservation) *fakeStore {
	return &fakeStore{journal: j, reservation: reservation}
}

// seed stores an attempt before the first call, so a test can start from a state
// only a crash, a concurrent buyer or a price change could produce. An empty
// fingerprint adopts the one the incoming request carries, because the seeded row
// then stands for "the row this same request had already written"; a test that
// wants a foreign payload sets one explicitly.
func (f *fakeStore) seed(attempt checkout.Attempt) {
	if attempt.ReservationID == "" {
		attempt.ReservationID = f.reservation.ID
	}
	if attempt.Provider == "" {
		attempt.Provider = testProvider
	}
	if attempt.IdempotencyKey == "" {
		attempt.IdempotencyKey = "key-1"
	}
	f.attempt = attempt
	f.stored = true
}

func (f *fakeStore) PrepareAttempt(_ context.Context, request checkout.AttemptRequest) (checkout.PreparedAttempt, error) {
	f.journal.add("prepare")
	f.prepareCalls = append(f.prepareCalls, request)
	if f.prepareErr != nil {
		return checkout.PreparedAttempt{}, f.prepareErr
	}

	if f.stored {
		if f.attempt.Fingerprint == "" {
			f.attempt.Fingerprint = request.Fingerprint
		}
		return checkout.PreparedAttempt{Reservation: f.reservation, Attempt: f.attempt}, nil
	}

	amount, err := f.reservation.AmountCents()
	if err != nil {
		return checkout.PreparedAttempt{}, err
	}
	f.attempt = checkout.Attempt{
		ID:             "attempt-1",
		ReservationID:  request.ReservationID,
		Provider:       request.Provider,
		AmountCents:    amount,
		Status:         checkout.StatusPending,
		IdempotencyKey: request.IdempotencyKey,
		Fingerprint:    request.Fingerprint,
	}
	f.stored = true
	return checkout.PreparedAttempt{Reservation: f.reservation, Attempt: f.attempt, Created: true}, nil
}

func (f *fakeStore) RecordOutcome(_ context.Context, outcome checkout.AttemptOutcome) (checkout.Attempt, error) {
	f.journal.add("record")
	f.outcomes = append(f.outcomes, outcome)
	if f.recordErr != nil {
		return checkout.Attempt{}, f.recordErr
	}

	ticketIDs := make([]string, 0, len(outcome.Tickets))
	for _, ticket := range outcome.Tickets {
		ticketIDs = append(ticketIDs, ticket.ID)
	}
	f.attempt.Status = outcome.Status
	f.attempt.Outcome = string(outcome.Outcome)
	f.attempt.ProviderReference = outcome.ProviderReference
	f.attempt.TicketIDs = ticketIDs
	if outcome.Status == checkout.StatusCaptured {
		f.reservation.Status = "paid"
	}
	return f.attempt, nil
}

// fakeGateway answers every charge with one configured outcome, recording what it
// was asked to charge.
type fakeGateway struct {
	journal  *journal
	outcome  payment.Outcome
	err      error
	requests []payment.Request
}

func (g *fakeGateway) Charge(_ context.Context, request payment.Request) (payment.Result, error) {
	g.journal.add("charge")
	g.requests = append(g.requests, request)
	if g.err != nil {
		return payment.Result{}, g.err
	}
	outcome := g.outcome
	if outcome == "" {
		outcome = payment.OutcomeApproved
	}
	return payment.Result{Outcome: outcome, Reference: strings.TrimSpace(request.Reference)}, nil
}

// fakeReleaser records the coordination releases the service asked for.
type fakeReleaser struct {
	journal *journal
	err     error
	holdIDs []string
	seats   [][]int64
}

func (r *fakeReleaser) Release(_ context.Context, eventID, holdID string, eventSeatIDs []int64) error {
	r.journal.add("release")
	r.holdIDs = append(r.holdIDs, holdID)
	r.seats = append(r.seats, append([]int64(nil), eventSeatIDs...))
	return r.err
}

// fixture is one wired service over the three fakes.
type fixture struct {
	service  *checkout.Service
	store    *fakeStore
	gateway  *fakeGateway
	releaser *fakeReleaser
	journal  journal
}

const (
	testReservationID = "11111111-1111-4111-8111-111111111111"
	testEventID       = "22222222-2222-4222-8222-222222222222"
	testProvider      = "simulated"
)

// payableReservation is a two-seat hold of a 12,13 EUR event: the amount ends in
// the sentinel the simulated provider used to decline by accident, so it proves
// the checkout charges a real ticket price instead of dodging the old rule.
func payableReservation() checkout.Reservation {
	return checkout.Reservation{
		ID:         testReservationID,
		EventID:    testEventID,
		Status:     "held",
		ExpiresAt:  time.Now().Add(10 * time.Minute),
		Seats:      []int64{21, 22},
		PriceCents: 1213,
	}
}

func newFixture(reservation checkout.Reservation, gateway *fakeGateway) *fixture {
	f := &fixture{}
	f.store = newFakeStore(&f.journal, reservation)
	if gateway == nil {
		gateway = &fakeGateway{}
	}
	gateway.journal = &f.journal
	f.gateway = gateway
	f.releaser = &fakeReleaser{journal: &f.journal}
	f.service = checkout.NewService(f.gateway, f.store, f.releaser, testProvider)
	return f
}

// TestReservationAmountIsPriceTimesSeats pins the one pricing rule of v1: the
// order amount is the event price times the seats the reservation holds, and a
// reservation that sells nothing is refused instead of charged a zero amount.
func TestReservationAmountIsPriceTimesSeats(t *testing.T) {
	cases := []struct {
		name        string
		priceCents  int64
		seats       []int64
		wantAmount  int64
		wantRefused bool
	}{
		{name: "a single seat at 12,13 EUR", priceCents: 1213, seats: []int64{21}, wantAmount: 1213},
		{name: "two seats at 12,13 EUR", priceCents: 1213, seats: []int64{21, 22}, wantAmount: 2426},
		{name: "three seats at 9,99 EUR", priceCents: 999, seats: []int64{1, 2, 3}, wantAmount: 2997},
		{name: "the cheapest seat", priceCents: 1, seats: []int64{7}, wantAmount: 1},
		{name: "a free event has nothing to sell", priceCents: 0, seats: []int64{1}, wantRefused: true},
		{name: "a negative price has nothing to sell", priceCents: -1, seats: []int64{1}, wantRefused: true},
		{name: "no seats have nothing to sell", priceCents: 1213, wantRefused: true},
		{name: "an amount over the provider limit is refused", priceCents: payment.MaxAmountMinorUnits, seats: []int64{1, 2}, wantRefused: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reservation := checkout.Reservation{PriceCents: testCase.priceCents, Seats: testCase.seats}
			amount, err := reservation.AmountCents()
			if testCase.wantRefused {
				if err == nil {
					t.Fatalf("AmountCents() = %d, want a refusal", amount)
				}
				if !errors.Is(err, checkout.ErrNothingToSell) {
					t.Fatalf("AmountCents() error = %v, want ErrNothingToSell", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("AmountCents() = unexpected error %v", err)
			}
			if amount != testCase.wantAmount {
				t.Fatalf("AmountCents() = %d, want %d", amount, testCase.wantAmount)
			}
		})
	}
}

// TestApprovedCheckoutRunsTheThreePhasesInOrder pins the transaction shape: the
// attempt is stored before the provider is called, the sale is committed before
// the coordination hold is released, and one ticket row is issued per seat with
// an outbox row that names every ticket.
func TestApprovedCheckoutRunsTheThreePhasesInOrder(t *testing.T) {
	reservation := payableReservation()
	f := newFixture(reservation, nil)

	result, err := f.service.Charge(context.Background(), testReservationID, "key-1")
	if err != nil {
		t.Fatalf("Charge() = unexpected error %v", err)
	}

	if got := f.journal.String(); got != "prepare,charge,record,release" {
		t.Fatalf("collaborator order = %s, want prepare,charge,record,release", got)
	}

	// Phase 1 stored the client key, a fingerprint, the provider name and the
	// server computed amount, never a client supplied one.
	if len(f.store.prepareCalls) != 1 {
		t.Fatalf("PrepareAttempt calls = %d, want 1", len(f.store.prepareCalls))
	}
	prepare := f.store.prepareCalls[0]
	if prepare.ReservationID != testReservationID || prepare.IdempotencyKey != "key-1" || prepare.Provider != testProvider {
		t.Fatalf("PrepareAttempt request = %+v, want the reservation, the key and the provider", prepare)
	}
	if prepare.Fingerprint == "" || strings.Contains(prepare.Fingerprint, "key-1") {
		t.Fatalf("fingerprint = %q, want an opaque digest of the request payload", prepare.Fingerprint)
	}

	// Phase 2 charged the real price, under a reference derived from the attempt
	// itself rather than from anything the client sent.
	if len(f.gateway.requests) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(f.gateway.requests))
	}
	charge := f.gateway.requests[0]
	if charge.AmountMinorUnits != 2426 {
		t.Fatalf("charged %d minor units, want 2426 (two seats at 12,13 EUR)", charge.AmountMinorUnits)
	}
	if charge.Reference != "payment_attempt:attempt-1" {
		t.Fatalf("provider reference = %q, want payment_attempt:attempt-1", charge.Reference)
	}

	// Phase 3 committed the capture, one ticket per seat and one outbox row.
	if len(f.store.outcomes) != 1 {
		t.Fatalf("RecordOutcome calls = %d, want 1", len(f.store.outcomes))
	}
	outcome := f.store.outcomes[0]
	if outcome.Status != checkout.StatusCaptured || outcome.Outcome != payment.OutcomeApproved {
		t.Fatalf("recorded outcome = %s/%s, want captured/approved", outcome.Status, outcome.Outcome)
	}
	if outcome.ProviderReference != "payment_attempt:attempt-1" {
		t.Fatalf("stored provider reference = %q, want the reference the provider answered under", outcome.ProviderReference)
	}
	if len(outcome.Tickets) != len(reservation.Seats) {
		t.Fatalf("tickets = %d, want one per held seat (%d)", len(outcome.Tickets), len(reservation.Seats))
	}
	seenTickets := map[string]bool{}
	for index, ticket := range outcome.Tickets {
		if ticket.ID == "" {
			t.Fatalf("ticket %d has no id", index)
		}
		if seenTickets[ticket.ID] {
			t.Fatalf("ticket id %q was issued twice", ticket.ID)
		}
		seenTickets[ticket.ID] = true
		if ticket.EventSeatID != reservation.Seats[index] {
			t.Fatalf("ticket %d covers seat %d, want %d", index, ticket.EventSeatID, reservation.Seats[index])
		}
	}
	if outcome.Outbox.Topic != checkout.OutboxTopicTicketIssued {
		t.Fatalf("outbox topic = %q, want %q", outcome.Outbox.Topic, checkout.OutboxTopicTicketIssued)
	}
	if outcome.Outbox.ID == "" {
		t.Fatal("the outbox row has no id")
	}
	var payload checkout.TicketIssued
	if err := json.Unmarshal(outcome.Outbox.Payload, &payload); err != nil {
		t.Fatalf("outbox payload = %s: %v", outcome.Outbox.Payload, err)
	}
	if payload.ReservationID != testReservationID || payload.EventID != testEventID || payload.AttemptID != "attempt-1" {
		t.Fatalf("outbox payload = %+v, want the reservation, event and attempt", payload)
	}
	if payload.AmountCents != 2426 {
		t.Fatalf("outbox amount = %d, want 2426", payload.AmountCents)
	}
	if len(payload.TicketIDs) != 2 || len(payload.SeatIDs) != 2 {
		t.Fatalf("outbox payload = %+v, want both ticket ids and both seats", payload)
	}

	// The release runs after the commit, compare-and-delete, with the hold id and
	// the seats of this reservation.
	if len(f.releaser.holdIDs) != 1 || f.releaser.holdIDs[0] != testReservationID {
		t.Fatalf("released holds = %v, want the reservation id", f.releaser.holdIDs)
	}
	if len(f.releaser.seats) != 1 || len(f.releaser.seats[0]) != 2 {
		t.Fatalf("released seats = %v, want the two held seats", f.releaser.seats)
	}

	if result.PaymentStatus != checkout.PaymentApproved || result.AttemptStatus != checkout.StatusCaptured {
		t.Fatalf("result = %+v, want an approved, captured payment", result)
	}
	if result.AmountCents != 2426 || result.ReservationID != testReservationID || result.AttemptID != "attempt-1" {
		t.Fatalf("result = %+v, want the amount, reservation and attempt", result)
	}
	if len(result.TicketIDs) != 2 {
		t.Fatalf("result ticket ids = %v, want both issued tickets", result.TicketIDs)
	}
	for _, id := range result.TicketIDs {
		if !seenTickets[id] {
			t.Fatalf("result ticket id %q was not issued", id)
		}
	}
}

// TestDeclinedCheckoutKeepsTheHoldAndIssuesNothing pins the decline semantics: a
// refusal is a provider answer, not a failure. The reservation stays held, no
// ticket and no outbox row exist, the coordination hold stays where it is, and the
// buyer is told the payment was declined.
//
// The provider is the real simulated adapter built with a decline rule, so the
// declined path is exercised through the same boundary production uses.
func TestDeclinedCheckoutKeepsTheHoldAndIssuesNothing(t *testing.T) {
	gateway, err := payment.NewSimulatedWithDeclineRule(payment.DefaultDeclineRemainder, payment.NewMemoryLedger())
	if err != nil {
		t.Fatalf("build the declining provider: %v", err)
	}
	// One seat at 12,13 EUR: the amount itself is the sentinel the configured rule
	// declines, so this exercises the declined path of a real ticket price.
	reservation := payableReservation()
	reservation.Seats = []int64{21}
	f := newFixture(reservation, nil)
	f.service = checkout.NewService(gateway, f.store, f.releaser, testProvider)

	result, err := f.service.Charge(context.Background(), testReservationID, "key-decline")
	if err != nil {
		t.Fatalf("Charge() of a declined payment = unexpected error %v", err)
	}

	if len(f.store.outcomes) != 1 {
		t.Fatalf("RecordOutcome calls = %d, want 1", len(f.store.outcomes))
	}
	outcome := f.store.outcomes[0]
	if outcome.Status != checkout.StatusDeclined || outcome.Outcome != payment.OutcomeDeclined {
		t.Fatalf("recorded outcome = %s/%s, want declined/declined", outcome.Status, outcome.Outcome)
	}
	if len(outcome.Tickets) != 0 {
		t.Fatalf("a declined payment recorded %d tickets, want none", len(outcome.Tickets))
	}
	if outcome.Outbox.ID != "" || outcome.Outbox.Topic != "" || len(outcome.Outbox.Payload) != 0 {
		t.Fatalf("a declined payment wrote an outbox row: %+v", outcome.Outbox)
	}
	if f.store.reservation.Status != "held" {
		t.Fatalf("reservation status = %q, want it left held", f.store.reservation.Status)
	}
	if len(f.releaser.holdIDs) != 0 {
		t.Fatalf("a declined payment released the coordination hold %v, want it kept", f.releaser.holdIDs)
	}
	if result.PaymentStatus != checkout.PaymentDeclined || result.AttemptStatus != checkout.StatusDeclined {
		t.Fatalf("result = %+v, want a declined payment", result)
	}
	if len(result.TicketIDs) != 0 {
		t.Fatalf("a declined payment reported ticket ids %v, want none", result.TicketIDs)
	}
}

// TestReplayAnswersFromTheStoredResult pins the replay rule: the same client key
// with the same request returns the stored result and never calls the provider
// again, so a retried purchase cannot be charged twice — not even after the
// reservation became paid, which a naive status check would report as a conflict.
func TestReplayAnswersFromTheStoredResult(t *testing.T) {
	f := newFixture(payableReservation(), nil)

	first, err := f.service.Charge(context.Background(), testReservationID, "key-1")
	if err != nil {
		t.Fatalf("the first Charge() = unexpected error %v", err)
	}
	// The sale is committed: the store now holds a captured attempt and the
	// reservation is paid.
	f.store.reservation.Status = "paid"

	chargesBefore := len(f.gateway.requests)
	replay, err := f.service.Charge(context.Background(), testReservationID, "key-1")
	if err != nil {
		t.Fatalf("the replayed Charge() = unexpected error %v", err)
	}

	if len(f.gateway.requests) != chargesBefore {
		t.Fatalf("the replay charged the provider again (%d calls), want the stored result", len(f.gateway.requests))
	}
	if len(f.store.outcomes) != 1 {
		t.Fatalf("the replay recorded a second outcome, want the stored one")
	}
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay = %+v, want the stored result %+v", replay, first)
	}
}

// TestReusedKeyWithADifferentRequestIsAConflict pins that one key cannot answer
// for two different payment requests: the fingerprint stored with the attempt is
// compared before anything is charged.
func TestReusedKeyWithADifferentRequestIsAConflict(t *testing.T) {
	f := newFixture(payableReservation(), nil)
	f.store.seed(checkout.Attempt{
		ID:            "attempt-1",
		AmountCents:   2426,
		Status:        checkout.StatusPending,
		Fingerprint:   "a-different-payload",
		ReservationID: testReservationID,
	})

	_, err := f.service.Charge(context.Background(), testReservationID, "key-1")
	if err == nil {
		t.Fatal("Charge() with a reused key succeeded, want a conflict")
	}
	if !errors.Is(err, checkout.ErrIdempotencyKeyReused) {
		t.Fatalf("Charge() error = %v, want ErrIdempotencyKeyReused", err)
	}
	if len(f.gateway.requests) != 0 {
		t.Fatalf("the provider was charged %d times for a reused key, want none", len(f.gateway.requests))
	}
	if len(f.store.outcomes) != 0 {
		t.Fatal("a reused key wrote an outcome, want nothing written")
	}
}

// TestPendingAttemptResumesFromTheProviderCall pins the crash window: an attempt
// stored before the provider answered is not duplicated on retry, and the retry
// charges under the same attempt derived reference.
func TestPendingAttemptResumesFromTheProviderCall(t *testing.T) {
	f := newFixture(payableReservation(), nil)
	f.store.seed(checkout.Attempt{
		ID:            "attempt-7",
		AmountCents:   2426,
		Status:        checkout.StatusPending,
		ReservationID: testReservationID,
	})

	result, err := f.service.Charge(context.Background(), testReservationID, "key-1")
	if err != nil {
		t.Fatalf("Charge() = unexpected error %v", err)
	}

	if len(f.gateway.requests) != 1 {
		t.Fatalf("provider calls = %d, want the resumed attempt to charge once", len(f.gateway.requests))
	}
	if f.gateway.requests[0].Reference != "payment_attempt:attempt-7" {
		t.Fatalf("the resume used reference %q, want the stored attempt's payment_attempt:attempt-7",
			f.gateway.requests[0].Reference)
	}
	if f.gateway.requests[0].AmountMinorUnits != 2426 {
		t.Fatalf("the resume charged %d minor units, want the stored 2426", f.gateway.requests[0].AmountMinorUnits)
	}
	if len(f.store.prepareCalls) != 1 {
		t.Fatalf("PrepareAttempt calls = %d, want the resume to reuse the stored attempt", len(f.store.prepareCalls))
	}
	if len(f.store.outcomes) != 1 {
		t.Fatalf("RecordOutcome calls = %d, want exactly one for the resumed attempt", len(f.store.outcomes))
	}
	if result.AttemptID != "attempt-7" {
		t.Fatalf("the resume answered for attempt %q, want the stored attempt attempt-7", result.AttemptID)
	}
	if result.PaymentStatus != checkout.PaymentApproved {
		t.Fatalf("result = %+v, want an approved payment", result)
	}
}

// TestAmountMismatchIsAConflict pins that a stored attempt can only ever be
// charged the amount it was created with: if the price changes while an attempt
// is pending, the retry is a conflict instead of a silent different charge.
func TestAmountMismatchIsAConflict(t *testing.T) {
	f := newFixture(payableReservation(), nil)
	f.store.seed(checkout.Attempt{
		ID:            "attempt-1",
		AmountCents:   payableReservation().PriceCents, // one seat's worth, not two
		Status:        checkout.StatusPending,
		ReservationID: testReservationID,
	})

	_, err := f.service.Charge(context.Background(), testReservationID, "key-1")
	if err == nil {
		t.Fatal("Charge() with a disagreeing amount succeeded, want a conflict")
	}
	if !errors.Is(err, checkout.ErrAmountMismatch) {
		t.Fatalf("Charge() error = %v, want ErrAmountMismatch", err)
	}
	if len(f.gateway.requests) != 0 {
		t.Fatalf("the provider was charged %d times for a disagreeing amount, want none", len(f.gateway.requests))
	}
}

// TestStoreFailuresAreReportedDistinctly pins that every durable refusal keeps its
// own meaning and that an unclassified failure is retryable rather than a success.
func TestStoreFailuresAreReportedDistinctly(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"an already paid reservation", fmt.Errorf("%w: %s", checkout.ErrAlreadyPaid, testReservationID), checkout.ErrAlreadyPaid},
		{"an expired hold", fmt.Errorf("%w: %s", checkout.ErrHoldExpired, testReservationID), checkout.ErrHoldExpired},
		{"a cancelled reservation", fmt.Errorf("%w: %s is cancelled", checkout.ErrReservationNotPayable, testReservationID), checkout.ErrReservationNotPayable},
		{"an unknown reservation", fmt.Errorf("%w: %s", checkout.ErrReservationNotFound, testReservationID), checkout.ErrReservationNotFound},
		{"nothing to sell", fmt.Errorf("%w: the event price is 0 minor units", checkout.ErrNothingToSell), checkout.ErrNothingToSell},
		{"a seat sold twice", fmt.Errorf("%w: seat 21", checkout.ErrSeatAlreadySold), checkout.ErrSeatAlreadySold},
		{"a payment already in flight", fmt.Errorf("%w: attempt %s is pending", checkout.ErrPaymentInFlight, "attempt-1"), checkout.ErrPaymentInFlight},
		{"a taken provider reference", fmt.Errorf("%w: %s", checkout.ErrProviderReferenceTaken, "payment_attempt:x"), checkout.ErrProviderReferenceTaken},
		{"a retryable storage failure", fmt.Errorf("%w: conn closed", checkout.ErrUnavailable), checkout.ErrUnavailable},
		{"an unclassified failure", errors.New("unexpected"), checkout.ErrUnavailable},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(payableReservation(), nil)
			f.store.prepareErr = testCase.err

			result, err := f.service.Charge(context.Background(), testReservationID, "key-1")
			if err == nil {
				t.Fatalf("Charge() = %+v, want a failure", result)
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Charge() error = %v, want %v", err, testCase.want)
			}
			if len(f.gateway.requests) != 0 {
				t.Fatalf("the provider was charged although phase 1 failed")
			}
		})
	}
}

// TestPaymentInFlightBlocksASecondCharge pins the guard that keeps the money safe
// when a buyer retries with a different key: while another attempt of the same
// reservation is open, a second key is refused with its own conflict instead of
// being charged, because the first payment may already be with the provider. The
// guarantee is the partial unique index payment_attempts_open_per_reservation, which
// the store enforces at any age of the open attempt (pinned against PostgreSQL in
// postgres_test.go); here the service only has to keep the refusal distinct, never
// retried and never reported as another conflict.
func TestPaymentInFlightBlocksASecondCharge(t *testing.T) {
	f := newFixture(payableReservation(), nil)
	f.store.prepareErr = fmt.Errorf("%w: attempt attempt-1 is still pending", checkout.ErrPaymentInFlight)

	result, err := f.service.Charge(context.Background(), testReservationID, "key-b")
	if err == nil {
		t.Fatalf("Charge() = %+v, want a conflict while another payment is in flight", result)
	}
	if !errors.Is(err, checkout.ErrPaymentInFlight) {
		t.Fatalf("Charge() error = %v, want ErrPaymentInFlight", err)
	}
	if len(f.gateway.requests) != 0 {
		t.Fatalf("the provider was charged %d times while another payment was in flight, want none",
			len(f.gateway.requests))
	}
	if len(f.store.outcomes) != 0 {
		t.Fatal("a refused second key wrote an outcome, want nothing written")
	}
	if len(f.releaser.holdIDs) != 0 {
		t.Fatalf("a refused second key released the hold %v, want it kept", f.releaser.holdIDs)
	}
}

// TestProviderFailuresAreRetryableAndNeverASuccess pins the provider side: a
// transport failure is retryable and leaves the attempt undecided, a provider
// amount conflict is a conflict, and an answer the boundary cannot classify is
// never reported as a success.
func TestProviderFailuresAreRetryableAndNeverASuccess(t *testing.T) {
	t.Run("a transport failure is retryable and stores nothing", func(t *testing.T) {
		f := newFixture(payableReservation(), nil)
		f.gateway.err = errors.New("dial tcp 10.0.0.1:443: connect: connection refused")

		result, err := f.service.Charge(context.Background(), testReservationID, "key-1")
		if err == nil {
			t.Fatalf("Charge() = %+v, want a retryable failure", result)
		}
		if !errors.Is(err, checkout.ErrProviderUnavailable) {
			t.Fatalf("Charge() error = %v, want ErrProviderUnavailable", err)
		}
		if len(f.store.outcomes) != 0 {
			t.Fatal("a failed provider call stored an outcome, want the attempt left undecided")
		}
		if len(f.releaser.holdIDs) != 0 {
			t.Fatal("a failed provider call released the hold, want it kept")
		}
	})

	t.Run("a provider amount conflict is a conflict", func(t *testing.T) {
		f := newFixture(payableReservation(), nil)
		f.gateway.err = payment.ErrAmountMismatch

		_, err := f.service.Charge(context.Background(), testReservationID, "key-1")
		if !errors.Is(err, checkout.ErrAmountMismatch) {
			t.Fatalf("Charge() error = %v, want ErrAmountMismatch", err)
		}
	})

	t.Run("a provider that answers nothing is not a success", func(t *testing.T) {
		f := newFixture(payableReservation(), nil)
		f.gateway.outcome = payment.Outcome("maybe")

		result, err := f.service.Charge(context.Background(), testReservationID, "key-1")
		if err == nil {
			t.Fatalf("Charge() = %+v, want a failure for an unknown outcome", result)
		}
		if !errors.Is(err, checkout.ErrProviderUnavailable) {
			t.Fatalf("Charge() error = %v, want ErrProviderUnavailable", err)
		}
	})
}

// TestMissingIdempotencyKeyIsRefusedBeforeAnyWork pins that the key is required:
// without it a retry could charge twice, so nothing is stored and nothing is
// charged.
func TestMissingIdempotencyKeyIsRefusedBeforeAnyWork(t *testing.T) {
	for _, key := range []string{"", "   ", strings.Repeat("k", 201)} {
		t.Run(fmt.Sprintf("key of %d bytes", len(key)), func(t *testing.T) {
			f := newFixture(payableReservation(), nil)

			_, err := f.service.Charge(context.Background(), testReservationID, key)
			if !errors.Is(err, checkout.ErrInvalidInput) {
				t.Fatalf("Charge(%q) error = %v, want ErrInvalidInput", key, err)
			}
			if got := f.journal.String(); got != "" {
				t.Fatalf("collaborators called (%s), want nothing for a rejected request", got)
			}
		})
	}

	t.Run("an empty reservation id is refused", func(t *testing.T) {
		f := newFixture(payableReservation(), nil)
		if _, err := f.service.Charge(context.Background(), "  ", "key-1"); !errors.Is(err, checkout.ErrInvalidInput) {
			t.Fatalf("Charge() error = %v, want ErrInvalidInput", err)
		}
		if got := f.journal.String(); got != "" {
			t.Fatalf("collaborators called (%s), want nothing for a rejected request", got)
		}
	})
}

// TestReleasedHoldFailureCannotUndoTheSale pins that the coordination release is a
// best-effort step after the commit: a Redis failure must not turn a captured
// payment into a reported failure, because the money was taken and the tickets
// exist.
func TestReleasedHoldFailureCannotUndoTheSale(t *testing.T) {
	f := newFixture(payableReservation(), nil)
	f.releaser.err = errors.New("redis: connection closed")

	result, err := f.service.Charge(context.Background(), testReservationID, "key-1")
	if err != nil {
		t.Fatalf("Charge() = unexpected error %v for a failed release", err)
	}
	if result.PaymentStatus != checkout.PaymentApproved {
		t.Fatalf("result = %+v, want the committed approval", result)
	}
}
