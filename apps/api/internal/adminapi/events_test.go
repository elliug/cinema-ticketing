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

func eventFixture() venue.Event {
	return venue.Event{
		ID:              testEventID,
		RoomID:          "room-1",
		FilmID:          "film-1",
		LayoutVersionID: "layout-1",
		StartsAt:        testStartsAt,
		Status:          "on_sale",
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
	for _, key := range []string{"id", "roomId", "filmId", "layoutVersionId", "startsAt", "status", "priceCents", "priceDisplay"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("event is missing key %q: %v", key, first)
		}
	}
	if first["priceCents"] != float64(1250) || first["priceDisplay"] != "12.50" {
		t.Fatalf("price = %v / %v, want 1250 and 12.50", first["priceCents"], first["priceDisplay"])
	}
	if len(svc.calls) != 1 || svc.calls[0] != "ListEvents" {
		t.Fatalf("calls = %v, want exactly ListEvents", svc.calls)
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
	for _, key := range []string{"id", "roomId", "filmId", "layoutVersionId", "startsAt", "status", "priceCents", "priceDisplay"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("event is missing key %q: %v", key, body)
		}
	}
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

func TestSetEventPriceAcceptsZero(t *testing.T) {
	event := eventFixture()
	svc := &fakeService{event: event}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"priceCents":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if svc.lastPriceCents != 0 {
		t.Fatalf("price = %d, want the explicit zero", svc.lastPriceCents)
	}
	body := decodeBody(t, w)
	if body["priceCents"] != float64(0) || body["priceDisplay"] != "0.00" {
		t.Fatalf("price = %v / %v, want 0 and 0.00", body["priceCents"], body["priceDisplay"])
	}
	if len(svc.calls) != 1 || svc.calls[0] != "SetEventPrice" {
		t.Fatalf("calls = %v, want exactly SetEventPrice", svc.calls)
	}
}

func TestSetEventPriceSetsANonZeroPrice(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1250}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if svc.lastPriceCents != 1250 {
		t.Fatalf("price = %d, want 1250", svc.lastPriceCents)
	}
	if body := decodeBody(t, w); body["priceCents"] != float64(1250) || body["priceDisplay"] != "12.50" {
		t.Fatalf("event = %v, want the stored price and its display", body)
	}
}

func TestSetEventPriceMissingFieldIsNotZero(t *testing.T) {
	svc := &fakeService{event: eventFixture()}
	w := do(t, svc, http.MethodPatch, "/events/"+testEventID, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_input" || body["detail"] != "priceCents: must be provided" {
		t.Fatalf("problem = %v, want priceCents must be provided", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestSetEventPriceRejectsNegative(t *testing.T) {
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

func TestSetEventPriceRejectsAboveInt4(t *testing.T) {
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

func TestSetEventPriceRejectsNonInteger(t *testing.T) {
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

func TestSetEventPriceRejectsMalformedIdentifier(t *testing.T) {
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

func TestSetEventPriceUnknownEventReturns404(t *testing.T) {
	w := do(t, &fakeService{createErr: venue.ErrNotFound}, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1250}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "not_found" {
		t.Fatalf("problem = %v, want not_found", body)
	}
}

func TestSetEventPriceStoreFailureReturns503(t *testing.T) {
	w := do(t, &fakeService{createErr: context.DeadlineExceeded}, http.MethodPatch, "/events/"+testEventID, `{"priceCents":1250}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "service_unavailable" {
		t.Fatalf("problem = %v, want service_unavailable", body)
	}
}

func TestSetEventPriceRejectsMalformedAndOversizedBodies(t *testing.T) {
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

func TestCreateEventResponseKeepsItsShapeAndAddsPrice(t *testing.T) {
	svc := &fakeService{}
	w := do(t, svc, http.MethodPost, "/events", `{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	for _, key := range []string{"id", "roomId", "filmId", "layoutVersionId", "startsAt", "status", "priceCents", "priceDisplay"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("created event is missing key %q: %v", key, body)
		}
	}
	if body["priceCents"] != float64(0) || body["priceDisplay"] != "0.00" {
		t.Fatalf("price = %v / %v, want the default zero", body["priceCents"], body["priceDisplay"])
	}
}
