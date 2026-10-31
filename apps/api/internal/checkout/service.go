package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/venue"
)

// Failures reported by the checkout service and its durable store. A caller
// classifies with errors.Is and decides the HTTP outcome itself, so every case the
// buyer can tell apart stays a distinct sentinel.
var (
	// ErrInvalidInput reports caller input rejected before anything is stored or
	// charged: a blank Idempotency-Key, a blank reservation id, or a request the
	// provider itself would refuse.
	ErrInvalidInput = errors.New("checkout: invalid input")

	// ErrReservationNotFound reports a reservation the durable store does not know.
	ErrReservationNotFound = errors.New("checkout: reservation not found")

	// ErrReservationNotPayable reports a reservation whose stored status is neither
	// held nor paid, such as a cancelled or already expired one.
	ErrReservationNotPayable = errors.New("checkout: the reservation is not awaiting payment")

	// ErrHoldExpired reports a held reservation whose window has already closed. It
	// is a conflict rather than caller error: the seats may be free again, so the
	// buyer has to select seats anew.
	ErrHoldExpired = errors.New("checkout: the hold window already closed")

	// ErrAlreadyPaid reports a reservation that already carries a captured payment.
	ErrAlreadyPaid = errors.New("checkout: the reservation is already paid")

	// ErrIdempotencyKeyReused reports one client key replayed with a different
	// payment request. It is a conflict, never a silent second charge.
	ErrIdempotencyKeyReused = errors.New("checkout: idempotency key reused with a different payment request")

	// ErrAmountMismatch reports a stored attempt whose amount disagrees with what
	// the reservation sells for now, or a provider reference already charged
	// another amount. Nothing is charged in either case.
	ErrAmountMismatch = errors.New("checkout: the amount disagrees with the stored payment attempt")

	// ErrPaymentInFlight reports that the reservation already carries an open payment
	// attempt, so this request must not store a second attempt and must not reach the
	// provider.
	//
	// The guarantee is structural rather than timed: the partial unique index
	// payment_attempts_open_per_reservation (0006) allows one open (`pending`) attempt
	// per reservation, and an open attempt means the provider may already hold the money
	// for those seats. Only the attempt that holds the client Idempotency-Key can settle
	// it, so the buyer resumes by sending the **same** key; a request with a different
	// key is refused here as `409 payment_in_flight` and nothing is stored or charged.
	//
	// There is no window to reason about: an attempt whose request crashed keeps
	// refusing a second key until the hold itself expires, because money may have moved
	// and nothing else can reconcile it. That makes persisting the key across a page
	// reload a requirement of the client, not a nicety: a browser that invents a fresh
	// key on refresh is told the truth, that a payment is already under way.
	ErrPaymentInFlight = errors.New("checkout: a payment for this reservation is already in flight")

	// ErrNothingToSell reports a reservation with no seats or a price that is not a
	// positive amount. The simulated provider needs a positive amount, and a free
	// event is a product decision this milestone does not make.
	ErrNothingToSell = errors.New("checkout: the reservation has nothing to sell")

	// ErrSeatAlreadySold reports a seat that already carries a ticket, which the
	// unique constraint on cinema.tickets refuses.
	ErrSeatAlreadySold = errors.New("checkout: the seat has already been sold")

	// ErrProviderReferenceTaken reports a provider reference that another attempt
	// already recorded. The same provider object can never be captured twice.
	ErrProviderReferenceTaken = errors.New("checkout: the provider reference was already recorded")

	// ErrProviderUnavailable reports a provider that could not answer, including an
	// answer the boundary cannot classify. It is retryable and never a success.
	ErrProviderUnavailable = errors.New("checkout: the payment provider is unavailable")

	// ErrUnavailable reports that the durable layer could not be reached. Callers
	// must surface it as a retryable failure and never as a success.
	ErrUnavailable = errors.New("checkout: checkout storage is unavailable")
)

