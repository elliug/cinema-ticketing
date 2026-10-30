// The catalogue read model: one screening with everything the catalogue screen and the
// seat page show — the venue, the price and how many seats are free right now — and
// whether the catalogue still offers it. Nothing in this file knows about HTTP or JSON:
// a transport translates these fields and adds no facts of its own.

package catalog

import (
	"context"
	"errors"
	"time"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// The failures a [Reader] reports. A screening or a reservation that cannot be named is
// not found; a read that could not be completed is unavailable. There is deliberately no
// third case: an "empty reservation" or a "blank screening" answer would let a page
// render a record that does not exist as if it did.
var (
	// ErrNotFound reports that no screening or reservation carries the requested
	// identifier. An identifier that cannot even be one answers the same way, so nothing
	// distinguishes a wrong guess from an identifier that never existed.
	ErrNotFound = errors.New("catalog: the requested record does not exist")

	// ErrUnavailable reports that a read could not be completed: a transport failure, or
	// a stored screening whose venue zone cannot be presented. It is retryable for the
	// caller and never a partial catalogue, a partial screening or an empty reservation.
	ErrUnavailable = errors.New("catalog: the read is unavailable")
)

// Reader reads the projections the public web needs. It is the whole contract this
// package offers: the catalogue route, the seat page's read of one screening and the
// confirmation route each read one of these and none assembles its own answer from
// parts.
type Reader interface {
	// Events answers the screenings a buyer can buy right now, in screening order. A
	// catalogue with nothing to sell is an empty list and not an error.
	Events(ctx context.Context) ([]Screening, error)
	// Event answers one screening, whatever its status: a buyer who followed a link to a
	// screening that is no longer offered needs to be told so, not told it never existed.
	// It answers the same projection, from the same statement, as the entry [Events]
	// would have listed for it. An unknown screening, or an identifier no screening could
	// carry, is ErrNotFound; a read that could not be completed is ErrUnavailable.
	Event(ctx context.Context, eventID string) (Screening, error)
	// Reservation answers the confirmation's read of one order. An unknown reservation,
	// or an identifier no reservation could carry, is ErrNotFound; a read that could not
	// be completed is ErrUnavailable.
	Reservation(ctx context.Context, reservationID string) (ReservationDetail, error)
}

// Screening is one catalogue entry: a screening with the venue, the price and how many
// seats are free right now. The list answers every screening a buyer can buy; the single
// read answers one screening that exists, whether or not it is still offered.
//
// It is a projection and not a second event model: the event, its film, its room, its
// cinema and its seats belong to the modules that own them, and this is the slice of
// them the catalogue screen needs, read in one statement.
type Screening struct {
	// ID is the event's identifier (`cinema.events.id`), which is what a seat selection
	// addresses.
	ID string
	// Status is `cinema.events.status` exactly as stored: "draft", "published",
	// "on_sale", "started", "completed" or "cancelled". It is carried verbatim rather
	// than interpreted, so a surface can say why a screening is not offered instead of
	// only that it is not.
	Status string
	// OnSale is whether the catalogue offers this screening right now, and it is the same
	// predicate that decides membership of the list: `status = 'on_sale'` and still ahead
	// of the database clock. One expression serves both reads, so the single read cannot
	// disagree with the list about which side of "now" a screening is on, and a client
	// never has to compare a clock of its own to know whether a seat can be bought.
	OnSale bool
	// FilmTitle is the film's title as stored, which is what the buyer recognises.
	FilmTitle string
	// CinemaName is the venue's name, so the catalogue can say which building to walk
	// into.
	CinemaName string
	// RoomName is the name of the room the screening happens in, as the venue
	// administration entered it.
	RoomName string
	// StartsAt is the raw instant the screening begins. A client that sorts or schedules
	// uses this one; the transport answers it in UTC so the string a client compares is
	// the same in every deployment, and the venue's own zone is [Screening.Timezone].
	StartsAt time.Time
	// Timezone is the venue's IANA zone (`cinema.cinemas.timezone`). It is carried so the
	// local presentation is computed from the venue and never from the machine the API
	// runs on.
	Timezone string
	// PriceCents is the price of one seat, in minor units, read from
	// `cinema.events.price_cents`.
	PriceCents int64
	// SeatCount is how many seats the screening sells: the event seats materialized from
	// its layout, whatever states they are in.
	SeatCount int
	// AvailableSeats is how many of them a buyer can select right now: neither sold nor
	// held by a live hold, which is the definition the seat map uses.
	AvailableSeats int
}

// LocalStartsAt renders the screening time in the venue's own timezone, with an
// explicit offset (RFC 3339).
//
// The rule is not reimplemented here. The stored instant and the venue zone are handed
// to [tickets.EventDetail], whose [tickets.EventDetail.LocalStartsAt] the ticket detail
// route and the PDF already use, so the catalogue and the ticket cannot disagree about
// the hour a screening starts and neither the browser nor the PDF has to know the
// venue's rules. A venue whose stored zone cannot be resolved is an error rather than a
// silent UTC answer.
func (s Screening) LocalStartsAt() (string, error) {
	return tickets.EventDetail{ID: s.ID, StartsAt: s.StartsAt, Timezone: s.Timezone}.LocalStartsAt()
}

// PriceText renders the price for display, without a currency: this package has no
// deployment currency to prefix and inventing one would be a guess the renderer cannot
// undo. The spelling is [tickets.FormatMoney]'s, the same one the ticket detail prints
// for an order amount.
func (s Screening) PriceText() string { return tickets.FormatMoney(s.PriceCents) }
