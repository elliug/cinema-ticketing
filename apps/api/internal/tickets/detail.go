// The ticket detail projection. Everything a surface may say about one ticket is
// here, so the web confirmation page and the PDF render one value instead of two
// assemblies that could disagree. Nothing in this file knows about HTTP, JSON or
// PDF: a transport translates these fields and adds no facts of its own.

package tickets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The ticket statuses `cinema.tickets.status` can hold. They are carried verbatim
// on the projection so a surface can say "voided" rather than only "not valid".
const (
	// TicketStatusIssued is a ticket that was sold and has not been withdrawn.
	TicketStatusIssued = "issued"
	// TicketStatusVoid is a withdrawn ticket: it still answers, with its status, so
	// the surface can tell the holder the ticket no longer admits anyone.
	TicketStatusVoid = "void"
)

// EventStatusCancelled is the one `cinema.events.status` value that makes an
// otherwise valid ticket unusable. The rest of the event lifecycle ('draft',
// 'published', 'on_sale', 'started', 'completed') is not interpreted here: a
// ticket of a screening that already started or finished is a question for M4's
// cashier workflows, not for the buyer's own copy of the ticket.
const EventStatusCancelled = "cancelled"

// The failures a [Reader] reports. A ticket that cannot be named is not found; a
// ticket that cannot be read is unavailable. There is deliberately no third case:
// an "empty ticket" answer would let a renderer print a blank ticket.
var (
	// ErrNotFound reports that no ticket carries the requested identifier. An
	// identifier that cannot even be one answers the same way, so nothing
	// distinguishes a wrong guess from an identifier that never existed.
	ErrNotFound = errors.New("tickets: the ticket does not exist")

	// ErrUnavailable reports that the projection could not be read: a transport
	// failure, or a stored ticket whose amount was not recorded by a captured
	// payment. It is retryable for the caller and never a ticket with missing
	// fields.
	ErrUnavailable = errors.New("tickets: the ticket detail is unavailable")
)

// Reader reads the ticket detail projection. It is the whole contract this package
// offers: the HTTP route, the PDF renderer and the QR verification of T5 read a
// ticket the same way, so none of them can invent a different ticket.
type Reader interface {
	// Detail answers the projection of one ticket. An unknown ticket, or an
	// identifier no ticket could carry, is ErrNotFound; a read that could not be
	// completed is ErrUnavailable.
	Detail(ctx context.Context, ticketID string) (TicketDetail, error)
}

// TicketDetail is the single projection of one ticket: the seat sale as it was
// recorded, plus the presentation facts its renderers need.
//
// The seat is this ticket's own, because one ticket is one seat and the PDF prints
// one document per ticket. The amount and the reservation describe the order the
// ticket belongs to: one reservation sells one order, its money was taken once for
// all of its seats, and every ticket of the purchase carries that same charge.
type TicketDetail struct {
	// TicketID is the ticket's own identifier (`cinema.tickets.id`), the durable
	// right of entry and the capability the public route is addressed by.
	TicketID string
	// Status is the stored ticket status, [TicketStatusIssued] or
	// [TicketStatusVoid], carried unchanged so a surface can explain itself.
	Status string
	// IssuedAt is the instant the ticket was issued, as the database recorded it.
	IssuedAt time.Time
	// ReservationID is the order the ticket was sold in. Two tickets of the same
	// purchase share it, and it is how a confirmation surface groups the tickets of
	// one order, since one reservation is one order.
	ReservationID string
	// Event is the screening the ticket admits its holder to.
	Event EventDetail
	// Seats is the seat this ticket admits its holder to. This milestone always
	// answers exactly one seat, because one ticket is one seat: a list that carried
	// every seat of the order would print a document that admits two people at the
	// door and would make ticket A1 indistinguishable from ticket A2. It stays a
	// slice because a later milestone that issues one ticket for several seats
	// extends this list; a renderer never invents a seat list of its own.
	Seats []Seat
	// OrderAmountCents is the money that was actually taken for the whole
	// reservation, in minor units, read from the reservation's captured payment
	// attempt. It is never recomputed from the event price and the seat count.
	//
	// It is deliberately not called the ticket's price: the captured attempt is per
	// reservation, so this is what the entire order cost, and M3 stores no
	// per-ticket amount because an order is priced as price × seats. A renderer
	// prints it as the order total.
	OrderAmountCents int64
	// QRToken is the signed QR code of this ticket, already minted by
	// [Service.Detail], so the web page and the PDF print the same code for the same
	// ticket instead of each renderer signing its own.
	//
	// It is a signed token: a payload naming the ticket, its event and its seats, plus
	// an HMAC signature over that payload. It carries no name, no email, no film, no
	// time and no price, so a photographed code exposes no personal data. Signed is not
	// encrypted, though: the payload is base64url text, so anyone holding the code can
	// read the identifiers it names, and the ticket route resolves them to the
	// screening. A photograph is therefore both a readable reference and a usable
	// credential, which is why the door must honour `valid` and why marking a ticket as
	// redeemed is M4's. The token is never logged and never echoed in an error.
	QRToken string
}

