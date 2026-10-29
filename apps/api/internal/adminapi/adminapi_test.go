package adminapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/adminapi"
	"cinema.local/ticketing/apps/api/internal/venue"
)

type fakeService struct {
	layout      venue.Layout
	seats       []venue.EventSeat
	calls       []string
	createErr   error
	lastRows    []venue.Row
	lastEvent   venue.Event
	lastCinema  venue.Cinema
	lastRequest string
}

func (f *fakeService) CreateCinema(_ context.Context, name, timezone, address string) (venue.Cinema, error) {
	f.calls = append(f.calls, "CreateCinema")
	f.lastCinema = venue.Cinema{ID: "cinema-1", Name: name, Timezone: timezone, Address: address}
	return f.lastCinema, f.createErr
}

func (f *fakeService) CreateRoom(_ context.Context, cinemaID, name string) (venue.Room, error) {
	f.calls = append(f.calls, "CreateRoom")
	return venue.Room{ID: "room-1", CinemaID: cinemaID, Name: name}, f.createErr
}

func (f *fakeService) CreateLayout(_ context.Context, roomID string) (venue.Layout, error) {
	f.calls = append(f.calls, "CreateLayout")
	return venue.Layout{ID: "layout-1", RoomID: roomID, Version: 1, Status: venue.LayoutDraft, Rows: []venue.Row{}}, f.createErr
}

func (f *fakeService) SetRows(_ context.Context, layoutID string, rows []venue.Row) (venue.Layout, error) {
	f.calls = append(f.calls, "SetRows")
	f.lastRows = rows
	return venue.Layout{ID: layoutID, Version: 1, Status: venue.LayoutDraft, Rows: rows}, f.createErr
}

func (f *fakeService) PublishLayout(_ context.Context, layoutID string) (venue.Layout, error) {
	f.calls = append(f.calls, "PublishLayout")
	return venue.Layout{ID: layoutID, Version: 1, Status: venue.LayoutPublished, Rows: f.layout.Rows}, f.createErr
}

func (f *fakeService) CloneLayout(_ context.Context, layoutID string) (venue.Layout, error) {
	f.calls = append(f.calls, "CloneLayout")
	return venue.Layout{ID: "layout-2", RoomID: layoutID, Version: 2, Status: venue.LayoutDraft}, f.createErr
}

func (f *fakeService) GetLayout(_ context.Context, layoutID string) (venue.Layout, error) {
	f.calls = append(f.calls, "GetLayout")
	if f.createErr != nil {
		return venue.Layout{}, f.createErr
	}
	return venue.Layout{ID: layoutID, Version: 1, Status: venue.LayoutPublished, Rows: f.layout.Rows}, nil
}

func (f *fakeService) CreateFilm(_ context.Context, title string, runtimeMinutes int, rating string) (venue.Film, error) {
	f.calls = append(f.calls, "CreateFilm")
	return venue.Film{ID: "film-1", Title: title, RuntimeMinutes: runtimeMinutes, Rating: rating}, f.createErr
}

func (f *fakeService) CreateEvent(_ context.Context, roomID, filmID, layoutVersionID string, startsAt time.Time) (venue.Event, error) {
	f.calls = append(f.calls, "CreateEvent")
	f.lastEvent = venue.Event{ID: "event-1", RoomID: roomID, FilmID: filmID, LayoutVersionID: layoutVersionID, StartsAt: startsAt, Status: "draft"}
	return f.lastEvent, f.createErr
}

func (f *fakeService) ListEventSeats(_ context.Context, eventID string) ([]venue.EventSeat, error) {
	f.calls = append(f.calls, "ListEventSeats")
	return f.seats, f.createErr
}

func do(t *testing.T, svc *fakeService, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	w := httptest.NewRecorder()
	adminapi.New(svc).Mux().ServeHTTP(w, r)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return body
}

