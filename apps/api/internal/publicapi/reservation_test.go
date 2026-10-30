// Behaviour tests for the public reservation read: GET
// /api/v1/reservations/{reservationId}.
//
// They need no database: the test holds a fake reader and asserts the contract the
// confirmation screen (T6) depends on — the reload of a confirmation keeps its
// state, the amount is the money actually taken and stays null while there is none,
// the seats are the ones the order holds or sold in seat order, the ticket list is
// always an array, an unknown or impossible identifier is the same 404, and a
// storage failure is a retryable problem document and never an empty reservation.
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
	"sync/atomic"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/publicapi"
)

// fakeReservationReader answers one configured reservation read and records what the
// transport forwarded.
type fakeReservationReader struct {
	detail catalog.ReservationDetail
	err    error

	calls         int32
	reservationID string
}

func (f *fakeReservationReader) Reservation(_ context.Context, reservationID string) (catalog.ReservationDetail, error) {
	atomic.AddInt32(&f.calls, 1)
	f.reservationID = reservationID
	if f.err != nil {
		return catalog.ReservationDetail{}, f.err
	}
	return f.detail, nil
}

// mountedReservationReadServer wires the public subtree the way cmd/api does: the
// reservation read next to the hold and payment routes, never behind the
// administrative guard.
func mountedReservationReadServer(t *testing.T, reader publicapi.ReservationService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).WithReservations(reader).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getReservation(t *testing.T, ts *httptest.Server, reservationID string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/reservations/"+reservationID, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", request.URL, err)
	}
	return response
}

// reservationFixtureExpiry is the durable deadline the fixtures carry.
var reservationFixtureExpiry = time.Date(2026, time.May, 4, 18, 40, 0, 0, time.UTC)

// reservationFixtureStart is the screening instant of the fixture. Madrid is +02:00 in
// May, so the venue-local form is the same instant two hours later — which is what the
// route must print without the browser knowing the zone.
var reservationFixtureStart = time.Date(2030, time.January, 15, 20, 30, 0, 0, time.UTC)