// EventDetail is the screening part of a ticket detail: what is being shown,
// where, when, and in which venue.
type EventDetail struct {
	// ID is the event's identifier (`cinema.events.id`).
	ID string
	// FilmTitle is the film's title as stored, which is what the buyer recognises.
	FilmTitle string
	// StartsAt is the raw instant the screening begins, in UTC. A consumer that
	// needs to compare times or schedule work uses this one.
	StartsAt time.Time
	// RoomName is the name of the room the screening happens in, as the venue
	// administration entered it.
	RoomName string
	// CinemaName is the venue's name, so a ticket printed from the PDF says which
	// building to walk into.
	CinemaName string
	// Timezone is the venue's IANA zone (`cinema.cinemas.timezone`). It is carried
	// so a surface can label the local time and so the local presentation is
	// computed from the venue and never from the machine the API runs on.
	Timezone string
	// Status is the stored event status. A cancelled screening keeps answering, so
	// the ticket can be shown as cancelled instead of silently valid.
	Status string
}

// Seat is the seat a ticket admits its holder to, as it is printed on the ticket.
type Seat struct {
	// EventSeatID is the event-specific seat (`cinema.event_seats.id`), the identity
	// a QR token or a cashier check has to name, since a layout seat is only the
	// physical position.
	EventSeatID int64
	// RowLabel is the row label exactly as the venue entered it ("A", "VIP-1"), not
	// a position, because that is what is painted at the door.
	RowLabel string
	// SeatNumber is the number within that row, as text, because venues number seats
	// "1", "12A" or "1-bis" and a number would lose that.
	SeatNumber string
}

// The machine-readable reasons a ticket does not admit its holder. They are the
// values a door scanner is told, so "this ticket was cancelled" stays
// distinguishable from "this code is a forgery" (which is a refusal, not a reason).
const (
	// InvalidReasonVoided is a ticket that was withdrawn: the ticket itself no longer
	// admits anyone, whatever happens to the screening.
	InvalidReasonVoided = "voided"
	// InvalidReasonEventCancelled is an issued ticket whose screening was cancelled.
	InvalidReasonEventCancelled = "event_cancelled"
)

// Valid reports whether the ticket still admits its holder: it was issued and the
// screening was not cancelled.
//
// This is the only place the two stored statuses are combined, so the web page and
// the PDF cannot disagree about whether a ticket is good. Redemption ("used") is
// not part of it: that is M4's cashier decision, and a ticket that has been used is
// still a valid ticket.
func (d TicketDetail) Valid() bool {
	return d.Status == TicketStatusIssued && d.Event.Status != EventStatusCancelled
}

// InvalidReason names why the ticket does not admit its holder, and answers the
// empty string for a ticket that does. It is derived from the same two statuses as
// [TicketDetail.Valid] and lives beside it, so the door, the web page and the PDF
// cannot disagree; the two are exhaustive in this milestone, whose schema allows the
// ticket statuses [TicketStatusIssued] and [TicketStatusVoid] only. A later status
// (M4's redeemed ticket, for example) extends this function rather than each
// renderer deriving its own answer.
func (d TicketDetail) InvalidReason() string {
	switch {
	case d.Valid():
		return ""
	case d.Status != TicketStatusIssued:
		// A ticket is void (or, later, redeemed): the ticket is the reason, not the
		// screening.
		return InvalidReasonVoided
	default:
		// The ticket was issued, so the screening is what makes it unusable.
		return InvalidReasonEventCancelled
	}
}

// OrderAmountText renders the order's charged amount for display, without a
// currency, using [FormatMoney].
//
// It renders the money taken for the whole reservation, so it is printed as the
// order total, not as the price of this one seat.
func (d TicketDetail) OrderAmountText() string { return FormatMoney(d.OrderAmountCents) }

// FormatMoney renders an amount in minor units for display, without a currency: two
// decimals over the minor units, since v1 stores no currency and inventing one here
// would be a guess the renderer cannot undo. A renderer that knows the deployment's
// currency (an open decision of the architecture baseline) prefixes it; this package
// never does.
//
// It is exported because it is the one spelling of money in the system: the ticket
// detail (the order amount) and the public catalogue (a screening's price) both print
// what it returns, so two surfaces can never show the same charge in two shapes. A
// negative amount keeps its sign rather than losing it to the division.
func FormatMoney(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// localTimeLayout is the RFC 3339 shape of the local presentation, with the offset
// always written numerically. RFC 3339 allows "Z" for a zero offset, and "Z" is
// exactly what a renderer would then have to special-case, so the venue's own zone
// is spelled out even when it happens to be UTC.
const localTimeLayout = "2006-01-02T15:04:05-07:00"

// LocalStartsAt renders the screening time in the venue's own timezone, with an
// explicit offset (RFC 3339), so neither the browser nor the PDF has to know the
// venue's rules and neither can render it in the machine's zone by accident.
//
// The zone comes from the venue row. A stored zone that cannot be resolved is an
// error rather than a silent UTC answer: a ticket printed with the wrong local time
// is worse than a ticket that was not printed at all, and the blank case is
// refused here too instead of being read as UTC (the schema already forbids it, so
// a blank one means the row is broken, not that the venue sits in Greenwich).
func (e EventDetail) LocalStartsAt() (string, error) {
	if strings.TrimSpace(e.Timezone) == "" {
		return "", fmt.Errorf("tickets: event %s has no venue timezone, so its local time cannot be presented", e.ID)
	}
	location, err := time.LoadLocation(e.Timezone)
	if err != nil {
		return "", fmt.Errorf("tickets: event %s has an unusable venue timezone %q: %w", e.ID, e.Timezone, err)
	}
	return e.StartsAt.In(location).Format(localTimeLayout), nil
}
