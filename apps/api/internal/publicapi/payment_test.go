// Behaviour tests for the public checkout route: POST
// /api/v1/reservations/{reservationID}/payment.
//
// They need no database, no Redis and no provider: the test holds a fake payment
// service, as the hold tests hold a fake reservation service. What is pinned here
// is the transport contract the buyer and the web client depend on — the route is
// public, the Idempotency-Key header is forwarded, a decline is a success whose
// body says so, and every domain failure keeps its own status and problem code.
package publicapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/publicapi"
)

// fakePaymentService records the reservation and key the transport forwarded and
// answers with a configured result.
type fakePaymentService struct {
	result        checkout.Result
	err           error
	calls         int
	reservationID string
	key           string
	// email is the address the handler forwarded, so a test can prove the body reached
	// the service and that a refused body never reached it at all.
	email string
}

func (f *fakePaymentService) ChargeWithContact(_ context.Context, request checkout.ChargeRequest) (checkout.Result, error) {
	f.calls++
	f.reservationID, f.key = request.ReservationID, request.IdempotencyKey
	f.email = request.ContactEmail
	return f.result, f.err
}

// mountedPaymentServer wires the public subtree exactly as cmd/api does: the hold
// routes and the checkout route are mounted at /api/v1, never behind the
// administrative guard.
func mountedPaymentServer(t *testing.T, payments publicapi.PaymentService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := publicapi.New(&fakeHoldService{}).WithPayments(payments).Mux()
	ts := httptest.NewServer(server.WithPublic(mux).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postPayment(t *testing.T, ts *httptest.Server, reservationID, key string) *http.Response {
	t.Helper()
	url := ts.URL + "/api/v1/reservations/" + reservationID + "/payment"
	request, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return response
}

type paymentBody struct {
	ReservationID string   `json:"reservationId"`
	AttemptID     string   `json:"attemptId"`
	PaymentStatus string   `json:"paymentStatus"`
	AmountCents   int64    `json:"amountCents"`
	TicketIDs     []string `json:"ticketIds"`
}

// T3 (S7): the checkout route is public — a buyer has no administrative token —
// and an approved payment answers 200 with the order, the payment status, the
// amount and the issued tickets.
func TestPaymentRouteIsMountedWithoutTheAdminGuard(t *testing.T) {
	svc := &fakePaymentService{result: checkout.Result{
		ReservationID: "reservation-1",
		AttemptID:     "attempt-1",
		PaymentStatus: checkout.PaymentApproved,
		AttemptStatus: checkout.StatusCaptured,
		AmountCents:   2426,
		TicketIDs:     []string{"ticket-1", "ticket-2"},
	}}
	ts := mountedPaymentServer(t, svc)

	response := postPayment(t, ts, "reservation-1", "key-1")
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("POST payment status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if svc.calls != 1 || svc.reservationID != "reservation-1" || svc.key != "key-1" {
		t.Fatalf("forwarded reservation %q key %q in %d calls, want reservation-1 key-1 once",
			svc.reservationID, svc.key, svc.calls)
	}

	body := decodeBody[paymentBody](t, response)
	want := paymentBody{
		ReservationID: "reservation-1",
		AttemptID:     "attempt-1",
		PaymentStatus: checkout.PaymentApproved,
		AmountCents:   2426,
		TicketIDs:     []string{"ticket-1", "ticket-2"},
	}
	if body.ReservationID != want.ReservationID || body.AttemptID != want.AttemptID ||
		body.PaymentStatus != want.PaymentStatus || body.AmountCents != want.AmountCents {
		t.Fatalf("payment body = %+v, want %+v", body, want)
	}
	if len(body.TicketIDs) != 2 || body.TicketIDs[0] != "ticket-1" || body.TicketIDs[1] != "ticket-2" {
		t.Fatalf("payment ticket ids = %v, want both issued tickets", body.TicketIDs)
	}

	// Preserve: the public checkout route must not expose the administrative
	// subtree, and the hold route keeps working next to it.
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
		t.Fatalf("admin route status = %d, want 404 without the guard admitting it", adminResponse.StatusCode)
	}
}

// A decline is a successful request whose body says so: the provider worked and
// the money was refused, so the route answers 200 with paymentStatus "declined"
// and no tickets.
func TestPaymentRouteReportsADeclineAsASuccess(t *testing.T) {
	svc := &fakePaymentService{result: checkout.Result{
		ReservationID: "reservation-1",
		AttemptID:     "attempt-1",
		PaymentStatus: checkout.PaymentDeclined,
		AttemptStatus: checkout.StatusDeclined,
		AmountCents:   1213,
		TicketIDs:     []string{},
	}}
	response := postPayment(t, mountedPaymentServer(t, svc), "reservation-1", "key-1")
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("a declined payment status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(raw), `"paymentStatus":"declined"`) {
		t.Fatalf("declined body = %s, want paymentStatus declined", raw)
	}
	if !strings.Contains(string(raw), `"ticketIds":[]`) {
		t.Fatalf("declined body = %s, want an empty ticket list", raw)
	}
}

// Every checkout failure keeps its own status and problem code, so a conflict is
// never reported as an expired window, and a provider or storage failure is
// retryable rather than a success.
func TestPaymentRouteMapsErrorsToStatuses(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"a reused key", fmt.Errorf("%w: %q", checkout.ErrIdempotencyKeyReused, "key-1"), http.StatusConflict, "idempotency_key_reused"},
		{"an amount disagreement", fmt.Errorf("%w: 2426 is not 1213", checkout.ErrAmountMismatch), http.StatusConflict, "amount_mismatch"},
		{"a payment already in flight", fmt.Errorf("%w: attempt attempt-1 is pending", checkout.ErrPaymentInFlight), http.StatusConflict, "payment_in_flight"},
		{"an expired hold", fmt.Errorf("%w: %s", checkout.ErrHoldExpired, "reservation-1"), http.StatusConflict, "hold_expired"},
		{"an already paid reservation", fmt.Errorf("%w: %s", checkout.ErrAlreadyPaid, "reservation-1"), http.StatusConflict, "already_paid"},
		{"a reservation that is not awaiting payment", fmt.Errorf("%w: %s is cancelled", checkout.ErrReservationNotPayable, "reservation-1"), http.StatusConflict, "reservation_not_payable"},
		{"a seat already sold", fmt.Errorf("%w: seat 21", checkout.ErrSeatAlreadySold), http.StatusConflict, "seat_already_sold"},
		{"a taken provider reference", fmt.Errorf("%w: payment_attempt:x", checkout.ErrProviderReferenceTaken), http.StatusConflict, "provider_reference_taken"},
		{"a validation failure", fmt.Errorf("%w: Idempotency-Key header is required", checkout.ErrInvalidInput), http.StatusBadRequest, "invalid_input"},
		{"nothing to sell", fmt.Errorf("%w: the event price is 0 minor units", checkout.ErrNothingToSell), http.StatusBadRequest, "not_sellable"},
		{"an unknown reservation", fmt.Errorf("%w: %s", checkout.ErrReservationNotFound, "reservation-1"), http.StatusNotFound, "not_found"},
		{"a provider failure", fmt.Errorf("%w: connect: connection refused", checkout.ErrProviderUnavailable), http.StatusServiceUnavailable, "payment_unavailable"},
		{"a storage failure", fmt.Errorf("%w: conn closed", checkout.ErrUnavailable), http.StatusServiceUnavailable, "service_unavailable"},
		{"an unclassified failure", errors.New("unexpected"), http.StatusServiceUnavailable, "service_unavailable"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			svc := &fakePaymentService{err: testCase.err}
			response := postPayment(t, mountedPaymentServer(t, svc), "reservation-1", "key-1")
			if response.StatusCode != testCase.wantStatus {
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				t.Fatalf("status = %d, want %d (body %s)", response.StatusCode, testCase.wantStatus, body)
			}
			problem := decodeBody[problemBody](t, response)
			if problem.Code != testCase.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, testCase.wantCode)
			}
			if problem.Status != testCase.wantStatus {
				t.Fatalf("problem status field = %d, want %d", problem.Status, testCase.wantStatus)
			}
			if problem.Detail == "" || problem.Title == "" || problem.Type == "" {
				t.Fatalf("problem detail is incomplete: %+v", problem)
			}
		})
	}
}

// Preserve: mounting the hold routes without a payment service leaves the checkout
// route unmounted, so a deployment without the checkout schema advertises no
// route it cannot serve.
func TestPaymentRouteIsNotMountedWithoutAPaymentService(t *testing.T) {
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).WithPayments(nil).Mux()).Handler())
	t.Cleanup(ts.Close)

	response := postPayment(t, ts, "reservation-1", "key-1")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("checkout route status = %d, want 404 without a payment service", response.StatusCode)
	}

	// Preserve: the hold route is still mounted next to the missing checkout route.
	hold := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
	hold.Body.Close()
	if hold.StatusCode == http.StatusNotFound {
		t.Fatal("the hold route disappeared when the payment service was not wired")
	}
}
