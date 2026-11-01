// The resend route, POST /api/v1/tickets/{ticketId}/email, lives in this file.
//
// It is the API half of the ticket page's "send it to this address" action: the same
// purchase email the checkout sends, carrying the same signed link to the ticket's order, but
// to an address typed at that moment and stored nowhere. It is public for the reason every
// other ticket route here is: in v1 the ticket identifier is the capability, because guest
// checkout has no account that could prove who owns an order.
//
// # What that makes possible
//
// A relay. Anybody holding a ticket identifier — which is printed on the ticket, in a URL,
// in a mailbox — could otherwise post any mailbox to it and have this deployment mail a link
// that opens the tickets. So the action is bounded per reservation by the email package's own
// counter, mounted here only where the deployment can actually send, and a counter that
// cannot be read refuses the send instead of allowing it.
//
// # The answers, per case
//
//   - sent: `202` with `{"sent": true}`. The send happened through the provider before the
//     answer is written, so the status means what it says;
//   - an address the checkout would refuse, a body that is not `{"email": "…"}`, an unknown
//     field: `400 invalid_input`. The rule is the checkout's own, imported rather than
//     restated, so the purchase form and the resend form cannot disagree about what an
//     address is;
//   - an unknown ticket and a ticket of an order that was never paid: one opaque refusal,
//     the ticket route's own `404 not_found` body, byte for byte. A caller must not learn
//     whether a ticket identifier exists;
//   - no mail provider on this deployment: `503 service_unavailable` saying so. A
//     deployment that cannot send must never say it did;
//   - over the limit: `429` with a `Retry-After` header and a problem body that repeats the
//     wait, because a client that only reads the body still has to tell the buyer something
//     useful;
//   - a provider, a counter or the link signing that cannot answer: `503 service_unavailable`,
//     retryable, never a `202`.
//
// The request log line names the reservation and the ticket. It never names the recipient —
// the address is not the order's business, which is the whole point of this route — and never
// the link, which is a credential.

package publicapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/email"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// TicketEmailService is the resend action contract: the order's ticket email, to an address
// typed on the page, stored nowhere. It is satisfied by the email package's [email.Resender];
// a deployment with no mail provider wires nothing, and then the route is unmounted.
type TicketEmailService interface {
	// Resend mails the reservation's ticket email to to, or reports why it will not.
	Resend(ctx context.Context, reservationID, to string) error
}

// WithTicketEmail attaches the resend action and returns the same handler, so the route is
// mounted only when one is wired.
//
// A nil action leaves the route unmounted: a resend with nothing behind it could only answer
// 503, and a deployment that sends no mail must not advertise a way to send it.
func (h *Handler) WithTicketEmail(svc TicketEmailService) *Handler {
	h.ticketEmails = svc
	return h
}

// resendTicketEmail mails the ticket's order to an address the caller names, once the limit
// allows it.
//
// The steps are ordered because each one decides what the next may see. The address is
// checked first, so a malformed request costs no read and answers the same way whether or not
// the ticket exists — a `400` before any lookup cannot become an oracle for identifiers. The
// ticket is then resolved to its order through the two reads the routes beside this one
// already use, and only a paid sale is mailed, because only a paid sale has tickets.
func (h *Handler) resendTicketEmail(w http.ResponseWriter, r *http.Request) {
	to, err := ticketEmailRecipient(r)
	if err != nil {
		writeTicketEmailInput(w, err)
		return
	}

	detail, err := h.tickets.Detail(r.Context(), r.PathValue("ticketId"))
	if err != nil {
		writeTicketEmailReadError(w, err, tickets.ErrNotFound)
		return
	}
	order, err := h.reservations.Reservation(r.Context(), detail.ReservationID)
	if err != nil {
		writeTicketEmailReadError(w, err, catalog.ErrNotFound)
		return
	}
	// An order that was never paid sold no ticket, so there is nothing to mail and the caller
	// learns nothing: it answers exactly like a ticket identifier that never existed.
	if order.Status != paidReservationStatus {
		writeTicketLinkNotFound(w)
		return
	}

	if err := h.ticketEmails.Resend(r.Context(), detail.ReservationID, to); err != nil {
		writeTicketEmailError(w, err)
		return
	}

	// The order and the ticket are what an operator needs to follow a resend. The address is
	// not: it is the one thing this route promised not to keep, and a log is a place things
	// are kept in. Neither is the link, which is a credential.
	slog.Default().Info("ticket email resent",
		"reservation", detail.ReservationID, "ticket", detail.TicketID)

	writeJSON(w, http.StatusAccepted, map[string]any{"sent": true})
}