const (
	// Attempt statuses, matching the cinema.payment_attempts status constraint.
	// StatusPending is a call that has not answered yet, which is why it is storable
	// without a provider reference or a verdict.

	// StatusPending is the attempt stored before the provider is called.
	StatusPending = "pending"
	// StatusCaptured is the attempt that took the money and marks the reservation paid.
	StatusCaptured = "captured"
	// StatusDeclined is the provider's refusal: history, not a payment.
	StatusDeclined = "declined"
	// StatusFailed is a decided failure that is not a verdict. This milestone never
	// writes it: a transport failure leaves the attempt pending, because pending is
	// exactly "no answer yet" and a retry resumes from it.
	StatusFailed = "failed"

	// Payment statuses reported to the buyer.
	PaymentApproved = "approved"
	PaymentDeclined = "declined"

	// OutboxTopicTicketIssued is the topic of the outbox row a captured payment
	// writes. The later delivery work consumes it; the sale never depends on it.
	OutboxTopicTicketIssued = "ticket.issued"

	// heldStatus and paidStatus are the cinema.reservations statuses the checkout
	// reads and writes.
	heldStatus = "held"
	paidStatus = "paid"

	// providerReferencePrefix namespaces a provider reference as one payment
	// attempt's own object, so the reference is derived from the attempt identity
	// and can never be confused with a client supplied value.
	providerReferencePrefix = "payment_attempt:"

	// maxIdempotencyKeyLength bounds the client supplied key so a record stays a
	// bounded row. It matches the reservation surface's bound.
	maxIdempotencyKeyLength = 200

	// releaseTimeout bounds the post-commit coordination release, which runs on a
	// context detached from the request so a client hanging up cannot leave the
	// Redis hold behind.
	releaseTimeout = 5 * time.Second
)

// Reservation is the durable state one checkout reads: the order, its held seats
// and the price of its event.
type Reservation struct {
	// ID is the reservation identifier, which is also the order reference and the
	// owner value of the Redis seats.
	ID string
	// EventID is the event whose seats the reservation holds.
	EventID string
	// Status is the stored cinema.reservations.status value.
	Status string
	// ExpiresAt is the durable hold deadline.
	ExpiresAt time.Time
	// Seats is the event seat set the reservation still holds, in ascending order.
	Seats []int64
	// PriceCents is cinema.events.price_cents: the v1 price of one seat.
	PriceCents int64
}

// AmountCents is the order amount: one price per event times the seats the
// reservation holds. It is computed here, from the durable rows, and never taken
// from the client.
//
// A reservation that sells nothing is refused instead of charged a zero amount:
// no seats, a price that is not positive, and an amount above the provider limit
// are all ErrNothingToSell.
func (r Reservation) AmountCents() (int64, error) {
	if len(r.Seats) == 0 {
		return 0, fmt.Errorf("%w: the reservation holds no seats", ErrNothingToSell)
	}
	if r.PriceCents <= 0 {
		return 0, fmt.Errorf("%w: the event price is %d minor units", ErrNothingToSell, r.PriceCents)
	}
	amount := r.PriceCents * int64(len(r.Seats))
	if amount > payment.MaxAmountMinorUnits {
		return 0, fmt.Errorf("%w: %d minor units exceeds the provider limit of %d",
			ErrNothingToSell, amount, payment.MaxAmountMinorUnits)
	}
	return amount, nil
}

