package venue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/venue"
)

type fakeStore struct {
	layout         venue.Layout
	layouts        map[string]venue.Layout
	layoutList     []venue.Layout
	cinemas        []venue.Cinema
	rooms          []venue.Room
	events         []venue.Event
	seats          []venue.EventSeat
	calls          []string
	failWith       error
	lastRowsSet    []venue.Row
	layoutCreated  int
	lastEventID    string
	lastPriceCents int64
}

func (f *fakeStore) record(name string) {
	f.calls = append(f.calls, name)
}

func (f *fakeStore) CreateCinema(_ context.Context, _ venue.Cinema) error {
	f.record("CreateCinema")
	return f.failWith
}

func (f *fakeStore) CreateRoom(_ context.Context, _ venue.Room) error {
	f.record("CreateRoom")
	return f.failWith
}

func (f *fakeStore) CreateLayout(_ context.Context, layoutID, roomID string) (venue.Layout, error) {
	f.record("CreateLayout")
	f.layoutCreated++
	return venue.Layout{ID: layoutID, RoomID: roomID, Version: f.layoutCreated, Status: venue.LayoutDraft, Rows: []venue.Row{}}, f.failWith
}

func (f *fakeStore) ReplaceRows(_ context.Context, layoutID string, rows []venue.Row) (venue.Layout, error) {
	f.record("ReplaceRows")
	f.lastRowsSet = rows
	return venue.Layout{ID: layoutID, Version: 1, Status: venue.LayoutDraft, Rows: rows}, f.failWith
}

func (f *fakeStore) PublishLayout(_ context.Context, layoutID string) (venue.Layout, error) {
	f.record("PublishLayout")
	return venue.Layout{ID: layoutID, Version: 1, Status: venue.LayoutPublished, Rows: f.layout.Rows}, f.failWith
}

func (f *fakeStore) CloneLayout(_ context.Context, _, targetID string) (venue.Layout, error) {
	f.record("CloneLayout")
	return venue.Layout{ID: targetID, Version: 2, Status: venue.LayoutDraft, Rows: f.layout.Rows}, f.failWith
}

func (f *fakeStore) GetLayout(_ context.Context, layoutID string) (venue.Layout, error) {
	f.record("GetLayout")
	if f.failWith != nil {
		return venue.Layout{}, f.failWith
	}
	if stored, ok := f.layouts[layoutID]; ok {
		return stored, nil
	}
	if f.layout.ID == "" {
		return venue.Layout{}, venue.ErrNotFound
	}
	return f.layout, nil
}

func (f *fakeStore) CreateFilm(_ context.Context, _ venue.Film) error {
	f.record("CreateFilm")
	return f.failWith
}

func (f *fakeStore) CreateEvent(_ context.Context, event venue.Event) error {
	f.record("CreateEvent")
	f.events = append(f.events, event)
	return f.failWith
}

func (f *fakeStore) ListEventSeats(_ context.Context, _ string) ([]venue.EventSeat, error) {
	f.record("ListEventSeats")
	return f.seats, f.failWith
}

func (f *fakeStore) ListEvents(_ context.Context) ([]venue.Event, error) {
	f.record("ListEvents")
	return f.events, f.failWith
}

func (f *fakeStore) GetEvent(_ context.Context, eventID string) (venue.Event, error) {
	f.record("GetEvent")
	f.lastEventID = eventID
	if f.failWith != nil {
		return venue.Event{}, f.failWith
	}
	for _, event := range f.events {
		if event.ID == eventID {
			return event, nil
		}
	}
	return venue.Event{}, venue.ErrNotFound
}

func (f *fakeStore) SetEventPrice(_ context.Context, eventID string, priceCents int64) (venue.Event, error) {
	f.record("SetEventPrice")
	f.lastEventID = eventID
	f.lastPriceCents = priceCents
	if f.failWith != nil {
		return venue.Event{}, f.failWith
	}
	for position := range f.events {
		if f.events[position].ID == eventID {
			f.events[position].PriceCents = priceCents
			return f.events[position], nil
		}
	}
	return venue.Event{}, venue.ErrNotFound
}

func (f *fakeStore) ListCinemas(_ context.Context) ([]venue.Cinema, error) {
	f.record("ListCinemas")
	return f.cinemas, f.failWith
}

func (f *fakeStore) ListRooms(_ context.Context, _ string) ([]venue.Room, error) {
	f.record("ListRooms")
	return f.rooms, f.failWith
}

func (f *fakeStore) ListLayouts(_ context.Context, _ string) ([]venue.Layout, error) {
	f.record("ListLayouts")
	return f.layoutList, f.failWith
}

