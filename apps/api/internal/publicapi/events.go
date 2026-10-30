// The public catalogue routes, GET /api/v1/events and GET /api/v1/events/{eventID}, live
// in this file. The list answers one page of the catalogue and says how to ask for the next
// one; the single read answers one screening, unchanged by the paging.
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
	"strconv"
	"time"

	"cinema.local/ticketing/apps/api/internal/catalog"
)

// CatalogService is the catalogue read contract. It is satisfied by the catalogue
// store, and a deployment without that schema wires nothing — in which case the routes
// are unmounted.
type CatalogService interface {
	// Events answers one page of the screenings a buyer can buy right now, in screening
	// order, and says where the next page starts. An empty catalogue is an empty page,
	// never an error; a request the catalogue cannot mean is catalog.ErrInvalidQuery,
	// naming the field.
	Events(ctx context.Context, query catalog.ListQuery) (catalog.Page, error)
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

// listEvents answers one page of the screenings a buyer can buy a seat for, in screening
// order, and says how to ask for the next page.
//
// Every entry is answered with everything the catalogue screen shows: the film, the venue
// and the room, the screening time as the stored instant and as the venue's own local
// presentation, the price as minor units and as its display text, and the seats — how many
// the screening sells and how many of them are available right now. The availability is the
// read model's number; nothing here recounts seats, because a transport that recomputed it
// would be a second answer to a question the database already answered.
//
// The page itself is asked for by `limit`, `cursor`, `from` and `to`, refused by naming the
// field when it cannot be used, and the answer carries the page beside the list: the limit
// that was applied, whether a next page exists, and the cursor that reaches it. The list
// stays exactly where it was — the screen already reads `events` and keeps working — and the
// paging is a sibling, not a replacement, so adding it changed no entry.
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	query, err := cataloguePageQuery(r)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	page, err := h.catalog.Events(r.Context(), query)
	if err != nil {
		writeCatalogError(w, err)
		return
	}

	// The list is always an array, so a client can iterate it without a nil check, and
	// an empty catalogue stays a 200 with an empty list.
	events := make([]map[string]any, 0, len(page.Screenings))
	for _, screening := range page.Screenings {
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

	// `nextCursor` is present and null on the last page, never absent: a client that asks
	// whether there is more can tell "no" from "not answered". Both flags come from the
	// read, which is the only layer that knows whether a row exists beyond this page.
	var nextCursor any
	if page.Next != nil {
		nextCursor = page.Next.String()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"page": map[string]any{
			"limit":      query.Limit,
			"hasMore":    page.Next != nil,
			"nextCursor": nextCursor,
		},
	})
}

// cataloguePageQuery translates the query string into the catalogue's page request.
//
// It is the only place the four parameters are read, and the documented default lives here:
// a caller that names no `limit` gets [catalog.DefaultEventLimit] screenings, which is the
// change this slice exists for — `GET /events` used to answer the whole programme. A limit
// that is named is passed on as named, so a value outside the range is refused by the read
// model (naming the field) instead of being clamped to something the client did not ask
// for.
//
// Every refusal is a *catalog.QueryError naming the parameter, and the transport maps it to
// the shared 400 problem document: a client learns which field to fix, and never receives a
// well-formed empty page that would read as "no more screenings".
func cataloguePageQuery(r *http.Request) (catalog.ListQuery, error) {
	values := r.URL.Query()
	query := catalog.ListQuery{Limit: catalog.DefaultEventLimit}

	if values.Has("limit") {
		limit, err := strconv.Atoi(values.Get("limit"))
		if err != nil {
			return catalog.ListQuery{}, &catalog.QueryError{
				Field:  "limit",
				Reason: fmt.Sprintf("must be a whole number between 1 and %d", catalog.MaxEventLimit),
			}
		}
		query.Limit = limit
	}

	// The bounds are instants, parsed by RFC 3339 and normalized to UTC by the read model,
	// so the instant a client sent is the instant the keyset compares.
	for _, bound := range []struct {
		field string
		dest  **time.Time
	}{
		{"from", &query.From},
		{"to", &query.To},
	} {
		if !values.Has(bound.field) {
			continue
		}
		instant, err := time.Parse(time.RFC3339, values.Get(bound.field))
		if err != nil {
			return catalog.ListQuery{}, &catalog.QueryError{
				Field:  bound.field,
				Reason: "must be an RFC 3339 instant, for example 2026-05-04T18:30:00Z",
			}
		}
		*bound.dest = &instant
	}

	if values.Has("cursor") {
		cursor, err := catalog.ParseCursor(values.Get("cursor"))
		if err != nil {
			return catalog.ListQuery{}, err
		}
		query.After = &cursor
	}

	// One validation for the whole request: the bounds' order and the cursor's pair are
	// rules the read model owns, and asking it here means the refusal a client reads is the
	// same sentence the store would have answered.
	return query.Normalize()
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

// writeCatalogError maps a failed catalogue list to its problem document.
//
// Two outcomes, and they are different promises: a page request the catalogue cannot mean
// is a 400 `invalid_input` whose detail names the field, because retrying it unchanged can
// only fail again; anything else is the read being retryable, answered 503. There is
// deliberately no 404, because the catalogue is not addressed by an identifier — a
// programme with nothing on sale is a successful empty page, so no request can name a
// catalogue that does not exist. A venue timezone that cannot be presented is answered the
// same retryable way: no local hour is invented from the machine's zone.
func writeCatalogError(w http.ResponseWriter, err error) {
	if errors.Is(err, catalog.ErrInvalidQuery) {
		// The detail is the query error's own sentence, which is the field and what was
		// wrong with it. It never carries a value the caller did not send.
		writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
		return
	}
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