// Attempt is one durable payment attempt: the row that makes a retried or crashed
// checkout charge once.
type Attempt struct {
	// ID is the attempt identifier, and the source of the provider reference.
	ID string
	// ReservationID is the reservation the attempt belongs to.
	ReservationID string
	// Provider names the provider that owns ProviderReference.
	Provider string
	// ProviderReference is the provider's own object once it answered. It is empty
	// until then.
	ProviderReference string
	// AmountCents is the amount the attempt was created with and the only amount it
	// can ever be charged.
	AmountCents int64
	// Outcome is the provider's verdict ("approved" or "declined"), empty while the
	// attempt is undecided.
	Outcome string
	// Status is where the attempt stands: pending, captured, declined or failed.
	Status string
	// IdempotencyKey is the client key that names this attempt in its reservation.
	IdempotencyKey string
	// Fingerprint identifies the request the key was first used with.
	Fingerprint string
	// TicketIDs are the tickets issued by a captured attempt, in seat order. They
	// are what the confirmation surface and the PDF link to.
	TicketIDs []string
}

// Decided reports whether the provider has answered for this attempt, which is
// what makes it a replay source instead of a call to resume.
func (a Attempt) Decided() bool {
	return a.Status == StatusCaptured || a.Status == StatusDeclined
}

// AttemptRequest is the input of phase 1.
type AttemptRequest struct {
	// ReservationID is the reservation being paid.
	ReservationID string
	// IdempotencyKey is the client key naming the attempt inside the reservation.
	IdempotencyKey string
	// Fingerprint identifies the request the key was used with.
	Fingerprint string
	// Provider names the provider that will own the attempt's reference.
	Provider string
}

// PreparedAttempt is phase 1's answer: the reservation as the transaction saw it,
// the attempt now stored, and whether this call stored it.
type PreparedAttempt struct {
	// Reservation is the reservation read inside the transaction, with its held
	// seats and the event price.
	Reservation Reservation
	// Attempt is the attempt stored for this reservation and key, either the one
	// this call inserted or the one already there.
	Attempt Attempt
	// Created reports whether this call inserted the attempt.
	Created bool
}

// Ticket is one ticket row the capture issues: one per held event seat.
type Ticket struct {
	// ID is the ticket identifier, which the confirmation surfaces read by.
	ID string
	// EventSeatID is the sold seat.
	EventSeatID int64
}

// Outbox is the transactional outbox row written in the capture transaction.
type Outbox struct {
	// ID is the outbox row identifier.
	ID string
	// Topic is the notification topic.
	Topic string
	// Payload is the JSON document the relay publishes.
	Payload []byte
}

// AttemptOutcome is phase 3's input: the verdict to store and, when the payment
// was approved, everything the sale consists of.
type AttemptOutcome struct {
	// AttemptID is the attempt the verdict belongs to.
	AttemptID string
	// ReservationID is the reservation being paid.
	ReservationID string
	// EventID is the reservation's event, which every ticket and seat write uses.
	EventID string
	// AmountCents is the amount that was charged, which must equal the amount the
	// stored attempt was created with.
	AmountCents int64
	// ProviderReference is the provider's object for this charge.
	ProviderReference string
	// Status is StatusCaptured or StatusDeclined.
	Status string
	// Outcome is the provider's verdict.
	Outcome payment.Outcome
	// Seats is the reservation's held seats, in ascending order.
	Seats []int64
	// Tickets is one ticket per seat, in seat order. It is empty for a decline.
	Tickets []Ticket
	// Outbox is the notification row. It is zero for a decline.
	Outbox Outbox
}

// TicketIssued is the payload of the OutboxTopicTicketIssued row: what a captured
// payment sold, so a consumer never has to read the sale back to know it.
type TicketIssued struct {
	ReservationID string   `json:"reservationId"`
	EventID       string   `json:"eventId"`
	AttemptID     string   `json:"attemptId"`
	AmountCents   int64    `json:"amountCents"`
	TicketIDs     []string `json:"ticketIds"`
	SeatIDs       []int64  `json:"seatIds"`
}

