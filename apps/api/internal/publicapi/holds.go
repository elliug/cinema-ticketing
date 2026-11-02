// Package publicapi exposes the customer facing HTTP surface: the M2 hold
// creation endpoint, the event seat availability read, the M3 checkout route that
// pays a held reservation, the M3 ticket detail route that the confirmation page
// and the PDF both read, the M3 ticket QR image route that renders that signed code as
// an SVG the ticket page shows, the M3 ticket PDF download route that answers a
// short-lived pre-signed redirect, the M3 ticket verification route a door
// scanner calls with a presented signed code, and the M3.5 reads the web needs: the
// public catalogue of screenings (GET /events), one screening of that catalogue
// (GET /events/{eventID}) so the seat page keeps the film, the venue, the hour and the
// price it arrived with, the confirmation's read of one order
// (GET /reservations/{reservationId}), the cancel route that ends a hold now instead
// of waiting out its window (POST /reservations/{reservationId}/cancel), and the door's
// admission route, which verifies a presented code and admits the ticket it names exactly
// once (POST /tickets/redeem), the read behind the signed link a purchase email carries
// (GET /ticket-links/{token}), which verifies the token and answers the paid order's tickets,
// the resend route that mails that same ticket email to an address typed on a ticket page
// (POST /tickets/{ticketId}/email), which stores nothing it is given, the sign-in link a login
// email carries (POST /identity/links), the route that spends one for a session token
// (POST /identity/sessions), the route that ends that session again
// (DELETE /identity/sessions), and the account's own list (GET /me/tickets), which is the read
// that makes signing in worth anything.
//
// Routes here are public application routes. They are mounted outside the
// administrative guard, because a customer selecting seats, paying for them, showing
// the ticket and scanning that ticket at the door has no administrative token; see
// httpserver.Server.WithPublic. The ticket and reservation routes are public for a
// further reason the tickets and catalog packages document: in v1 the identifier is the
// capability, because guest checkout has no account that could prove who owns a ticket
// or an order, and the M3.5 demonstration session authenticates nobody.
package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cinema.local/ticketing/apps/api/internal/reservation"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// HoldService is the reservation application service contract.
type HoldService interface {
	CreateHold(ctx context.Context, idempotencyKey, eventID string, eventSeatIDs []int64) (reservation.Hold, error)
	ListAvailability(ctx context.Context, eventID string) ([]reservation.SeatAvailability, error)
}

// Handler serves the public reservation routes.
type Handler struct {
	svc          HoldService
	payments     PaymentService
	tickets      TicketService
	ticketPDFs   TicketPDFService
	catalog      CatalogService
	reservations ReservationService
	cancels      CancellationService
	redeems      RedemptionService
	// ticketLinks verifies the signed link a purchase email carries. It is a concrete
	// signer rather than a service: the token is checked here, and the order it names is
	// read through the reservation read the confirmation already uses.
	ticketLinks *tickets.LinkSigner
	// ticketEmails is the resend action: the order's ticket email, to an address typed on a
	// ticket page, stored nowhere. It is a service because the counter, the provider and the
	// link signing behind it are the wiring's business, not this package's.
	ticketEmails TicketEmailService
	// identityLinks issues the sign-in link a login email carries and identitySessions spends
	// one for a session token. They are two halves of one act, so they are mounted together or
	// not at all: see [Handler.WithIdentity].
	identityLinks    IdentityLinkService
	identitySessions IdentitySessionService
	// identitySignOut ends the session the sign-out route presents. It is the session
	// service's Close, and it is mounted inside the login surface: see [Handler.Mux].
	identitySignOut IdentitySessionCloser
	// sessionAuthenticator reads the session a presented token opens, and accountTickets lists
	// the tickets the account behind it owns. Together they are GET /me/tickets, which is
	// mounted only inside the login surface and only with both of them: see [Handler.Mux].
	sessionAuthenticator SessionAuthenticator
	accountTickets       AccountTicketsService
}

// New builds a Handler over the given service. The checkout, ticket detail, ticket
// PDF, ticket admission, ticket link, ticket email, identity, account list, catalogue and
// reservation read routes are attached separately, by WithPayments, WithTickets, WithTicketPDFs,
// WithRedemption, WithTicketLinks, WithTicketEmail, WithIdentity, WithIdentityTickets,
// WithCatalog, WithReservations and WithCancellation, so a deployment without the checkout
// schema, without object storage, without a mail provider or without identity mounts none of
// them.
func New(svc HoldService) *Handler { return &Handler{svc: svc} }

