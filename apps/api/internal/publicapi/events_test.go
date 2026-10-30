// Behaviour tests for the public catalogue route: GET /api/v1/events.
//
// They need no database: the test holds a fake catalogue reader and asserts the
// transport contract the web catalogue screen (T4) depends on — the route is
// public, every entry is answered with camelCase keys, the seats available are the
// reader's number and never recomputed, the venue-local hour is served next to the
// raw instant, an empty catalogue is an empty list and not a 404, and a storage
// failure is a retryable problem document and never a partial catalogue.
package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/publicapi"
)

// fakeCatalogService answers one configured catalogue and records what the
// transport asked it.
type fakeCatalogService struct {
	screenings []catalog.Screening
	err        error

	calls int
}

func (f *fakeCatalogService) Events(context.Context) ([]catalog.Screening, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.screenings, nil
}

// mountedCatalogServer wires the public subtree the way cmd/api does: the
// catalogue route next to the hold routes, never behind the administrative guard.
func mountedCatalogServer(t *testing.T, svc publicapi.CatalogService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).WithCatalog(svc).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getEvents(t *testing.T, ts *httptest.Server) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", request.URL, err)
	}
	return response
}

// catalogueStartsAt is the instant the catalogue fixtures start: 18:30 UTC is
// 20:30 in Madrid, so a transport that let the machine's zone through fails.
var catalogueStartsAt = time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC)

// fixtureCatalogue is what the reader answers for the web catalogue: two screenings
// of the same venue, in the order the reader reports them.
func fixtureCatalogue() []catalog.Screening {
	return []catalog.Screening{
		{
			ID:             "00000000-0000-0000-0000-00000000f1a1",
			FilmTitle:      "La película",
			CinemaName:     "Cine Centro",
			RoomName:       "Sala 1",
			StartsAt:       catalogueStartsAt,
			Timezone:       "Europe/Madrid",
			PriceCents:     1250,
			SeatCount:      8,
			AvailableSeats: 3,
		},
		{
			ID:             "00000000-0000-0000-0000-00000000f1a2",
			FilmTitle:      "Otra película",
			CinemaName:     "Cine Centro",
			RoomName:       "Sala 2",
			StartsAt:       catalogueStartsAt.Add(3 * time.Hour),
			Timezone:       "Europe/Madrid",
			PriceCents:     900,
			SeatCount:      4,
			AvailableSeats: 0,
		},
	}
}

// catalogueEntryBody is the wire shape of one catalogue entry the web screen renders.
type catalogueEntryBody struct {
	ID             string `json:"id"`
	FilmTitle      string `json:"filmTitle"`
	CinemaName     string `json:"cinemaName"`
	RoomName       string `json:"roomName"`
	StartsAt       string `json:"startsAt"`
	StartsAtLocal  string `json:"startsAtLocal"`
	PriceCents     int64  `json:"priceCents"`
	PriceDisplay   string `json:"priceDisplay"`
	SeatCount      int    `json:"seatCount"`
	AvailableSeats int    `json:"availableSeats"`
}

type catalogueBody struct {
	Events []catalogueEntryBody `json:"events"`
}

