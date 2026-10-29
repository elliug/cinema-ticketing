package payment

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Errors reported by the simulated adapter itself.
var (
	// ErrNotConfigured reports that the adapter was never built by a constructor.
	// It is a fail-closed answer, never a charge: a caller that forgot the
	// constructor learns about it instead of getting a silent approval.
	ErrNotConfigured = errors.New("payment: the simulated provider is not configured")

	// ErrInvalidRule reports a decline sentinel outside the two minor units it can
	// match.
	ErrInvalidRule = errors.New("payment: the decline sentinel is not a two-digit remainder")

	// ErrInvalidLedger reports a nil replay ledger. Sharing a ledger is the only
	// way two adapter instances agree on a replay, so a missing one is refused
	// rather than replaced by a private map the caller cannot see.
	ErrInvalidLedger = errors.New("payment: the replay ledger is required")
)

const (
	// DefaultDeclineRemainder is the sentinel of the documented simulated outcome
	// rule: an amount whose two least significant minor units are 13 is declined
	// (13 cents), and every other amount is approved. It is fixed, not random, so
	// a test can select the outcome by choosing the amount, and the same amount
	// always produces the same answer.
	DefaultDeclineRemainder int64 = 13

	// minorUnitsPerMajorUnit is the base of the remainder the sentinel matches.
	minorUnitsPerMajorUnit int64 = 100
)

// Charge is one recorded simulated charge: the provider's own idempotency record
// of a reference.
type Charge struct {
	// Reference is the provider reference the charge was recorded under.
	Reference string
	// AmountMinorUnits is the amount that was charged.
	AmountMinorUnits int64
	// Outcome is the answer the provider gave.
	Outcome Outcome
}

// MemoryLedger is the in-memory replay ledger the simulated provider records its
// charges in.
//
// It is deliberately a value the caller owns rather than state hidden inside the
// adapter: two adapters built over the same ledger agree on every replay, which is
// what a restarted process or a second adapter instance needs. The ledger is
// process local, so the checkout cannot rely on it for durability; the provider
// reference and its outcome are persisted by the checkout in PostgreSQL, and this
// ledger only guarantees that the provider itself never charges a reference twice
// while the process serves.
type MemoryLedger struct {
	mu      sync.Mutex
	charges map[string]Charge
}

// NewMemoryLedger returns an empty replay ledger.
func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{charges: make(map[string]Charge)}
}

// A zero-value MemoryLedger is an empty ledger ready to use: the map is created on
// first use under the lock. Only a nil pointer is refused, because a nil ledger
// cannot keep the replay record the boundary promises.
//
// record stores charge and returns the record now held for its reference, which is
// either the new one or the one already stored.
//
// The lookup and the write happen under one lock, so concurrent charges of the
// same reference cannot both record: one stores and the other replays, and each
// caller sees exactly one outcome. A different amount for a used reference is
// ErrAmountMismatch and leaves the stored charge untouched.
func (l *MemoryLedger) record(charge Charge) (Charge, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// A zero-value ledger is usable, not broken: the map is created on first use
	// under the lock, so the struct can be embedded without a constructor.
	if l.charges == nil {
		l.charges = make(map[string]Charge)
	}

	if stored, found := l.charges[charge.Reference]; found {
		if stored.AmountMinorUnits != charge.AmountMinorUnits {
			return Charge{}, fmt.Errorf("%w: reference %q was charged %d minor units, not %d",
				ErrAmountMismatch, charge.Reference, stored.AmountMinorUnits, charge.AmountMinorUnits)
		}
		return stored, nil
	}

	l.charges[charge.Reference] = charge
	return charge, nil
}

// Simulated is the simulated payment provider: a deterministic outcome rule over a
// replay ledger, with no I/O.
//
// A real provider replaces only this adapter. It implements the same PaymentGateway
// boundary, so nothing above the boundary changes, and it keeps no credentials and
// makes no network calls, which is what leaves v1 without a live payment flow.
type Simulated struct {
	ledger           *MemoryLedger
	declineRemainder int64
}

// Compile-time proof that the adapter satisfies the boundary it is built for.
var _ PaymentGateway = (*Simulated)(nil)

// NewSimulated builds the simulated provider with the documented rule
// (DefaultDeclineRemainder) and a private replay ledger.
func NewSimulated() *Simulated {
	return &Simulated{ledger: NewMemoryLedger(), declineRemainder: DefaultDeclineRemainder}
}

// NewSimulatedWithLedger builds the simulated provider with a chosen decline
// sentinel over a caller supplied ledger, so several adapter instances share one
// replay record and a test can select which amounts decline.
//
// declineRemainder must be a two-digit remainder, so within 0 and 99: it is
// compared with the two least significant minor units of the amount.
//
// A nil ledger is ErrInvalidLedger; a zero-value MemoryLedger is accepted and used
// as an empty ledger.
func NewSimulatedWithLedger(declineRemainder int64, ledger *MemoryLedger) (*Simulated, error) {
	if declineRemainder < 0 || declineRemainder >= minorUnitsPerMajorUnit {
		return nil, fmt.Errorf("%w: %d is outside 0 and 99", ErrInvalidRule, declineRemainder)
	}
	if ledger == nil {
		return nil, fmt.Errorf("%w: pass NewMemoryLedger() to build a private one", ErrInvalidLedger)
	}
	return &Simulated{ledger: ledger, declineRemainder: declineRemainder}, nil
}

// Charge charges request under its provider reference.
//
// The steps are the documented ones, and the order is what makes a rejection
// harmless: validate first, so a rejected request never touches the ledger, then
// record the charge, which either stores the deterministic outcome or replays the
// one already stored for that reference.
//
// The context is accepted because the boundary a real provider implements is
// cancellation aware; this adapter performs no I/O, so it never consults it.
func (s *Simulated) Charge(ctx context.Context, request Request) (Result, error) {
	if s == nil || s.ledger == nil {
		return Result{}, fmt.Errorf("%w: the adapter was not built by NewSimulated", ErrNotConfigured)
	}

	reference, err := validateRequest(request)
	if err != nil {
		return Result{}, err
	}

	stored, err := s.ledger.record(Charge{
		Reference:        reference,
		AmountMinorUnits: request.AmountMinorUnits,
		Outcome:          s.outcomeFor(request.AmountMinorUnits),
	})
	if err != nil {
		return Result{}, err
	}

	return Result{Outcome: stored.Outcome, Reference: stored.Reference}, nil
}

// outcomeFor applies the deterministic rule: an amount whose two least significant
// minor units equal the configured sentinel is declined, every other amount is
// approved. The amount is already validated as positive, so no sign handling is
// needed and the same amount always yields the same outcome.
func (s *Simulated) outcomeFor(amountMinorUnits int64) Outcome {
	if amountMinorUnits%minorUnitsPerMajorUnit == s.declineRemainder {
		return OutcomeDeclined
	}
	return OutcomeApproved
}
