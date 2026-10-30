// The reservation read model: the confirmation's read of one order. Nothing in this
// file knows about HTTP or JSON, and nothing in it exposes how the money was taken —
// only what was taken, for whom it bought a seat, and which tickets it produced.

package catalog

import (
	"time"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// ReservationDetail is the confirmation's read of one order: the state the buyer saw
// when the order was created, so reloading the page keeps it.
//
// It is addressed by the reservation identifier, which is a capability in v1 — see the
// package documentation. It therefore carries the order's own facts and nothing of the
// checkout's ledger: no attempt identifier, provider, provider reference, idempotency
// key, request fingerprint or attempt status.
type ReservationDetail struct {
	// ID is the reservation identifier (`cinema.reservations.id`), the capability this
	// read is addressed by.
	ID string
	// Status is the stored reservation status: "held" while the seats are held,
	// "payment_pending" while a payment is under way, "paid" once the money was taken,
	// and "expired" or "cancelled" once it ended without one. It is carried unchanged so a
	// surface can say what happened instead of guessing.
	Status string
	// ExpiresAt is the durable hold deadline, as the database recorded it. A paid order
	// keeps the deadline it was held with; its state is decided by Status. The transport
	// answers it in UTC, so the countdown a client runs compares one canonical instant.
	ExpiresAt time.Time
	// AmountCents is the money that was actually taken for the order, in minor units,
	// read from the reservation's captured payment attempt. It is nil while the order is
	// unpaid: an unpaid order has no charge, and a zero would claim one.
	//
	// It is never recomputed as price × seats. A price that moved after the sale, or a
	// hold that was never paid, would then be answered with money nobody ever took.
	AmountCents *int64
	// Seats are the seats the order holds or sold, in the order they are painted at the
	// door. Seats released by an ended hold are not part of it: an expired order holds
	// nothing.
	Seats []Seat
	// TicketIDs are the tickets the checkout issued for this order, in seat order. It is
	// an empty list for an unpaid order, never nil, so a confirmation page can iterate it
	// without a nil check.
	TicketIDs []string

	// AmountDueCents is what paying this order costs right now, in minor units: the
	// event's price per seat times the seats the order still holds. It is a **quote, not
	// a charge** — the price may move between the quote and the capture, and the money
	// actually taken is what the payment answer reports and what [ReservationDetail.AmountCents]
	// carries afterwards. A disagreement becomes the checkout's existing amount conflict
	// rather than a silent different charge.
	//
	// It is nil whenever nothing is due: the order is paid (the captured amount is the
	// truth then), it ended without a payment, or it holds no seats.
	AmountDueCents *int64
	// The screening the order is for. A reloaded confirmation has to say *what* is being
	// bought and *when*, not only how many seats and what they cost: without these, a
	// buyer returning to an unpaid order sees a countdown attached to nothing.
	//
	// They are read from the event's own row, so a screening that is later rescheduled
	// answers its current time — the order holds seats for the event, not for a moment.
	EventID    string
	FilmTitle  string
	CinemaName string
	RoomName   string
	// StartsAt is the raw screening instant. The transport answers it in UTC for the same
	// reason the rest of the API does.
	StartsAt time.Time
	// Timezone is the venue's zone (`cinema.cinemas.timezone`), the input to
	// [ReservationDetail.LocalStartsAt].
	Timezone string
}

// Seat is one seat of an order, as it is painted at the door.
type Seat struct {
	// EventSeatID is the event-specific seat (`cinema.event_seats.id`), the identity a
	// seat selection and a ticket name.
	EventSeatID int64
	// RowLabel is the row label exactly as the venue entered it ("A", "VIP-1"), not a
	// position, because that is what is painted at the door.
	RowLabel string
	// SeatNumber is the number within that row, as text, because venues number seats "1",
	// "12A" or "1-bis" and a number would lose that.
	SeatNumber string
}

// AmountText renders the money taken for the order for display, without a currency, and
// reports false when there is none. The spelling is [tickets.FormatMoney]'s, so the
// confirmation page and the ticket detail print the same charge the same way.
//
// The second result is what makes an unpaid order presentable honestly: a renderer that
// only had a string would have to print something for "no charge yet", and every choice
// there ("0.00", "", "-") claims something the system did not do.
// DueText renders the amount due now, and reports false when nothing is due. Same spelling
// rule as the rest of the money in this API, and the same reason for the second result.
func (r ReservationDetail) DueText() (string, bool) {
	if r.AmountDueCents == nil {
		return "", false
	}
	return tickets.FormatMoney(*r.AmountDueCents), true
}

func (r ReservationDetail) AmountText() (string, bool) {
	if r.AmountCents == nil {
		return "", false
	}
	return tickets.FormatMoney(*r.AmountCents), true
}

// LocalStartsAt renders the screening time in the venue's own zone, with the offset
// spelled numerically. It delegates to the ticket projection's rule rather than
// reimplementing it, so a confirmation page, a ticket and a PDF cannot show the same
// screening at three different hours.
func (r ReservationDetail) LocalStartsAt() (string, error) {
	return tickets.EventDetail{ID: r.EventID, StartsAt: r.StartsAt, Timezone: r.Timezone}.LocalStartsAt()
}