// Store is the durable layer of the checkout. Every method is exactly one
// transaction, so the caller never holds a transaction across the provider call.
type Store interface {
	// PrepareAttempt runs phase 1: in one transaction it locks and reads the
	// reservation with its held seats and event price, answers from the stored
	// attempt when the key was already used, requires the reservation to be payable
	// otherwise, and inserts the pending attempt with the amount it just computed.
	// It commits before returning.
	PrepareAttempt(ctx context.Context, request AttemptRequest) (PreparedAttempt, error)

	// RecordOutcome runs phase 3: in one transaction it stores the verdict on the
	// attempt and, when the payment was approved, marks the reservation paid, sets
	// the seats to sold, issues one ticket per seat and writes the outbox row. It
	// returns the attempt as stored, so a loser of a concurrent race answers with the
	// winner's result instead of writing a second sale.
	RecordOutcome(ctx context.Context, outcome AttemptOutcome) (Attempt, error)

	// ListStaleAttempts reads the attempts that are still pending after olderThan,
	// oldest first, each with the reservation it is paying for. It is the input of the
	// reconciler (issue #87): an attempt whose process died during the provider call is
	// the only kind a sweep must never settle on its own.
	//
	// The age is compared against the **database** clock inside the query, the same clock
	// the hold trigger and the expiry sweep use, so "stale" cannot mean one thing in Go
	// and another in PostgreSQL.
	ListStaleAttempts(ctx context.Context, olderThan time.Duration, batch int) ([]PreparedAttempt, error)
}

// HoldReleaser frees the expiring coordination hold of a paid reservation. It is
// satisfied by reservation.Coordinator, whose Release only deletes the seats the
// given hold still owns.
type HoldReleaser interface {
	Release(ctx context.Context, eventID, holdID string, eventSeatIDs []int64) error
}

// Result is the answered checkout, which is what the buyer is told and what a
// replay is answered with.
type Result struct {
	// ReservationID is the order reference.
	ReservationID string
	// AttemptID is the payment attempt the answer belongs to.
	AttemptID string
	// PaymentStatus is PaymentApproved or PaymentDeclined.
	PaymentStatus string
	// AttemptStatus is the stored attempt status: captured or declined.
	AttemptStatus string
	// AmountCents is the amount of the order.
	AmountCents int64
	// TicketIDs are the tickets of an approved payment, in seat order, and empty for
	// a decline.
	TicketIDs []string
}

// Service runs the checkout over a payment boundary, a durable store and the
// coordination layer.
type Service struct {
	gateway  payment.PaymentGateway
	store    Store
	holds    HoldReleaser
	provider string
}

// NewService builds the checkout service over the payment boundary, the durable
// store, the coordination layer and the provider name stored with every attempt.
func NewService(gateway payment.PaymentGateway, store Store, holds HoldReleaser, provider string) *Service {
	return &Service{gateway: gateway, store: store, holds: holds, provider: strings.TrimSpace(provider)}
}

