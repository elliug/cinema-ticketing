// Behaviour tests for the cancel route: POST /api/v1/reservations/{reservationId}/cancel
// (S10).
//
// They need no database: the test holds a fake cancel service and a fake confirmation
// reader, and asserts the transport contract the confirmation page depends on — the
// answer is the reservation state in the shape the read already serves, cancelling twice
// is not an error, a paid order is an explicit conflict, an expired hold is answered as
// expired, an unknown identifier is the read's 404, and an unreachable service is
// retryable rather than a success.
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

	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/reservation"
)

// fakeCanceller answers one configured cancel and records what the transport forwarded.
type fakeCanceller struct {
	result reservation.Cancellation
	err    error

	calls         int32
	reservationID string
}

func (f *fakeCanceller) Cancel(_ context.Context, reservationID string) (reservation.Cancellation, error) {
	atomic.AddInt32(&f.calls, 1)
	f.reservationID = reservationID
	if f.err != nil {
		return reservation.Cancellation{}, f.err
	}
	return f.result, nil
}

// mountedCancelServer wires the public subtree the way cmd/api does: the cancel route
// next to the confirmation read it answers in the same shape, and never behind the
// administrative guard.
func mountedCancelServer(t *testing.T, canceller publicapi.CancellationService, reader publicapi.ReservationService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).
		WithReservations(reader).WithCancellation(canceller).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postCancel(t *testing.T, ts *httptest.Server, reservationID string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/reservations/"+reservationID+"/cancel", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", request.URL, err)
	}
	return response
}

// fixtureCancelledReservation is what a cancelled order is read as: the state is
// cancelled, no money was taken, no amount is due and the seats it held are released, so
// the read carries none of them.
func fixtureCancelledReservation() catalog.ReservationDetail {
	return catalog.ReservationDetail{
		ID:         "00000000-0000-0000-0000-00000000d1a1",
		Status:     "cancelled",
		ExpiresAt:  reservationFixtureExpiry,
		EventID:    "00000000-0000-0000-0000-00000000d1e1",
		FilmTitle:  "La película de la cancelación",
		CinemaName: "Cine Cancelación",
		RoomName:   "Sala 3",
		StartsAt:   reservationFixtureStart,
		Timezone:   "Europe/Madrid",
	}
}

// fixtureHeldOrder is an unpaid order as the confirmation read serves it while it holds
// its seat: the state a cancel is asked to end.
func fixtureHeldOrder() catalog.ReservationDetail {
	due := int64(2500)
	return catalog.ReservationDetail{
		ID:             "00000000-0000-0000-0000-00000000d1a1",
		Status:         "held",
		ExpiresAt:      reservationFixtureExpiry,
		AmountDueCents: &due,
		EventID:        "00000000-0000-0000-0000-00000000d1e1",
		FilmTitle:      "La película de la cancelación",
		CinemaName:     "Cine Cancelación",
		RoomName:       "Sala 3",
		StartsAt:       reservationFixtureStart,
		Timezone:       "Europe/Madrid",
		Seats:          []catalog.Seat{{EventSeatID: 41, RowLabel: "C", SeatNumber: "7"}},
	}
}

