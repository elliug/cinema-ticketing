// The public reservation read, GET /api/v1/reservations/{reservationId}, lives in this
// file, and so does the body it answers: the cancel route renders that same body (see
// cancel.go), so one order is described in one shape whichever route answers for it.
//
// It exists so reloading the confirmation page keeps its state: the hold response and
// the payment response are one-shot answers, and a browser that refreshes has nothing
// left to show without this read. It is public for the reason the ticket route is (see
// the catalog and tickets package documentation): in v1 the reservation identifier is
// the capability, because the demonstration session authenticates nobody and a guest
// order has no owner to check.
//
// What it must therefore not do is answer more than the confirmation screen needs.
// Everything here is the order's own state — its status, its deadline, the money
// actually taken, the seats it holds or sold and the tickets issued for it. No payment
// attempt internals are exposed: the attempt, its provider, its reference, its
// idempotency key, its fingerprint and its outcome stay in the checkout's ledger.

package publicapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/catalog"
)

// ReservationService is the confirmation read contract: the projection of one order.
// It is satisfied by the catalogue store, and a deployment without that schema wires
// nothing — in which case the route is unmounted.
type ReservationService interface {
	// Reservation answers the confirmation's read of one order. An unknown reservation,
	// or an identifier no reservation could carry, is catalog.ErrNotFound; a read that
	// could not be completed is catalog.ErrUnavailable.
	Reservation(ctx context.Context, reservationID string) (catalog.ReservationDetail, error)
}

// WithReservations attaches the reservation reader and returns the same handler, so the
// confirmation read is mounted only when one is wired.
//
// A nil reader leaves the route unmounted: a read with nothing behind it could only
// answer 503, and the hold creation route on the same subtree keeps working.
func (h *Handler) WithReservations(svc ReservationService) *Handler {
	h.reservations = svc
	return h
}

// getReservation answers the stored state of the order named by the path, which is what
// keeps a reloaded confirmation showing the same order.
func (h *Handler) getReservation(w http.ResponseWriter, r *http.Request) {
	detail, err := h.reservations.Reservation(r.Context(), r.PathValue("reservationID"))
	if err != nil {
		writeReservationError(w, err)
		return
	}
	writeReservationState(w, detail)
}

// writeReservationState answers the state of one order in the confirmation read's body.
//
// It is the single place that shapes that body, so the cancel route — whose answer is the
// reservation's new state — serves the same keys, the same nullability and the same UTC
// deadline. A client that already renders an order reads one vocabulary, not two.
//
// The amount is the money that was actually taken, and it is answered as `null` while
// there is none: an unpaid order has no charge, so `0` would claim one and a price
// multiplied by the seats would claim one that never happened. The seats are the ones
// the order holds or sold, in seat order, and the ticket list is always an array.
func writeReservationState(w http.ResponseWriter, detail catalog.ReservationDetail) {
	// The amount is nullable on the wire on purpose, and both of its forms are answered
	// so a client never has to format money itself. They are null together: an order
	// without a charge has neither a number nor a text for one.
	var (
		amountCents   *int64
		amountDisplay *string
		dueCents      *int64
		dueDisplay    *string
	)
	if text, ok := detail.DueText(); ok {
		dueCents = detail.AmountDueCents
		dueDisplay = &text
	}
	if text, ok := detail.AmountText(); ok {
		amountCents = detail.AmountCents
		amountDisplay = &text
	}

	// The seat list is always an array, so a client can iterate it without a nil check.
	// Row label and seat number are the values painted at the door; the event seat id is
	// what a later request addresses.
	seats := make([]map[string]any, 0, len(detail.Seats))
	for _, seat := range detail.Seats {
		seats = append(seats, map[string]any{
			"eventSeatId": seat.EventSeatID,
			"rowLabel":    seat.RowLabel,
			"seatNumber":  seat.SeatNumber,
		})
	}

	// A venue zone that cannot be resolved is a failure, not a guess: a confirmation that
	// printed the wrong hour would send the buyer to the screening at the wrong time.
	localStartsAt, err := detail.LocalStartsAt()
	if err != nil {
		writeReservationError(w, fmt.Errorf("%w: %s", catalog.ErrUnavailable, err))
		return
	}

	// The tickets are an empty array while the order is unpaid, never null.
	ticketIDs := detail.TicketIDs
	if ticketIDs == nil {
		ticketIDs = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reservationId": detail.ID,
		"status":        detail.Status,
		// The deadline is answered in UTC for the same reason the catalogue's screening
		// time is: the driver hands timestamps back in the database session's zone, and a
		// countdown that compared an offset-bearing string would break as soon as a
		// deployment changed its session zone.
		"expiresAt": detail.ExpiresAt.UTC(),
		// The reservation *is* the order, so the keys do not need the "order" qualifier
		// the ticket route uses to keep a per-ticket document from reading the order total
		// as one seat's price.
		"amountCents":   amountCents,
		"amountDisplay": amountDisplay,
		// What paying costs right now, and null whenever nothing is due. It is a quote:
		// the charge is whatever the payment answer reports.
		"amountDueCents":   dueCents,
		"amountDueDisplay": dueDisplay,
		"seats":            seats,
		"ticketIds":        ticketIDs,
		// The screening, so a reloaded confirmation says what is being bought and when.
		// Both time forms are answered for the same reason the catalogue answers both: the
		// instant for comparison, the venue's local form for reading, computed server side
		// so the browser never has to know the venue's zone.
		"eventId":       detail.EventID,
		"filmTitle":     detail.FilmTitle,
		"cinemaName":    detail.CinemaName,
		"roomName":      detail.RoomName,
		"startsAt":      detail.StartsAt.UTC(),
		"startsAtLocal": localStartsAt,
	})
}

// writeReservationError maps the two read outcomes to their status and problem code.
//
// There is no third outcome on purpose. An unknown reservation — and an identifier that
// could not name one — is 404 `not_found`, and the detail never echoes what was asked
// for: the identifier is the capability, so a wrong guess and an identifier that never
// existed must be indistinguishable. Anything else is 503 `service_unavailable`, which
// is retryable and never an empty reservation a confirmation page would render as an
// order with no seats.
func writeReservationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "Not found",
			"the requested reservation does not exist")
	default:
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the reservation is temporarily unavailable; retry the request")
	}
}

// reservationReadRoute is the confirmation read's route pattern, relative to the
// /api/v1 mount point. It does not collide with the hold creation route: that one is
// POST /reservations/holds, and this one answers GET.
const reservationReadRoute = "GET /reservations/{reservationID}"