// Charge pays one held reservation, in the three phases the design fixes: store
// the attempt and commit, call the provider, then store the answer and commit.
//
// A replay with the same key and fingerprint answers from the stored attempt
// without calling the provider; a pending attempt resumes from the provider call;
// a declined payment answers with the decline and changes nothing else.
func (s *Service) Charge(ctx context.Context, reservationID, idempotencyKey string) (Result, error) {
	reservation, err := validateReservationID(reservationID)
	if err != nil {
		return Result{}, err
	}
	key, err := validateIdempotencyKey(idempotencyKey)
	if err != nil {
		return Result{}, err
	}

	fingerprint := requestFingerprint(reservation)
	prepared, err := s.store.PrepareAttempt(ctx, AttemptRequest{
		ReservationID:  reservation,
		IdempotencyKey: key,
		Fingerprint:    fingerprint,
		Provider:       s.provider,
	})
	if err != nil {
		return Result{}, mapStoreError(err)
	}

	if prepared.Attempt.Fingerprint != fingerprint {
		return Result{}, fmt.Errorf("%w: %q was already used for a different payment request", ErrIdempotencyKeyReused, key)
	}
	if prepared.Attempt.Decided() {
		// A replay: the answer is already stored, so the provider is not asked again
		// and a completed sale is never reported as a conflict.
		return s.answered(ctx, prepared.Reservation, prepared.Attempt), nil
	}

	// The attempt is undecided, so it is about to be charged. It can only be charged
	// the amount it was created with: a price that changed underneath it is a
	// conflict instead of a different charge under the same provider reference.
	amount, err := prepared.Reservation.AmountCents()
	if err != nil {
		return Result{}, err
	}
	if amount != prepared.Attempt.AmountCents {
		return Result{}, fmt.Errorf("%w: attempt %s holds %d minor units and the reservation now sells for %d",
			ErrAmountMismatch, prepared.Attempt.ID, prepared.Attempt.AmountCents, amount)
	}

	answer, err := s.gateway.Charge(ctx, payment.Request{
		AmountMinorUnits: prepared.Attempt.AmountCents,
		Reference:        providerReference(prepared.Attempt.ID),
	})
	if err != nil {
		return Result{}, mapProviderError(err)
	}
	if strings.TrimSpace(answer.Reference) == "" {
		return Result{}, fmt.Errorf("%w: the provider answered without a reference", ErrProviderUnavailable)
	}

	outcome, err := recordFrom(prepared, answer)
	if err != nil {
		return Result{}, err
	}
	recorded, err := s.store.RecordOutcome(ctx, outcome)
	if err != nil {
		return Result{}, mapStoreError(err)
	}
	return s.answered(ctx, prepared.Reservation, recorded), nil
}

// ReconcileResult is what one reconciliation tick did. Every count is read from what
// actually happened, never from the size of the batch the store selected.
type ReconcileResult struct {
	// Candidates is how many stale attempts the store returned.
	Candidates int
	// Captured is how many of them the provider had already charged, which are now
	// sales with tickets.
	Captured int
	// Declined is how many the provider refused, which are history and no longer
	// pending, so the expiry sweep can release their holds.
	Declined int
	// Unresolved is how many could not be settled this tick: the provider gave no
	// answer, or the store refused the verdict. They stay pending and are retried.
	Unresolved int
}

// minReconcileAge is the smallest age bound the durable query can express. The store
// sends the bound as whole microseconds (`time.Duration.Microseconds` truncates), so a
// smaller value would arrive as zero and mean "now" — turning a live provider call into
// an abandoned one, which is the failure this worker exists to avoid. The bound is
// refused instead of silently rounded away.
const minReconcileAge = time.Microsecond

// ReconcileStaleAttempts settles the attempts a dead process left pending: for each
// one it asks the provider **again** with the attempt's own amount and its
// deterministic reference, and records the answer the provider gives (issue #87).
//
// It never assigns a verdict. That is the whole safety argument: the provider is
// idempotent per reference, so re-calling cannot charge twice, and an attempt stops
// being pending only when the provider has said what happened. Marking a stale attempt
// failed would let the sweep release the seats of a buyer the provider had charged —
// exactly the defect #82 closed.
//
// A call that gets no answer leaves the attempt pending, and therefore leaves the seats
// held: an unanswered call is not a verdict, and silent release is the one outcome this
// design refuses.
func (s *Service) ReconcileStaleAttempts(ctx context.Context, olderThan time.Duration, batch int) (ReconcileResult, error) {
	if olderThan < minReconcileAge {
		return ReconcileResult{}, fmt.Errorf("%w: reconciliation age bound must be at least one microsecond (%s), got %s",
			ErrInvalidInput, minReconcileAge, olderThan)
	}
	if batch <= 0 {
		return ReconcileResult{}, fmt.Errorf("%w: reconciliation batch must be positive, got %d", ErrInvalidInput, batch)
	}

	stale, err := s.store.ListStaleAttempts(ctx, olderThan, batch)
	if err != nil {
		return ReconcileResult{}, mapStoreError(err)
	}

	result := ReconcileResult{Candidates: len(stale)}
	for _, prepared := range stale {
		// The buyer's own request may have settled this attempt between the read and
		// here. An attempt that already carries a verdict is never sent to the provider
		// again, so a reconciliation cannot become a second sale.
		if prepared.Attempt.Decided() {
			continue
		}

		answer, err := s.gateway.Charge(ctx, payment.Request{
			AmountMinorUnits: prepared.Attempt.AmountCents,
			Reference:        providerReference(prepared.Attempt.ID),
		})
		if err != nil || strings.TrimSpace(answer.Reference) == "" {
			result.Unresolved++
			continue
		}

		outcome, err := recordFrom(prepared, answer)
		if err != nil {
			result.Unresolved++
			continue
		}

		if _, err := s.store.RecordOutcome(ctx, outcome); err != nil {
			// A row the database refuses is that row's problem: it is counted and the
			// next candidate is still tried. An unreachable store is the tick's problem
			// and is returned, so the worker logs a real failure instead of a trickle of
			// unresolvable rows.
			if isSettlementRefusal(err) {
				result.Unresolved++
				continue
			}
			return result, mapStoreError(err)
		}

		if outcome.Status == StatusCaptured {
			result.Captured++
		} else {
			result.Declined++
		}
	}
	return result, nil
}

