// The contact address a payment carries (email delivery, S1).
//
// The rule these tests pin is an ordering rule, and ordering is what a type checker
// cannot see: the address has to reach the store in **phase 1**, before the provider is
// asked for money. If it travelled after the charge, a process that died mid-call would
// leave a captured sale with nowhere to send the ticket — and the email worker would find
// a row it cannot deliver. So the assertion is not "the email is stored"; it is "the email
// is stored before the charge".
package checkout_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/checkout"
)

// TestChargeRecordsTheContactEmailBeforeTheProviderIsCalled walks one payment and reads
// the journal the fakes keep: the address must be in the prepare call, and prepare must
// come before charge.
func TestChargeRecordsTheContactEmailBeforeTheProviderIsCalled(t *testing.T) {
	ctx := context.Background()
	f := newFixture(payableReservation(), nil)

	if _, err := f.service.ChargeWithContact(ctx, checkout.ChargeRequest{
		ReservationID:  testReservationID,
		IdempotencyKey: "key-email",
		ContactEmail:   "  Compras@Example.COM  ",
	}); err != nil {
		t.Fatalf("charge with a contact address: %v", err)
	}

	if len(f.store.prepareCalls) != 1 {
		t.Fatalf("prepare was called %d time(s), want once", len(f.store.prepareCalls))
	}
	// Trimmed, and otherwise stored as written: the address is not verified, so the server
	// does not rewrite it into something it cannot prove.
	if got := f.store.prepareCalls[0].ContactEmail; got != "Compras@Example.COM" {
		t.Fatalf("contact email recorded as %q, want the trimmed address and nothing else", got)
	}

	prepare := strings.Index(f.journal.String(), "prepare")
	charge := strings.Index(f.journal.String(), "charge")
	if prepare < 0 || charge < 0 {
		t.Fatalf("journal = %s, want both a prepare and a charge", f.journal)
	}
	if prepare > charge {
		t.Fatalf("journal = %s: the address must be stored before the provider is asked for money", f.journal)
	}
}

// TestInvalidContactEmailIsRefusedBeforeAnythingHappens pins the other half: a value that
// cannot be an address is refused as caller error, and it is refused *before* an attempt
// is stored or a provider is called, so a typo never leaves a pending attempt behind.
func TestInvalidContactEmailIsRefusedBeforeAnythingHappens(t *testing.T) {
	cases := []struct {
		name  string
		email string
	}{
		{"no at sign", "sin-arroba.example.com"},
		{"nothing after the at", "nombre@"},
		{"nothing before the at", "@example.com"},
		{"no dot in the domain", "nombre@example"},
		{"whitespace inside", "nom bre@example.com"},
		{"longer than the RFC allows", strings.Repeat("a", 320) + "@example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(payableReservation(), nil)

			_, err := f.service.ChargeWithContact(ctx, checkout.ChargeRequest{
				ReservationID:  testReservationID,
				IdempotencyKey: "key-email",
				ContactEmail:   tc.email,
			})
			if !errors.Is(err, checkout.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if len(f.store.prepareCalls) != 0 {
				t.Fatalf("a refused address still stored an attempt (%d prepare call(s))", len(f.store.prepareCalls))
			}
			if len(f.gateway.requests) != 0 {
				t.Fatalf("a refused address still reached the provider (%d charge(s))", len(f.gateway.requests))
			}
		})
	}
}

// TestChargeWithoutAnAddressStillPays is the promise the other 54 call sites rely on: an
// empty address is the ordinary case, not an error. The product has never required an
// address to buy, and this must not become a requirement by accident.
func TestChargeWithoutAnAddressStillPays(t *testing.T) {
	ctx := context.Background()
	f := newFixture(payableReservation(), nil)

	if _, err := f.service.Charge(ctx, testReservationID, "key-no-email"); err != nil {
		t.Fatalf("charge without an address: %v", err)
	}
	if got := f.store.prepareCalls[0].ContactEmail; got != "" {
		t.Fatalf("contact email recorded as %q, want empty", got)
	}
}