// S10: the cancel answer is the reservation's new state in the body the confirmation
// read already serves — the same keys, the same nullability, the same UTC deadline — so
// a client does not learn a second vocabulary for one order.
func TestCancelRouteAnswersTheReservationInTheReadsShape(t *testing.T) {
	detail := fixtureCancelledReservation()
	reader := &fakeReservationReader{detail: detail}
	canceller := &fakeCanceller{result: reservation.Cancellation{
		ReservationID: detail.ID,
		Status:        "cancelled",
		SeatIDs:       []int64{41},
	}}
	ts := mountedCancelServer(t, canceller, reader)

	response := postCancel(t, ts, detail.ID)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("POST cancel status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if got := atomic.LoadInt32(&canceller.calls); got != 1 {
		t.Fatalf("the route asked the cancel service %d times, want once", got)
	}
	if canceller.reservationID != detail.ID {
		t.Fatalf("forwarded reservation %q, want %q", canceller.reservationID, detail.ID)
	}
	raw := readBody(t, response)

	var body reservationBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode cancel body %s: %v", raw, err)
	}
	if body.ReservationID != detail.ID || body.Status != "cancelled" {
		t.Fatalf("cancel answered %q status %q, want %q / cancelled", body.ReservationID, body.Status, detail.ID)
	}
	if !strings.HasPrefix(body.ExpiresAt, "2026-05-04T18:40:00") {
		t.Fatalf("expiresAt = %q, want the stored deadline", body.ExpiresAt)
	}
	if body.FilmTitle != detail.FilmTitle || body.CinemaName != detail.CinemaName ||
		body.RoomName != detail.RoomName || body.StartsAtLocal == "" {
		t.Fatalf("cancel body = %+v, want the order's screening fields", body)
	}
	// Nothing was taken and nothing is due: both stay null, and the seat and ticket lists
	// stay arrays, exactly as the read answers them.
	if got := string(objectField(t, raw, "amountCents")); got != "null" {
		t.Fatalf("amountCents = %s, want null", got)
	}
	if got := string(objectField(t, raw, "amountDueCents")); got != "null" {
		t.Fatalf("amountDueCents = %s, want null", got)
	}
	if got := string(objectField(t, raw, "seats")); got != "[]" {
		t.Fatalf("seats = %s, want an empty array", got)
	}
	if got := string(objectField(t, raw, "ticketIds")); got != "[]" {
		t.Fatalf("ticketIds = %s, want an empty array", got)
	}

	// The shape is the read's own, key for key: the same order answered by the read is
	// the comparison, not a list restated here.
	getResponse := getReservation(t, ts, detail.ID)
	if getResponse.StatusCode != http.StatusOK {
		getBody := readBody(t, getResponse)
		t.Fatalf("GET reservation status = %d, want 200 (body %s)", getResponse.StatusCode, getBody)
	}
	cancelKeys := objectKeys(t, raw)
	readKeys := objectKeys(t, readBody(t, getResponse))
	if !equalKeys(cancelKeys, readKeys) {
		t.Fatalf("cancel body keys = %v, want the read's own keys %v", cancelKeys, readKeys)
	}
}

// S10: cancelling twice is not an error. The second call answers the same cancelled
// reservation, so a retry after a lost response is safe.
func TestCancelRouteAnswersASecondCancelWithoutAnError(t *testing.T) {
	detail := fixtureCancelledReservation()
	canceller := &fakeCanceller{result: reservation.Cancellation{ReservationID: detail.ID, Status: "cancelled"}}
	ts := mountedCancelServer(t, canceller, &fakeReservationReader{detail: detail})

	for attempt := 1; attempt <= 2; attempt++ {
		response := postCancel(t, ts, detail.ID)
		if response.StatusCode != http.StatusOK {
			body := readBody(t, response)
			t.Fatalf("cancel attempt %d status = %d, want 200 (body %s)", attempt, response.StatusCode, body)
		}
		var body reservationBody
		if err := json.Unmarshal(readBody(t, response), &body); err != nil {
			t.Fatalf("decode cancel attempt %d body: %v", attempt, err)
		}
		if body.Status != "cancelled" {
			t.Fatalf("cancel attempt %d status = %q, want cancelled", attempt, body.Status)
		}
	}
}

// S10: an already paid order is refused with an explicit conflict, and the refusal does
// not read the order or release anything: the payment route's own `already_paid` code is
// the vocabulary, not a new one.
func TestCancelRouteRefusesAPaidReservationWith409(t *testing.T) {
	detail := fixturePaidReservation()
	reader := &fakeReservationReader{detail: detail}
	canceller := &fakeCanceller{err: fmt.Errorf("%w: %s", reservation.ErrAlreadyPaid, detail.ID)}

	response := postCancel(t, mountedCancelServer(t, canceller, reader), detail.ID)
	if response.StatusCode != http.StatusConflict {
		body := readBody(t, response)
		t.Fatalf("POST cancel of a paid order status = %d, want 409 (body %s)", response.StatusCode, body)
	}
	problem := decodeBody[problemBody](t, response)
	if problem.Code != "already_paid" || problem.Status != http.StatusConflict {
		t.Fatalf("problem = %+v, want a 409 already_paid document", problem)
	}
	if problem.Detail == "" || problem.Title == "" || problem.Type == "" {
		t.Fatalf("problem document is incomplete: %+v", problem)
	}
	if got := atomic.LoadInt32(&reader.calls); got != 0 {
		t.Fatalf("the refusal read the reservation %d times, want none", got)
	}
}