// isSettlementRefusal reports an error that belongs to one attempt instead of to the
// whole tick. Each of these means "this attempt cannot be settled now", which is
// exactly what the reconciler reports as unresolved and retries on the next tick.
func isSettlementRefusal(err error) bool {
	for _, refusal := range []error{
		ErrReservationNotPayable,
		ErrReservationNotFound,
		ErrAlreadyPaid,
		ErrAmountMismatch,
		ErrSeatAlreadySold,
		ErrProviderReferenceTaken,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// answered builds the buyer's answer from the stored attempt and, once the sale is
// committed, frees the coordination hold of the sold seats.
//
// The release runs after the commit and its failure cannot undo the sale: the
// money was taken and the tickets exist, so reporting a failure would invite a
// retry of a completed purchase. The durable seat rows stay owned by the paid
// reservation, which is what keeps the seat unsellable even if the Redis key
// outlives its use.
func (s *Service) answered(ctx context.Context, reservation Reservation, attempt Attempt) Result {
	result := Result{
		ReservationID: reservation.ID,
		AttemptID:     attempt.ID,
		PaymentStatus: PaymentDeclined,
		AttemptStatus: attempt.Status,
		AmountCents:   attempt.AmountCents,
		TicketIDs:     attempt.TicketIDs,
	}
	if attempt.Status != StatusCaptured {
		return result
	}

	result.PaymentStatus = PaymentApproved
	if s.holds == nil || len(reservation.Seats) == 0 {
		return result
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	_ = s.holds.Release(releaseCtx, reservation.EventID, reservation.ID, reservation.Seats)
	return result
}

// recordFrom builds the phase 3 transaction from the prepared attempt and the
// provider's answer: a decline records only the verdict, an approval records the
// whole sale.
func recordFrom(prepared PreparedAttempt, answer payment.Result) (AttemptOutcome, error) {
	outcome := AttemptOutcome{
		AttemptID:         prepared.Attempt.ID,
		ReservationID:     prepared.Reservation.ID,
		EventID:           prepared.Reservation.EventID,
		AmountCents:       prepared.Attempt.AmountCents,
		ProviderReference: strings.TrimSpace(answer.Reference),
		Seats:             prepared.Reservation.Seats,
	}
	switch answer.Outcome {
	case payment.OutcomeApproved:
		outcome.Status = StatusCaptured
		outcome.Outcome = payment.OutcomeApproved
	case payment.OutcomeDeclined:
		outcome.Status = StatusDeclined
		outcome.Outcome = payment.OutcomeDeclined
		return outcome, nil
	default:
		return AttemptOutcome{}, fmt.Errorf("%w: the provider answered %q", ErrProviderUnavailable, answer.Outcome)
	}

	tickets := make([]Ticket, 0, len(prepared.Reservation.Seats))
	payload := TicketIssued{
		ReservationID: prepared.Reservation.ID,
		EventID:       prepared.Reservation.EventID,
		AttemptID:     prepared.Attempt.ID,
		AmountCents:   prepared.Attempt.AmountCents,
		TicketIDs:     make([]string, 0, len(prepared.Reservation.Seats)),
		SeatIDs:       prepared.Reservation.Seats,
	}
	for _, eventSeatID := range prepared.Reservation.Seats {
		ticket := Ticket{ID: venue.NewID(), EventSeatID: eventSeatID}
		tickets = append(tickets, ticket)
		payload.TicketIDs = append(payload.TicketIDs, ticket.ID)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return AttemptOutcome{}, fmt.Errorf("encode the ticket issued event: %w", err)
	}
	outcome.Tickets = tickets
	outcome.Outbox = Outbox{ID: venue.NewID(), Topic: OutboxTopicTicketIssued, Payload: encoded}
	return outcome, nil
}

// providerReference derives the provider's reference from the attempt's own
// identity. It is never derived from the request, so a retry of the same attempt
// re-sends the same reference and the provider answers from its own replay record
// instead of charging a second time.
func providerReference(attemptID string) string {
	return providerReferencePrefix + attemptID
}

// validateReservationID normalizes the reservation identifier.
func validateReservationID(value string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" {
		return "", fmt.Errorf("%w: the reservation id must not be empty", ErrInvalidInput)
	}
	return id, nil
}

// validateIdempotencyKey normalizes the client supplied key. A blank key is
// rejected instead of treated as "no idempotency", because that would let a retry
// charge twice.
func validateIdempotencyKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" {
		return "", fmt.Errorf("%w: Idempotency-Key header is required", ErrInvalidInput)
	}
	if len(key) > maxIdempotencyKeyLength {
		return "", fmt.Errorf("%w: Idempotency-Key must be at most %d characters", ErrInvalidInput, maxIdempotencyKeyLength)
	}
	return key, nil
}