// Mux returns the public route table. The routes are relative to the mount
// point /api/v1, matching httpserver.Server.WithPublic.
func (h *Handler) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /reservations/holds", h.createHold)
	mux.HandleFunc("GET /events/{eventID}/seats", h.listEventSeats)
	// The catalogue is what makes the seat page reachable without an event id, so it is
	// mounted with the seat read it feeds. Both come from one read model — the catalogue
	// of screenings and the confirmation's read of one order — which is why one wiring
	// decision mounts them together.
	if h.catalog != nil {
		mux.HandleFunc(eventsRoute, h.listEvents)
		// One screening of the catalogue, mounted with the list it belongs to: the seat page
		// a buyer reaches from the catalogue holds an event id and would otherwise lose the
		// film, the venue, the hour and the price the entry just showed it. Both answers come
		// from the same read model, and neither is mounted without it.
		mux.HandleFunc(eventRoute, h.getEvent)
	}
	if h.reservations != nil {
		// GET on the reservation subtree, next to the POST that creates a hold there: the
		// buyer who reloads the confirmation has an identifier and no way to ask again.
		mux.HandleFunc(reservationReadRoute, h.getReservation)
		// The cancel route is mounted with the read it answers in the same shape: a cancel
		// that had nowhere to read the order from could only fail, and a buyer who abandons
		// a hold would keep waiting out its window. Both halves are required.
		if h.cancels != nil {
			mux.HandleFunc(cancelRoute, h.cancelReservation)
		}
	}
	if h.payments != nil {
		mux.HandleFunc(paymentRoute, h.createPayment)
	}
	if h.tickets != nil {
		mux.HandleFunc(ticketRoute, h.getTicket)
		// The verification route is mounted with the ticket route: it checks the code
		// that route serves, so mounting one without the other would advertise half a
		// capability.
		mux.HandleFunc(verifyTicketRoute, h.verifyTicket)
		// The QR image is the same signed code rendered as a picture, so the page and
		// the PDF show one code and the web needs no QR dependency. It is mounted with
		// the ticket route for the same reason the verification route is: it draws the
		// code that route serves.
		mux.HandleFunc(ticketQRRoute, h.getTicketQR)
		// The admission route is mounted with both halves of the act it performs: the code
		// that route serves, which the verification route checks, and the guarded write that
		// admits the ticket it names. A door that could check a code but not admit it would
		// advertise half of what a door does, so both are required.
		if h.redeems != nil {
			mux.HandleFunc(redeemTicketRoute, h.redeemTicket)
		}
	}
	// The download route needs both halves: a ticket to download and somewhere to store
	// the document. A deployment without object storage never attaches the archive, so it
	// advertises no download it could not honour.
	if h.tickets != nil && h.ticketPDFs != nil {
		mux.HandleFunc(ticketPDFRoute, h.downloadTicketPDF)
	}
	// The signed ticket link is the one URL a purchase email carries. It needs both halves it
	// is built from — the signer that checks the token and the reservation read that resolves
	// the order's tickets — so a deployment with only one of them advertises nothing. It is
	// public like the ticket routes beside it for the same reason: the link arrives from a
	// mailbox, and no session comes with it.
	if h.ticketLinks != nil && h.reservations != nil {
		mux.HandleFunc(ticketLinkRoute, h.openTicketLink)
	}
	// The resend route is the other half of the same email: it mails that message to an
	// address typed now, so it needs the action, the ticket that names the order and the read
	// that confirms the order was paid. A deployment missing any of the three advertises
	// nothing rather than a route that could only answer 503 — in particular, one with no mail
	// provider never advertises a way to send mail.
	if h.ticketEmails != nil && h.tickets != nil && h.reservations != nil {
		mux.HandleFunc(ticketEmailRoute, h.resendTicketEmail)
	}
	// The login is the two routes of one act — ask for a link, spend it for a session — so
	// both collaborators are required before either is mounted. A deployment with no signing
	// key, no provider or no identity schema wires neither, and then this surface behaves
	// exactly as it did before identity existed: nothing here is required to buy, open or show
	// a ticket.
	if h.identityLinks != nil && h.identitySessions != nil {
		mux.HandleFunc(identityLinkRoute, h.issueIdentityLink)
		mux.HandleFunc(identitySessionRoute, h.redeemIdentitySession)
		// Signing out is the end of the session the two routes above open, so it is mounted
		// inside the same block: a deployment without identity has nothing a person could be
		// signed out of. It is the one route here with a third collaborator — the session
		// service's Close — so it is absent where that is absent, rather than mounted against
		// nothing: a sign-out that could close nothing would be a button that lies.
		if h.identitySignOut != nil {
			mux.HandleFunc(identitySignOutRoute, h.closeIdentitySession)
		}
		// The account's own list is mounted inside the login surface, never beside it: a
		// deployment without identity has no signed-in account for it to answer for, so
		// advertising "these are your tickets" there would advertise a route that could only
		// ever refuse. It also needs both halves it is built from — the session read that
		// proves who is asking and the list that fills the answer — so a deployment with only
		// one of them mounts nothing rather than half a capability.
		if h.sessionAuthenticator != nil && h.accountTickets != nil {
			mux.HandleFunc(myTicketsRoute, h.listMyTickets)
		}
	}
	return mux
}

// maxBodyBytes bounds a hold request body; a seat selection is a small list.
const maxBodyBytes = 1 << 20

type createHoldRequest struct {
	EventID string          `json:"eventId"`
	SeatIDs seatIdentifiers `json:"seatIds"`
}

