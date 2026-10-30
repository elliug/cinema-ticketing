// The ticket detail route, GET /api/v1/tickets/{ticketId}, lives in this file.
//
// It is public, like the hold and payment routes next to it, and for a stronger
// reason than they have: v1 sells to guests, so there is no account that could
// prove who owns a ticket. The ticket identifier is the capability — see the
// tickets package documentation — and the route serves that ticket's one shared
// projection, which the confirmation page (T7) and the PDF (T6) both render.
//
// Nothing about the detail is assembled here: this file only translates the
// projection into JSON, asks the projection for its derived presentations (the
// validity, the order amount text and the venue's local time), and maps the two
// failures to the problem shape the rest of the public API uses.

package publicapi

import (
	"context"
	"errors"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// TicketService is the ticket detail read contract. It is satisfied by the
// projection's PostgreSQL reader; a deployment without the checkout schema wires
// nothing, and then the route is not mounted at all.
type TicketService interface {
	Detail(ctx context.Context, ticketID string) (tickets.TicketDetail, error)
}

// WithTickets attaches the ticket detail reader and returns the same handler, so
// the ticket route is mounted only when one is wired.
//
// A nil reader leaves the route unmounted: a ticket route with nothing to read
// could only answer 503, and a deployment whose schema predates the checkout
// schema knows of no tickets to serve.
func (h *Handler) WithTickets(svc TicketService) *Handler {
	h.tickets = svc
	return h
}

// getTicket answers the projection of the ticket named by the path.
//
// The local screening time is computed from the venue row, so a surface never has
// to know the venue's zone and can never render the ticket in the machine's zone
// instead. A venue whose stored zone cannot be resolved is refused rather than
// answered with an invented local time: a ticket that says the wrong hour is worse
// than a ticket that could not be fetched.
func (h *Handler) getTicket(w http.ResponseWriter, r *http.Request) {
	detail, err := h.tickets.Detail(r.Context(), r.PathValue("ticketId"))
	if err != nil {
		writeTicketError(w, err)
		return
	}
	startsAtLocal, err := detail.Event.LocalStartsAt()
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the ticket detail cannot be presented right now; retry the request")
		return
	}

	// The seat list is always an array, so a client can iterate it without a nil
	// check, and the keys are camelCase like the rest of the public API. It holds
	// exactly this ticket's own seat, because one ticket is one seat.
	seats := make([]map[string]any, 0, len(detail.Seats))
	for _, seat := range detail.Seats {
		seats = append(seats, map[string]any{
			"eventSeatId": seat.EventSeatID,
			"rowLabel":    seat.RowLabel,
			"seatNumber":  seat.SeatNumber,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ticketId":      detail.TicketID,
		"status":        detail.Status,
		"valid":         detail.Valid(),
		"issuedAt":      detail.IssuedAt,
		"reservationId": detail.ReservationID,
		"event": map[string]any{
			"id":            detail.Event.ID,
			"filmTitle":     detail.Event.FilmTitle,
			"startsAt":      detail.Event.StartsAt,
			"startsAtLocal": startsAtLocal,
			"roomName":      detail.Event.RoomName,
			"cinemaName":    detail.Event.CinemaName,
			"timezone":      detail.Event.Timezone,
			"status":        detail.Event.Status,
		},
		"seats": seats,
		// The amount is the money taken for the whole reservation, so its keys say
		// "order": a per-ticket document that served it as "amountCents" would invite
		// a renderer to print the order total as this seat's price.
		"orderAmountCents":   detail.OrderAmountCents,
		"orderAmountDisplay": detail.OrderAmountText(),
	})
}

// writeTicketError maps the two ticket read outcomes to their status and problem
// code.
//
// There is no third outcome on purpose. An unknown ticket — and an identifier that
// could not name a ticket — is 404 `not_found`: the identifier is the capability,
// so a wrong guess and an identifier that never existed must be indistinguishable,
// and the detail never echoes what was asked for. Anything else is 503
// `service_unavailable`, which is retryable and never an empty ticket a renderer
// would print.
func writeTicketError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tickets.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "Not found",
			"the requested ticket does not exist")
	default:
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the ticket detail is temporarily unavailable; retry the request")
	}
}

// ticketRoute is the ticket detail route pattern, relative to the /api/v1 mount
// point.
const ticketRoute = "GET /tickets/{ticketId}"