// fixturePaidReservation is what a paid two-seat order answers: the seats it sold, in
// seat order, the money the capture took, and the tickets issued for it.
func fixturePaidReservation() catalog.ReservationDetail {
	amount := int64(1998)
	return catalog.ReservationDetail{
		ID:          "00000000-0000-0000-0000-00000000f2a1",
		Status:      "paid",
		ExpiresAt:   reservationFixtureExpiry,
		EventID:     "00000000-0000-0000-0000-00000000f2e1",
		FilmTitle:   "La película de la confirmación",
		CinemaName:  "Cine Confirmación",
		RoomName:    "Sala 2",
		StartsAt:    reservationFixtureStart,
		Timezone:    "Europe/Madrid",
		AmountCents: &amount,
		Seats: []catalog.Seat{
			{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"},
			{EventSeatID: 22, RowLabel: "A", SeatNumber: "2"},
		},
		TicketIDs: []string{
			"00000000-0000-0000-0000-00000000f2b1",
			"00000000-0000-0000-0000-00000000f2b2",
		},
	}
}

// reservationSeatBody is the wire shape of one seat of an order.
type reservationSeatBody struct {
	EventSeatID int64  `json:"eventSeatId"`
	RowLabel    string `json:"rowLabel"`
	SeatNumber  string `json:"seatNumber"`
}

// reservationBody is the wire shape of the confirmation's read. The amount keys are
// nullable on purpose: an unpaid order has no charge, and printing 0.00 would claim
// money was taken when none was.
type reservationBody struct {
	ReservationID string                `json:"reservationId"`
	Status        string                `json:"status"`
	ExpiresAt     string                `json:"expiresAt"`
	AmountCents    *int64              `json:"amountCents"`
	AmountDisplay  *string             `json:"amountDisplay"`
	AmountDueCents   *int64            `json:"amountDueCents"`
	AmountDueDisplay *string           `json:"amountDueDisplay"`
	Seats         []reservationSeatBody `json:"seats"`
	TicketIDs     []string              `json:"ticketIds"`
	EventID       string                `json:"eventId"`
	FilmTitle     string                `json:"filmTitle"`
	CinemaName    string                `json:"cinemaName"`
	RoomName      string                `json:"roomName"`
	StartsAt      string                `json:"startsAt"`
	StartsAtLocal string                `json:"startsAtLocal"`
}

// S5: reloading the confirmation of a paid order answers everything the screen shows,
// without asking the payment route again.
func TestReservationRouteServesTheConfirmationState(t *testing.T) {
	reader := &fakeReservationReader{detail: fixturePaidReservation()}
	ts := mountedReservationReadServer(t, reader)

	response := getReservation(t, ts, fixturePaidReservation().ID)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("GET reservation status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if got := atomic.LoadInt32(&reader.calls); got != 1 {
		t.Fatalf("the route asked the reader %d times, want once", got)
	}
	if reader.reservationID != fixturePaidReservation().ID {
		t.Fatalf("forwarded reservation %q, want %q", reader.reservationID, fixturePaidReservation().ID)
	}

	raw := readBody(t, response)
	var body reservationBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode reservation body %s: %v", raw, err)
	}

	want := fixturePaidReservation()
	if body.ReservationID != want.ID || body.Status != want.Status {
		t.Fatalf("reservation %q status %q, want %q / %q", body.ReservationID, body.Status, want.ID, want.Status)
	}
	if !strings.HasPrefix(body.ExpiresAt, "2026-05-04T18:40:00") {
		t.Fatalf("expiresAt = %q, want the stored deadline", body.ExpiresAt)
	}
	if body.AmountCents == nil || *body.AmountCents != 1998 {
		t.Fatalf("amountCents = %v, want 1998", body.AmountCents)
	}
	if body.AmountDisplay == nil || *body.AmountDisplay != "19.98" {
		t.Fatalf("amountDisplay = %v, want 19.98", body.AmountDisplay)
	}
	if len(body.Seats) != 2 || body.Seats[0] != (reservationSeatBody{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"}) ||
		body.Seats[1] != (reservationSeatBody{EventSeatID: 22, RowLabel: "A", SeatNumber: "2"}) {
		t.Fatalf("seats = %+v, want the order's two seats in seat order", body.Seats)
	}
	if len(body.TicketIDs) != 2 || body.TicketIDs[0] != want.TicketIDs[0] || body.TicketIDs[1] != want.TicketIDs[1] {
		t.Fatalf("ticketIds = %v, want the issued tickets %v", body.TicketIDs, want.TicketIDs)
	}

	// The key names are the contract the confirmation page reads, so they are pinned
	// as a set. There is deliberately no attempt, provider or buyer key here: this
	// read is a capability URL, and payment internals are not part of what the
	// confirmation shows.
	if got := objectKeys(t, raw); !equalKeys(got, []string{
		"amountCents", "amountDisplay", "amountDueCents", "amountDueDisplay", "cinemaName",
		"eventId", "expiresAt", "filmTitle",
		"reservationId", "roomName", "seats", "startsAt", "startsAtLocal", "status", "ticketIds",
	}) {
		t.Fatalf("reservation keys = %v, want the documented confirmation fields", got)
	}
	seats := arrayField(t, raw, "seats")
	if len(seats) != 2 {
		t.Fatalf("seats array = %s, want two seats", seats)
	}
	if got := objectKeys(t, seats[0]); !equalKeys(got, []string{"eventSeatId", "rowLabel", "seatNumber"}) {
		t.Fatalf("seat keys = %v, want eventSeatId, rowLabel and seatNumber", got)
	}
}

// S5/S8: an unpaid order answered the truth — no amount and no ticket — instead of a
// zero charge invented from a missing one. The amount stays null, not 0 and not a
// price multiplied by the seats.
func TestReservationRouteKeepsAnUnpaidAmountNull(t *testing.T) {
	due := int64(2500)
	unpaid := catalog.ReservationDetail{
		ID:             "00000000-0000-0000-0000-00000000f2c1",
		AmountDueCents: &due,
		Status:     "held",
		ExpiresAt:  reservationFixtureExpiry,
		EventID:    "00000000-0000-0000-0000-00000000f2e1",
		FilmTitle:  "La película de la confirmación",
		CinemaName: "Cine Confirmación",
		RoomName:   "Sala 2",
		StartsAt:   reservationFixtureStart,
		Timezone:   "Europe/Madrid",
		Seats:      []catalog.Seat{{EventSeatID: 30, RowLabel: "B", SeatNumber: "4"}},
	}
	response := getReservation(t, mountedReservationReadServer(t, &fakeReservationReader{detail: unpaid}), unpaid.ID)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("GET unpaid reservation status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	raw := readBody(t, response)

	// The raw JSON is checked, because a decoded pointer cannot tell a null from an
	// absent key and the screen must receive an explicit null.
	if got := string(objectField(t, raw, "amountCents")); got != "null" {
		t.Fatalf("amountCents = %s, want null", got)
	}
	if got := string(objectField(t, raw, "amountDisplay")); got != "null" {
		t.Fatalf("amountDisplay = %s, want null", got)
	}
	if got := string(objectField(t, raw, "ticketIds")); got != "[]" {
		t.Fatalf("ticketIds = %s, want an empty array", got)
	}

	var body reservationBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode reservation body %s: %v", raw, err)
	}
	if body.AmountCents != nil || body.AmountDisplay != nil {
		t.Fatalf("unpaid amount = %v / %v, want null for both", body.AmountCents, body.AmountDisplay)
	}
	if body.Status != "held" || len(body.Seats) != 1 || len(body.TicketIDs) != 0 {
		t.Fatalf("unpaid reservation = %+v, want a held order with one seat and no ticket", body)
	}
}

// S5: the deadline is answered in UTC whatever zone the reader hands it in, so the
// countdown the confirmation page runs compares one canonical instant.
func TestReservationRouteAnswersTheDeadlineInUTC(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	detail := fixturePaidReservation()
	detail.ExpiresAt = reservationFixtureExpiry.In(cest)

	response := getReservation(t, mountedReservationReadServer(t, &fakeReservationReader{detail: detail}), detail.ID)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("GET reservation status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	var body reservationBody
	if err := json.Unmarshal(readBody(t, response), &body); err != nil {
		t.Fatalf("decode reservation body: %v", err)
	}
	if body.ExpiresAt != "2026-05-04T18:40:00Z" {
		t.Fatalf("expiresAt = %q, want the same deadline in UTC", body.ExpiresAt)
	}
}

// S5: an identifier that names no reservation is a plain 404 with the problem shape
// the public API already uses. The identifier is the capability, so a wrong guess and
// an identifier that never existed answer identically.
func TestReservationRouteAnswersAnUnknownReservationWith404(t *testing.T) {
	for _, reservationID := range []string{
		"00000000-0000-0000-0000-00000000ffff",
		"not-a-reservation-id",
	} {
		reader := &fakeReservationReader{err: fmt.Errorf("%w: %s", catalog.ErrNotFound, reservationID)}
		response := getReservation(t, mountedReservationReadServer(t, reader), reservationID)
		if response.StatusCode != http.StatusNotFound {
			body := readBody(t, response)
			t.Fatalf("GET reservation %q status = %d, want 404 (body %s)", reservationID, response.StatusCode, body)
		}
		problem := decodeBody[problemBody](t, response)
		if problem.Code != "not_found" || problem.Status != http.StatusNotFound {
			t.Fatalf("problem = %+v, want a 404 not_found document", problem)
		}
		if problem.Type == "" || problem.Title == "" || problem.Detail == "" {
			t.Fatalf("problem document is incomplete: %+v", problem)
		}
		if strings.Contains(problem.Detail, reservationID) {
			t.Fatalf("the problem detail echoed the identifier: %q", problem.Detail)
		}
	}
}

// S5: a read that could not be completed is a retryable 503 — never an empty
// reservation, which the confirmation page would render as an order without seats.
func TestReservationRouteAnswersAStorageFailureWith503(t *testing.T) {
	cases := []struct {
		name   string
		reader *fakeReservationReader
	}{
		{"a storage failure", &fakeReservationReader{err: fmt.Errorf("%w: conn closed", catalog.ErrUnavailable)}},
		{"an unclassified failure", &fakeReservationReader{err: errors.New("unexpected")}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := getReservation(t, mountedReservationReadServer(t, testCase.reader), fixturePaidReservation().ID)
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
			// Nothing of an empty reservation leaks into a failure.
			for _, leaked := range []string{"reservationId", "seats", "ticketIds", "amountCents"} {
				if strings.Contains(string(raw), leaked) {
					t.Fatalf("the 503 body carries %q: %s", leaked, raw)
				}
			}
		})
	}
}

// Preserve: a deployment without a reservation reader mounts no reservation read, so
// it advertises nothing it cannot serve, the hold creation route keeps winning the
// POST on the same path, and the routes next to it keep working.
func TestReservationRouteIsNotMountedWithoutAReader(t *testing.T) {
	ts := mountedReservationReadServer(t, nil)

	response := getReservation(t, ts, fixturePaidReservation().ID)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("reservation read status = %d, want 404 without a reader", response.StatusCode)
	}

	// Preserve: creating a hold still reaches the hold handler, so the read on the
	// same subtree did not take the route over.
	hold := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
	hold.Body.Close()
	if hold.StatusCode == http.StatusNotFound {
		t.Fatal("the hold route disappeared when the reservation reader was not wired")
	}
}
