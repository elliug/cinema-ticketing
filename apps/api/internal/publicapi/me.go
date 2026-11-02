// The account's own list lives in this file: GET /api/v1/me/tickets (T4, S5, D3).
//
// Until this route existed, "my tickets" was a note the browser wrote: the list of identifiers
// that one tab happened to keep (apps/web/src/lib/issued-tickets.ts). It survived neither the
// browser nor the device nor clearing site data, and it could never honestly be called the
// account's — nothing on the server knew who was asking. Reading it here is the whole reason a
// login is worth having (D3), and it is the one surface the session protects: a ticket handed
// out stays reachable by its own link whatever happens to accounts (D1, and the tickets package
// documentation on why the identifier is the capability).
//
// # The session is presented, never stored
//
// The credential arrives as `Authorization: Bearer <token>` — the token the sign-in route
// answered exactly once, which the web keeps in its own HttpOnly cookie and presents on the
// server's behalf. This API sets no cookie and reads none, so there is nothing here to
// confuse with the web's own session store.
//
// # Every unusable session is the sign-in route's own refusal
//
// A missing header, a header that is not a bearer, a string that is not shaped like a token, a
// session that expired, one that was signed out and one this deployment never issued all
// answer [writeIdentityUnauthorized]: the same status and the same bytes the login route
// already uses. The reason is not tidiness. A session that has just ended and a credential
// somebody is watching must be indistinguishable from outside, or the route becomes a way to
// ask "was this token ever real?" — the question the whole login surface exists to refuse.
//
// The token's shape is not checked here: [identity.SessionService.Authenticate] owns that
// rule, refuses anything that cannot be a token before it costs a query, and answers
// [identity.ErrNoSession] for all of them. A second shape check in the transport could only
// disagree with the one in the domain.
//
// # An empty account is a list, not a refusal
//
// `200 {"tickets": []}` for an account with no orders, and it is asserted as exactly that: the
// account exists and has bought nothing, which is the truth. A 404 would say the account does
// not exist, and a 401 would say the session is not theirs; both would be lies about a person
// who simply has not bought a ticket yet.
//
// # The list is the ticket page's document
//
// Each entry is the projection the ticket route serves for the same ticket, field for field,
// including its signed code, because the web renders one ticket component in both places. A
// second, thinner shape would be a second rendering of a ticket that could disagree with the
// page it links to — and the code in particular cannot be minted by a renderer: it is the
// detail's own, attached by the ticket service so the page and the PDF show one code.
//
// # A store that cannot answer is never an empty list
//
// A read failure is `503 service_unavailable`, retryable. An empty list is a claim about the
// account — "you bought nothing" — and a database that is down has not earned the right to make
// it, exactly as the ticket route refuses to answer an empty ticket. A session store that
// cannot answer is a 503 too and not a 401, for the same reason: an outage must never look like
// a silent sign-out.

package publicapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"cinema.local/ticketing/apps/api/internal/identity"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// myTicketsRoute is the account's list route, relative to the /api/v1 mount point.
const myTicketsRoute = "GET /me/tickets"

// SessionAuthenticator answers the session a presented token opens. It is satisfied by
// [identity.SessionService], which owns the hashing, the liveness rule and the one opaque
// refusal; a deployment without identity wires nothing and mounts no account route.
type SessionAuthenticator interface {
	// Authenticate answers the live session the token opens, [identity.ErrNoSession] for
	// every way a session can be unusable, and [identity.ErrSessionUnavailable] when the
	// store could not answer at all.
	Authenticate(ctx context.Context, token string) (identity.Session, error)
}

// AccountTicketsService lists the tickets one account owns. It is satisfied by the ticket
// store's read, so the list answers the same projection the ticket page does instead of a
// summary invented for this route.
type AccountTicketsService interface {
	// TicketsByCustomer answers every ticket of the account's own orders, newest first. An
	// account that bought nothing answers an empty slice and no error.
	TicketsByCustomer(ctx context.Context, customerID string) ([]tickets.TicketDetail, error)
}