// S5: the catalogue answers the screenings a buyer can buy, each with everything the
// screen shows: who, where, when (twice), the price (twice) and how many seats are
// free right now.
func TestCatalogRouteServesTheCatalogue(t *testing.T) {
	reader := &fakeCatalogService{screenings: fixtureCatalogue()}
	ts := mountedCatalogServer(t, reader)

	response := getEvents(t, ts)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("GET /api/v1/events status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if reader.calls != 1 {
		t.Fatalf("the route asked the reader %d times, want once", reader.calls)
	}

	raw := readBody(t, response)
	var body catalogueBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode catalogue body %s: %v", raw, err)
	}

	want := fixtureCatalogue()
	if len(body.Events) != len(want) {
		t.Fatalf("the catalogue answered %d entries, want %d", len(body.Events), len(want))
	}
	// The order is the reader's, which the SQL fixes as the screening time ascending:
	// the transport reorders nothing.
	for position, screening := range want {
		got := body.Events[position]
		if got.ID != screening.ID || got.FilmTitle != screening.FilmTitle ||
			got.CinemaName != screening.CinemaName || got.RoomName != screening.RoomName {
			t.Fatalf("entry %d = %+v, want the screening %+v", position, got, screening)
		}
		if got.StartsAt != screening.StartsAt.UTC().Format(time.RFC3339) {
			t.Fatalf("entry %d startsAt = %q, want the raw instant", position, got.StartsAt)
		}
		if got.PriceCents != screening.PriceCents {
			t.Fatalf("entry %d priceCents = %d, want %d", position, got.PriceCents, screening.PriceCents)
		}
		// The availability is the reader's number, not one the transport invented from
		// the seat count.
		if got.SeatCount != screening.SeatCount || got.AvailableSeats != screening.AvailableSeats {
			t.Fatalf("entry %d seats = %d/%d, want %d/%d", position,
				got.AvailableSeats, got.SeatCount, screening.AvailableSeats, screening.SeatCount)
		}
	}
	if body.Events[0].StartsAtLocal != "2026-05-04T20:30:00+02:00" {
		t.Fatalf("entry 0 startsAtLocal = %q, want the venue's 20:30 at +02:00", body.Events[0].StartsAtLocal)
	}
	if body.Events[0].PriceDisplay != "12.50" || body.Events[1].PriceDisplay != "9.00" {
		t.Fatalf("price displays = %q / %q, want 12.50 and 9.00",
			body.Events[0].PriceDisplay, body.Events[1].PriceDisplay)
	}

	// The key names are the contract the web catalogue screen reads, so they are
	// pinned as a set: an added, renamed or dropped key is a change it would notice.
	if got := objectKeys(t, raw); !equalKeys(got, []string{"events"}) {
		t.Fatalf("catalogue keys = %v, want only events", got)
	}
	entries := arrayField(t, raw, "events")
	if len(entries) != len(want) {
		t.Fatalf("events array = %s, want %d entries", entries, len(want))
	}
	if got := objectKeys(t, entries[0]); !equalKeys(got, []string{
		"availableSeats", "cinemaName", "filmTitle", "id", "priceCents", "priceDisplay",
		"roomName", "seatCount", "startsAt", "startsAtLocal",
	}) {
		t.Fatalf("catalogue entry keys = %v, want the documented screening fields", got)
	}
}

// S5: the local presentation follows the venue row, not the machine.
func TestCatalogRouteRendersLocalTimeFromTheVenueTimezone(t *testing.T) {
	cases := []struct {
		timezone string
		want     string
	}{
		{"Europe/Madrid", "2026-05-04T20:30:00+02:00"},
		{"Pacific/Auckland", "2026-05-05T06:30:00+12:00"},
		{"UTC", "2026-05-04T18:30:00+00:00"},
	}
	for _, testCase := range cases {
		screening := fixtureCatalogue()[0]
		screening.Timezone = testCase.timezone
		response := getEvents(t, mountedCatalogServer(t, &fakeCatalogService{screenings: []catalog.Screening{screening}}))
		if response.StatusCode != http.StatusOK {
			body := readBody(t, response)
			t.Fatalf("GET /api/v1/events in %s status = %d, want 200 (body %s)", testCase.timezone, response.StatusCode, body)
		}
		var body catalogueBody
		if err := json.Unmarshal(readBody(t, response), &body); err != nil {
			t.Fatalf("decode catalogue body: %v", err)
		}
		if body.Events[0].StartsAtLocal != testCase.want {
			t.Fatalf("startsAtLocal for %s = %q, want %q", testCase.timezone, body.Events[0].StartsAtLocal, testCase.want)
		}
		if body.Events[0].StartsAt != "2026-05-04T18:30:00Z" {
			t.Fatalf("startsAt for %s = %q, want the same raw instant for every venue",
				testCase.timezone, body.Events[0].StartsAt)
		}
	}
}

