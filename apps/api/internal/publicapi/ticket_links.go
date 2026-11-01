// The signed ticket link route, GET /api/v1/ticket-links/{token}, lives in this file.
//
// It is the API half of the link a purchase email carries: the web route behind that link
// asks this route what the token opens, and renders the tickets it answers. The token is
// the only credential the request has — it needs no session, because guest checkout has no
// account to prove who owns an order — so this route is public for the reason the ticket
// and reservation routes are, only more so: the link arrives from a mailbox, possibly one
// that is not the buyer's.
//
// # Why the token is opaque here too
//
// The token names a reservation and an expiry, signed with the link key
// ([tickets.LinkSigner]). It is in the path because that is what a link is, and the path is
// exactly why the route never logs it, never echoes it in a body and never puts it in a
// problem detail: a URL spends its life in access logs, browser histories and `Referer`
// headers. What the route answers is the order's ticket identifiers and the instant the
// signature already carries, and nothing about the buyer.
//
// # The answers, per case
//
//   - a token that verifies, names a paid order and has tickets answers `200` with the
//     order, its ticket identifiers ascending, and the expiry the signature spelled. Only a
//     **paid** sale opens: an unpaid, expired, cancelled or ticketless reservation sold no
//     ticket to show;
//   - **every** other way of not opening is one refusal with one status and one body: a
//     forged, malformed or tampered token, an expired one, a reservation this database does
//     not know, an order that was never paid. That is the ticket route's own unknown-ticket
//     `404 not_found` body, byte for byte, so a caller cannot tell an aged-out email from a
//     guess, and probing cannot confirm that a reservation exists;
//   - an unusable signing key or a store that cannot be read is `503 service_unavailable`:
//     our failure, retryable, and never a verdict on the link in somebody's inbox.
//
// The expiry is checked before the store is read, so an old email costs one HMAC and no
// query, and an outage can never turn an aged-out link into a retry invitation.

package publicapi

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// paidReservationStatus is the one stored reservation status a link opens. The value is the
// database's own vocabulary (`cinema.reservations.status`), carried here rather than
// reinterpreted: a link is for tickets that were sold, and "paid" is the only status that
// means they were.
const paidReservationStatus = "paid"

// WithTicketLinks attaches the signed link verifier and returns the same handler, so the
// link route is mounted only when one is wired.
//
// The route needs the reservation read as well as the signer: the token names an order and
// the route resolves its tickets, so a deployment without either half advertises nothing
// (see [Handler.Mux]). A nil signer therefore leaves the route unmounted, exactly like a nil
// reservation reader does.
func (h *Handler) WithTicketLinks(signer *tickets.LinkSigner) *Handler {
	h.ticketLinks = signer
	return h
}

// openTicketLink verifies a link and answers the tickets of the paid order it names.
//
// The steps are ordered because each one decides what the next may see:
//
//  1. Verify the token. Nothing below this line runs for a link this deployment did not
//     sign, and an expired link stops here so an aged-out email is refused without a read.
//  2. Read the order through the confirmation's own projection, which is the query that
//     already lists an order's tickets; no second statement is invented for the link, so the
//     two cannot disagree about what an order sold.
//  3. Refuse anything that is not a paid sale, with the same body as step 1.
//  4. Answer the identifiers ascending, so two readers of one link never see two orders.
func (h *Handler) openTicketLink(w http.ResponseWriter, r *http.Request) {
	claims, err := h.ticketLinks.Verify(r.PathValue("token"), time.Now())
	if err != nil {
		// The signing key is unusable: no link can be checked at all, which is our failure
		// rather than a verdict on the one presented, and is retryable.
		if errors.Is(err, tickets.ErrInvalidKey) {
			writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
				"the ticket link cannot be checked right now; retry the request")
			return
		}
		// Expiry and every verification failure are one answer: see writeTicketLinkNotFound.
		writeTicketLinkNotFound(w)
		return
	}

	order, err := h.reservations.Reservation(r.Context(), claims.ReservationID)
	if err != nil {
		// A reservation that does not exist keeps the ticket route's refusal; anything else
		// is a read we could not complete, which says nothing about the link.
		if errors.Is(err, catalog.ErrNotFound) {
			writeTicketLinkNotFound(w)
			return
		}
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the ticket link cannot be opened right now; retry the request")
		return
	}
	// An order that was never paid sold no ticket, so there is nothing for the link to open.
	// It answers exactly like a link that never existed, which is also what an order whose
	// tickets are missing answers: a paid order without them is not a link target either.
	if order.Status != paidReservationStatus || len(order.TicketIDs) == 0 {
		writeTicketLinkNotFound(w)
		return
	}

	// One stable order, so the same link answers the same list twice and a client can diff
	// it. The copy keeps the projection's own slice untouched for whoever reads it next.
	ticketIDs := make([]string, len(order.TicketIDs))
	copy(ticketIDs, order.TicketIDs)
	sort.Strings(ticketIDs)

	// The order and the count are what an operator needs; the token is a credential and the
	// buyer's address is not this route's business, so neither is here.
	slog.Default().Info("ticket link opened",
		"reservation", claims.ReservationID, "tickets", len(ticketIDs))

	writeJSON(w, http.StatusOK, map[string]any{
		// The identifier inside the signature, not one a client could have sent, and it is
		// the one the read confirmed: the link opens the order it names and no other.
		"reservationId": claims.ReservationID,
		// The instant the link closes, read from the signature rather than recomputed, so the
		// page can say when the email stops working and cannot disagree with the verifier.
		"expiresAt": claims.ExpiresAt.UTC(),
		"ticketIds": ticketIDs,
	})
}

// writeTicketLinkNotFound is the one refusal this route answers with: a forged, malformed or
// tampered token, an expired one, an unknown reservation, an unpaid order and a paid order
// with no tickets are all indistinguishable here.
//
// It is the ticket route's own unknown-ticket body, reused rather than restated, so the two
// routes cannot drift into two different spellings of "there is nothing here" and a prober
// has nothing to compare. The detail names neither the token nor the reservation.
func writeTicketLinkNotFound(w http.ResponseWriter) {
	writeTicketError(w, tickets.ErrNotFound)
}

// ticketLinkRoute is the signed link route pattern, relative to the /api/v1 mount point. The
// token is in the path because that is the shape of the URL an email carries; it is the one
// thing this route must never log.
const ticketLinkRoute = "GET /ticket-links/{token}"