// S10: an expired hold is answered honestly with what it is, not a 500 and not a silent
// success — and the state the cancel established wins over the stored one the read still
// carries until the expiry sweep rewrites it.
func TestCancelRouteAnswersAnExpiredHoldAsExpired(t *testing.T) {
	detail := fixtureHeldOrder()
	canceller := &fakeCanceller{result: reservation.Cancellation{ReservationID: detail.ID, Status: "expired"}}
	ts := mountedCancelServer(t, canceller, &fakeReservationReader{detail: detail})

	response := postCancel(t, ts, detail.ID)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("POST cancel of an expired hold status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	var body reservationBody
	if err := json.Unmarshal(readBody(t, response), &body); err != nil {
		t.Fatalf("decode cancel body: %v", err)
	}
	if body.Status != "expired" {
		t.Fatalf("status = %q, want expired", body.Status)
	}
	if body.ReservationID != detail.ID {
		t.Fatalf("reservationId = %q, want %q", body.ReservationID, detail.ID)
	}
}

// S10: an unknown identifier — and one that could never name a reservation — is the same
// 404 the confirmation read answers, and the detail never echoes the identifier: it is
// the capability, so a wrong guess must stay indistinguishable from an absent order.
func TestCancelRouteAnswersAnUnknownReservationWith404(t *testing.T) {
	for _, reservationID := range []string{
		"00000000-0000-0000-0000-00000000d0ff",
		"not-a-reservation-id",
	} {
		reader := &fakeReservationReader{detail: fixtureCancelledReservation()}
		canceller := &fakeCanceller{err: fmt.Errorf("%w: %s", reservation.ErrReservationNotFound, reservationID)}
		response := postCancel(t, mountedCancelServer(t, canceller, reader), reservationID)
		if response.StatusCode != http.StatusNotFound {
			body := readBody(t, response)
			t.Fatalf("POST cancel %q status = %d, want 404 (body %s)", reservationID, response.StatusCode, body)
		}
		problem := decodeBody[problemBody](t, response)
		if problem.Code != "not_found" || problem.Status != http.StatusNotFound {
			t.Fatalf("problem = %+v, want a 404 not_found document", problem)
		}
		if strings.Contains(problem.Detail, reservationID) {
			t.Fatalf("the problem detail echoed the identifier: %q", problem.Detail)
		}
		if got := atomic.LoadInt32(&reader.calls); got != 0 {
			t.Fatalf("the 404 read the reservation %d times, want none", got)
		}
	}
}

// S10: a cancel that could not be completed is a retryable 503 — never a body a client
// would read as a cancelled order.
func TestCancelRouteAnswersAnUnreachableServiceWith503(t *testing.T) {
	cases := []struct {
		name      string
		canceller *fakeCanceller
	}{
		{"the durable layer is unreachable", &fakeCanceller{err: fmt.Errorf("%w: conn closed", reservation.ErrUnavailable)}},
		{"an unclassified failure", &fakeCanceller{err: errors.New("unexpected")}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			detail := fixtureHeldOrder()
			response := postCancel(t, mountedCancelServer(t, testCase.canceller, &fakeReservationReader{detail: detail}), detail.ID)
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
				t.Fatalf("problem = %+v, want service_unavailable", problem)
			}
			for _, leaked := range []string{"reservationId", "seats", "ticketIds", "expiresAt"} {
				if strings.Contains(string(raw), leaked) {
					t.Fatalf("the 503 body carries %q: %s", leaked, raw)
				}
			}
		})
	}
}

// Preserve: a deployment that cannot answer the read's shape mounts no cancel — a cancel
// that had nowhere to read the order from, or nothing to cancel with, would be a route
// that can only fail. The routes next to it keep working.
func TestCancelRouteIsNotMountedWithoutItsTwoHalves(t *testing.T) {
	detail := fixtureCancelledReservation()

	t.Run("without a cancel service", func(t *testing.T) {
		ts := mountedCancelServer(t, nil, &fakeReservationReader{detail: detail})
		response := postCancel(t, ts, detail.ID)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("cancel status = %d, want 404 without a cancel service", response.StatusCode)
		}
		// Preserve: the confirmation read on the same subtree still answers.
		read := getReservation(t, ts, detail.ID)
		read.Body.Close()
		if read.StatusCode != http.StatusOK {
			t.Fatalf("reservation read status = %d, want the read left working", read.StatusCode)
		}
	})

	t.Run("without a reservation reader", func(t *testing.T) {
		canceller := &fakeCanceller{result: reservation.Cancellation{Status: "cancelled"}}
		ts := mountedCancelServer(t, canceller, nil)
		response := postCancel(t, ts, detail.ID)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("cancel status = %d, want 404 without a reader to answer in its shape", response.StatusCode)
		}
		if got := atomic.LoadInt32(&canceller.calls); got != 0 {
			t.Fatalf("the cancel service was asked %d times although the route is not mounted", got)
		}
		// Preserve: creating a hold still reaches the hold handler, so the cancel route
		// did not take the POST subtree over.
		hold := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
		hold.Body.Close()
		if hold.StatusCode == http.StatusNotFound {
			t.Fatal("the hold route disappeared when the cancel route was not mounted")
		}
	})
}