// S5: the raw instant is answered in UTC whatever zone the reader hands it in, so a
// client that sorts or compares the string compares the same thing every deployment
// does. The venue's offset belongs to startsAtLocal, which carries it.
func TestCatalogRouteAnswersTheScreeningInstantInUTC(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	screening := fixtureCatalogue()[0]
	screening.StartsAt = catalogueStartsAt.In(cest)

	response := getEvents(t, mountedCatalogServer(t, &fakeCatalogService{screenings: []catalog.Screening{screening}}))
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("GET /api/v1/events status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	var body catalogueBody
	if err := json.Unmarshal(readBody(t, response), &body); err != nil {
		t.Fatalf("decode catalogue body: %v", err)
	}
	if body.Events[0].StartsAt != "2026-05-04T18:30:00Z" {
		t.Fatalf("startsAt = %q, want the same instant in UTC", body.Events[0].StartsAt)
	}
	// The local presentation still carries the venue's offset: the two are different
	// questions and neither is derived from the machine's zone.
	if body.Events[0].StartsAtLocal != "2026-05-04T20:30:00+02:00" {
		t.Fatalf("startsAtLocal = %q, want the venue's own hour", body.Events[0].StartsAtLocal)
	}
}

// S5: nobody needs to know an event id to reach the catalogue, so a catalogue with
// nothing to sell is a successful empty list — never a 404 and never an error.
func TestCatalogRouteServesAnEmptyCatalogueAsAnEmptyList(t *testing.T) {
	for _, reader := range []*fakeCatalogService{
		// No screenings at all.
		{},
		// An explicitly empty list.
		{screenings: []catalog.Screening{}},
	} {
		response := getEvents(t, mountedCatalogServer(t, reader))
		if response.StatusCode != http.StatusOK {
			body := readBody(t, response)
			t.Fatalf("GET /api/v1/events with an empty catalogue status = %d, want 200 (body %s)",
				response.StatusCode, body)
		}
		raw := readBody(t, response)
		// "events": [] — an empty list, not null, so the screen can iterate it
		// without a nil check.
		if got := string(objectField(t, raw, "events")); got != "[]" {
			t.Fatalf("empty catalogue answered events: %s, want an empty array", got)
		}
		var body catalogueBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode catalogue body %s: %v", raw, err)
		}
		if body.Events == nil || len(body.Events) != 0 {
			t.Fatalf("empty catalogue decoded to %+v, want an empty list", body.Events)
		}
	}
}

// S5: a read that could not be completed is a retryable 503 — never a catalogue that
// silently drops the screenings it could not read, and never a local hour invented
// from the machine's zone.
func TestCatalogRouteAnswersAStorageFailureWith503(t *testing.T) {
	unresolvable := fixtureCatalogue()
	unresolvable[1].Timezone = "Mars/Olympus"

	cases := []struct {
		name   string
		reader *fakeCatalogService
	}{
		{"a storage failure", &fakeCatalogService{err: fmt.Errorf("%w: conn closed", catalog.ErrUnavailable)}},
		{"an unclassified failure", &fakeCatalogService{err: errors.New("unexpected")}},
		{"a venue timezone that cannot be resolved", &fakeCatalogService{screenings: unresolvable}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := getEvents(t, mountedCatalogServer(t, testCase.reader))
			if response.StatusCode != http.StatusServiceUnavailable {
				body := readBody(t, response)
				t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, body)
			}
			raw := readBody(t, response)
			var problem problemBody
			if err := json.Unmarshal(raw, &problem); err != nil {
				t.Fatalf("decode problem %s: %v", raw, err)
			}
			if problem.Code != "service_unavailable" || problem.Status != http.StatusServiceUnavailable {
				t.Fatalf("problem = %+v, want a 503 service_unavailable document", problem)
			}
			// Nothing of a partial catalogue leaks into a failure.
			for _, leaked := range []string{"events", "filmTitle", "priceCents", "availableSeats"} {
				if strings.Contains(string(raw), leaked) {
					t.Fatalf("the 503 body carries %q: %s", leaked, raw)
				}
			}
		})
	}
}

// Preserve: a deployment without a catalogue reader mounts no catalogue route, so it
// advertises nothing it cannot serve, and the routes next to it keep working.
func TestCatalogRouteIsNotMountedWithoutAReader(t *testing.T) {
	ts := mountedCatalogServer(t, nil)

	response := getEvents(t, ts)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("catalogue route status = %d, want 404 without a catalogue reader", response.StatusCode)
	}

	// Preserve: the seat read and the hold route are still mounted next to the
	// missing catalogue route, and the administrative subtree is still unreachable.
	seats, err := http.Get(ts.URL + "/api/v1/events/00000000-0000-0000-0000-00000000f1a1/seats")
	if err != nil {
		t.Fatalf("GET the seat read: %v", err)
	}
	seats.Body.Close()
	if seats.StatusCode == http.StatusNotFound {
		t.Fatal("the seat availability route disappeared when the catalogue reader was not wired")
	}
	hold := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
	hold.Body.Close()
	if hold.StatusCode == http.StatusNotFound {
		t.Fatal("the hold route disappeared when the catalogue reader was not wired")
	}
	admin, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/admin/cinemas", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build admin request: %v", err)
	}
	adminResponse, err := http.DefaultClient.Do(admin)
	if err != nil {
		t.Fatalf("POST /api/v1/admin/cinemas: %v", err)
	}
	adminResponse.Body.Close()
	if adminResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("admin route status = %d, want 404: the catalogue route is public, the administrative subtree is not",
			adminResponse.StatusCode)
	}
}
