package adminapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// The event identifiers in these tests are well formed because the administration
// validates an event identifier before it reaches the service or SQL. "event-1" is the
// shape the older layout handlers use; an event identifier is a UUID.
const (
	testEventID      = "00000000-0000-0000-0000-000000000e01"
	testOtherEventID = "00000000-0000-0000-0000-000000000e02"
)

var testStartsAt = time.Date(2030, time.January, 2, 18, 0, 0, 0, time.UTC)

// testVenueTimezone is the zone the read fixtures store. It is an hour ahead of UTC in
// January and two in July, so a read that answered the UTC hour twice cannot pass.
const testVenueTimezone = "Europe/Madrid"

// testStartsAtLocal is testStartsAt in the fixture venue's own zone.
const testStartsAtLocal = "2030-01-02T19:00:00+01:00"

// eventFixture is one stored screening. It carries the venue zone the administrative
// reads join because the reads render the venue's hour from it; the write responses ignore
// it, since RETURNING cannot join and the write answers the stored columns only.
func eventFixture() venue.Event {
	return venue.Event{
		ID:              testEventID,
		RoomID:          "room-1",
		FilmID:          "film-1",
		LayoutVersionID: "layout-1",
		StartsAt:        testStartsAt,
		Status:          "on_sale",
		CinemaTimezone:  testVenueTimezone,
	}
}

// namedEventFixture is a screening as the administrative read answers it: the stored
// columns plus the names of the film, the room and the cinema the store joined.
func namedEventFixture() venue.Event {
	event := eventFixture()
	event.PriceCents = 1250
	event.FilmTitle = "The Quiet Cartographer"
	event.RoomName = "Sala 2"
	event.CinemaName = "Cine Central"
	return event
}

// storedEventKeys is the shape a screening answered before the administrative reads
// named what it is. It is what the create and the update write still answer.
var storedEventKeys = []string{"id", "roomId", "filmId", "layoutVersionId", "startsAt", "status", "priceCents", "priceDisplay"}

// eventReadKeys is the administrative read's shape: the stored keys plus the three names
// and the venue's own hour, and nothing else. startsAtLocal is here because these fixtures
// store a zone that resolves; a venue whose zone does not is covered separately, and its
// read omits the hour instead of guessing.
var eventReadKeys = []string{
	"id", "roomId", "filmId", "layoutVersionId", "startsAt", "startsAtLocal", "status", "priceCents", "priceDisplay",
	"filmTitle", "roomName", "cinemaName",
}

// eventReadKeysWithoutLocal is the same read for a venue whose stored zone does not resolve:
// everything the read knows, minus the hour it refuses to invent.
var eventReadKeysWithoutLocal = []string{
	"id", "roomId", "filmId", "layoutVersionId", "startsAt", "status", "priceCents", "priceDisplay",
	"filmTitle", "roomName", "cinemaName",
}

// requireKeys proves an answer carries exactly the given keys: an administrative screen
// pins what it reads, so a key that appears or disappears is a change, not a detail.
func requireKeys(t *testing.T, what string, body map[string]any, keys []string) {
	t.Helper()
	if len(body) != len(keys) {
		t.Fatalf("%s = %v, want exactly the keys %v", what, body, keys)
	}
	for _, key := range keys {
		if _, ok := body[key]; !ok {
			t.Fatalf("%s is missing key %q: %v", what, key, body)
		}
	}
}

