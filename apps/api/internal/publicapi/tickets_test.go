// Behaviour tests for the public ticket detail route: GET
// /api/v1/tickets/{ticketId}.
//
// They need no database: the test holds a fake ticket reader and asserts the
// transport contract the web confirmation page and the PDF download depend on —
// the route is public, the projection is answered with camelCase keys, the local
// screening time is the venue's and not the machine's, an unknown ticket is a
// problem document, and a storage failure is never an empty ticket.
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
	"sort"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// fakeTicketReader is the projection reader the real ticket service is wrapped
// around, so the route tests exercise the wiring a deployment builds: the store, the
// signer and the service that attaches the code. It answers one configured detail and
// records what the transport forwarded.
type fakeTicketReader struct {
	detail tickets.TicketDetail
	err    error

	calls    int
	ticketID string
}

func (f *fakeTicketReader) Detail(_ context.Context, ticketID string) (tickets.TicketDetail, error) {
	f.calls++
	f.ticketID = ticketID
	if f.err != nil {
		return tickets.TicketDetail{}, f.err
	}
	return f.detail, nil
}

// ticketRouteQRKey is the throwaway signing key the route tests mount the service
// with. It is longer than the documented minimum and distinct from the production
// development default on purpose.
const ticketRouteQRKey = "ticket-route-test-qr-key-0123456789ab"

// signedTicketService builds the service a deployment mounts: the projection reader
// wrapped in a signer, so every served detail carries its own signed code.
func signedTicketService(t *testing.T, reader *fakeTicketReader) publicapi.TicketService {
	t.Helper()
	signer, err := tickets.NewSigner(ticketRouteQRKey)
	if err != nil {
		t.Fatalf("NewSigner() = unexpected error %v", err)
	}
	return tickets.NewService(reader, signer)
}

// mountedTicketServer wires the public subtree exactly as cmd/api does: the ticket
// route is mounted at /api/v1 next to the hold routes, never behind the
// administrative guard.
func mountedTicketServer(t *testing.T, ticketService publicapi.TicketService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).WithTickets(ticketService).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getTicket(t *testing.T, ts *httptest.Server, ticketID string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/tickets/"+ticketID, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", request.URL, err)
	}
	return response
}

// ticketScreening is the instant every fixture renders: 18:30 UTC is 20:30 in
// Madrid and 06:30 the next day in Auckland, so a rendering that used the
// machine's zone cannot pass by accident.
var ticketScreening = time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC)

// fixtureTicket is the projection a real checkout would produce for one ticket of
// a two-seat order in a Madrid venue. One ticket is one seat, so the seats array
// carries this ticket's own seat; the money is the charge for the whole order.
func fixtureTicket() tickets.TicketDetail {
	return tickets.TicketDetail{
		TicketID:      "00000000-0000-0000-0000-00000000f0a1",
		Status:        tickets.TicketStatusIssued,
		IssuedAt:      ticketScreening.Add(-48 * time.Hour),
		ReservationID: "00000000-0000-0000-0000-00000000f0a2",
		Event: tickets.EventDetail{
			ID:         "00000000-0000-0000-0000-00000000f0a3",
			FilmTitle:  "La película",
			StartsAt:   ticketScreening,
			RoomName:   "Sala 1",
			CinemaName: "Cine Centro",
			Timezone:   "Europe/Madrid",
			Status:     "on_sale",
		},
		Seats: []tickets.Seat{
			{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"},
		},
		// 19,98 EUR is what this order cost: two seats at 9,99 EUR.
		OrderAmountCents: 1998,
	}
}

// The wire shape of the projection. Decoding it proves the key names the web
// client and the PDF download rely on.
type ticketSeatBody struct {
	EventSeatID int64  `json:"eventSeatId"`
	RowLabel    string `json:"rowLabel"`
	SeatNumber  string `json:"seatNumber"`
}

type ticketEventBody struct {
	ID            string `json:"id"`
	FilmTitle     string `json:"filmTitle"`
	StartsAt      string `json:"startsAt"`
	StartsAtLocal string `json:"startsAtLocal"`
	RoomName      string `json:"roomName"`
	CinemaName    string `json:"cinemaName"`
	Timezone      string `json:"timezone"`
	Status        string `json:"status"`
}

type ticketBody struct {
	TicketID      string           `json:"ticketId"`
	Status        string           `json:"status"`
	Valid         bool             `json:"valid"`
	IssuedAt      string           `json:"issuedAt"`
	ReservationID string           `json:"reservationId"`
	Event         ticketEventBody  `json:"event"`
	Seats         []ticketSeatBody `json:"seats"`
	// The money taken for the whole reservation, deliberately named after the
	// order: a per-ticket document that called it the ticket's price would make a
	// two-seat order look like a very expensive single seat.
	OrderAmountCents   int64  `json:"orderAmountCents"`
	OrderAmountDisplay string `json:"orderAmountDisplay"`
	// QRToken is the opaque signed code of this ticket, the same one the PDF prints.
	QRToken string `json:"qrToken"`
}

// equalKeys reports whether two key sets are identical.
func equalKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// objectField returns the raw JSON of one object field of a JSON object.
func objectField(t *testing.T, raw []byte, key string) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode object %s: %v", raw, err)
	}
	value, ok := object[key]
	if !ok {
		t.Fatalf("object %s has no %q field", raw, key)
	}
	return value
}

