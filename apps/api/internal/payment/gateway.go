package payment

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Failures reported by the boundary. A caller classifies with errors.Is and
// decides the HTTP outcome itself; the checkout reports the validation failures
// as caller error and ErrAmountMismatch as a conflict, never as a retryable
// failure.
var (
	// ErrInvalidAmount reports a charge whose amount is not a positive number of
	// minor units. A zero or negative amount is caller error: there is nothing to
	// sell and nothing to charge.
	ErrInvalidAmount = errors.New("payment: amount must be a positive number of minor units")

	// ErrAmountTooLarge reports a charge above MaxAmountMinorUnits. It is kept
	// apart from ErrInvalidAmount so a caller can tell "you sent nonsense" from
	// "you sent more money than this provider accepts".
	ErrAmountTooLarge = errors.New("payment: amount exceeds the provider limit")

	// ErrEmptyReference reports a charge with no provider reference. A blank
	// reference is refused instead of being generated here, because replay safety
	// depends on the caller sending the same reference again.
	ErrEmptyReference = errors.New("payment: a provider reference is required")

	// ErrReferenceTooLong reports a provider reference above MaxReferenceLength.
	ErrReferenceTooLong = errors.New("payment: the provider reference is too long")

	// ErrAmountMismatch reports a provider reference that was already charged a
	// different amount. It is a conflict, never a silent second charge: the stored
	// outcome stays untouched and the caller has to use a new reference.
	ErrAmountMismatch = errors.New("payment: the provider reference was already charged a different amount")
)

const (
	// MaxAmountMinorUnits is the largest amount, in minor units, the simulated
	// provider accepts (1,000,000.00). A larger amount is refused instead of
	// silently clamped, so the boundary never approves an amount it did not see.
	MaxAmountMinorUnits int64 = 100_000_000

	// MaxReferenceLength bounds a provider reference, so the replay record and the
	// identifier the checkout persists stay bounded whatever the caller sends. It
	// matches the idempotency key bound of the reservation surface, because the
	// payment reference is derived from that key.
	MaxReferenceLength = 200
)

// Outcome is the provider's answer for one charge. A declined charge is an
// outcome, not an error: the provider worked, the money was refused.
type Outcome string

const (
	// OutcomeApproved reports that the charge was accepted.
	OutcomeApproved Outcome = "approved"

	// OutcomeDeclined reports that the charge was refused and no money moved.
	OutcomeDeclined Outcome = "declined"
)

// Request is one charge submitted to the provider.
type Request struct {
	// AmountMinorUnits is the money to charge in the currency's minor units, so
	// cents for EUR. It is computed and authorized server side; the client never
	// supplies it.
	AmountMinorUnits int64

	// Reference is the caller supplied provider reference. It is the provider's own
	// idempotency key: charging the same reference again replays the stored
	// outcome instead of charging twice, so the caller must persist it and send the
	// same value when it retries.
	Reference string
}

// Result is the provider's answer for an accepted request.
type Result struct {
	// Outcome is approved or declined.
	Outcome Outcome

	// Reference is the provider reference the charge was recorded under: the
	// trimmed form of Request.Reference. The caller persists this value, so a later
	// replay can only ever address the same charge.
	Reference string
}

// PaymentGateway is the replaceable payment boundary the checkout calls. A real provider
// later implements this same interface; nothing above the boundary knows which
// implementation answered.
//
// Implementations must treat one Reference as one charge: the second charge of a
// reference returns the stored outcome, and the same reference with a different
// amount is ErrAmountMismatch rather than a second charge.
type PaymentGateway interface {
	// Charge charges amount request.AmountMinorUnits under request.Reference. A
	// declined charge returns a Result with OutcomeDeclined and a nil error.
	Charge(ctx context.Context, request Request) (Result, error)
}

// validateRequest checks the request before anything is charged and returns the
// normalized provider reference.
//
// The checks are fail closed and ordered cheapest first, and they run before the
// replay lookup, so a rejected request can never record, spend or overwrite a
// provider reference.
func validateRequest(request Request) (string, error) {
	if request.AmountMinorUnits <= 0 {
		return "", fmt.Errorf("%w: %d is not a positive amount", ErrInvalidAmount, request.AmountMinorUnits)
	}
	if request.AmountMinorUnits > MaxAmountMinorUnits {
		return "", fmt.Errorf("%w: %d exceeds the maximum of %d minor units",
			ErrAmountTooLarge, request.AmountMinorUnits, MaxAmountMinorUnits)
	}

	reference := strings.TrimSpace(request.Reference)
	if reference == "" {
		return "", fmt.Errorf("%w: the provider reference is blank", ErrEmptyReference)
	}
	if len(reference) > MaxReferenceLength {
		return "", fmt.Errorf("%w: %d bytes exceeds the %d byte limit",
			ErrReferenceTooLong, len(reference), MaxReferenceLength)
	}
	return reference, nil
}