// writeTicketEmailReadError maps a failed read to its answer: an identifier this deployment
// does not know is the opaque ticket refusal, and anything else is a read we could not
// complete, which says nothing about the ticket and is retryable.
func writeTicketEmailReadError(w http.ResponseWriter, err, unknown error) {
	if errors.Is(err, unknown) {
		writeTicketLinkNotFound(w)
		return
	}
	writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
		"the ticket email cannot be prepared right now; retry the request")
}

// maxTicketEmailBodyBytes bounds the one field this route reads, so a client cannot make the
// server buffer a body it has no use for.
const maxTicketEmailBodyBytes = 1 << 10

// ticketEmailRecipient reads the address to mail from the request body.
//
// The body is **not** optional here, unlike the checkout's: there the address is an addition
// to a sale that must happen anyway and an absent body means "mail nothing", while here the
// address is the entire request. So an absent body, an empty object, an empty address and a
// value the checkout's own rule refuses are all the same `ErrInvalidInput`.
//
// Unknown fields are refused for the reason the checkout refuses them: `{"emial": …}` would
// silently become "no address", and the caller would learn it only when the mail never
// arrived.
func ticketEmailRecipient(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", fmt.Errorf("%w: the request body must be {\"email\": \"…\"}", checkout.ErrInvalidInput)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxTicketEmailBodyBytes))
	if err != nil {
		return "", fmt.Errorf("%w: the request body could not be read", checkout.ErrInvalidInput)
	}

	var body struct {
		Email string `json:"email"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return "", fmt.Errorf("%w: the request body must be {\"email\": \"…\"} and nothing else", checkout.ErrInvalidInput)
	}

	// The checkout's rule, imported rather than restated: two answers to "is this an address"
	// can only drift apart, and the one that drifts is the one nobody is looking at.
	address, err := checkout.NormalizeContactEmail(body.Email)
	if err != nil {
		return "", err
	}
	if address == "" {
		return "", fmt.Errorf("%w: email is required: this request has no address to fall back on", checkout.ErrInvalidInput)
	}
	return address, nil
}

// writeTicketEmailInput maps an unreadable or unusable request to its refusal.
func writeTicketEmailInput(w http.ResponseWriter, err error) {
	writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
}

// writeTicketEmailError maps each resend outcome to its own status and problem code, because
// they are four different things with four different things the caller can do.
func writeTicketEmailError(w http.ResponseWriter, err error) {
	var limited *email.RateLimitedError
	switch {
	case errors.As(err, &limited):
		// The header is the machine readable wait and the detail repeats it for a client
		// that reads only the body. Seconds are rounded up, never down, so the retry is not
		// answered by the same refusal again.
		seconds := int64(math.Ceil(limited.RetryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeProblem(w, http.StatusTooManyRequests, "rate_limited", "Too many requests",
			fmt.Sprintf("this reservation has been emailed the maximum of %d times in the last %s; wait %d seconds and try again",
				email.ResendLimit, email.ResendWindow, seconds))
	case errors.Is(err, email.ErrNotConfigured):
		// The honest answer for a deployment with no provider: not "try again later", which
		// would be a promise nobody configured.
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"this deployment does not send mail: no mail provider is configured")
	case errors.Is(err, checkout.ErrInvalidInput), errors.Is(err, email.ErrInvalidRecipient):
		// The action validates too, so a defect in the transport cannot spend the order's
		// allowance on an address that could never be mailed.
		writeTicketEmailInput(w, err)
	default:
		// A provider, a counter or the signing that could not answer. Retryable, and never a
		// success: the caller's next step is to tell somebody to check their inbox.
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the ticket email could not be sent right now; retry the request")
	}
}

// ticketEmailRoute is the resend route pattern, relative to the /api/v1 mount point. It is
// on the ticket subtree because the ticket is the capability the caller holds.
const ticketEmailRoute = "POST /tickets/{ticketId}/email"