// arrayField returns the raw JSON entries of one array field of a JSON object.
func arrayField(t *testing.T, raw []byte, key string) [][]byte {
	t.Helper()
	var entries []json.RawMessage
	if err := json.Unmarshal(objectField(t, raw, key), &entries); err != nil {
		t.Fatalf("decode %q array: %v", key, err)
	}
	values := make([][]byte, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry)
	}
	return values
}

// readBody returns the raw response body, so one test can decode it twice: once
// into the typed shape, once into a key set.
func readBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return raw
}

// objectKeys returns the sorted keys of one JSON object in the body.
func objectKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode object %s: %v", raw, err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// T4 (S4): the ticket route answers the one shared projection as JSON, with the
// camelCase keys the rest of the public API uses. The seat list, the amount and
// both time forms are all served to the confirmation page, which renders exactly
// what the PDF will render.
func TestTicketRouteServesTheDetailProjection(t *testing.T) {
	reader := &fakeTicketReader{detail: fixtureTicket()}
	ts := mountedTicketServer(t, signedTicketService(t, reader))

	response := getTicket(t, ts, fixtureTicket().TicketID)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("GET ticket status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if reader.calls != 1 || reader.ticketID != fixtureTicket().TicketID {
		t.Fatalf("forwarded ticket %q in %d calls, want %q once", reader.ticketID, reader.calls, fixtureTicket().TicketID)
	}

	raw := readBody(t, response)
	var body ticketBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode ticket body %s: %v", raw, err)
	}

	if body.TicketID != fixtureTicket().TicketID || body.ReservationID != fixtureTicket().ReservationID {
		t.Fatalf("ticket %q reservation %q, want %q / %q",
			body.TicketID, body.ReservationID, fixtureTicket().TicketID, fixtureTicket().ReservationID)
	}
	if body.Status != tickets.TicketStatusIssued || !body.Valid {
		t.Fatalf("status %q valid %t, want an issued, valid ticket", body.Status, body.Valid)
	}
	if !strings.HasPrefix(body.IssuedAt, "2026-05-02T18:30:00") {
		t.Fatalf("issuedAt = %q, want the stored issue instant", body.IssuedAt)
	}
	if body.OrderAmountCents != 1998 || body.OrderAmountDisplay != "19.98" {
		t.Fatalf("order amount = %d %q, want 1998 and 19.98", body.OrderAmountCents, body.OrderAmountDisplay)
	}

	event := body.Event
	if event.ID != fixtureTicket().Event.ID || event.FilmTitle != "La película" ||
		event.RoomName != "Sala 1" || event.CinemaName != "Cine Centro" ||
		event.Timezone != "Europe/Madrid" || event.Status != "on_sale" {
		t.Fatalf("event = %+v, want the screening of the fixture", event)
	}
	if event.StartsAt != "2026-05-04T18:30:00Z" {
		t.Fatalf("startsAt = %q, want the raw UTC instant", event.StartsAt)
	}
	if event.StartsAtLocal != "2026-05-04T20:30:00+02:00" {
		t.Fatalf("startsAtLocal = %q, want the venue's local time with its offset", event.StartsAtLocal)
	}

	// One ticket is one seat: the document the PDF prints for this ticket names the
	// seat it admits, never the other seat of the order.
	if len(body.Seats) != 1 {
		t.Fatalf("seats = %+v, want exactly this ticket's own seat", body.Seats)
	}
	if want := (ticketSeatBody{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"}); body.Seats[0] != want {
		t.Fatalf("seat = %+v, want %+v", body.Seats[0], want)
	}

	// The code the page and the PDF print is the served detail's own: opaque, and it
	// verifies to this ticket, its event and its seat.
	if body.QRToken == "" {
		t.Fatal("the served detail carries no qrToken")
	}
	if strings.Contains(body.QRToken, body.TicketID) || strings.Contains(body.QRToken, body.Event.ID) {
		t.Fatalf("the qrToken carries an identifier in the clear: %q", body.QRToken)
	}
	signer, err := tickets.NewSigner(ticketRouteQRKey)
	if err != nil {
		t.Fatalf("NewSigner() = unexpected error %v", err)
	}
	claims, err := signer.Verify(body.QRToken)
	if err != nil {
		t.Fatalf("Verify(served qrToken) = unexpected error %v", err)
	}
	if claims.TicketID != body.TicketID || claims.EventID != body.Event.ID {
		t.Fatalf("claims = %+v, want ticket %q event %q", claims, body.TicketID, body.Event.ID)
	}
	if len(claims.SeatIDs) != 1 || claims.SeatIDs[0] != body.Seats[0].EventSeatID {
		t.Fatalf("claim seats = %v, want the served seat %d", claims.SeatIDs, body.Seats[0].EventSeatID)
	}

	// The key names are part of the contract, so they are pinned as a set: an
	// added, renamed or dropped key is a change the web page and the PDF would
	// notice.
	if got := objectKeys(t, raw); !equalKeys(got, []string{
		"event", "issuedAt", "orderAmountCents", "orderAmountDisplay", "qrToken", "reservationId",
		"seats", "status", "ticketId", "valid",
	}) {
		t.Fatalf("ticket keys = %v, want the documented ticket fields", got)
	}
	eventRaw := objectField(t, raw, "event")
	if got := objectKeys(t, eventRaw); !equalKeys(got, []string{
		"cinemaName", "filmTitle", "id", "roomName", "startsAt", "startsAtLocal", "status", "timezone",
	}) {
		t.Fatalf("event keys = %v, want the documented screening fields", got)
	}
	seatsRaw := arrayField(t, raw, "seats")
	if len(seatsRaw) != 1 {
		t.Fatalf("seats array = %s, want the ticket's one seat", seatsRaw)
	}
	if got := objectKeys(t, seatsRaw[0]); !equalKeys(got, []string{"eventSeatId", "rowLabel", "seatNumber"}) {
		t.Fatalf("seat keys = %v, want eventSeatId, rowLabel and seatNumber", got)
	}
}

