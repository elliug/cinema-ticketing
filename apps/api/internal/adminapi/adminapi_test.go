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
	layout         venue.Layout
	layoutVersions []venue.Layout
	cinemas        []venue.Cinema
	rooms          []venue.Room
	event          venue.Event
	events         []venue.Event
	films          []venue.Film
	seats          []venue.EventSeat
	calls          []string
	createErr      error
	lastRows       []venue.Row
	lastEvent      venue.Event
	lastCinema     venue.Cinema
	lastEventID    string
	lastUpdate     venue.EventUpdate
	lastRequest    string
	// lastCreatePrice and lastCreateStatus are what POST /events carried, so a test can tell
	// "the field was not sent" from "the field was sent as zero" — the difference the create
	// and the update both hinge on.
	lastCreatePrice  *int64
	lastCreateStatus *string
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

func (f *fakeService) CreateEvent(_ context.Context, roomID, filmID, layoutVersionID string, startsAt time.Time, priceCents *int64, status *string) (venue.Event, error) {
	f.calls = append(f.calls, "CreateEvent")
	f.lastCreatePrice, f.lastCreateStatus = priceCents, status
	event := venue.Event{ID: "event-1", RoomID: roomID, FilmID: filmID, LayoutVersionID: layoutVersionID, StartsAt: startsAt, Status: venue.EventDraft}
	if priceCents != nil {
		event.PriceCents = *priceCents
	}
	if status != nil {
		event.Status = *status
	}
	f.lastEvent = event
	return f.lastEvent, f.createErr
}

func (f *fakeService) ListEventSeats(_ context.Context, eventID string) ([]venue.EventSeat, error) {
	f.calls = append(f.calls, "ListEventSeats")
	return f.seats, f.createErr
}

func (f *fakeService) ListFilms(_ context.Context) ([]venue.Film, error) {
	f.calls = append(f.calls, "ListFilms")
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.films, nil
}

func (f *fakeService) ListEvents(_ context.Context) ([]venue.Event, error) {
	f.calls = append(f.calls, "ListEvents")
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.events, nil
}

func (f *fakeService) GetEvent(_ context.Context, eventID string) (venue.Event, error) {
	f.calls = append(f.calls, "GetEvent")
	f.lastEventID = eventID
	if f.createErr != nil {
		return venue.Event{}, f.createErr
	}
	event := f.event
	event.ID = eventID
	return event, nil
}

func (f *fakeService) UpdateEvent(_ context.Context, eventID string, update venue.EventUpdate) (venue.Event, error) {
	f.calls = append(f.calls, "UpdateEvent")
	f.lastEventID = eventID
	f.lastUpdate = update
	if f.createErr != nil {
		return venue.Event{}, f.createErr
	}
	event := f.event
	event.ID = eventID
	if update.PriceCents != nil {
		event.PriceCents = *update.PriceCents
	}
	if update.Status != nil {
		event.Status = *update.Status
	}
	return event, nil
}

func (f *fakeService) ListCinemas(_ context.Context) ([]venue.Cinema, error) {
	f.calls = append(f.calls, "ListCinemas")
	return f.cinemas, f.createErr
}

func (f *fakeService) ListRooms(_ context.Context, cinemaID string) ([]venue.Room, error) {
	f.calls = append(f.calls, "ListRooms")
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.rooms, nil
}

