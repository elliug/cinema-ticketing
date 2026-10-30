package venue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/venue"
)

type fakeStore struct {
	createdWithPrice *int64
	layout           venue.Layout
	layouts          map[string]venue.Layout
	layoutList       []venue.Layout
	cinemas          []venue.Cinema
	rooms            []venue.Room
	events           []venue.Event
	films            []venue.Film
	seats            []venue.EventSeat
	calls            []string
	failWith         error
	lastRowsSet      []venue.Row
	layoutCreated    int
	lastEventID      string
	lastUpdate       venue.EventUpdate
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

func (f *fakeStore) CreateEvent(_ context.Context, event venue.Event, priceCents *int64) error {
	f.record("CreateEvent")
	// The pointer is the caller's intent, recorded so a test can tell "named no price" from
	// "named zero": the store writes the column only in the second case, and an M1 schema has
	// no such column.
	f.createdWithPrice = priceCents
	f.events = append(f.events, event)
	return f.failWith
}

func (f *fakeStore) ListEventSeats(_ context.Context, _ string) ([]venue.EventSeat, error) {
	f.record("ListEventSeats")
	return f.seats, f.failWith
}

func (f *fakeStore) ListFilms(_ context.Context) ([]venue.Film, error) {
	f.record("ListFilms")
	return f.films, f.failWith
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

func (f *fakeStore) UpdateEvent(_ context.Context, eventID string, update venue.EventUpdate) (venue.Event, error) {
	f.record("UpdateEvent")
	f.lastEventID = eventID
	f.lastUpdate = update
	if f.failWith != nil {
		return venue.Event{}, f.failWith
	}
	for position := range f.events {
		if f.events[position].ID == eventID {
			if update.PriceCents != nil {
				f.events[position].PriceCents = *update.PriceCents
			}
			if update.Status != nil {
				f.events[position].Status = *update.Status
			}
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

// A venue's timezone is what its own clock is rendered from (the ticket detail reads it
// with time.LoadLocation), so the name has to resolve at creation instead of surfacing as
// an unrenderable ticket much later. A resolvable name is trimmed and stored as given.
func TestCreateCinemaStoresAResolvableTimezone(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	cinema, err := svc.CreateCinema(context.Background(), "  Cine Central  ", " Europe/Madrid ", " Gran Via 1 ")
	if err != nil {
		t.Fatalf("CreateCinema returned error: %v", err)
	}
	if cinema.Timezone != "Europe/Madrid" {
		t.Fatalf("timezone = %q, want the trimmed IANA name", cinema.Timezone)
	}
	if cinema.Name != "Cine Central" || cinema.Address != "Gran Via 1" {
		t.Fatalf("cinema = %+v, want the trimmed name and address", cinema)
	}
	if len(store.calls) != 1 || store.calls[0] != "CreateCinema" {
		t.Fatalf("store calls = %v, want exactly CreateCinema", store.calls)
	}
}

// A timezone that does not resolve is refused as the timezone field and never reaches the
// store: a stored zone nobody can load is a ticket nobody can print, so the write that
// would create it must not happen at all.
func TestCreateCinemaRejectsAnUnusableTimezone(t *testing.T) {
	for _, tc := range []struct {
		timezone string
		message  string
	}{
		{"Madrid", "must be an IANA timezone name such as Europe/Madrid"},
		{"Europe/Madridd", "must be an IANA timezone name such as Europe/Madrid"},
		{"", "must not be empty"},
		{"   ", "must not be empty"},
	} {
		store := &fakeStore{}
		svc := venue.NewService(store)
		_, err := svc.CreateCinema(context.Background(), "Cine Central", tc.timezone, "")
		var invalid *venue.InvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("timezone %q: error = %v, want a validation rejection", tc.timezone, err)
		}
		if invalid.Field != "timezone" || invalid.Message != tc.message {
			t.Fatalf("timezone %q: error = %v, want timezone: %s", tc.timezone, err, tc.message)
		}
		if len(store.calls) != 0 {
			t.Fatalf("timezone %q: store calls = %v, want no write", tc.timezone, store.calls)
		}
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
	_, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Time{}, nil, nil)
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
	_, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Now(), nil, nil)
	if !errors.Is(err, venue.ErrConflict) {
		t.Fatalf("error = %v, want conflict propagation", err)
	}
}

const serviceEventID = "00000000-0000-0000-0000-000000000e01"

// A create that names neither field stores what the administration's first step always
// stored: a draft at no price. The defaults are one product decision, in one place.
func TestCreateEventDefaultsThePriceToZero(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	event, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Now(), nil, nil)
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	if event.PriceCents != 0 {
		t.Fatalf("price = %d, want the schema default zero", event.PriceCents)
	}
	if event.Status != venue.EventDraft {
		t.Fatalf("status = %q, want the default draft", event.Status)
	}
}

// Putting a screening on sale is one call: the price and the status travel with the create,
// so the administration does not schedule one request and publish with a second.
func TestCreateEventStoresThePriceAndStatusItWasGiven(t *testing.T) {
	store := &fakeStore{}
	svc := venue.NewService(store)
	event, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Now(),
		ptrInt64(1400), ptrString(venue.EventOnSale))
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	if event.PriceCents != 1400 || event.Status != venue.EventOnSale {
		t.Fatalf("event = %+v, want on_sale at 1400", event)
	}
	if len(store.calls) != 1 || store.calls[0] != "CreateEvent" {
		t.Fatalf("store calls = %v, want exactly one create", store.calls)
	}
	if len(store.events) != 1 || store.events[0].PriceCents != 1400 || store.events[0].Status != venue.EventOnSale {
		t.Fatalf("stored events = %+v, want the price and the status stored with the screening", store.events)
	}
}

// The update path is the one that decides what a legal price and a legal status are, so the
// create refuses the same values in the same words instead of forming a second opinion.
// The store writes the price column only when the request named a price, because
// price_cents arrives with the checkout schema and an M1 database has no such column: a
// create that never mentions a price has to keep working there. So the service must hand the
// store the caller's intent, not its own default — "named zero" is a free screening, and
// "named nothing" is a column that must not appear in the INSERT at all.
func TestCreateEventTellsTheStoreWhetherAPriceWasNamed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		price    *int64
		wantPoin bool
		wantZero bool
	}{
		{"omitted", nil, false, false},
		{"explicit zero", ptrInt64(0), true, true},
		{"a price", ptrInt64(1400), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			svc := venue.NewService(store)
			if _, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Now().UTC().Add(time.Hour), tc.price, nil); err != nil {
				t.Fatalf("CreateEvent() = %v", err)
			}
			if got := store.createdWithPrice != nil; got != tc.wantPoin {
				t.Fatalf("store received price intent = %v, want pointer present = %v", store.createdWithPrice, tc.wantPoin)
			}
			if tc.wantZero && store.createdWithPrice != nil && *store.createdWithPrice != 0 {
				t.Fatalf("store received %d, want the explicit zero", *store.createdWithPrice)
			}
		})
	}
}