// T4 (S4): the local presentation follows the venue row, not the machine: the same
// instant answers in Madrid and in Auckland according to the cinema's own zone.
func TestTicketRouteRendersLocalTimeFromTheVenueTimezone(t *testing.T) {
	cases := []struct {
		timezone string
		want     string
	}{
		{"Europe/Madrid", "2026-05-04T20:30:00+02:00"},
		{"Pacific/Auckland", "2026-05-05T06:30:00+12:00"},
		{"UTC", "2026-05-04T18:30:00+00:00"},
	}
	for _, testCase := range cases {
		detail := fixtureTicket()
		detail.Event.Timezone = testCase.timezone
		response := getTicket(t, mountedTicketServer(t, signedTicketService(t, &fakeTicketReader{detail: detail})), detail.TicketID)
		if response.StatusCode != http.StatusOK {
			body := readBody(t, response)
			t.Fatalf("GET ticket in %s status = %d, want 200 (body %s)", testCase.timezone, response.StatusCode, body)
		}
		var body ticketBody
		if err := json.Unmarshal(readBody(t, response), &body); err != nil {
			t.Fatalf("decode ticket body: %v", err)
		}
		if body.Event.StartsAtLocal != testCase.want {
			t.Fatalf("startsAtLocal for %s = %q, want %q", testCase.timezone, body.Event.StartsAtLocal, testCase.want)
		}
		if body.Event.StartsAt != "2026-05-04T18:30:00Z" {
			t.Fatalf("startsAt for %s = %q, want the same raw instant for every venue",
				testCase.timezone, body.Event.StartsAt)
		}
	}
}