// requestFingerprint identifies the payment request a client key was used with.
// The client payload of this route is the reservation it pays, so that is what the
// digest covers: the amount is server computed and is compared with the stored
// attempt instead, which is why a price change is ErrAmountMismatch and not a key
// conflict.
func requestFingerprint(reservationID string) string {
	sum := sha256.Sum256([]byte("payment|" + reservationID))
	return hex.EncodeToString(sum[:])
}

// mapStoreError keeps every distinct durable outcome distinct and reports anything
// else as a retryable failure, never as a success.
func mapStoreError(err error) error {
	for _, sentinel := range []error{
		ErrInvalidInput,
		ErrReservationNotFound,
		ErrReservationNotPayable,
		ErrHoldExpired,
		ErrAlreadyPaid,
		ErrIdempotencyKeyReused,
		ErrAmountMismatch,
		ErrPaymentInFlight,
		ErrNothingToSell,
		ErrSeatAlreadySold,
		ErrProviderReferenceTaken,
		ErrUnavailable,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// mapProviderError classifies a provider failure. A provider that disagrees about
// the amount of a reference is a conflict, a provider that refuses the request is
// caller error, and anything else — a refused connection, a timeout, an answer this
// boundary cannot read — is retryable and never a success.
func mapProviderError(err error) error {
	switch {
	case errors.Is(err, payment.ErrAmountMismatch):
		return fmt.Errorf("%w: %v", ErrAmountMismatch, err)
	case errors.Is(err, payment.ErrInvalidAmount),
		errors.Is(err, payment.ErrAmountTooLarge),
		errors.Is(err, payment.ErrEmptyReference),
		errors.Is(err, payment.ErrReferenceTooLong):
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	default:
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
}
