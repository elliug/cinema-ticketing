// Package publicapi exposes the customer facing reservation HTTP surface: the
// M2 hold creation endpoint and the event seat availability read.
//
// Routes here are public application routes. They are mounted outside the
// administrative guard, because a customer selecting seats has no
// administrative token; see httpserver.Server.WithPublic.
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
)

// HoldService is the reservation application service contract.
type HoldService interface {
	CreateHold(ctx context.Context, idempotencyKey, eventID string, eventSeatIDs []int64) (reservation.Hold, error)
	ListAvailability(ctx context.Context, eventID string) ([]reservation.SeatAvailability, error)
}

// Handler serves the public reservation routes.
type Handler struct {
	svc HoldService
}

// New builds a Handler over the given service.
func New(svc HoldService) *Handler { return &Handler{svc: svc} }

// Mux returns the public route table. The routes are relative to the mount
// point /api/v1, matching httpserver.Server.WithPublic.
func (h *Handler) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /reservations/holds", h.createHold)
	mux.HandleFunc("GET /events/{eventID}/seats", h.listEventSeats)
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