// T4 (S4): a voided ticket and a ticket of a cancelled screening still answer —
// carrying their statuses — so the confirmation page can say what happened
// instead of pretending the ticket is valid.
func TestTicketRouteAnswersAWithdrawnTicketAndACancelledScreening(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*tickets.TicketDetail)
		wantStatus string
		wantEvent  string
	}{
		{
			name:       "a voided ticket",
			mutate:     func(detail *tickets.TicketDetail) { detail.Status = tickets.TicketStatusVoid },
			wantStatus: tickets.TicketStatusVoid,
			wantEvent:  "on_sale",
		},
		{
			name: "an issued ticket of a cancelled screening",
			mutate: func(detail *tickets.TicketDetail) {
				detail.Event.Status = tickets.EventStatusCancelled
			},
			wantStatus: tickets.TicketStatusIssued,
			wantEvent:  tickets.EventStatusCancelled,
		},
		{
			name: "a voided ticket of a cancelled screening",
			mutate: func(detail *tickets.TicketDetail) {
				detail.Status = tickets.TicketStatusVoid
				detail.Event.Status = tickets.EventStatusCancelled
			},
			wantStatus: tickets.TicketStatusVoid,
			wantEvent:  tickets.EventStatusCancelled,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			detail := fixtureTicket()
			testCase.mutate(&detail)
			response := getTicket(t, mountedTicketServer(t, signedTicketService(t, &fakeTicketReader{detail: detail})), detail.TicketID)
			if response.StatusCode != http.StatusOK {
				body := readBody(t, response)
				t.Fatalf("status = %d, want 200 (body %s)", response.StatusCode, body)
			}
			var body ticketBody
			if err := json.Unmarshal(readBody(t, response), &body); err != nil {
				t.Fatalf("decode ticket body: %v", err)
			}
			if body.Status != testCase.wantStatus {
				t.Fatalf("ticket status = %q, want %q", body.Status, testCase.wantStatus)
			}
			if body.Event.Status != testCase.wantEvent {
				t.Fatalf("event status = %q, want %q", body.Event.Status, testCase.wantEvent)
			}
			if body.Valid {
				t.Fatalf("valid = true for %s, want false", testCase.name)
			}
			// "Still answers" means the detail is complete, not an empty shell: the
			// seats, the amount and both times are served exactly as for a good ticket.
			if len(body.Seats) != 1 || body.OrderAmountCents != 1998 || body.Event.StartsAtLocal == "" {
				t.Fatalf("the withdrawn ticket lost its detail: %+v", body)
			}
		})
	}
}

