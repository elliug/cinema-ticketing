// The administrative screening reads and the price and status write live in this file.
//
// The price and the status are the two fields of a screening the administration may change
// after it is scheduled; the date, room, film and layout are not writable here. The read is
// part of the same task because the screen that edits a screening has to see what it
// currently carries, and the screen that lists screenings has to know what each one is.
//
// The wire shape is shared by every event response, so the create, list, show and update
// write cannot drift apart:
//
//   - id, roomId, filmId, layoutVersionId, startsAt, status are the screening's stored
//     columns;
//   - priceCents is the stored price in minor units;
//   - priceDisplay is the same amount spelled by tickets.FormatMoney ("12.50"), the one
//     money spelling the public catalogue and the ticket detail already use.
//
// The two administrative reads add three names on top of that shape — filmTitle,
// roomName and cinemaName — because an identifier is not something a scheduling screen
// can show. The names are a read-only enrichment: the store joins them in the same
// statement as the columns, while the responses that follow a write answer the stored
// columns only (RETURNING cannot join, and a name nobody read is not a name to answer).

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

// listEvents answers every screening in start order, with the price it currently carries
// and the names of what it is scheduled against.
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.svc.ListEvents(r.Context())
	if err != nil {
		writeEventError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, eventReadJSON(event))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": items})
}

// getEvent answers one screening, named the same way the list names it. The identifier is
// validated before the service and the store, so a value that cannot name an event is a
// rejected field rather than a cast failure.
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
	writeJSON(w, http.StatusOK, eventReadJSON(event))
}

// updateEvent changes the price of one screening, its status, or both, in one request.
//
// This route is the only way a screening reaches the catalogue. POST /admin/events stores
// a screening as a draft, and the public catalogue answers a screening only when its status
// is on_sale and its start is still ahead, so a screening created in the administration is
// not for sale until this request publishes it.
//
// What each value of cinema.events.status means for the buyer-facing surface:
//
//   - draft — not for sale. A stored screening nobody has offered yet.
//   - on_sale — what the public catalogue shows, while the start is still ahead. Once the
//     start has passed the same row drops out of the catalogue on its own, because the
//     list's membership predicate is this status and the database clock together.
//   - cancelled — a screening the ticket surface reports as cancelled: a ticket already
//     sold for it stays stored and printable and no longer admits its holder.
//   - published, started, completed — recording states. This milestone writes them and
//     acts on none of them: no surface treats them as saleable, and the catalogue's
//     predicate is exactly on_sale.
//
// The body is {"priceCents": <integer>} and/or {"status": "<status>"}. A field that is not
// sent is not written at all, so a price-only request leaves the status where it is and a
// status-only request leaves the price where it is; a missing price is deliberately not an
// explicit 0, because a free screening is a legal price and "I did not send it" must never
// mean "make it free". A body with neither field is refused rather than accepted as a
// request that changes nothing. Every rejection happens before the service and SQL, and no
// rejection echoes the request body.
func (h *Handler) updateEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventId")
	if err := venue.ValidateEventID(eventID); err != nil {
		writeEventError(w, err)
		return
	}
	update, ok := decodeEventUpdate(w, r)
	if !ok {
		return
	}
	if err := venue.ValidateEventUpdate(update); err != nil {
		writeEventError(w, err)
		return
	}
	event, err := h.svc.UpdateEvent(r.Context(), eventID, update)
	if err != nil {
		writeEventError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eventJSON(event))
}

// decodeEventUpdate reads the writable fields from the request body as a field map, so the
// absence of a field stays distinguishable from a value that looks like a default. Each
// field that is present is parsed by its own rule, and a value of the wrong JSON type is
// named by the field that carried it instead of being coerced. The body is bounded like
// every other administrative body.
func decodeEventUpdate(w http.ResponseWriter, r *http.Request) (venue.EventUpdate, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{
			Type: "https://cinema.local/problems/invalid-request", Title: "Invalid request",
			Status: http.StatusBadRequest, Code: "invalid_request", Detail: "request body must be valid JSON",
		})
		return venue.EventUpdate{}, false
	}
	var update venue.EventUpdate
	if raw, present := body["priceCents"]; present {
		priceCents, ok := decodePriceCents(w, raw)
		if !ok {
			return venue.EventUpdate{}, false
		}
		update.PriceCents = &priceCents
	}
	if raw, present := body["status"]; present {
		status, ok := decodeEventStatus(w, raw)
		if !ok {
			return venue.EventUpdate{}, false
		}
		update.Status = &status
	}
	return update, true
}

// decodePriceCents parses one price value. A JSON string that happens to spell a number
// must not be read as one: only a number literal is a price, so the value has to begin like
// one before it is parsed.
func decodePriceCents(w http.ResponseWriter, raw json.RawMessage) (int64, bool) {
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

// decodeEventStatus parses one status value, which has to be a JSON string: null, a number
// or an object is not a status and is refused as the wrong type. Whether the string is a
// status cinema.events.status allows is ValidateEventStatus's rule, not this parser's.
func decodeEventStatus(w http.ResponseWriter, raw json.RawMessage) (string, bool) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || value[0] != '"' {
		writeEventError(w, venue.NewInvalidError("status", "must be a string"))
		return "", false
	}
	var status string
	if err := json.Unmarshal(value, &status); err != nil {
		writeEventError(w, venue.NewInvalidError("status", "must be a string"))
		return "", false
	}
	return status, true
}

// eventJSON is the wire shape of a screening, shared by every event response so the
// create, list, show and update write cannot drift apart. The display is
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

// eventReadJSON is the shape of the two administrative screening reads: the stored
// columns eventJSON builds, plus the names of the film, the room and the cinema the store
// joined in the same statement. It is composed from eventJSON so a read can never lose a
// stored column the create or the update write still answers.
func eventReadJSON(event venue.Event) map[string]any {
	body := eventJSON(event)
	body["filmTitle"] = event.FilmTitle
	body["roomName"] = event.RoomName
	body["cinemaName"] = event.CinemaName
	return body
}

// writeEventError maps the screening reads and the update write to problem details. An
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