// WithIdentityTickets attaches the session read and the account list, and returns the same
// handler, so the account route is mounted only where the login is.
//
// The transport is mounted inside the identity surface (see [Handler.Mux]) and never beside
// it: a deployment with no login has no signed-in account for this route to answer for, and a
// route that could only ever refuse would advertise "these are your tickets" to people who
// cannot sign in. Both collaborators are required for the same reason the two login routes
// are mounted as a pair — a session read with no list, or a list with no way to prove who is
// asking, is half a capability.
func (h *Handler) WithIdentityTickets(sessions SessionAuthenticator, account AccountTicketsService) *Handler {
	h.sessionAuthenticator = sessions
	h.accountTickets = account
	return h
}

// listMyTickets answers the account's tickets for the session presented in the Authorization
// header.
//
// The order of the answer is the store's order — newest first, with the ticket identifier
// breaking ties — because the read is the only place that knows what "newest" means; the
// transport preserves it and never re-sorts, which would be a second opinion about the
// account's history.
func (h *Handler) listMyTickets(w http.ResponseWriter, r *http.Request) {
	session, err := h.sessionAuthenticator.Authenticate(r.Context(), bearerToken(r))
	if err != nil {
		if errors.Is(err, identity.ErrNoSession) {
			// The login route's own refusal, byte for byte: see the file comment.
			writeIdentityUnauthorized(w)
			return
		}
		// A store that could not answer is our failure and retryable. It is never a 401,
		// which would tell every signed-in person their session had ended.
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"your tickets cannot be read right now; retry the request")
		return
	}

	details, err := h.accountTickets.TicketsByCustomer(r.Context(), session.CustomerID)
	if err != nil {
		// Every failure lands here, including one the store reported as "not found": the
		// account's identifier came from the same database the read just used, so an
		// identifier it does not know is a broken read rather than an account with no
		// tickets — and an account with no tickets is a 200 with an empty list.
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"your tickets cannot be read right now; retry the request")
		return
	}

	// The list is always an array, so a client iterates it without a nil check and an empty
	// account is answered as [] rather than null.
	entries := make([]map[string]any, 0, len(details))
	for _, detail := range details {
		document, err := ticketDocument(detail)
		if err != nil {
			// A venue whose stored zone cannot be resolved is refused rather than answered
			// with an invented local time, the same way the ticket route refuses it: a
			// ticket that says the wrong hour is worse than a list that could not be read.
			writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
				"your tickets cannot be read right now; retry the request")
			return
		}
		entries = append(entries, document)
	}

	writeJSON(w, http.StatusOK, map[string]any{"tickets": entries})
}

// bearerToken reads the session token from the Authorization header, and answers the empty
// string when there is nothing a bearer could be.
//
// An empty string is not a second refusal rule: [identity.SessionService.Authenticate] refuses
// it like any other string that cannot be a token, so a request with no credential never
// reaches the store and every unusable header still answers the same fixed 401.
func bearerToken(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// ticketDocument is the wire document of one ticket: the ticket route's own fields, with the
// same camelCase names, the same derived presentations and the same signed code.
//
// It exists because the account's list answers the same document the ticket page does. The
// ticket route assembles its answer in tickets.go, which this read does not rewrite, so the two
// spellings are kept equal by construction wherever they can be and by a test that compares the
// bytes the two routes serve for one ticket — a renamed or dropped key on either side is a
// change the web would notice, and the test is what notices it first.
func ticketDocument(detail tickets.TicketDetail) (map[string]any, error) {
	startsAtLocal, err := detail.Event.LocalStartsAt()
	if err != nil {
		return nil, err
	}

	seats := make([]map[string]any, 0, len(detail.Seats))
	for _, seat := range detail.Seats {
		seats = append(seats, map[string]any{
			"eventSeatId": seat.EventSeatID,
			"rowLabel":    seat.RowLabel,
			"seatNumber":  seat.SeatNumber,
		})
	}

	return map[string]any{
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
		"seats":              seats,
		"orderAmountCents":   detail.OrderAmountCents,
		"orderAmountDisplay": detail.OrderAmountText(),
		"qrToken":            detail.QRToken,
	}, nil
}