func TestCreateEventRefusesWhatTheUpdateRefuses(t *testing.T) {
	for name, testCase := range map[string]struct {
		price  *int64
		status *string
	}{
		"negative price": {price: ptrInt64(-1)},
		"price above int4": {
			price: ptrInt64(venue.MaxPriceCents + 1),
		},
		"unknown status": {status: ptrString("sold_out")},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{events: []venue.Event{{ID: serviceEventID, Status: venue.EventDraft}}}
			svc := venue.NewService(store)
			_, err := svc.CreateEvent(context.Background(), "room-1", "film-1", "layout-1", time.Now(), testCase.price, testCase.status)
			var invalid *venue.InvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want a rejected field", err)
			}
			// The same value through the update must be refused in exactly these words.
			_, updateErr := svc.UpdateEvent(context.Background(), serviceEventID,
				venue.EventUpdate{PriceCents: testCase.price, Status: testCase.status})
			var updateInvalid *venue.InvalidError
			if !errors.As(updateErr, &updateInvalid) || updateInvalid.Error() != invalid.Error() {
				t.Fatalf("create refused %q but the update refused %v, want one rule", invalid.Error(), updateErr)
			}
			// Neither route reached the store, so a refused request wrote nothing.
			if len(store.calls) != 0 {
				t.Fatalf("store calls = %v, want no persistence attempt", store.calls)
			}
		})
	}
}

// priceUpdate is a request that sends the price and nothing else.
func priceUpdate(cents int64) venue.EventUpdate { return venue.EventUpdate{PriceCents: &cents} }

// statusUpdate is a request that sends the status and nothing else.
func statusUpdate(status string) venue.EventUpdate { return venue.EventUpdate{Status: &status} }

func TestUpdateEventAcceptsZeroWithoutRejectingItAsMissing(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, RoomID: "room-1", Status: venue.EventDraft, PriceCents: 1250}}}
	svc := venue.NewService(store)
	event, err := svc.UpdateEvent(context.Background(), serviceEventID, priceUpdate(0))
	if err != nil {
		t.Fatalf("UpdateEvent returned error: %v", err)
	}
	if event.PriceCents != 0 || store.lastUpdate.PriceCents == nil || *store.lastUpdate.PriceCents != 0 {
		t.Fatalf("price = %d (store saw %v), want the explicit zero", event.PriceCents, store.lastUpdate.PriceCents)
	}
	// A price-only request says nothing about the status, so it must not carry one.
	if store.lastUpdate.Status != nil || event.Status != venue.EventDraft {
		t.Fatalf("status = %v (store saw %v), want a price-only request to leave the status alone", event.Status, store.lastUpdate.Status)
	}
	if len(store.calls) != 1 || store.calls[0] != "UpdateEvent" {
		t.Fatalf("calls = %v, want exactly UpdateEvent", store.calls)
	}
}

