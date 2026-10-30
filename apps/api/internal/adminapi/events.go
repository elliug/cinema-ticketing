// The administrative screening reads and the price write live in this file.
//
// The price is the one field of a screening the administration may change after it is
// scheduled; the date, room, film and status are not writable here. The read is part of
// the same task because the screen that edits a price has to see the price the event
// currently carries.
//
// The wire shape is shared by every event response, so the create, list, show and price
// write cannot drift apart:
//
//   - id, roomId, filmId, layoutVersionId, startsAt, status are the screening's stored
//     columns;
//   - priceCents is the stored price in minor units;
//   - priceDisplay is the same amount spelled by tickets.FormatMoney ("12.50"), the one
//     money spelling the public catalogue and the ticket detail already use.

package adminapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"cinema.local/ticketing/apps/api/internal/tickets"
	"cinema.local/ticketing/apps/api/internal/venue"
)

// maxAdminBodyBytes bounds an administrative request body the same way decode bounds
// every other administrative route: one mebibyte, refused before any work.
const maxAdminBodyBytes = 1 << 20

// listEvents answers every screening in start order, with the price it currently carries.
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.svc.ListEvents(r.Context())
	if err != nil {
		writeEventError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, eventJSON(event))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": items})
}

// getEvent answers one screening. The identifier is validated before the service and
// the store, so a value that cannot name an event is a rejected field rather than a
// cast failure.
func (h *Handler) getEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventId")
	if err := venue.ValidateEventID(eventID); err != nil {
		writeEventError(w, err)
		return
	}
	event, err := h.svc.GetEvent(r.Context(), eventID)
	if err != nil {
		writeEventError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eventJSON(event))
}

// setEventPrice changes only the price of one screening.
//
// The body is {"priceCents": <integer>}. A missing field is an error and is deliberately
// not the same as an explicit 0: a free screening is a legal price, and "I did not send
// it" must never mean "make it free". Every rejection happens before the service and
// SQL, and no rejection echoes the request body.
func (h *Handler) setEventPrice(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventId")
	if err := venue.ValidateEventID(eventID); err != nil {
		writeEventError(w, err)
		return
	}
	priceCents, ok := decodeEventPrice(w, r)
	if !ok {
		return
	}
	if err := venue.ValidatePriceCents(priceCents); err != nil {
		writeEventError(w, err)
		return
	}
	event, err := h.svc.SetEventPrice(r.Context(), eventID, priceCents)
	if err != nil {
		writeEventError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eventJSON(event))
}

// decodeEventPrice reads only the price field from the request body, so the absence of
// the field stays distinguishable from an explicit zero. The body is bounded like every
// other administrative body, and a value that is not an integer is named by the field
// that carried it.
func decodeEventPrice(w http.ResponseWriter, r *http.Request) (int64, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{
			Type: "https://cinema.local/problems/invalid-request", Title: "Invalid request",
			Status: http.StatusBadRequest, Code: "invalid_request", Detail: "request body must be valid JSON",
		})
		return 0, false
	}
	raw, present := body["priceCents"]
	if !present {
		writeEventError(w, venue.NewInvalidError("priceCents", "must be provided"))
		return 0, false
	}
	// A JSON string that happens to spell a number must not be read as one: only a
	// number literal is a price, so the value has to begin like one before it is parsed.
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) {
		writeEventError(w, venue.NewInvalidError("priceCents", "must be an integer"))
		return 0, false
	}
	priceCents, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		writeEventError(w, venue.NewInvalidError("priceCents", "must be an integer"))
		return 0, false
	}
	return priceCents, true
}

// eventJSON is the wire shape of a screening, shared by every event response so the
// create, list, show and price write cannot drift apart. The display is
// tickets.FormatMoney's, never a second formatter.
func eventJSON(event venue.Event) map[string]any {
	return map[string]any{
		"id":              event.ID,
		"roomId":          event.RoomID,
		"filmId":          event.FilmID,
		"layoutVersionId": event.LayoutVersionID,
		"startsAt":        event.StartsAt.UTC(),
		"status":          event.Status,
		"priceCents":      event.PriceCents,
		"priceDisplay":    tickets.FormatMoney(event.PriceCents),
	}
}

// writeEventError maps the screening reads and the price write to problem details. An
// unknown event is not found, a rejected field names itself, and a read or write that
// could not be completed is unavailable instead of a success or a leaked driver error.
func writeEventError(w http.ResponseWriter, err error) {
	var invalid *venue.InvalidError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, problem{
			Type: "https://cinema.local/problems/invalid-input", Title: "Invalid input",
			Status: http.StatusBadRequest, Code: "invalid_input", Detail: invalid.Error(),
		})
	case errors.Is(err, venue.ErrNotFound):
		writeJSON(w, http.StatusNotFound, problem{
			Type: "https://cinema.local/problems/not-found", Title: "Not found",
			Status: http.StatusNotFound, Code: "not_found", Detail: "the requested resource does not exist",
		})
	case errors.Is(err, venue.ErrConflict):
		writeJSON(w, http.StatusConflict, problem{
			Type: "https://cinema.local/problems/conflict", Title: "Conflict",
			Status: http.StatusConflict, Code: "conflict", Detail: err.Error(),
		})
	default:
		writeJSON(w, http.StatusServiceUnavailable, problem{
			Type: "https://cinema.local/problems/service-unavailable", Title: "Service unavailable",
			Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Detail: "the request could not be completed; retry the request",
		})
	}
}
