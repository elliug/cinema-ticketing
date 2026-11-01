// The one optional field the payment route accepts (email delivery, S1).
//
// The route used to carry no body at all, and the comment above it said so: the amount is
// computed server side, so there was "nothing to trust if it did". It now accepts exactly
// one field, and it is not money — the address the tickets are to be mailed to. These
// tests exist because that is a contract change with two failure modes worth pinning: a
// misspelt key must not silently drop the address, and a refused body must never reach the
// service, so a bad request cannot charge anybody.
package publicapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/checkout"
)

// postPaymentWithBody sends one payment request carrying a body. The helper next door
// sends body-less requests, which remain the ordinary case.
func postPaymentWithBody(t *testing.T, ts *httptest.Server, body string) *http.Response {
	t.Helper()
	url := ts.URL + "/api/v1/reservations/res-1/payment"
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(http.MethodPost, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Idempotency-Key", "key-1")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestPaymentForwardsTheContactAddress(t *testing.T) {
	payments := &fakePaymentService{result: checkout.Result{PaymentStatus: checkout.PaymentApproved, AmountCents: 1998}}
	ts := mountedPaymentServer(t, payments)

	response := postPaymentWithBody(t, ts, `{"email":"compras@example.com"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if payments.email != "compras@example.com" {
		t.Fatalf("the service received %q, want the address from the body", payments.email)
	}
}

func TestPaymentWithoutABodyIsStillAPayment(t *testing.T) {
	payments := &fakePaymentService{result: checkout.Result{PaymentStatus: checkout.PaymentApproved}}
	ts := mountedPaymentServer(t, payments)

	response := postPaymentWithBody(t, ts, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if payments.email != "" {
		t.Fatalf("the service received %q, want no address", payments.email)
	}
}

// TestPaymentRefusesAnUnknownBodyField is the reason the decoder is strict: `emial` would
// otherwise be ignored, the buyer would pay, and nobody would learn that the ticket had
// nowhere to go until no email arrived.
func TestPaymentRefusesAnUnknownBodyField(t *testing.T) {
	payments := &fakePaymentService{result: checkout.Result{PaymentStatus: checkout.PaymentApproved}}
	ts := mountedPaymentServer(t, payments)

	response := postPaymentWithBody(t, ts, `{"emial":"compras@example.com"}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatalf("decode the problem: %v", err)
	}
	if problem.Code != "invalid_input" {
		t.Fatalf("problem code = %q, want invalid_input", problem.Code)
	}
	if payments.calls != 0 {
		t.Fatalf("a refused body still reached the service (%d call(s))", payments.calls)
	}
}