func (f *fakeService) ListLayouts(_ context.Context, roomID string) ([]venue.Layout, error) {
	f.calls = append(f.calls, "ListLayouts")
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.layoutVersions, nil
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

// The administration cannot schedule a screening against a film it cannot list, so the
// read answers every stored film as the create wrote it, in the order the store
// decided (title, then identifier): the handler does not re-sort a repertoire.
func TestListFilmsAnswersEveryStoredFilm(t *testing.T) {
	svc := &fakeService{films: []venue.Film{
		{ID: "film-1", Title: "Aardvark", RuntimeMinutes: 88, Rating: "PG"},
		{ID: "film-2", Title: "Zulu", RuntimeMinutes: 121, Rating: "18"},
	}}
	w := do(t, svc, http.MethodGet, "/films", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	films, ok := body["films"].([]any)
	if !ok || len(films) != 2 {
		t.Fatalf("films = %v, want both stored films", body["films"])
	}
	first, _ := films[0].(map[string]any)
	if len(first) != 4 {
		t.Fatalf("film = %v, want exactly id, title, runtimeMinutes and rating", first)
	}
	for _, key := range []string{"id", "title", "runtimeMinutes", "rating"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("film is missing key %q: %v", key, first)
		}
	}
	if first["id"] != "film-1" || first["title"] != "Aardvark" || first["runtimeMinutes"] != float64(88) || first["rating"] != "PG" {
		t.Fatalf("first film = %v, want the stored Aardvark", first)
	}
	if second, _ := films[1].(map[string]any); second["id"] != "film-2" || second["title"] != "Zulu" {
		t.Fatalf("second film = %v, want Zulu after Aardvark, in the order the service answered", second)
	}
	if len(svc.calls) != 1 || svc.calls[0] != "ListFilms" {
		t.Fatalf("calls = %v, want exactly ListFilms", svc.calls)
	}
}

func TestListFilmsWithoutFilmsReturnsEmptyArray(t *testing.T) {
	w := do(t, &fakeService{}, http.MethodGet, "/films", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	films, ok := decodeBody(t, w)["films"].([]any)
	if !ok || len(films) != 0 {
		t.Fatalf("films = %v, want an empty JSON array", films)
	}
}

func TestListFilmsStoreFailureIsNotAnEmptyRepertoire(t *testing.T) {
	w := do(t, &fakeService{createErr: context.DeadlineExceeded}, http.MethodGet, "/films", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	if body["code"] != "internal_error" || body["detail"] != "the request could not be completed" {
		t.Fatalf("problem = %v, want the generic internal error", body)
	}
	if _, ok := body["films"]; ok {
		t.Fatalf("problem = %v, want no film list beside the failure", body)
	}
}

// The create and the list answer the same four keys, because both use one film shape: a
// second copy is how a create and a list start to disagree about what a film is.
func TestCreateFilmAndListAnswerTheSameFilm(t *testing.T) {
	svc := &fakeService{}
	created := do(t, svc, http.MethodPost, "/films", `{"title":"Aardvark","runtimeMinutes":88,"rating":"PG"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", created.Code, created.Body)
	}
	body := decodeBody(t, created)
	for _, key := range []string{"id", "title", "runtimeMinutes", "rating"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("created film is missing key %q: %v", key, body)
		}
	}
	if len(body) != 4 {
		t.Fatalf("created film = %v, want exactly id, title, runtimeMinutes and rating", body)
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

func TestListCinemasReturnsEveryCinema(t *testing.T) {
	svc := &fakeService{cinemas: []venue.Cinema{
		{ID: "cinema-1", Name: "Central", Timezone: "Europe/Madrid"},
		{ID: "cinema-2", Name: "Norte", Timezone: "Europe/Madrid", Address: "Sol 2"},
	}}
	w := do(t, svc, http.MethodGet, "/cinemas", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	cinemas, _ := body["cinemas"].([]any)
	if len(cinemas) != 2 {
		t.Fatalf("cinemas = %v, want both venues", body["cinemas"])
	}
	first, _ := cinemas[0].(map[string]any)
	for _, key := range []string{"id", "name", "timezone", "address"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("cinema is missing key %q: %v", key, first)
		}
	}
	if second, _ := cinemas[1].(map[string]any); second["address"] != "Sol 2" {
		t.Fatalf("second cinema = %v, want its address", second)
	}
}

func TestListCinemasWithoutVenuesReturnsEmptyArray(t *testing.T) {
	w := do(t, &fakeService{}, http.MethodGet, "/cinemas", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeBody(t, w)
	cinemas, ok := body["cinemas"].([]any)
	if !ok || len(cinemas) != 0 {
		t.Fatalf("cinemas = %v, want an empty JSON array", body["cinemas"])
	}
}

func TestListRoomsReturnsRoomsOfOneCinema(t *testing.T) {
	svc := &fakeService{rooms: []venue.Room{
		{ID: "room-1", CinemaID: "cinema-1", Name: "Sala 1"},
		{ID: "room-2", CinemaID: "cinema-1", Name: "Sala 2"},
	}}
	w := do(t, svc, http.MethodGet, "/cinemas/cinema-1/rooms", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	if body["cinemaId"] != "cinema-1" {
		t.Fatalf("cinemaId = %v, want the requested cinema", body["cinemaId"])
	}
	rooms, _ := body["rooms"].([]any)
	if len(rooms) != 2 {
		t.Fatalf("rooms = %v, want both rooms", body["rooms"])
	}
	first, _ := rooms[0].(map[string]any)
	for _, key := range []string{"id", "cinemaId", "name"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("room is missing key %q: %v", key, first)
		}
	}
}

func TestListRoomsMissingCinemaReturns404(t *testing.T) {
	svc := &fakeService{createErr: venue.ErrNotFound}
	w := do(t, svc, http.MethodGet, "/cinemas/cinema-9/rooms", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	if body["code"] != "not_found" || body["detail"] != "the requested resource does not exist" {
		t.Fatalf("problem = %v, want the not_found problem", body)
	}
}

func TestListLayoutsReturnsVersionsWithStatus(t *testing.T) {
	svc := &fakeService{layoutVersions: []venue.Layout{
		{ID: "layout-1", RoomID: "room-1", Version: 1, Status: venue.LayoutPublished},
		{ID: "layout-2", RoomID: "room-1", Version: 2, Status: venue.LayoutDraft},
	}}
	w := do(t, svc, http.MethodGet, "/rooms/room-1/layouts", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	if body["roomId"] != "room-1" {
		t.Fatalf("roomId = %v, want the requested room", body["roomId"])
	}
	layouts, _ := body["layouts"].([]any)
	if len(layouts) != 2 {
		t.Fatalf("layouts = %v, want both versions", body["layouts"])
	}
	first, _ := layouts[0].(map[string]any)
	if first["id"] != "layout-1" || first["version"] != float64(1) || first["status"] != "published" {
		t.Fatalf("first layout = %v, want id, version, and status", first)
	}
	if second, _ := layouts[1].(map[string]any); second["status"] != "draft" {
		t.Fatalf("second layout = %v, want the draft status", second)
	}
}

func TestListLayoutsMissingRoomReturns404(t *testing.T) {
	svc := &fakeService{createErr: venue.ErrNotFound}
	w := do(t, svc, http.MethodGet, "/rooms/room-9/layouts", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body)
	}
	if body := decodeBody(t, w); body["code"] != "not_found" {
		t.Fatalf("problem = %v, want not_found", body)
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
