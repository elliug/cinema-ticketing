// Behaviour tests for the simulated payment provider. They need no Docker and no
// network: the outcome rule, the replay ledger and the validation are all decided
// in process.
//
// The tests are deliberately written against the boundary only (`Gateway`,
// `Request`, `Result` and the exported error sentinels), so they pin what the
// checkout will actually observe. Replay is exercised through two independently
// constructed adapters that share one ledger, which is the property T3 relies on
// when it re-sends the same provider reference after a crash.
package payment_test

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cinema.local/ticketing/apps/api/internal/payment"
)

// charge is a small helper that charges through the boundary and fails the test
// on an unexpected error, so each test states only what it is about.
func charge(t *testing.T, gateway payment.PaymentGateway, reference string, amountMinorUnits int64) payment.Result {
	t.Helper()
	result, err := gateway.Charge(context.Background(), payment.Request{
		AmountMinorUnits: amountMinorUnits,
		Reference:        reference,
	})
	if err != nil {
		t.Fatalf("Charge(%q, %d) = unexpected error %v", reference, amountMinorUnits, err)
	}
	return result
}

// TestRuleIsDeterministicForAGivenInput pins the documented outcome rule: an
// amount whose two least significant minor units equal the configured sentinel is
// declined, every other amount is approved, and the same input always produces
// the same outcome.
//
// Determinism is proved with a fresh reference per call, so no call can be
// answered from the replay ledger: each one reaches the rule itself.
func TestRuleIsDeterministicForAGivenInput(t *testing.T) {
	cases := []struct {
		name             string
		amountMinorUnits int64
		want             payment.Outcome
	}{
		{"an amount ending in 13 is declined", 1213, payment.OutcomeDeclined},
		{"the sentinel itself is declined", 13, payment.OutcomeDeclined},
		{"a larger amount ending in 13 is declined", 99999913, payment.OutcomeDeclined},
		{"an amount ending in 00 is approved", 1200, payment.OutcomeApproved},
		{"the smallest amount is approved", 1, payment.OutcomeApproved},
		{"an amount ending in 12 is approved", 1212, payment.OutcomeApproved},
		{"the largest amount is approved", payment.MaxAmountMinorUnits, payment.OutcomeApproved},
	}

	const repeats = 25
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := payment.NewSimulated()
			for i := 0; i < repeats; i++ {
				reference := fmt.Sprintf("attempt-%d-%d", tc.amountMinorUnits, i)
				result := charge(t, gateway, reference, tc.amountMinorUnits)
				if result.Outcome != tc.want {
					t.Fatalf("Charge(%q, %d) outcome = %q, want %q for every call",
						reference, tc.amountMinorUnits, result.Outcome, tc.want)
				}
				if result.Reference != reference {
					t.Fatalf("Charge(%q, %d) reference = %q, want %q",
						reference, tc.amountMinorUnits, result.Reference, reference)
				}
			}
		})
	}
}

// TestDeclineSentinelIsConfigurable pins that a caller may choose the sentinel,
// which is what makes the simulated rule usable from the checkout tests, and that
// an unusable rule or a missing ledger is refused instead of silently defaulted.
func TestDeclineSentinelIsConfigurable(t *testing.T) {
	t.Run("another sentinel moves the decline to that amount", func(t *testing.T) {
		gateway, err := payment.NewSimulatedWithLedger(0, payment.NewMemoryLedger())
		if err != nil {
			t.Fatalf("NewSimulatedWithLedger(0, ledger) = unexpected error %v", err)
		}
		if got := charge(t, gateway, "sentinel-00", 1200).Outcome; got != payment.OutcomeDeclined {
			t.Fatalf("an amount ending in 00 with sentinel 0 = %q, want %q", got, payment.OutcomeDeclined)
		}
		if got := charge(t, gateway, "sentinel-13", 1213).Outcome; got != payment.OutcomeApproved {
			t.Fatalf("an amount ending in 13 with sentinel 0 = %q, want %q", got, payment.OutcomeApproved)
		}
	})

	for _, remainder := range []int64{-1, 100} {
		t.Run("an out-of-range sentinel "+strconv.FormatInt(remainder, 10)+" is refused", func(t *testing.T) {
			gateway, err := payment.NewSimulatedWithLedger(remainder, payment.NewMemoryLedger())
			if err == nil {
				t.Fatalf("NewSimulatedWithLedger(%d, ledger) = %v, want an error", remainder, gateway)
			}
			if !errors.Is(err, payment.ErrInvalidRule) {
				t.Fatalf("NewSimulatedWithLedger(%d, ledger) error = %v, want ErrInvalidRule", remainder, err)
			}
		})
	}

	t.Run("a nil ledger is refused", func(t *testing.T) {
		gateway, err := payment.NewSimulatedWithLedger(payment.DefaultDeclineRemainder, nil)
		if err == nil {
			t.Fatalf("NewSimulatedWithLedger(default, nil) = %v, want an error", gateway)
		}
		if !errors.Is(err, payment.ErrInvalidLedger) {
			t.Fatalf("NewSimulatedWithLedger(default, nil) error = %v, want ErrInvalidLedger", err)
		}
	})
}