// A status-only request is what puts a draft on sale, and it must not touch the price it
// was not asked about.
func TestUpdateEventPublishesADraftAndLeavesThePriceAlone(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, Status: venue.EventDraft, PriceCents: 1250}}}
	svc := venue.NewService(store)
	event, err := svc.UpdateEvent(context.Background(), serviceEventID, statusUpdate(venue.EventOnSale))
	if err != nil {
		t.Fatalf("UpdateEvent returned error: %v", err)
	}
	if event.Status != venue.EventOnSale || event.PriceCents != 1250 {
		t.Fatalf("event = %+v, want the status changed to on_sale and the price kept at 1250", event)
	}
	if store.lastUpdate.PriceCents != nil {
		t.Fatalf("price = %v, want a status-only request to carry no price", store.lastUpdate.PriceCents)
	}
}

func TestUpdateEventWritesBothFieldsInOneCall(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, Status: venue.EventDraft, PriceCents: 0}}}
	svc := venue.NewService(store)
	event, err := svc.UpdateEvent(context.Background(), serviceEventID, venue.EventUpdate{
		PriceCents: ptrInt64(1400), Status: ptrString(venue.EventOnSale),
	})
	if err != nil {
		t.Fatalf("UpdateEvent returned error: %v", err)
	}
	if event.Status != venue.EventOnSale || event.PriceCents != 1400 {
		t.Fatalf("event = %+v, want both fields changed by one call", event)
	}
	if len(store.calls) != 1 || store.calls[0] != "UpdateEvent" {
		t.Fatalf("calls = %v, want exactly one write", store.calls)
	}
}

func TestUpdateEventAcceptsEveryStatusTheColumnAllows(t *testing.T) {
	for _, status := range venue.EventStatuses {
		t.Run(status, func(t *testing.T) {
			store := &fakeStore{events: []venue.Event{{ID: serviceEventID, Status: venue.EventDraft}}}
			svc := venue.NewService(store)
			if _, err := svc.UpdateEvent(context.Background(), serviceEventID, statusUpdate(status)); err != nil {
				t.Fatalf("UpdateEvent(%q) = %v, want the value cinema.events.status allows to be accepted", status, err)
			}
		})
	}
}

