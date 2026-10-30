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
func (r ReservationDetail) AmountText() (string, bool) {
	if r.AmountCents == nil {
		return "", false
	}
	return tickets.FormatMoney(*r.AmountCents), true
}