// errSeatIdentifier marks a seat list the transport could parse as JSON but not
// as event seat identifiers, which is invalid input rather than a malformed
// request.
var errSeatIdentifier = errors.New("invalid seat identifier")

// seatIdentifiers decodes the selected event seat identifiers. The accepted
// concurrency contract sends them as JSON strings while PostgreSQL stores them
// as bigints, so a quoted integer and a JSON number are both accepted; anything
// else is caller error and is rejected before the service is called.
type seatIdentifiers []int64

func (ids *seatIdentifiers) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: seatIds must be a list of event seat ids", errSeatIdentifier)
	}
	decoded := make(seatIdentifiers, 0, len(raw))
	for _, item := range raw {
		var number int64
		if err := json.Unmarshal(item, &number); err == nil {
			decoded = append(decoded, number)
			continue
		}
		var text string
		if err := json.Unmarshal(item, &text); err != nil {
			return fmt.Errorf("%w: each seat id must be an integer or a numeric string", errSeatIdentifier)
		}
		number, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return fmt.Errorf("%w: %q is not a numeric event seat id", errSeatIdentifier, text)
		}
		decoded = append(decoded, number)
	}
	*ids = decoded
	return nil
}

// createHold claims a seat selection. The Idempotency-Key header is required:
// the service answers a replay from its durable record instead of creating a
// second hold.
func (h *Handler) createHold(w http.ResponseWriter, r *http.Request) {
	var req createHoldRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		if errors.Is(err, errSeatIdentifier) {
			writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
			return
		}
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request",
			"request body must be valid JSON with an eventId and a seatIds list")
		return
	}

	hold, err := h.svc.CreateHold(r.Context(), r.Header.Get("Idempotency-Key"), req.EventID, []int64(req.SeatIDs))
	if err != nil {
		writeError(w, err)
		return
	}

	// The stored outcome status answers a fresh hold and an idempotent replay
	// alike, so a replay can never be reported with a different status.
	status := hold.Status
	if status == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"id":        hold.ID,
		"eventId":   hold.EventID,
		"seatIds":   hold.SeatIDs,
		"expiresAt": hold.ExpiresAt,
	})
}

// listEventSeats reports every seat of one event with whether a live hold
// currently owns it, which is what the seat selection UI refetches after a
// conflict (S10).
//
// Each seat carries its layout projection as well (S9): `rowLabel`, `seatNumber`
// and `kind`, so the browser can render the event's real rows, with each row's own
// seat count and no invented geometry. The fields are additive next to the ones
// the read already served; a client that ignores them loses only the map shape.
func (h *Handler) listEventSeats(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	seats, err := h.svc.ListAvailability(r.Context(), eventID)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(seats))
	for _, seat := range seats {
		items = append(items, map[string]any{
			"id":           seat.ID,
			"layoutSeatId": seat.LayoutSeatID,
			"state":        seat.State,
			"held":         seat.Held,
			"rowLabel":     seat.RowLabel,
			"seatNumber":   seat.SeatNumber,
			"kind":         seat.Kind,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"eventId": eventID, "seats": items})
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// writeError maps each reservation outcome to its own status and problem code.
// The cases are deliberately distinct: a seat held by another hold, a reused
// idempotency key, and a claim whose own window closed are three different
// situations, and a transport failure is retryable rather than a success.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reservation.ErrSeatsUnavailable):
		writeProblem(w, http.StatusConflict, "seat_unavailable", "Seat unavailable",
			"at least one selected seat is already held by another reservation")
	case errors.Is(err, reservation.ErrIdempotencyKeyReused):
		writeProblem(w, http.StatusConflict, "idempotency_key_reused", "Conflict",
			"the Idempotency-Key was already used for a different seat selection")
	case errors.Is(err, reservation.ErrDuplicateClaim):
		writeProblem(w, http.StatusConflict, "duplicate_seat", "Conflict",
			"one reservation cannot claim the same seat twice")
	case errors.Is(err, reservation.ErrHoldWindowExpired):
		writeProblem(w, http.StatusBadRequest, "hold_window_expired", "Hold window expired",
			"the hold window closed before the seats could be claimed")
	case errors.Is(err, reservation.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
	case errors.Is(err, reservation.ErrUnknownReference):
		// The durable store reported a reference it does not know. Its message names
		// tables, constraints and values, so the client gets the service's own words
		// and the driver message stays in the operator's log.
		writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input",
			"the request references an event or seat that does not exist")
	case errors.Is(err, reservation.ErrEventNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "Not found",
			"the requested event does not exist")
	default:
		writeProblem(w, http.StatusServiceUnavailable, "coordination_unavailable", "Service unavailable",
			"the reservation service is temporarily unavailable; retry the request")
	}
}

func writeProblem(w http.ResponseWriter, status int, code, title, detail string) {
	writeJSON(w, status, problem{
		Type:   "https://cinema.local/problems/" + strings.ReplaceAll(code, "_", "-"),
		Title:  title,
		Status: status,
		Code:   code,
		Detail: detail,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		_, _ = fmt.Fprintln(w, err)
	}
}
