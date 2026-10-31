// The door's admission route, POST /api/v1/tickets/redeem, lives in this file.
//
// It is the act the verification route only asks about: the door presents the same signed
// code, and this route verifies it and then admits the ticket it names, exactly once.
//
// # One route, one answer
//
// Verification and redemption are one server answer, so the order of the two steps is the
// contract: the code is verified *first*, and only the ticket the verified code names is
// ever offered for admission. A code that does not verify is refused with the verification
// route's own status and body — same status, same code, same words — and the redemption is
// never attempted, so a forged code cannot write a row, cannot reach the "already
// redeemed" state, and cannot be told apart from a genuine code by its refusal. The
// identifier that reaches the store is the one inside the signature, never one a client
// sent.
//
// # The answers, per case
//
//   - a code that does not verify, and a code that verifies but names a ticket this
//     database does not know, are `400 invalid_token` — exactly what POST /tickets/verify
//     answers, because it is the same question. The refusal names no stored value;
//   - an unusable signing key, or a store that cannot be read or written, is
//     `503 service_unavailable`: our failure, never a verdict on the code in a cashier's
//     hand;
//   - an admission answers `200` with the ticket, its seat, its screening in both time
//     forms and the instant it was admitted, so the door can show what it just let in;
//   - a ticket that was already admitted is **not** an error: it is `200` with the *first*
//     instant and the *first* label, and `alreadyRedeemed: true` is the field a client
//     reads. A 4xx here would tell a cashier the ticket is bad when the ticket is fine and
//     its holder is not the first to present it;
//   - a void ticket and a cancelled screening are two refusals, `409 voided` and
//     `409 event_cancelled`, in the reason vocabulary the verification route already
//     answers with, because a cashier acts differently on each.
//
// The label is the opaque name a door gives itself — a till, a gate, a shift — trimmed by
// the store, and absent when the door names nothing. It is never a person: there are no
// accounts yet, and this file invents none.

package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// RedemptionService is the admission contract: the guarded write that admits one ticket,
// satisfied by the ticket store. A deployment that cannot write a redemption wires
// nothing, and then this route is unmounted.
type RedemptionService interface {
	// Redeem admits the named ticket at the door exactly once. The outcome is a value
	// rather than an error, because "already redeemed" and the two refusals are answers
	// a cashier acts on; an unreadable store is tickets.ErrUnavailable and an identifier
	// that names no ticket is tickets.ErrNotFound.
	Redeem(ctx context.Context, ticketID, label string) (tickets.Redemption, error)
}

// WithRedemption attaches the admission service and returns the same handler, so the
// admission route is mounted only when one is wired.
//
// The route needs the ticket service as well as this one: it verifies the code before it
// admits anything, and a deployment that could check a code but not admit it advertises
// half an act. A nil service therefore leaves the route unmounted, exactly like a nil
// ticket service does.
func (h *Handler) WithRedemption(svc RedemptionService) *Handler {
	h.redeems = svc
	return h
}

// redeemTicketRequest is the whole request: the presented code, and the label this door
// gives itself when it has one.
type redeemTicketRequest struct {
	Token string `json:"token"`
	Label string `json:"label"`
}

