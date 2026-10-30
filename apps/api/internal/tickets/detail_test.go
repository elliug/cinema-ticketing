// Behaviour tests for the ticket detail projection that needs no database: the
// valid/status pair the surfaces trust, the amount presentation, and the local
// presentation of the screening time.
//
// The rule these pin is that the local time is derived from the venue's own
// timezone (`cinema.cinemas.timezone`, carried on the projection) and never from
// the machine's zone: the same instant answers differently for a venue in Madrid
// and a venue in Auckland, and a venue row whose zone cannot be resolved is an
// error instead of a silent UTC answer.
package tickets_test

import (
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// screeningInstant is the fixed instant every case renders. 18:30 UTC is chosen
// because it lands on a different local date in Auckland, so a rendering that
// merely re-labels the machine's zone cannot pass by accident.
var screeningInstant = time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC)

// publishedEvent builds the event part of a projection for one venue.
func publishedEvent(timezone, status string) tickets.EventDetail {
	return tickets.EventDetail{
		ID:         "event-1",
		FilmTitle:  "La película",
		StartsAt:   screeningInstant,
		RoomName:   "Sala 1",
		CinemaName: "Cine Centro",
		Timezone:   timezone,
		Status:     status,
	}
}

func issuedTicket(event tickets.EventDetail) tickets.TicketDetail {
	return tickets.TicketDetail{
		TicketID:      "ticket-1",
		Status:        tickets.TicketStatusIssued,
		IssuedAt:      screeningInstant.Add(-48 * time.Hour),
		ReservationID: "reservation-1",
		Event:         event,
		// One ticket is one seat, so the projection carries this ticket's own seat.
		Seats: []tickets.Seat{
			{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"},
		},
		// The charge of the whole order: two seats at 9,99 EUR.
		OrderAmountCents: 1998,
	}
}

// T4 (S4): a ticket whose status is not issued, and an issued ticket of a
// cancelled screening, must be answerable and must say so instead of looking
// valid. The pair is derived from the two stored statuses in one place, so the
// web page and the PDF cannot disagree about whether a ticket admits entry.
func TestTicketDetailValidityFollowsTheStoredStatuses(t *testing.T) {
	cases := []struct {
		name   string
		detail tickets.TicketDetail
		want   bool
	}{
		{
			name:   "an issued ticket of a live screening",
			detail: issuedTicket(publishedEvent("Europe/Madrid", "on_sale")),
			want:   true,
		},
		{
			name:   "an issued ticket of a screening that has not opened yet",
			detail: issuedTicket(publishedEvent("Europe/Madrid", "published")),
			want:   true,
		},
		{
			name:   "an issued ticket of a screening that already started",
			detail: issuedTicket(publishedEvent("Europe/Madrid", "started")),
			want:   true,
		},
		{
			name: "a voided ticket",
			detail: func() tickets.TicketDetail {
				detail := issuedTicket(publishedEvent("Europe/Madrid", "on_sale"))
				detail.Status = tickets.TicketStatusVoid
				return detail
			}(),
			want: false,
		},
		{
			name:   "an issued ticket of a cancelled screening",
			detail: issuedTicket(publishedEvent("Europe/Madrid", tickets.EventStatusCancelled)),
			want:   false,
		},
		{
			name: "a status the projection does not know",
			detail: func() tickets.TicketDetail {
				detail := issuedTicket(publishedEvent("Europe/Madrid", "on_sale"))
				detail.Status = "used"
				return detail
			}(),
			want: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.detail.Valid(); got != testCase.want {
				t.Fatalf("Valid() = %t, want %t for status %q and event status %q",
					got, testCase.want, testCase.detail.Status, testCase.detail.Event.Status)
			}
			// The stored status is carried as it is, never rewritten: the surface
			// has to be able to say "voided" or "cancelled" rather than only "not
			// valid".
			if testCase.detail.Status == "" || testCase.detail.Event.Status == "" {
				t.Fatalf("the projection dropped a status: ticket %q, event %q",
					testCase.detail.Status, testCase.detail.Event.Status)
			}
		})
	}
}

// T4 (S4): the order amount is presented from the minor units the projection
// carries, without inventing a currency the schema does not store. It is named
// after the order because the captured attempt is per reservation: this is what
// the whole order cost, not what one seat of it cost.
func TestTicketDetailOrderAmountText(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{2426, "24.26"},
		{1998, "19.98"},
		{100, "1.00"},
		{5, "0.05"},
		{0, "0.00"},
		{-250, "-2.50"},
	}
	for _, testCase := range cases {
		detail := tickets.TicketDetail{OrderAmountCents: testCase.cents}
		if got := detail.OrderAmountText(); got != testCase.want {
			t.Fatalf("OrderAmountText() for %d minor units = %q, want %q", testCase.cents, got, testCase.want)
		}
		if strings.ContainsAny(detail.OrderAmountText(), "€$£") {
			t.Fatalf("OrderAmountText() = %q, want no currency symbol", detail.OrderAmountText())
		}
	}

	if got := (tickets.TicketDetail{OrderAmountCents: 2426}).OrderAmountText(); got != "24.26" {
		t.Fatalf("a zero-value projection changed the rendering: %q", got)
	}
}

// T4 (S4): the same instant is presented in the venue's own zone, with an
// explicit offset, so neither the browser nor the PDF has to know the venue's
// rules — and so the answer does not depend on where the API happens to run.
func TestLocalStartsAtUsesTheVenueTimezone(t *testing.T) {
	cases := []struct {
		timezone string
		want     string
	}{
		{"Europe/Madrid", "2026-05-04T20:30:00+02:00"},
		{"Pacific/Auckland", "2026-05-05T06:30:00+12:00"},
		{"UTC", "2026-05-04T18:30:00+00:00"},
		{"America/New_York", "2026-05-04T14:30:00-04:00"},
	}
	for _, testCase := range cases {
		event := publishedEvent(testCase.timezone, "on_sale")
		got, err := event.LocalStartsAt()
		if err != nil {
			t.Fatalf("LocalStartsAt() for %s = unexpected error %v", testCase.timezone, err)
		}
		if got != testCase.want {
			t.Fatalf("LocalStartsAt() for %s = %q, want %q", testCase.timezone, got, testCase.want)
		}
	}

	// The instant itself is answered unchanged and in UTC, so a consumer that needs
	// to compare times never has to parse a local presentation.
	event := publishedEvent("Europe/Madrid", "on_sale")
	if !event.StartsAt.Equal(screeningInstant) || event.StartsAt.Location() != time.UTC {
		t.Fatalf("StartsAt = %v, want the stored instant in UTC", event.StartsAt)
	}
}

// A venue row whose zone cannot be resolved is an error rather than a silent UTC
// answer: a ticket printed with the wrong time is worse than one not printed.
func TestLocalStartsAtRefusesAnUnknownVenueTimezone(t *testing.T) {
	for _, timezone := range []string{"", "Mars/Olympus", "Europe/Madrid "} {
		event := publishedEvent(timezone, "on_sale")
		got, err := event.LocalStartsAt()
		if err == nil {
			t.Fatalf("LocalStartsAt() for timezone %q = %q, want an error", timezone, got)
		}
		if got != "" {
			t.Fatalf("LocalStartsAt() for timezone %q answered %q next to an error", timezone, got)
		}
		if !strings.Contains(err.Error(), timezone) {
			t.Fatalf("error %q does not name the unusable timezone %q", err, timezone)
		}
	}
}
