// Package payment is the M3 payment integration boundary.
//
// The checkout calls one small interface, PaymentGateway, with a Request carrying the
// money to charge in minor units and a caller supplied provider reference, and
// receives a Result carrying an outcome and the reference the charge was recorded
// under. The simulated provider implements it; a real provider later replaces only
// that adapter, because nothing above the boundary knows which implementation
// answered (design S8).
//
// # Why the reference is supplied by the caller
//
// The provider reference is the provider's own idempotency key. The caller derives
// it from the payment attempt it already persists, sends it with every retry, and
// stores it with the outcome, so a retried or replayed payment addresses exactly
// one charge at the provider no matter how often it is sent.
//
// # Determinism
//
// The simulated outcome is deterministic for a given input, never random, so tests
// can select it: DefaultDeclineRemainder describes the rule (an amount whose two
// least significant minor units are 13 is declined, every other amount is
// approved). NewSimulatedWithLedger lets a test choose another sentinel. A declined
// charge is a Result with OutcomeDeclined and a nil error, because the provider
// worked and the money was refused.
//
// # Replay
//
// Replay state lives in a MemoryLedger rather than inside the adapter, so two
// adapter instances built over the same ledger agree, and so does a caller that
// keeps one ledger across a restart of its own wiring. Charging a used reference
// returns the stored outcome without charging again; charging a used reference with
// a different amount is ErrAmountMismatch. The ledger is process local, so the
// checkout, not this package, owns the durable record.
//
// # Fail closed
//
// A non-positive amount, an amount above MaxAmountMinorUnits, a blank reference and
// a reference above MaxReferenceLength are refused with distinct errors before any
// charge is recorded, so a rejected request can neither spend nor overwrite a
// provider reference. An adapter that was never built by a constructor answers
// ErrNotConfigured instead of pretending to charge.
//
// # No live payment flow
//
// This package imports no HTTP client and no credential provider, and it reads no
// credentials or environment: it is the M3 acceptance item "No real payment
// credentials or live payment flow exist in v1" expressed as code. A real provider
// replaces only the adapter behind PaymentGateway.
package payment