func TestSetRowsRejectsDuplicateSeatNumberWithoutPersisting(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	_, err := svc.SetRows(context.Background(), "layout-1", []venue.Row{{
		Label: "A", Position: 1,
		Seats: []venue.Seat{
			{Number: "1", Position: 1, Kind: venue.SeatStandard, Enabled: true},
			{Number: "1", Position: 2, Kind: venue.SeatStandard, Enabled: true},
		},
	}})
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Message != "must be unique within its row" {
		t.Fatalf("error = %v, want duplicate seat number rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("store calls = %v, want no persistence attempt", store.calls)
	}
}

func TestSetRowsAcceptsRowsWithDifferentSeatCounts(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	rows, err := svc.SetRows(context.Background(), "layout-1", []venue.Row{
		{Label: "A", Position: 1, Seats: []venue.Seat{
			{Number: "1", Position: 1, Kind: venue.SeatStandard, Enabled: true},
			{Number: "2", Position: 2, Kind: venue.SeatAccessible, Enabled: true},
		}},
		{Label: "B", Position: 2, Seats: []venue.Seat{
			{Number: "1", Position: 1, Kind: venue.SeatCompanion, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatalf("SetRows returned error: %v", err)
	}
	if len(rows.Rows) != 2 || len(rows.Rows[0].Seats) != 2 || len(rows.Rows[1].Seats) != 1 {
		t.Fatalf("rows = %+v, want two rows with two and one seat", rows.Rows)
	}
	if len(store.calls) != 1 || store.calls[0] != "ReplaceRows" {
		t.Fatalf("store calls = %v, want exactly ReplaceRows", store.calls)
	}
}

func TestSetRowsRejectsUnknownSeatKind(t *testing.T) {
	svc := venue.NewService(&fakeStore{})
	_, err := svc.SetRows(context.Background(), "layout-1", []venue.Row{{
		Label: "A", Position: 1,
		Seats: []venue.Seat{{Number: "1", Position: 1, Kind: "vip", Enabled: true}},
	}})
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Message != "must be standard, accessible, or companion" {
		t.Fatalf("error = %v, want seat kind rejection", err)
	}
}

func TestPublishLayoutRequiresEnabledSeat(t *testing.T) {
	store := &fakeStore{layout: venue.Layout{
		ID: "layout-1", Version: 1, Status: venue.LayoutDraft,
		Rows: []venue.Row{{Label: "A", Position: 1, Seats: []venue.Seat{{Number: "1", Position: 1, Kind: venue.SeatStandard, Enabled: false}}}},
	}}
	svc := venue.NewService(store)
	_, err := svc.PublishLayout(context.Background(), "layout-1")
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Message != "must contain at least one enabled seat before publishing" {
		t.Fatalf("error = %v, want enabled-seat rejection", err)
	}
	for _, call := range store.calls {
		if call == "PublishLayout" {
			t.Fatal("published a layout without an enabled seat")
		}
	}
}

func TestPublishLayoutRejectsAlreadyPublished(t *testing.T) {
	store := &fakeStore{layout: venue.Layout{
		ID: "layout-1", Version: 1, Status: venue.LayoutPublished,
		Rows: []venue.Row{{Label: "A", Position: 1, Seats: []venue.Seat{{Number: "1", Position: 1, Kind: venue.SeatStandard, Enabled: true}}}},
	}}
	svc := venue.NewService(store)
	if _, err := svc.PublishLayout(context.Background(), "layout-1"); !errors.Is(err, venue.ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestCloneLayoutRejectsDraftSource(t *testing.T) {
	store := &fakeStore{layout: venue.Layout{ID: "layout-1", Version: 1, Status: venue.LayoutDraft}}
	svc := venue.NewService(store)
	_, err := svc.CloneLayout(context.Background(), "layout-1")
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Message != "only a published layout can be versioned" {
		t.Fatalf("error = %v, want draft-source rejection", err)
	}
}

func TestCreateFilmRejectsInvalidRuntime(t *testing.T) {
	for _, runtime := range []int{0, -5, 601} {
		svc := venue.NewService(&fakeStore{})
		_, err := svc.CreateFilm(context.Background(), "Film", runtime, "PG")
		var invalid *venue.InvalidError
		if !errors.As(err, &invalid) || invalid.Message != "must be between 1 and 600" {
			t.Fatalf("runtime %d: error = %v, want runtime rejection", runtime, err)
		}
	}
}

func TestCreateEventRejectsZeroStart(t *testing.T) {
	svc := venue.NewService(&fakeStore{})
	_, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Time{})
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "startsAt" {
		t.Fatalf("error = %v, want startsAt rejection", err)
	}
}

func TestListRoomsRejectsEmptyCinemaID(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	_, err := svc.ListRooms(context.Background(), "   ")
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "cinemaId" {
		t.Fatalf("error = %v, want cinemaId rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("store calls = %v, want no read attempt", store.calls)
	}
}

func TestListLayoutsRejectsEmptyRoomID(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	_, err := svc.ListLayouts(context.Background(), "")
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "roomId" {
		t.Fatalf("error = %v, want roomId rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("store calls = %v, want no read attempt", store.calls)
	}
}

func TestListCinemasReturnsEveryStoredVenue(t *testing.T) {
	store := &fakeStore{cinemas: []venue.Cinema{
		{ID: "cinema-1", Name: "Central", Timezone: "Europe/Madrid"},
		{ID: "cinema-2", Name: "Norte", Timezone: "Europe/Madrid", Address: "Sol 2"},
	}}
	svc := venue.NewService(store)
	cinemas, err := svc.ListCinemas(context.Background())
	if err != nil {
		t.Fatalf("ListCinemas returned error: %v", err)
	}
	if len(cinemas) != 2 || cinemas[1].Address != "Sol 2" {
		t.Fatalf("cinemas = %+v, want both stored venues", cinemas)
	}
	if len(store.calls) != 1 || store.calls[0] != "ListCinemas" {
		t.Fatalf("store calls = %v, want exactly ListCinemas", store.calls)
	}
}

func TestListRoomsPropagatesMissingCinema(t *testing.T) {
	svc := venue.NewService(&fakeStore{failWith: venue.ErrNotFound})
	if _, err := svc.ListRooms(context.Background(), "cinema-9"); !errors.Is(err, venue.ErrNotFound) {
		t.Fatalf("error = %v, want not found propagation", err)
	}
}

func TestListLayoutsReturnsVersionsWithStatus(t *testing.T) {
	store := &fakeStore{layoutList: []venue.Layout{
		{ID: "layout-1", RoomID: "room-1", Version: 1, Status: venue.LayoutPublished},
		{ID: "layout-2", RoomID: "room-1", Version: 2, Status: venue.LayoutDraft},
	}}
	svc := venue.NewService(store)
	layouts, err := svc.ListLayouts(context.Background(), "room-1")
	if err != nil {
		t.Fatalf("ListLayouts returned error: %v", err)
	}
	if len(layouts) != 2 || layouts[0].Version != 1 || layouts[1].Status != venue.LayoutDraft {
		t.Fatalf("layouts = %+v, want both versions in version order", layouts)
	}
}

func TestCreateEventPropagatesMaterializationConflict(t *testing.T) {
	store := &fakeStore{failWith: errors.Join(venue.ErrConflict, errors.New("event requires a published layout for its room"))}
	svc := venue.NewService(store)
	_, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Now())
	if !errors.Is(err, venue.ErrConflict) {
		t.Fatalf("error = %v, want conflict propagation", err)
	}
}

const serviceEventID = "00000000-0000-0000-0000-000000000e01"

func TestCreateEventDefaultsThePriceToZero(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	event, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Now())
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	if event.PriceCents != 0 {
		t.Fatalf("price = %d, want the schema default zero", event.PriceCents)
	}
}

func TestSetEventPriceAcceptsZeroWithoutRejectingItAsMissing(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, RoomID: "room-1", PriceCents: 1250}}}
	svc := venue.NewService(store)
	event, err := svc.SetEventPrice(context.Background(), serviceEventID, 0)
	if err != nil {
		t.Fatalf("SetEventPrice returned error: %v", err)
	}
	if event.PriceCents != 0 || store.lastPriceCents != 0 {
		t.Fatalf("price = %d (store saw %d), want the explicit zero", event.PriceCents, store.lastPriceCents)
	}
	if len(store.calls) != 1 || store.calls[0] != "SetEventPrice" {
		t.Fatalf("calls = %v, want exactly SetEventPrice", store.calls)
	}
}

func TestSetEventPriceRejectsNegativeWithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, PriceCents: 1250}}}
	svc := venue.NewService(store)
	_, err := svc.SetEventPrice(context.Background(), serviceEventID, -1)
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "priceCents" || invalid.Message != "must be zero or greater" {
		t.Fatalf("error = %v, want the priceCents range rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func TestSetEventPriceRejectsAboveInt4WithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID}}}
	svc := venue.NewService(store)
	_, err := svc.SetEventPrice(context.Background(), serviceEventID, venue.MaxPriceCents+1)
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "priceCents" || invalid.Message != "must be at most 2147483647" {
		t.Fatalf("error = %v, want the int4 range rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func TestSetEventPriceRejectsMalformedEventIDWithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID}}}
	svc := venue.NewService(store)
	_, err := svc.SetEventPrice(context.Background(), "event-1", 1250)
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "eventId" || invalid.Message != "must be a valid identifier" {
		t.Fatalf("error = %v, want the eventId rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func TestGetEventRejectsMalformedEventIDWithoutReading(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID}}}
	svc := venue.NewService(store)
	_, err := svc.GetEvent(context.Background(), "event-1")
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "eventId" {
		t.Fatalf("error = %v, want the eventId rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no read attempt", store.calls)
	}
}

func TestListEventsPropagatesReadFailure(t *testing.T) {
	svc := venue.NewService(&fakeStore{failWith: errors.New("database unreachable")})
	if _, err := svc.ListEvents(context.Background()); err == nil {
		t.Fatal("ListEvents returned no error for a failed read")
	}
}
