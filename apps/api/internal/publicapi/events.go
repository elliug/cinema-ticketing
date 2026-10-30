// The public catalogue routes, GET /api/v1/events and GET /api/v1/events/{eventID},
// live in this file.
//
// They are public because the whole point of them is that nobody needs an identifier to
// reach the customer surface: a stranger arriving at the site asks what is on sale and
// the answer is the list of screenings a buyer can buy a seat for. The second route is
// what the seat page does with the entry the buyer clicked: it answers one screening, so
// the film, the venue, the room, the hour and the price survive the navigation and the
// checkout has an amount to multiply.
//
// Nothing about either answer is assembled here: this file translates the read model into
// JSON with the camelCase keys the rest of the public API uses, asks each screening for
// its derived presentations (the venue's local hour and the price text) through one
// function, and maps a failed read to the problem shape the public API already answers.
// Both routes answer one entry shape, produced once, so a client renders a single
// screening with the code it renders a catalogue row.

package publicapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/catalog"
)

// CatalogService is the catalogue read contract. It is satisfied by the catalogue
// store, and a deployment without that schema wires nothing — in which case the routes
// are unmounted.
type CatalogService interface {
	// Events answers the screenings a buyer can buy right now, in screening order. An
	// empty catalogue is an empty list, never an error.
	Events(ctx context.Context) ([]catalog.Screening, error)
	// Event answers one screening, whatever its status, so a buyer who arrived with a
	// stale link is told the screening is no longer on sale instead of not found. An
	// unknown screening, or an identifier no screening could carry, is
	// catalog.ErrNotFound; a read that could not be completed is catalog.ErrUnavailable.
	Event(ctx context.Context, eventID string) (catalog.Screening, error)
}

// WithCatalog attaches the catalogue reader and returns the same handler, so the
// catalogue route is mounted only when one is wired.
//
// A nil reader leaves the route unmounted: a catalogue route with nothing to read could
// only answer 503, and a deployment whose schema predates the reads knows of no
// screenings to serve.
func (h *Handler) WithCatalog(svc CatalogService) *Handler {
	h.catalog = svc
	return h
}

// listEvents answers the screenings a buyer can buy a seat for, in screening order.
//
// Every entry is answered with everything the catalogue screen shows: the film, the
// venue and the room, the screening time as the stored instant and as the venue's own
// local presentation, the price as minor units and as its display text, and the seats —
// how many the screening sells and how many of them are available right now. The
// availability is the read model's number; nothing here recounts seats, because a
// transport that recomputed it would be a second answer to a question the database
// already answered.
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	screenings, err := h.catalog.Events(r.Context())
	if err != nil {
		writeCatalogError(w, err)
		return
	}

	// The list is always an array, so a client can iterate it without a nil check, and
	// an empty catalogue stays a 200 with an empty list.
	events := make([]map[string]any, 0, len(screenings))
	for _, screening := range screenings {
		document, err := screeningDocument(screening)
		if err != nil {
			// A venue whose stored zone cannot be resolved is refused rather than
			// answered with an invented local hour: the whole list is retried, because a
			// catalogue that silently dropped or misdated one screening would be worse
			// than one that could not be read.
			writeCatalogError(w, err)
			return
		}
		events = append(events, document)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// screeningDocument translates one screening into the wire shape both catalogue routes
// answer: the film, the venue and the room, the screening time as the stored instant and
// as the venue's own local presentation, the price as minor units and as its display
// text, the seats — how many the screening sells and how many of them are available
// right now — and whether the catalogue still offers it, with the stored status that
// explains why not.
//
// It is one function precisely so the list and the single read cannot drift: a field
// added for one of them is answered by both, and neither recounts seats (the availability
// is the read model's number) nor formats money or time of its own.
func screeningDocument(screening catalog.Screening) (map[string]any, error) {
	startsAtLocal, err := screening.LocalStartsAt()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":         screening.ID,
		"status":     screening.Status,
		"onSale":     screening.OnSale,
		"filmTitle":  screening.FilmTitle,
		"cinemaName": screening.CinemaName,
		"roomName":   screening.RoomName,
		// The stored instant is answered in UTC, with the "Z" suffix, so a client that
		// sorts or compares the string it is handed compares the same thing every deployment
		// does. The driver hands timestamps back in the database session's zone, so without
		// this the same screening would be spelled with an offset in one deployment and in
		// UTC in another; the venue's own hour is the separate startsAtLocal field, which is
		// where an offset belongs.
		"startsAt":       screening.StartsAt.UTC(),
		"startsAtLocal":  startsAtLocal,
		"priceCents":     screening.PriceCents,
		"priceDisplay":   screening.PriceText(),
		"seatCount":      screening.SeatCount,
		"availableSeats": screening.AvailableSeats,
	}, nil
}

// getEvent answers one screening by the identifier the seat page arrived with.
//
// It answers the same document the list answers as an entry, for any screening that
// exists: a cancelled, draft or already-started screening is a 200 with its own status
// and `onSale: false`, because a buyer holding a stale link needs to be told the
// screening is no longer on sale rather than that it never existed. Only an identifier
// that names nothing is a 404.
func (h *Handler) getEvent(w http.ResponseWriter, r *http.Request) {
	screening, err := h.catalog.Event(r.Context(), r.PathValue("eventID"))
	if err != nil {
		writeScreeningError(w, err)
		return
	}
	document, err := screeningDocument(screening)
	if err != nil {
		// The same refusal as the list: a venue zone that cannot be resolved is a failed
		// read, not an hour invented from the machine's zone.
		writeScreeningError(w, fmt.Errorf("%w: %w", catalog.ErrUnavailable, err))
		return
	}
	// The body is the screening itself, not a wrapper: it is exactly the entry the list
	// answered, so the seat page renders it with the code it already has.
	writeJSON(w, http.StatusOK, document)
}

// writeCatalogError maps a failed catalogue read to its problem document.
//
// There is one outcome: the read is retryable. There is deliberately no 404, because
// the catalogue is not addressed by an identifier — a programme with nothing on sale is
// a successful empty list, so no request can name a catalogue that does not exist. A
// venue timezone that cannot be presented is answered the same way: no local hour is
// invented from the machine's zone.
func writeCatalogError(w http.ResponseWriter, _ error) {
	writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
		"the catalogue is temporarily unavailable; retry the request")
}

// writeScreeningError maps the two outcomes of reading one screening to their status and
// problem code.
//
// An identifier that names no screening is 404 `not_found`, and the document never echoes
// what was asked for. Anything else — a storage failure, or a venue zone that cannot be
// resolved into an hour — is 503 `service_unavailable`: retryable, and never a screening
// with missing fields.
//
// A screening that exists but is no longer on sale is neither: it is a 200 whose body
// says so, which is why this function has no "gone" case.
func writeScreeningError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "Not found",
			"the requested screening does not exist")
	default:
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the screening is temporarily unavailable; retry the request")
	}
}

// eventsRoute is the catalogue route pattern, relative to the /api/v1 mount point, and
// eventRoute is the single screening read beneath it.
const (
	eventsRoute = "GET /events"
	eventRoute  = "GET /events/{eventID}"
)