// TestReplayIsSharedBetweenAdapterInstances pins the idempotency rule at the
// boundary: one provider reference is charged once, and a second adapter over the
// same ledger answers from the stored outcome instead of deciding again.
//
// The second adapter uses a different decline sentinel on purpose. If the replay
// re-decided the outcome it would approve 1213 (sentinel 12) instead of returning
// the stored decline, so the assertion can only pass when the stored outcome wins.
func TestReplayIsSharedBetweenAdapterInstances(t *testing.T) {
	ledger := payment.NewMemoryLedger()
	first, err := payment.NewSimulatedWithLedger(payment.DefaultDeclineRemainder, ledger)
	if err != nil {
		t.Fatalf("build the first adapter: %v", err)
	}
	second, err := payment.NewSimulatedWithLedger(12, ledger)
	if err != nil {
		t.Fatalf("build the second adapter: %v", err)
	}

	const reference = "attempt-replay-1"
	const amountMinorUnits = 1213

	original := charge(t, first, reference, amountMinorUnits)
	if original.Outcome != payment.OutcomeDeclined {
		t.Fatalf("the first charge = %q, want %q", original.Outcome, payment.OutcomeDeclined)
	}

	replayed := charge(t, second, reference, amountMinorUnits)
	if replayed != original {
		t.Fatalf("the replay through a second adapter = %+v, want the stored outcome %+v", replayed, original)
	}

	// A third call through the first adapter still answers the same thing, so the
	// replay did not overwrite or drop the stored outcome.
	if again := charge(t, first, reference, amountMinorUnits); again != original {
		t.Fatalf("the second replay = %+v, want %+v", again, original)
	}

	// The reference really is spent: the same reference with another amount is a
	// conflict, which is only knowable from the stored charge.
	_, err = second.Charge(context.Background(), payment.Request{
		AmountMinorUnits: amountMinorUnits + 100,
		Reference:        reference,
	})
	if !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("charging a used reference with another amount = %v, want ErrAmountMismatch", err)
	}
}

// TestConcurrentReplayIsIdempotent pins that the shared ledger makes the replay
// decision atomically: many callers charging one reference at once all observe the
// one stored outcome.
func TestConcurrentReplayIsIdempotent(t *testing.T) {
	ledger := payment.NewMemoryLedger()
	adapters := make([]*payment.Simulated, 2)
	for i := range adapters {
		adapter, err := payment.NewSimulatedWithLedger(payment.DefaultDeclineRemainder, ledger)
		if err != nil {
			t.Fatalf("build adapter %d: %v", i, err)
		}
		adapters[i] = adapter
	}

	const callers = 32
	const reference = "attempt-concurrent-1"
	const amountMinorUnits = 1200

	results := make([]payment.Result, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = adapters[i%len(adapters)].Charge(context.Background(), payment.Request{
				AmountMinorUnits: amountMinorUnits,
				Reference:        reference,
			})
		}(i)
	}
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d = unexpected error %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("caller %d = %+v, want the shared outcome %+v", i, results[i], results[0])
		}
	}
	if results[0].Outcome != payment.OutcomeApproved {
		t.Fatalf("the shared outcome = %q, want %q", results[0].Outcome, payment.OutcomeApproved)
	}
}

