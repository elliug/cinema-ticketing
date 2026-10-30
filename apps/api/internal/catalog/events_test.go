// The rules the two read models own, tested without a database.
//
// The catalogue and the reservation read add no presentation logic of their own: the
// venue-local hour and the money come from the ticket package, so these tests prove
// the read models delegate instead of reimplementing. A screening rendered in the
// machine's zone, or a price with an invented currency, fails here.
package catalog_test

import (
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// catalogueStartsAt is the instant every screening fixture starts. 18:30 UTC is
// 20:30 in Madrid and 06:30 the next day in Auckland, so a rendering that used the
// machine's zone cannot pass by accident.
var catalogueStartsAt = time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC)

// fixtureScreening is one catalogue entry a real read would answer, priced at the
// 12,50 EUR the milestone's examples use.
func fixtureScreening() catalog.Screening {
	return catalog.Screening{
		ID:             "00000000-0000-0000-0000-00000000f1a1",
		FilmTitle:      "La película",
		CinemaName:     "Cine Centro",
		RoomName:       "Sala 1",
		StartsAt:       catalogueStartsAt,
		Timezone:       "Europe/Madrid",
		PriceCents:     1250,
		SeatCount:      8,
		AvailableSeats: 3,
	}
}

// S5: the catalogue's local time is the venue's, computed by the ticket package's
// rule and never by this package or the browser.
func TestScreeningRendersTheVenueLocalTime(t *testing.T) {
	cases := []struct {
		timezone string
		want     string
	}{
		{"Europe/Madrid", "2026-05-04T20:30:00+02:00"},
		{"Pacific/Auckland", "2026-05-05T06:30:00+12:00"},
		{"UTC", "2026-05-04T18:30:00+00:00"},
	}
	for _, testCase := range cases {
		screening := fixtureScreening()
		screening.Timezone = testCase.timezone
		local, err := screening.LocalStartsAt()
		if err != nil {
			t.Fatalf("LocalStartsAt() for %s = unexpected error %v", testCase.timezone, err)
		}
		if local != testCase.want {
			t.Fatalf("LocalStartsAt() for %s = %q, want %q", testCase.timezone, local, testCase.want)
		}
		// The raw instant is served unchanged, whatever the venue: a client that
		// sorts or schedules uses this one.
		if !screening.StartsAt.Equal(catalogueStartsAt) {
			t.Fatalf("startsAt = %v, want the stored instant %v", screening.StartsAt, catalogueStartsAt)
		}
	}
}

// A venue whose stored zone cannot be resolved is refused rather than answered with
// an invented local hour: a catalogue entry saying the wrong time is worse than one
// that could not be read.
func TestScreeningRefusesAnUnusableVenueTimezone(t *testing.T) {
	for _, timezone := range []string{"", "   ", "Mars/Olympus"} {
		screening := fixtureScreening()
		screening.Timezone = timezone
		local, err := screening.LocalStartsAt()
		if err == nil {
			t.Fatalf("LocalStartsAt() with timezone %q = %q, want a refusal", timezone, local)
		}
		if local != "" {
			t.Fatalf("LocalStartsAt() with timezone %q answered %q next to its error", timezone, local)
		}
	}
}

// S5: the price is presented by the one formatter the ticket detail already uses,
// with two decimals over the minor units and no currency.
func TestScreeningRendersThePriceWithoutACurrency(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "0.00"},
		{5, "0.05"},
		{1250, "12.50"},
		{99, "0.99"},
		{123456789, "1234567.89"},
	}
	for _, testCase := range cases {
		screening := fixtureScreening()
		screening.PriceCents = testCase.cents
		got := screening.PriceText()
		if got != testCase.want {
			t.Fatalf("PriceText() of %d minor units = %q, want %q", testCase.cents, got, testCase.want)
		}
		// One formatter, shared: the catalogue and the ticket detail cannot disagree
		// about how money is spelled.
		if want := tickets.FormatMoney(testCase.cents); got != want {
			t.Fatalf("PriceText() of %d = %q but tickets.FormatMoney = %q", testCase.cents, got, want)
		}
	}
}

// S5: the reservation's amount is the money that was actually taken, and there is
// none while the order is unpaid — never a zero invented from a missing charge.
func TestReservationDetailRendersTheAmountOnlyWhenThereIsOne(t *testing.T) {
	unpaid := catalog.ReservationDetail{ID: "r-1", Status: "held"}
	if text, ok := unpaid.AmountText(); ok || text != "" {
		t.Fatalf("AmountText() of an unpaid reservation = %q, %t, want no amount", text, ok)
	}

	charged := int64(2500)
	paid := catalog.ReservationDetail{ID: "r-2", Status: "paid", AmountCents: &charged}
	text, ok := paid.AmountText()
	if !ok || text != "25.00" {
		t.Fatalf("AmountText() of a paid reservation = %q, %t, want 25.00", text, ok)
	}

	// Zero is a charge somebody recorded, so it is presented; only a missing one is
	// absent.
	zero := int64(0)
	free := catalog.ReservationDetail{ID: "r-3", Status: "paid", AmountCents: &zero}
	if text, ok := free.AmountText(); !ok || text != "0.00" {
		t.Fatalf("AmountText() of a recorded zero = %q, %t, want 0.00", text, ok)
	}
}