// redeemTicket verifies a presented code and then admits the ticket that code names.
//
// The steps are ordered and each has one reason:
//
//  1. Decode a body of the same bounded shape the verification route takes, so both routes
//     refuse a malformed request with the same answer.
//  2. Verify the code. Everything below this line runs only for a code that verified, which
//     is the whole of S3.
//  3. Present the screening. This happens before the write on purpose: a ticket whose venue
//     time cannot be computed must not be admitted, because the door would then hold a
//     redemption it cannot show.
//  4. Admit the ticket the code names, with this door's own label.
//  5. Answer what the door needs, whether this call admitted the ticket or found it already
//     admitted.
func (h *Handler) redeemTicket(w http.ResponseWriter, r *http.Request) {
	var req redeemTicketRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVerifyBodyBytes)).Decode(&req); err != nil {
		// The decoder's message can quote the body it refused, and the body may carry a
		// token, so the caller is given a fixed sentence instead.
		writeMalformedTokenRequest(w)
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
		writeUnverifiableToken(w, err)
		return
	}

	startsAtLocal, err := detail.Event.LocalStartsAt()
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the ticket cannot be presented right now; retry the request")
		return
	}

	redemption, err := h.redeems.Redeem(r.Context(), detail.TicketID, req.Label)
	if err != nil {
		writeRedeemError(w, err)
		return
	}
	redeemedAtLocal, localErr := detail.Event.LocalTime(redemption.RedeemedAt)
	if localErr != nil {
		writeRedeemError(w, localErr)
		return
	}
	if err != nil {
		writeRedeemError(w, err)
		return
	}
	switch redemption.Outcome {
	case tickets.RedemptionRefusedVoided:
		// The ticket was withdrawn: it no longer admits anybody, so nothing was written.
		writeProblem(w, http.StatusConflict, tickets.InvalidReasonVoided, "Conflict",
			"the ticket was voided, so it does not admit its holder")
		return
	case tickets.RedemptionRefusedEventCancelled:
		writeProblem(w, http.StatusConflict, tickets.InvalidReasonEventCancelled, "Conflict",
			"the screening was cancelled, so the ticket does not admit its holder")
		return
	case tickets.RedemptionRedeemed, tickets.RedemptionAlreadyRedeemed:
		// The two admissions: one wrote, the other was told when the first one did.
	default:
		// A store answered an outcome this route cannot present, and inventing a body for
		// it would report an admission nobody recorded.
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the admission is temporarily unavailable; retry the request")
		return
	}

	// The seats are the ticket's own, in the shape the verification route serves them, so a
	// door reads one seat list format on both routes. The instant and the label are the
	// stored ones: a losing call is told the winner's, never its own.
	seats := make([]map[string]any, 0, len(detail.Seats))
	for _, seat := range detail.Seats {
		seats = append(seats, map[string]any{
			"eventSeatId": seat.EventSeatID,
			"rowLabel":    seat.RowLabel,
			"seatNumber":  seat.SeatNumber,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ticketId":     detail.TicketID,
		"ticketStatus": detail.Status,
		"seats":        seats,
		"eventId":      detail.Event.ID,
		"eventStatus":  detail.Event.Status,
		"filmTitle":    detail.Event.FilmTitle,
		"roomName":     detail.Event.RoomName,
		"cinemaName":   detail.Event.CinemaName,
		// Both time forms, for the reason the ticket read answers both: the instant for
		// comparison, the venue's own hour for reading.
		"startsAt":      detail.Event.StartsAt.UTC(),
		"startsAtLocal": startsAtLocal,
		// When the door admitted the ticket, and which door it was. A used ticket answers
		// the first instant, so "used at 20:14" survives a second presentation.
		"redeemedAt": redemption.RedeemedAt.UTC(),
		// The same instant in the venue's own clock, beside the UTC form for exactly the
		// reason `startsAt` and `startsAtLocal` sit together in the ticket read: the venue's
		// hour is what a person at a door reads. Unreachable failure: the venue zone was
		// resolved before the write, so a zone that could not render here would already have
		// answered 503 without admitting anybody.
		"redeemedAtLocal": redeemedAtLocal,
		"redeemedBy":      redemption.RedeemedBy,
		// The field a client reads instead of a status code: true means this ticket was
		// already admitted, and `redeemedAt` is when it happened the first time.
		"alreadyRedeemed": redemption.Outcome == tickets.RedemptionAlreadyRedeemed,
	})
}

// writeRedeemError maps the admission outcomes to their status and problem code.
//
// The vocabulary is the one the routes next to it already use: a ticket that is no longer
// there is the verification route's `400 invalid_token` — the same answer the same code got
// a moment earlier — and an unreadable or unwritable store is retryable.
func writeRedeemError(w http.ResponseWriter, err error) {
	if errors.Is(err, tickets.ErrNotFound) {
		// The code verified and the ticket disappeared before it could be admitted. The
		// door is told what the verification route tells a code naming no ticket, which
		// names nothing either.
		writeInvalidToken(w)
		return
	}
	// Anything else is our failure to write or to read, never a verdict on the code: it is
	// retryable and says nothing about which tickets exist.
	writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
		"the ticket cannot be admitted right now; retry the request")
}

// redeemTicketRoute is the admission route pattern, relative to the /api/v1 mount point.
// It is a POST that carries the code in its body for the reason the verification route
// does: a token in a path would end up in access logs and browser histories.
const redeemTicketRoute = "POST /tickets/redeem"
