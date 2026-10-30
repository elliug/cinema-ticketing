// The ticket verification route, POST /api/v1/tickets/verify, lives in this file.
//
// It is the door scanner's route, and it is public for the same reason the routes
// next to it are, only more so: a scanner at the entrance is a browser or a phone,
// and it has no administrative token. Nobody is authenticated here, and nothing in
// the request is trusted. The body carries the signed code that was presented
// (`{"token": "..."}`), the service checks that code against the stored ticket, and
// the answer says what the ticket is and whether it admits its holder.
//
// The answers, per case, are the contract:
//
//   - a code that does not verify — malformed, truncated, forged, tampered, signed in
//     a version this build does not understand — is `400 invalid_token`;
//   - a code that verifies but names a ticket this database does not know, or whose
//     signed ticket, event or seats disagree with the stored ticket, is the **same**
//     `400 invalid_token`. A signature over data that no longer matches the sale is
//     not proof of anything, and one answer for both keeps a scanner (or anybody
//     probing ticket identifiers) from learning whether an id exists. The refusal
//     never names a stored value;
//   - a genuine code answers `200` with the ticket id, the event id, the seats, the
//     ticket status, the event status, a `valid` flag and — when the ticket no longer
//     admits its holder — a machine-readable `reason` (`voided` or
//     `event_cancelled`), so the door can tell "this ticket was cancelled" from
//     "this code is a forgery". The flag is [tickets.TicketDetail.Valid], the same
//     rule the web page and the PDF use, so the three cannot disagree;
//   - a database that could not be read is `503 service_unavailable`: a door that
//     cannot check anything must not be told the ticket is a forgery. That answer
//     says nothing about the code or about which tickets exist.
//
// The submitted token is never echoed in a response, in a problem detail or in a log
// line: the request body is read for exactly one purpose and then discarded. The body
// is bounded, because this route is public and reachable by anybody.

package publicapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// maxVerifyBodyBytes bounds a verification request body. A QR token is a payload of
// well under a hundred bytes plus its 32 byte signature, so a few kilobytes is
// already generous; the bound exists so this public route cannot be used to make the
// process buffer an arbitrary request.
const maxVerifyBodyBytes = 4 << 10

// verifyTicketRequest is the whole request: the presented code and nothing else.
type verifyTicketRequest struct {
	Token string `json:"token"`
}

// verifyTicket checks a presented code against the stored ticket.
func (h *Handler) verifyTicket(w http.ResponseWriter, r *http.Request) {
	var req verifyTicketRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVerifyBodyBytes)).Decode(&req); err != nil {
		// The decoder's message can quote the body it refused, and the body may carry
		// a token, so the caller is given a fixed sentence instead.
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request",
			"request body must be valid JSON with a token")
		return
	}
	// A blank code is not a code: it cannot verify, and it is answered exactly like a
	// forged one rather than as a malformed request.
	if strings.TrimSpace(req.Token) == "" {
		writeInvalidToken(w)
		return
	}

	detail, err := h.tickets.Verify(r.Context(), req.Token)
	if err != nil {
		switch {
		case errors.Is(err, tickets.ErrInvalidToken):
			writeInvalidToken(w)
		case errors.Is(err, tickets.ErrInvalidKey):
			// The deployment's signing key is unusable, so no code can be checked. That
			// is our failure, not a verdict on the ticket.
			writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
				"the ticket check is not available right now; retry the request")
		default:
			writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
				"the ticket check is temporarily unavailable; retry the request")
		}
		return
	}

	// The seats are the ticket's own, in the same shape the detail route serves, so a
	// scanner reads one seat list format on both routes.
	seats := make([]map[string]any, 0, len(detail.Seats))
	for _, seat := range detail.Seats {
		seats = append(seats, map[string]any{
			"eventSeatId": seat.EventSeatID,
			"rowLabel":    seat.RowLabel,
			"seatNumber":  seat.SeatNumber,
		})
	}

	body := map[string]any{
		"ticketId":     detail.TicketID,
		"eventId":      detail.Event.ID,
		"seats":        seats,
		"ticketStatus": detail.Status,
		"eventStatus":  detail.Event.Status,
		"valid":        detail.Valid(),
	}
	// A reason is answered only for a ticket that does not admit its holder: a valid
	// ticket has no reason, and the key is absent rather than empty.
	if reason := detail.InvalidReason(); reason != "" {
		body["reason"] = reason
	}
	writeJSON(w, http.StatusOK, body)
}

// writeInvalidToken is the one refusal for every code that is not a ticket of ours:
// the same status, code, title and detail whether the ticket it names exists or not.
func writeInvalidToken(w http.ResponseWriter) {
	writeProblem(w, http.StatusBadRequest, "invalid_token", "Invalid token",
		"the presented code is not a valid ticket")
}

// verifyTicketRoute is the ticket verification route pattern, relative to the
// /api/v1 mount point. It is a POST because it carries the presented code in its body
// rather than in a URL: a token in a path would end up in access logs and browser
// histories, and the token is exactly what must not be logged.
const verifyTicketRoute = "POST /tickets/verify"