// TestMismatchedAmountIsRefused pins that a used reference with a different
// amount is a conflict and not a silent second charge, and that the refusal leaves
// the stored charge untouched.
func TestMismatchedAmountIsRefused(t *testing.T) {
	gateway := payment.NewSimulated()
	const reference = "attempt-mismatch-1"

	original := charge(t, gateway, reference, 1200)

	_, err := gateway.Charge(context.Background(), payment.Request{AmountMinorUnits: 2400, Reference: reference})
	if err == nil {
		t.Fatalf("charging %q again with a different amount succeeded, want ErrAmountMismatch", reference)
	}
	if !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("charging %q again with a different amount = %v, want ErrAmountMismatch", reference, err)
	}

	if replayed := charge(t, gateway, reference, 1200); replayed != original {
		t.Fatalf("the replay after a refused mismatch = %+v, want the untouched stored outcome %+v", replayed, original)
	}

	// A different reference is a different charge, so the same amount is not a
	// conflict: the refusal above is about the reference, not the amount.
	if other := charge(t, gateway, "attempt-mismatch-2", 2400); other.Outcome != payment.OutcomeApproved {
		t.Fatalf("a fresh reference with the second amount = %q, want %q", other.Outcome, payment.OutcomeApproved)
	}
}

// TestValidationFailsClosed pins that every rejected input is refused with an
// error a caller can tell apart, and that the rejection happens before any charge
// is recorded, so a rejected attempt never spends its provider reference.
func TestValidationFailsClosed(t *testing.T) {
	longestReference := strings.Repeat("r", payment.MaxReferenceLength)
	tooLongReference := longestReference + "r"

	cases := []struct {
		name             string
		reference        string
		amountMinorUnits int64
		want             error
	}{
		{"a zero amount is refused", "attempt-valid-1", 0, payment.ErrInvalidAmount},
		{"a negative amount is refused", "attempt-valid-2", -1200, payment.ErrInvalidAmount},
		{"an amount over the limit is refused", "attempt-valid-3", payment.MaxAmountMinorUnits + 1, payment.ErrAmountTooLarge},
		{"an empty reference is refused", "", 1200, payment.ErrEmptyReference},
		{"a blank reference is refused", "   ", 1200, payment.ErrEmptyReference},
		{"an oversized reference is refused", tooLongReference, 1200, payment.ErrReferenceTooLong},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := payment.NewSimulated()
			result, err := gateway.Charge(context.Background(), payment.Request{
				AmountMinorUnits: tc.amountMinorUnits,
				Reference:        tc.reference,
			})
			if err == nil {
				t.Fatalf("Charge(%q, %d) = %+v, want an error", tc.reference, tc.amountMinorUnits, result)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Charge(%q, %d) = %v, want %v", tc.reference, tc.amountMinorUnits, err, tc.want)
			}
			if result != (payment.Result{}) {
				t.Fatalf("Charge(%q, %d) returned %+v with the error, want the zero result", tc.reference, tc.amountMinorUnits, result)
			}
		})
	}

	t.Run("the boundary limits themselves are accepted", func(t *testing.T) {
		gateway := payment.NewSimulated()
		result := charge(t, gateway, longestReference, payment.MaxAmountMinorUnits)
		if result.Reference != longestReference {
			t.Fatalf("a reference of exactly %d bytes was stored as %d bytes", payment.MaxReferenceLength, len(result.Reference))
		}
		if result.Outcome != payment.OutcomeApproved {
			t.Fatalf("the largest accepted amount = %q, want %q", result.Outcome, payment.OutcomeApproved)
		}
	})

	t.Run("a rejected attempt does not spend its reference", func(t *testing.T) {
		gateway := payment.NewSimulated()
		const reference = "attempt-not-spent-1"

		if _, err := gateway.Charge(context.Background(), payment.Request{AmountMinorUnits: 0, Reference: reference}); !errors.Is(err, payment.ErrInvalidAmount) {
			t.Fatalf("a zero amount = %v, want ErrInvalidAmount", err)
		}
		if _, err := gateway.Charge(context.Background(), payment.Request{AmountMinorUnits: 1200, Reference: ""}); !errors.Is(err, payment.ErrEmptyReference) {
			t.Fatalf("an empty reference = %v, want ErrEmptyReference", err)
		}

		// The same reference is still unused, so this is a first charge and not a
		// replay or a mismatch.
		result := charge(t, gateway, reference, 1200)
		if result.Outcome != payment.OutcomeApproved || result.Reference != reference {
			t.Fatalf("the first real charge = %+v, want an approval under %q", result, reference)
		}
	})
}