// A ticket identifier that names no ticket is a plain 404 with the problem shape
// the public API already uses. The identifier is the capability, so a wrong guess
// and an identifier that never existed answer identically.
func TestTicketRouteAnswersAnUnknownTicketWith404(t *testing.T) {
	// The error is wrapped, so the mapping is errors.Is and not string matching.
	reader := &fakeTicketReader{err: fmt.Errorf("%w: %s", tickets.ErrNotFound, "00000000-0000-0000-0000-00000000ffff")}
	response := getTicket(t, mountedTicketServer(t, signedTicketService(t, reader)), "00000000-0000-0000-0000-00000000ffff")
	if response.StatusCode != http.StatusNotFound {
		body := readBody(t, response)
		t.Fatalf("status = %d, want 404 (body %s)", response.StatusCode, body)
	}
	problem := decodeBody[problemBody](t, response)
	if problem.Code != "not_found" {
		t.Fatalf("problem code = %q, want not_found", problem.Code)
	}
	if problem.Status != http.StatusNotFound || problem.Type == "" || problem.Title == "" || problem.Detail == "" {
		t.Fatalf("problem document is incomplete: %+v", problem)
	}
	if strings.Contains(problem.Detail, "00000000-0000-0000-0000-00000000ffff") {
		t.Fatalf("the problem detail echoed the identifier: %q", problem.Detail)
	}
}

// A read that could not be completed is a retryable 503 — never an empty ticket,
// which a renderer would happily print. A venue whose stored timezone cannot be
// resolved is refused the same way: no local time is invented from the machine's
// zone.
func TestTicketRouteAnswersAServiceFailureWith503(t *testing.T) {
	unresolvable := fixtureTicket()
	unresolvable.Event.Timezone = "Mars/Olympus"

	cases := []struct {
		name   string
		reader *fakeTicketReader
	}{
		{"a storage failure", &fakeTicketReader{err: fmt.Errorf("%w: conn closed", tickets.ErrUnavailable)}},
		{"an unclassified failure", &fakeTicketReader{err: errors.New("unexpected")}},
		{"a venue timezone that cannot be resolved", &fakeTicketReader{detail: unresolvable}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := getTicket(t, mountedTicketServer(t, signedTicketService(t, testCase.reader)), fixtureTicket().TicketID)
			if response.StatusCode != http.StatusServiceUnavailable {
				body := readBody(t, response)
				t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, body)
			}
			raw := readBody(t, response)
			var problem problemBody
			if err := json.Unmarshal(raw, &problem); err != nil {
				t.Fatalf("decode problem %s: %v", raw, err)
			}
			if problem.Code != "service_unavailable" {
				t.Fatalf("problem code = %q, want service_unavailable", problem.Code)
			}
			if problem.Status != http.StatusServiceUnavailable {
				t.Fatalf("problem status = %d, want 503", problem.Status)
			}
			// Nothing of the ticket leaks into a failure: the caller gets a problem
			// document, not a half-filled ticket.
			for _, leaked := range []string{"ticketId", "seats", "orderAmountCents"} {
				if strings.Contains(string(raw), leaked) {
					t.Fatalf("the 503 body carries %q: %s", leaked, raw)
				}
			}
		})
	}
}

// Preserve: a deployment without a ticket reader mounts no ticket route, so it
// advertises nothing it cannot serve, and the routes next to it keep working.
func TestTicketRouteIsNotMountedWithoutAReader(t *testing.T) {
	ts := mountedTicketServer(t, nil)

	response := getTicket(t, ts, fixtureTicket().TicketID)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("ticket route status = %d, want 404 without a ticket reader", response.StatusCode)
	}

	// Preserve: the public hold route is still mounted next to the missing ticket
	// route, and the administrative subtree is still unreachable.
	hold := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
	hold.Body.Close()
	if hold.StatusCode == http.StatusNotFound {
		t.Fatal("the hold route disappeared when the ticket reader was not wired")
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
		t.Fatalf("admin route status = %d, want 404: the ticket route is public, the administrative subtree is not",
			adminResponse.StatusCode)
	}
}
