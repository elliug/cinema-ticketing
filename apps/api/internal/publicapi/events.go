// The public catalogue route, GET /api/v1/events, lives in this file.
//
// It is public because the whole point of it is that nobody needs an identifier to
// reach the customer surface: a stranger arriving at the site asks what is on sale and
// the answer is the list of screenings a buyer can buy a seat for. Nothing about the
// catalogue is assembled here: this file translates the read model into JSON with the
// camelCase keys the rest of the public API uses, asks each entry for its derived
// presentations (the venue's local hour and the price text), and maps a failed read to
// the problem shape the public API already answers.

package publicapi

import (
	"context"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/catalog"
)

// CatalogService is the catalogue read contract. It is satisfied by the catalogue
// store, and a deployment without that schema wires nothing — in which case the route
// is unmounted.
type CatalogService interface {
	// Events answers the screenings a buyer can buy right now, in screening order. An
	// empty catalogue is an empty list, never an error.
	Events(ctx context.Context) ([]catalog.Screening, error)
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
		startsAtLocal, err := screening.LocalStartsAt()
		if err != nil {
			// A venue whose stored zone cannot be resolved is refused rather than
			// answered with an invented local hour: the whole list is retried, because a
			// catalogue that silently dropped or misdated one screening would be worse
			// than one that could not be read.
			writeCatalogError(w, err)
			return
		}
		// The stored instant is answered in UTC, with the "Z" suffix, so a client that
		// sorts or compares the string it is handed compares the same thing every deployment
		// does. The driver hands timestamps back in the database session's zone, so without
		// this the same screening would be spelled with an offset in one deployment and in
		// UTC in another; the venue's own hour is the separate startsAtLocal field, which is
		// where an offset belongs.
		events = append(events, map[string]any{
			"id":             screening.ID,
			"filmTitle":      screening.FilmTitle,
			"cinemaName":     screening.CinemaName,
			"roomName":       screening.RoomName,
			"startsAt":       screening.StartsAt.UTC(),
			"startsAtLocal":  startsAtLocal,
			"priceCents":     screening.PriceCents,
			"priceDisplay":   screening.PriceText(),
			"seatCount":      screening.SeatCount,
			"availableSeats": screening.AvailableSeats,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
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

// eventsRoute is the catalogue route pattern, relative to the /api/v1 mount point.
const eventsRoute = "GET /events"