// TestZeroValueAdapterFailsClosed pins that an adapter that was never built by a
// constructor refuses to charge instead of pretending to have charged.
func TestZeroValueAdapterFailsClosed(t *testing.T) {
	var adapter payment.Simulated
	result, err := adapter.Charge(context.Background(), payment.Request{AmountMinorUnits: 1200, Reference: "attempt-zero-1"})
	if err == nil {
		t.Fatalf("a zero-value adapter charged %+v, want an error", result)
	}
	if !errors.Is(err, payment.ErrNotConfigured) {
		t.Fatalf("a zero-value adapter = %v, want ErrNotConfigured", err)
	}
}

// TestPackageImportsNoNetworkOrCredentials pins the v1 acceptance item that no
// real payment flow exists: the boundary package may not reach a network client or
// a credential provider, because a real provider is meant to replace only the
// adapter behind the same interface.
func TestPackageImportsNoNetworkOrCredentials(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("read the import path %s in %s: %v", spec.Path.Value, name, err)
			}
			if isNetworkOrCredentialImport(path) {
				t.Fatalf("%s imports %q; the simulated provider must not reach the network or a credential store", name, path)
			}
		}
	}

	if checked < 2 {
		t.Fatalf("checked %d source files, want at least the boundary and the adapter", checked)
	}
}

// isNetworkOrCredentialImport reports the import paths that would make the
// payment package part of a live payment flow.
func isNetworkOrCredentialImport(path string) bool {
	switch {
	case path == "net" || strings.HasPrefix(path, "net/"),
		path == "crypto/tls", path == "crypto/x509",
		strings.Contains(path, "credential"),
		path == "os/exec", path == "os/user":
		return true
	default:
		return false
	}
}

// A zero-value ledger must be usable rather than a panic waiting to happen: the
// struct carries no constructor requirement, and a nil map would otherwise blow up
// on the first charge with "assignment to entry in nil map".
func TestZeroValueLedgerIsEmptyAndUsable(t *testing.T) {
	ledger := &payment.MemoryLedger{}

	g, err := payment.NewSimulatedWithLedger(payment.DefaultDeclineRemainder, ledger)
	if err != nil {
		t.Fatalf("NewSimulatedWithLedger(&MemoryLedger{}) returned error: %v", err)
	}

	first, err := g.Charge(context.Background(), payment.Request{AmountMinorUnits: 1200, Reference: "zero-value-ledger"})
	if err != nil {
		t.Fatalf("Charge on a zero-value ledger returned error: %v", err)
	}
	if first.Outcome != payment.OutcomeApproved {
		t.Fatalf("Outcome = %q, want approved", first.Outcome)
	}

	replay, err := g.Charge(context.Background(), payment.Request{AmountMinorUnits: 1200, Reference: "zero-value-ledger"})
	if err != nil {
		t.Fatalf("replay returned error: %v", err)
	}
	if replay != first {
		t.Fatalf("replay = %+v, want the stored %+v", replay, first)
	}

	if _, err := g.Charge(context.Background(), payment.Request{AmountMinorUnits: 1300, Reference: "zero-value-ledger"}); !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("mismatched amount = %v, want ErrAmountMismatch", err)
	}
}