func TestCreateCinemaReturnsCreated(t *testing.T) {
	svc := &fakeService{}
	w := do(t, svc, http.MethodPost, "/cinemas", `{"name":"  Cine  ","timezone":"UTC"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
	}
	if svc.lastCinema.Name != "  Cine  " {
		t.Fatalf("handler must pass raw input to the service, got %q", svc.lastCinema.Name)
	}
}

func TestCreateCinemaRejectsInvalidInputWithoutPersisting(t *testing.T) {
	svc := &fakeService{createErr: venue.NewInvalidError("name", "must not be empty")}
	w := do(t, svc, http.MethodPost, "/cinemas", `{"name":"","timezone":"UTC"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	body := decodeBody(t, w)
	if body["code"] != "invalid_input" || body["detail"] != "name: must not be empty" {
		t.Fatalf("problem = %v, want invalid_input with field detail", body)
	}
}

func TestMalformedJSONRejected(t *testing.T) {
	svc := &fakeService{}
	w := do(t, svc, http.MethodPost, "/films", `{oops`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if body := decodeBody(t, w); body["code"] != "invalid_request" {
		t.Fatalf("problem = %v, want invalid_request", body)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("calls = %v, want no service call", svc.calls)
	}
}

func TestSetRowsPassesRowsThrough(t *testing.T) {
	svc := &fakeService{}
	body := `{"rows":[{"label":"A","position":1,"seats":[{"number":"1","position":1,"kind":"standard","enabled":true}]},{"label":"B","position":2,"seats":[{"number":"1","position":1,"kind":"companion"},{"number":"2","position":2,"kind":"accessible"}]}]}`
	w := do(t, svc, http.MethodPut, "/layouts/layout-1/rows", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if len(svc.lastRows) != 2 || len(svc.lastRows[0].Seats) != 1 || len(svc.lastRows[1].Seats) != 2 {
		t.Fatalf("rows = %+v, want 1 and 2 seats", svc.lastRows)
	}
}

func TestPublishConflictReturns409(t *testing.T) {
	svc := &fakeService{createErr: venue.ErrConflict}
	w := do(t, svc, http.MethodPost, "/layouts/layout-1/publish", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if body := decodeBody(t, w); body["code"] != "conflict" {
		t.Fatalf("problem = %v, want conflict", body)
	}
}

func TestGetLayoutMissingReturns404(t *testing.T) {
	svc := &fakeService{createErr: venue.ErrNotFound}
	w := do(t, svc, http.MethodGet, "/layouts/layout-9", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestInternalErrorHidesDetails(t *testing.T) {
	svc := &fakeService{createErr: context.DeadlineExceeded}
	w := do(t, svc, http.MethodGet, "/layouts/layout-1", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if body := decodeBody(t, w); body["detail"] != "the request could not be completed" {
		t.Fatalf("problem = %v, want generic detail", body)
	}
}

func TestCreateEventReturnsMaterializedSeatEndpoint(t *testing.T) {
	svc := &fakeService{seats: []venue.EventSeat{{ID: 1, LayoutSeatID: "seat-1", State: "available"}}}
	w := do(t, svc, http.MethodPost, "/events", `{"roomId":"room-1","filmId":"film-1","layoutVersionId":"layout-1","startsAt":"2030-01-01T10:00:00Z"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body)
	}
	w = do(t, svc, http.MethodGet, "/events/event-1/seats", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeBody(t, w)
	seats, _ := body["seats"].([]any)
	if len(seats) != 1 {
		t.Fatalf("seats = %v, want one materialized seat", body["seats"])
	}
}

func TestLayoutJSONContract(t *testing.T) {
	svc := &fakeService{layout: venue.Layout{Rows: []venue.Row{{
		ID: "row-1", Label: "A", Position: 1,
		Seats: []venue.Seat{{ID: "seat-1", Number: "1", Position: 1, Kind: venue.SeatStandard, Enabled: true}},
	}}}}
	w := do(t, svc, http.MethodGet, "/layouts/layout-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeBody(t, w)
	for _, key := range []string{"id", "roomId", "version", "status", "rows"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("layout response is missing key %q: %v", key, body)
		}
	}
	rows, _ := body["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one row", body["rows"])
	}
	row, _ := rows[0].(map[string]any)
	seats, _ := row["seats"].([]any)
	if len(seats) != 1 {
		t.Fatalf("row.seats = %v, want one seat", row["seats"])
	}
	seat, _ := seats[0].(map[string]any)
	for _, key := range []string{"id", "number", "position", "kind", "enabled"} {
		if _, ok := seat[key]; !ok {
			t.Fatalf("seat is missing key %q: %v", key, seat)
		}
	}
}