func TestUpdateEventRejectsNothingToChangeWithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, Status: venue.EventDraft}}}
	svc := venue.NewService(store)
	_, err := svc.UpdateEvent(context.Background(), serviceEventID, venue.EventUpdate{})
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Error() != "nothing to change: provide priceCents or status" {
		t.Fatalf("error = %v, want the nothing-to-change rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func TestUpdateEventRejectsUnknownStatusWithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, Status: venue.EventDraft}}}
	svc := venue.NewService(store)
	_, err := svc.UpdateEvent(context.Background(), serviceEventID, statusUpdate("sold_out"))
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "status" ||
		invalid.Message != "must be one of draft, published, on_sale, started, completed, cancelled" {
		t.Fatalf("error = %v, want the status rejection naming the column's values", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func TestUpdateEventRejectsNegativeWithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID, PriceCents: 1250}}}
	svc := venue.NewService(store)
	_, err := svc.UpdateEvent(context.Background(), serviceEventID, priceUpdate(-1))
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "priceCents" || invalid.Message != "must be zero or greater" {
		t.Fatalf("error = %v, want the priceCents range rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func TestUpdateEventRejectsAboveInt4WithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID}}}
	svc := venue.NewService(store)
	_, err := svc.UpdateEvent(context.Background(), serviceEventID, priceUpdate(venue.MaxPriceCents+1))
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "priceCents" || invalid.Message != "must be at most 2147483647" {
		t.Fatalf("error = %v, want the int4 range rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func TestUpdateEventRejectsMalformedEventIDWithoutPersisting(t *testing.T) {
	store := &fakeStore{events: []venue.Event{{ID: serviceEventID}}}
	svc := venue.NewService(store)
	_, err := svc.UpdateEvent(context.Background(), "event-1", priceUpdate(1250))
	var invalid *venue.InvalidError
	if !errors.As(err, &invalid) || invalid.Field != "eventId" || invalid.Message != "must be a valid identifier" {
		t.Fatalf("error = %v, want the eventId rejection", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls = %v, want no persistence attempt", store.calls)
	}
}

func ptrInt64(value int64) *int64 { return &value }

func ptrString(value string) *string { return &value }

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

// The administration schedules a screening against a film it picked from this list, so
// the read is a pass-through of everything stored, in the store's own order: the service
// has nothing to validate in a read that takes no argument.
func TestListFilmsReturnsEveryStoredFilm(t *testing.T) {
	store := &fakeStore{films: []venue.Film{
		{ID: "film-1", Title: "Aardvark", RuntimeMinutes: 88, Rating: "PG"},
		{ID: "film-2", Title: "Zulu", RuntimeMinutes: 121, Rating: "18"},
	}}
	svc := venue.NewService(store)
	films, err := svc.ListFilms(context.Background())
	if err != nil {
		t.Fatalf("ListFilms returned error: %v", err)
	}
	if len(films) != 2 || films[0].Title != "Aardvark" || films[1].Title != "Zulu" {
		t.Fatalf("films = %+v, want both stored films in the order the store answered", films)
	}
	if films[0].RuntimeMinutes != 88 || films[0].Rating != "PG" || films[0].ID != "film-1" {
		t.Fatalf("film = %+v, want the stored columns unchanged", films[0])
	}
	if len(store.calls) != 1 || store.calls[0] != "ListFilms" {
		t.Fatalf("store calls = %v, want exactly ListFilms", store.calls)
	}
}

func TestListFilmsPropagatesReadFailure(t *testing.T) {
	svc := venue.NewService(&fakeStore{failWith: errors.New("database unreachable")})
	if _, err := svc.ListFilms(context.Background()); err == nil {
		t.Fatal("ListFilms returned no error for a failed read")
	}
}

// The names of what a screening is scheduled against travel with it through the
// service, so the administrative read answers what the store projected instead of an
// entry the service filled in.
func TestListEventsAndGetEventKeepTheNamesTheStoreProjected(t *testing.T) {
	event := venue.Event{ID: serviceEventID, FilmTitle: "The Quiet Cartographer", RoomName: "Sala 2", CinemaName: "Cine Central"}
	store := &fakeStore{events: []venue.Event{event}}
	svc := venue.NewService(store)
	events, err := svc.ListEvents(context.Background())
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) != 1 || events[0].FilmTitle != "The Quiet Cartographer" || events[0].RoomName != "Sala 2" || events[0].CinemaName != "Cine Central" {
		t.Fatalf("events = %+v, want the store's names kept", events)
	}
	single, err := svc.GetEvent(context.Background(), serviceEventID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if single.FilmTitle != "The Quiet Cartographer" || single.RoomName != "Sala 2" || single.CinemaName != "Cine Central" {
		t.Fatalf("event = %+v, want the store's names kept", single)
	}
}

// The administrative read carries the venue's own hour beside the stored instant, and it is
// the ticket detail's own rule: the stored instant and the venue zone through the same
// time.LoadLocation path the ticket page and the PDF already use.
func TestEventLocalStartsAtRendersTheVenueHour(t *testing.T) {
	for name, testCase := range map[string]struct {
		startsAt time.Time
		timezone string
		want     string
	}{
		"Madrid in winter": {
			startsAt: time.Date(2030, time.January, 2, 18, 0, 0, 0, time.UTC),
			timezone: "Europe/Madrid",
			want:     "2030-01-02T19:00:00+01:00",
		},
		"Madrid in summer": {
			startsAt: time.Date(2030, time.July, 2, 18, 0, 0, 0, time.UTC),
			timezone: "Europe/Madrid",
			want:     "2030-07-02T20:00:00+02:00",
		},
		"a venue on UTC keeps its offset spelled out": {
			startsAt: time.Date(2030, time.January, 2, 18, 0, 0, 0, time.UTC),
			timezone: "UTC",
			want:     "2030-01-02T18:00:00+00:00",
		},
	} {
		t.Run(name, func(t *testing.T) {
			event := venue.Event{ID: serviceEventID, StartsAt: testCase.startsAt, CinemaTimezone: testCase.timezone}
			got, err := event.LocalStartsAt()
			if err != nil {
				t.Fatalf("LocalStartsAt() = unexpected error %v", err)
			}
			if got != testCase.want {
				t.Fatalf("LocalStartsAt() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A stored zone nobody can resolve is a row the backfill has not repaired yet. The hour is
// refused rather than guessed at in UTC: a screen that shows a wrong hour is worse than one
// that shows none.
func TestEventLocalStartsAtRefusesAnUnusableVenueTimezone(t *testing.T) {
	for _, timezone := range []string{"America/Madrid", ""} {
		event := venue.Event{ID: serviceEventID, StartsAt: time.Date(2030, time.January, 2, 18, 0, 0, 0, time.UTC), CinemaTimezone: timezone}
		got, err := event.LocalStartsAt()
		if err == nil {
			t.Fatalf("LocalStartsAt() with timezone %q = %q, want a refusal", timezone, got)
		}
		if got != "" {
			t.Fatalf("LocalStartsAt() with timezone %q answered %q next to its error", timezone, got)
		}
	}
}