func TestListEventsCarriesPriceAndDisplay(t *testing.T) {
	early := eventFixture()
	early.StartsAt = testStartsAt.Add(-24 * time.Hour)
	early.PriceCents = 1250
	late := eventFixture()
	late.ID = testOtherEventID
	late.StartsAt = testStartsAt
	late.PriceCents = 900

	svc := &fakeService{events: []venue.Event{early, late}}
	w := do(t, svc, http.MethodGet, "/events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	events, ok := body["events"].([]any)
	if !ok || len(events) != 2 {
		t.Fatalf("events = %v, want both screenings", body["events"])
	}
	first, _ := events[0].(map[string]any)
	requireKeys(t, "listed event", first, eventReadKeys)
	if first["priceCents"] != float64(1250) || first["priceDisplay"] != "12.50" {
		t.Fatalf("price = %v / %v, want 1250 and 12.50", first["priceCents"], first["priceDisplay"])
	}
	if len(svc.calls) != 1 || svc.calls[0] != "ListEvents" {
		t.Fatalf("calls = %v, want exactly ListEvents", svc.calls)
	}
}

// The list a scheduling screen renders has to name what each row is, or the row is an
// identifier nobody can read.
func TestListEventsNamesTheFilmTheRoomAndTheCinema(t *testing.T) {
	svc := &fakeService{events: []venue.Event{namedEventFixture()}}
	w := do(t, svc, http.MethodGet, "/events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	events, ok := decodeBody(t, w)["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %v, want the one screening", events)
	}
	entry, _ := events[0].(map[string]any)
	requireKeys(t, "listed event", entry, eventReadKeys)
	if entry["filmTitle"] != "The Quiet Cartographer" || entry["roomName"] != "Sala 2" || entry["cinemaName"] != "Cine Central" {
		t.Fatalf("names = %v / %v / %v, want the film, the room and the cinema",
			entry["filmTitle"], entry["roomName"], entry["cinemaName"])
	}
	// The identifiers the screen addresses the row with are still answered beside them.
	if entry["id"] != testEventID || entry["filmId"] != "film-1" || entry["roomId"] != "room-1" {
		t.Fatalf("identifiers = %v, want id, filmId and roomId kept", entry)
	}
}

func TestGetEventNamesTheFilmTheRoomAndTheCinema(t *testing.T) {
	svc := &fakeService{event: namedEventFixture()}
	w := do(t, svc, http.MethodGet, "/events/"+testEventID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	requireKeys(t, "event", body, eventReadKeys)
	if body["filmTitle"] != "The Quiet Cartographer" || body["roomName"] != "Sala 2" || body["cinemaName"] != "Cine Central" {
		t.Fatalf("names = %v / %v / %v, want the film, the room and the cinema",
			body["filmTitle"], body["roomName"], body["cinemaName"])
	}
	if body["priceCents"] != float64(1250) || body["priceDisplay"] != "12.50" {
		t.Fatalf("price = %v / %v, want 1250 and 12.50", body["priceCents"], body["priceDisplay"])
	}
	if svc.lastEventID != testEventID {
		t.Fatalf("event id = %q, want %q", svc.lastEventID, testEventID)
	}
}

// The two administrative writes answer the stored columns only. Naming a film in a
// response that never read its title would be a shape claiming a join it did not make,
// so the enrichment belongs to the reads and the write keeps the shape it had.
func TestEventWriteResponsesKeepTheStoredColumnsOnly(t *testing.T) {
	created := decodeBody(t, do(t, &fakeService{}, http.MethodPost, "/events",
		`{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z"}`))
	requireKeys(t, "created event", created, storedEventKeys)

	// The fake holds the names, so the write can only lose them by choosing to.
	updated := decodeBody(t, do(t, &fakeService{event: namedEventFixture()}, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1250}`))
	requireKeys(t, "updated event", updated, storedEventKeys)
	if updated["priceCents"] != float64(1250) || updated["priceDisplay"] != "12.50" {
		t.Fatalf("price = %v / %v, want 1250 and 12.50", updated["priceCents"], updated["priceDisplay"])
	}

	// A status write answers the same stored columns: the status change is not an excuse
	// for a second shape, and RETURNING cannot join.
	published := decodeBody(t, do(t, &fakeService{event: namedEventFixture()}, http.MethodPatch, "/events/"+testEventID,
		`{"priceCents":1400,"status":"on_sale"}`))
	requireKeys(t, "published event", published, storedEventKeys)
	if published["status"] != venue.EventOnSale || published["priceCents"] != float64(1400) {
		t.Fatalf("event = %v, want the stored on_sale screening at 1400", published)
	}
}

func TestListEventsWithoutScreeningsReturnsEmptyArray(t *testing.T) {
	w := do(t, &fakeService{}, http.MethodGet, "/events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	events, ok := decodeBody(t, w)["events"].([]any)
	if !ok || len(events) != 0 {
		t.Fatalf("events = %v, want an empty JSON array", events)
	}
}

func TestGetEventCarriesPriceAndDisplay(t *testing.T) {
	event := eventFixture()
	event.PriceCents = 1250
	svc := &fakeService{event: event}
	w := do(t, svc, http.MethodGet, "/events/"+testEventID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	requireKeys(t, "event", body, eventReadKeys)
	if body["priceCents"] != float64(1250) || body["priceDisplay"] != "12.50" {
		t.Fatalf("price = %v / %v, want 1250 and 12.50", body["priceCents"], body["priceDisplay"])
	}
	if svc.lastEventID != testEventID {
		t.Fatalf("event id = %q, want %q", svc.lastEventID, testEventID)
	}
}

func TestGetEventMissingReturns404(t *testing.T) {
	w := do(t, &fakeService{createErr: venue.ErrNotFound}, http.MethodGet, "/events/"+testEventID, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "not_found" {
		t.Fatalf("problem = %v, want not_found", body)
	}
}

func TestGetEventRejectsMalformedIdentifier(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodGet, "/events/not-an-event", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_input" || body["detail"] != "eventId: must be a valid identifier" {
		t.Fatalf("problem = %v, want the eventId rejection", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestEventReadFailureReturns503(t *testing.T) {
	w := do(t, &fakeService{createErr: context.DeadlineExceeded}, http.MethodGet, "/events", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	if body["code"] != "service_unavailable" || body["detail"] != "the request could not be completed; retry the request" {
		t.Fatalf("problem = %v, want the service_unavailable problem", body)
	}
}

func TestUpdateEventAcceptsZero(t *testing.T) {
	// The stored screening is on sale at a price, so a price-only request can only leave
	// the status as it is by choosing to.
	event := eventFixture()
	event.PriceCents = 1250
	svc := &fakeService{event: event}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"priceCents":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if svc.lastUpdate.PriceCents == nil || *svc.lastUpdate.PriceCents != 0 {
		t.Fatalf("price = %v, want the explicit zero", svc.lastUpdate.PriceCents)
	}
	// A price-only body says nothing about the status, so it must not carry one.
	if svc.lastUpdate.Status != nil {
		t.Fatalf("status = %v, want a price-only request to leave the status untouched", *svc.lastUpdate.Status)
	}
	body := decodeBody(t, w)
	if body["priceCents"] != float64(0) || body["priceDisplay"] != "0.00" {
		t.Fatalf("price = %v / %v, want 0 and 0.00", body["priceCents"], body["priceDisplay"])
	}
	if len(svc.calls) != 1 || svc.calls[0] != "UpdateEvent" {
		t.Fatalf("calls = %v, want exactly UpdateEvent", svc.calls)
	}
}

func TestUpdateEventSetsANonZeroPriceOnly(t *testing.T) {
	// The stored status is a draft, so a price-only request that wrote a status would show
	// here as a published screening.
	event := eventFixture()
	event.Status = venue.EventDraft
	svc := &fakeService{event: event}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1250}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if svc.lastUpdate.PriceCents == nil || *svc.lastUpdate.PriceCents != 1250 {
		t.Fatalf("price = %v, want 1250", svc.lastUpdate.PriceCents)
	}
	if svc.lastUpdate.Status != nil {
		t.Fatalf("status = %v, want a price-only request to carry no status", *svc.lastUpdate.Status)
	}
	body := decodeBody(t, w)
	if body["priceCents"] != float64(1250) || body["priceDisplay"] != "12.50" || body["status"] != venue.EventDraft {
		t.Fatalf("event = %v, want the stored price and its display with the status kept", body)
	}
}

// A screening is stored as a draft and the public catalogue answers only an on_sale one,
// so this request is the only way what the administration created becomes buyable.
func TestUpdateEventPublishesADraftWithAStatusOnlyBody(t *testing.T) {
	draft := eventFixture()
	draft.Status = venue.EventDraft
	draft.PriceCents = 1250
	svc := &fakeService{event: draft}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"status":"on_sale"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if svc.lastUpdate.Status == nil || *svc.lastUpdate.Status != venue.EventOnSale {
		t.Fatalf("status = %v, want on_sale", svc.lastUpdate.Status)
	}
	// The price was not in the body and must not be in the write either.
	if svc.lastUpdate.PriceCents != nil {
		t.Fatalf("price = %v, want a status-only request to carry no price", *svc.lastUpdate.PriceCents)
	}
	body := decodeBody(t, w)
	requireKeys(t, "published event", body, storedEventKeys)
	if body["status"] != venue.EventOnSale || body["priceCents"] != float64(1250) {
		t.Fatalf("event = %v, want the stored on_sale screening at 1250", body)
	}
}

// columnEventStatuses is cinema.events.status's own CHECK constraint, spelled out here from
// db/migrations/0002_venue_layout.up.sql rather than read from the constant the handler
// validates against, so a list that drifted from the column would fail this test instead of
// agreeing with it.
var columnEventStatuses = []string{"draft", "published", "on_sale", "started", "completed", "cancelled"}

func TestUpdateEventAcceptsEveryStatusTheColumnAllows(t *testing.T) {
	for _, status := range columnEventStatuses {
		t.Run(status, func(t *testing.T) {
			svc := &fakeService{event: eventFixture()}
			w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"status":"`+status+`"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
			}
			if svc.lastUpdate.Status == nil || *svc.lastUpdate.Status != status {
				t.Fatalf("status = %v, want %q", svc.lastUpdate.Status, status)
			}
			if body := decodeBody(t, w); body["status"] != status {
				t.Fatalf("answered status = %v, want %q", body["status"], status)
			}
		})
	}
}

func TestUpdateEventRejectsAnUnknownStatus(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"status":"sold_out"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_input" ||
		body["detail"] != "status: must be one of draft, published, on_sale, started, completed, cancelled" {
		t.Fatalf("problem = %v, want the status rejection naming the field", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestUpdateEventRejectsANonStringStatus(t *testing.T) {
	for _, body := range []string{
		`{"status":1}`, `{"status":null}`, `{"status":true}`,
		`{"status":["on_sale"]}`, `{"status":{"name":"on_sale"}}`,
	} {
		svc := &fakeService{event: eventFixture()}
		w := do(t, svc, http.MethodPatch, "/events/"+testEventID, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400 (%s)", body, w.Code, w.Body)
		}
		if problem := decodeBody(t, w); problem["code"] != "invalid_input" || problem["detail"] != "status: must be a string" {
			t.Fatalf("body %s: problem = %v, want the string rejection", body, problem)
		}
		if len(svc.calls) != 0 {
			t.Fatalf("body %s: calls = %v, want no service call", body, svc.calls)
		}
	}
}

func TestUpdateEventSetsPriceAndStatusInOneRequest(t *testing.T) {
	draft := eventFixture()
	draft.Status = venue.EventDraft
	svc := &fakeService{event: draft}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1400,"status":"on_sale"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if svc.lastUpdate.PriceCents == nil || *svc.lastUpdate.PriceCents != 1400 ||
		svc.lastUpdate.Status == nil || *svc.lastUpdate.Status != venue.EventOnSale {
		t.Fatalf("update = %+v, want both fields in one request", svc.lastUpdate)
	}
	if len(svc.calls) != 1 || svc.calls[0] != "UpdateEvent" {
		t.Fatalf("calls = %v, want exactly one write", svc.calls)
	}
	body := decodeBody(t, w)
	if body["priceCents"] != float64(1400) || body["priceDisplay"] != "14.00" || body["status"] != venue.EventOnSale {
		t.Fatalf("event = %v, want the stored on_sale screening at 14.00", body)
	}
}

// A body that names neither field is refused instead of writing nothing: an empty update is
// not a price of zero and not a status of anything.
func TestUpdateEventRejectsABodyThatChangesNothing(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_input" || body["detail"] != "nothing to change: provide priceCents or status" {
		t.Fatalf("problem = %v, want the nothing-to-change rejection", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestUpdateEventRejectsNegative(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"priceCents":-1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_input" || body["detail"] != "priceCents: must be zero or greater" {
		t.Fatalf("problem = %v, want the negative rejection", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestUpdateEventRejectsAboveInt4(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"priceCents":2147483648}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_input" || body["detail"] != "priceCents: must be at most 2147483647" {
		t.Fatalf("problem = %v, want the int4 range rejection", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestUpdateEventRejectsNonInteger(t *testing.T) {
	for _, body := range []string{`{"priceCents":12.5}`, `{"priceCents":"12"}`, `{"priceCents":null}`} {
		svc := &fakeService{event: eventFixture()}
		w := do(t, svc, http.MethodPatch, "/events/"+testEventID, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400 (%s)", body, w.Code, w.Body)
		}
		if problem := decodeBody(t, w); problem["code"] != "invalid_input" || problem["detail"] != "priceCents: must be an integer" {
			t.Fatalf("body %s: problem = %v, want the integer rejection", body, problem)
		}
		if len(svc.calls) != 0 {
			t.Fatalf("body %s: calls = %v, want no service call", body, svc.calls)
		}
	}
}

func TestUpdateEventRejectsMalformedIdentifier(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodPatch, "/events/not-an-event", `{"priceCents":1250}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_input" || body["detail"] != "eventId: must be a valid identifier" {
		t.Fatalf("problem = %v, want the eventId rejection", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestUpdateEventUnknownEventReturns404(t *testing.T) {
	w := do(t, &fakeService{createErr: venue.ErrNotFound}, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1250}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "not_found" {
		t.Fatalf("problem = %v, want not_found", body)
	}

	// The identifier is the guard for both columns: a status-only request for a screening
	// that does not exist is not found, never a success that published nothing.
	w = do(t, &fakeService{createErr: venue.ErrNotFound}, http.MethodPatch, "/events/"+testEventID, `{"status":"on_sale"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a status-only request too (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "not_found" {
		t.Fatalf("problem = %v, want not_found", body)
	}
}

func TestUpdateEventStoreFailureReturns503(t *testing.T) {
	w := do(t, &fakeService{createErr: context.DeadlineExceeded}, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1250}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "service_unavailable" {
		t.Fatalf("problem = %v, want service_unavailable", body)
	}
}

func TestUpdateEventRejectsMalformedAndOversizedBodies(t *testing.T) {
	for name, body := range map[string]string{
		"malformed": `{`,
		"oversized": strings.Repeat("x", (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeService{event: eventFixture()}
			w := do(t, svc, http.MethodPatch, "/events/"+testEventID, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
			}
			if problem := decodeBody(t, w); problem["code"] != "invalid_request" {
				t.Fatalf("problem = %v, want invalid_request", problem)
			}
			if len(svc.calls) != 0 {
				t.Fatalf("calls = %v, want no service call", svc.calls)
			}
		})
	}
}

// Putting a screening on sale is one request: the price and the status travel with the
// create, so the administration does not have to schedule and then patch.
func TestCreateEventCarriesAPriceAndAStatusInOneRequest(t *testing.T) {
	svc := &fakeService{}
	w := do(t, svc, http.MethodPost, "/events",
		`{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z","priceCents":1400,"status":"on_sale"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
	}
	if len(svc.calls) != 1 || svc.calls[0] != "CreateEvent" {
		t.Fatalf("calls = %v, want exactly one create", svc.calls)
	}
	if svc.lastCreatePrice == nil || *svc.lastCreatePrice != 1400 {
		t.Fatalf("price = %v, want 1400", svc.lastCreatePrice)
	}
	if svc.lastCreateStatus == nil || *svc.lastCreateStatus != venue.EventOnSale {
		t.Fatalf("status = %v, want on_sale", svc.lastCreateStatus)
	}
	body := decodeBody(t, w)
	requireKeys(t, "created event", body, storedEventKeys)
	if body["priceCents"] != float64(1400) || body["priceDisplay"] != "14.00" || body["status"] != venue.EventOnSale {
		t.Fatalf("event = %v, want the on_sale screening at 14.00", body)
	}
}

// A create that says nothing about the price and the status behaves exactly as it did
// before they were accepted: a draft at no price. That is the request the web form and the
// gate send today.
func TestCreateEventStaysADraftAtNoPriceWhenNeitherIsSent(t *testing.T) {
	svc := &fakeService{}
	w := do(t, svc, http.MethodPost, "/events",
		`{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
	}
	// Nothing was sent, so nothing may be invented: neither a price nor a status reaches the
	// service, and the defaults are the schema's own.
	if svc.lastCreatePrice != nil {
		t.Fatalf("price = %v, want no price sent", *svc.lastCreatePrice)
	}
	if svc.lastCreateStatus != nil {
		t.Fatalf("status = %v, want no status sent", *svc.lastCreateStatus)
	}
	body := decodeBody(t, w)
	requireKeys(t, "created event", body, storedEventKeys)
	if body["status"] != venue.EventDraft || body["priceCents"] != float64(0) || body["priceDisplay"] != "0.00" {
		t.Fatalf("event = %v, want the default draft at no price", body)
	}
}

// Zero is a free screening, not a missing price, so an explicit zero reaches the service as
// the value it is.
func TestCreateEventAcceptsAnExplicitZeroPrice(t *testing.T) {
	svc := &fakeService{}
	w := do(t, svc, http.MethodPost, "/events",
		`{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z","priceCents":0}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
	}
	if svc.lastCreatePrice == nil || *svc.lastCreatePrice != 0 {
		t.Fatalf("price = %v, want the explicit zero", svc.lastCreatePrice)
	}
	if svc.lastCreateStatus != nil {
		t.Fatalf("status = %v, want a price-only create to carry no status", *svc.lastCreateStatus)
	}
	body := decodeBody(t, w)
	if body["priceCents"] != float64(0) || body["priceDisplay"] != "0.00" || body["status"] != venue.EventDraft {
		t.Fatalf("event = %v, want the free draft screening", body)
	}
}

// What a legal price and a legal status are is one rule, not two: the same value sent to the
// create and to the update is refused in the same words, and neither route writes anything.
func TestCreateEventRefusesTheSameValuesAsTheUpdate(t *testing.T) {
	for _, field := range []string{
		`"priceCents":-1`,
		`"priceCents":2147483648`,
		`"priceCents":12.5`,
		`"priceCents":"12"`,
		`"priceCents":null`,
		`"status":"sold_out"`,
		`"status":1`,
		`"status":null`,
	} {
		t.Run(field, func(t *testing.T) {
			svc := &fakeService{event: eventFixture()}
			created := do(t, svc, http.MethodPost, "/events",
				`{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z",`+field+`}`)
			updated := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{`+field+`}`)
			if created.Code != http.StatusBadRequest {
				t.Fatalf("create status = %d, want 400 (%s)", created.Code, created.Body)
			}
			if updated.Code != http.StatusBadRequest {
				t.Fatalf("update status = %d, want 400 (%s)", updated.Code, updated.Body)
			}
			createProblem, updateProblem := decodeBody(t, created), decodeBody(t, updated)
			if createProblem["code"] != "invalid_input" || updateProblem["code"] != "invalid_input" {
				t.Fatalf("problems = %v / %v, want the invalid_input problem from both routes", createProblem, updateProblem)
			}
			if createProblem["detail"] != updateProblem["detail"] {
				t.Fatalf("create refused %q but the update refused %q, want one rule in one spelling",
					createProblem["detail"], updateProblem["detail"])
			}
			if len(svc.calls) != 0 {
				t.Fatalf("calls = %v, want no service call from either route", svc.calls)
			}
		})
	}
}

// The administrative reads answer the venue's own hour beside the stored instant, so an
// operator scheduling a month of screenings does not convert by hand. startsAt keeps its
// exact UTC value and format: the local hour is added, never substituted.
func TestEventReadsCarryTheVenueHourBesideStartsAt(t *testing.T) {
	svc := &fakeService{event: namedEventFixture(), events: []venue.Event{namedEventFixture()}}

	listed := do(t, svc, http.MethodGet, "/events", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", listed.Code, listed.Body)
	}
	events, ok := decodeBody(t, listed)["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %v, want the one screening", events)
	}
	entry, _ := events[0].(map[string]any)
	requireKeys(t, "listed event", entry, eventReadKeys)
	if entry["startsAt"] != "2030-01-02T18:00:00Z" {
		t.Fatalf("listed startsAt = %v, want the stored instant in UTC", entry["startsAt"])
	}
	if entry["startsAtLocal"] != testStartsAtLocal {
		t.Fatalf("listed startsAtLocal = %v, want %q", entry["startsAtLocal"], testStartsAtLocal)
	}

	single := do(t, svc, http.MethodGet, "/events/"+testEventID, "")
	if single.Code != http.StatusOK {
		t.Fatalf("read status = %d, want 200 (%s)", single.Code, single.Body)
	}
	body := decodeBody(t, single)
	requireKeys(t, "event", body, eventReadKeys)
	if body["startsAt"] != "2030-01-02T18:00:00Z" {
		t.Fatalf("startsAt = %v, want the stored instant in UTC", body["startsAt"])
	}
	if body["startsAtLocal"] != testStartsAtLocal {
		t.Fatalf("startsAtLocal = %v, want %q", body["startsAtLocal"], testStartsAtLocal)
	}
}

// A stored zone that does not resolve is a legacy row the backfill has not reached yet. The
// read still answers the stored instant, and the local hour is absent: a screen that shows a
// wrong hour is worse than one that shows none, so the API never guesses.
func TestEventReadsOmitTheVenueHourWhenTheTimezoneDoesNotResolve(t *testing.T) {
	for _, timezone := range []string{"America/Madrid", ""} {
		t.Run("timezone "+timezone, func(t *testing.T) {
			event := namedEventFixture()
			event.CinemaTimezone = timezone
			svc := &fakeService{event: event, events: []venue.Event{event}}

			listed := do(t, svc, http.MethodGet, "/events", "")
			if listed.Code != http.StatusOK {
				t.Fatalf("list status = %d, want 200 (%s)", listed.Code, listed.Body)
			}
			events, ok := decodeBody(t, listed)["events"].([]any)
			if !ok || len(events) != 1 {
				t.Fatalf("events = %v, want the one screening", events)
			}
			entry, _ := events[0].(map[string]any)
			requireKeys(t, "listed event without a resolvable zone", entry, eventReadKeysWithoutLocal)
			if entry["startsAt"] != "2030-01-02T18:00:00Z" || entry["cinemaName"] != "Cine Central" {
				t.Fatalf("listed event = %v, want the stored instant and the names kept", entry)
			}

			body := decodeBody(t, do(t, svc, http.MethodGet, "/events/"+testEventID, ""))
			requireKeys(t, "event without a resolvable zone", body, eventReadKeysWithoutLocal)
			if body["startsAt"] != "2030-01-02T18:00:00Z" {
				t.Fatalf("startsAt = %v, want the stored instant in UTC", body["startsAt"])
			}
		})
	}
}

func TestCreateEventResponseKeepsItsShapeAndAddsPrice(t *testing.T) {
	svc := &fakeService{}
	w := do(t, svc, http.MethodPost, "/events", `{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	requireKeys(t, "created event", body, storedEventKeys)
	if body["priceCents"] != float64(0) || body["priceDisplay"] != "0.00" {
		t.Fatalf("price = %v / %v, want the default zero", body["priceCents"], body["priceDisplay"])
	}
}
