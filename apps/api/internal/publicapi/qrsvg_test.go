// Behaviour tests for the ticket QR image route: GET /api/v1/tickets/{id}/qr.svg.
//
// They need no database and no bucket: the ticket service is the same fake-and-signer
// wiring the detail and verification routes are tested with, so the tests pin the
// transport contract the ticket page depends on — an SVG of the served token's matrix,
// the same bytes on every read, `no-store` because the token is a credential, the
// detail route's own problem document for a ticket that does not exist, a retryable
// 503 when the ticket cannot be read, and no route at all when no ticket service is
// mounted.
package publicapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// getTicketQR requests one ticket's QR image.
func getTicketQR(t *testing.T, ts *httptest.Server, ticketID string) *http.Response {
	t.Helper()
	response, err := http.Get(ts.URL + "/api/v1/tickets/" + ticketID + "/qr.svg")
	if err != nil {
		t.Fatalf("GET /api/v1/tickets/%s/qr.svg: %v", ticketID, err)
	}
	return response
}

// TestTicketQRRouteServesTheServedTokensMatrix pins the whole answer: the status, the
// media type, the cache rule and the bytes.
func TestTicketQRRouteServesTheServedTokensMatrix(t *testing.T) {
	reader := &fakeTicketReader{detail: fixtureTicket()}
	svc := signedTicketService(t, reader)
	ts := mountedTicketServer(t, svc)
	token := servedToken(t, svc, fixtureTicket().TicketID)

	response := getTicketQR(t, ts, fixtureTicket().TicketID)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("qr status = %d, want 200 (body %s)", response.StatusCode, readBody(t, response))
	}
	if got, want := response.Header.Get("Content-Type"), "image/svg+xml"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	// The token is a credential: an intermediary must not keep the image.
	if got, want := response.Header.Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}

	body := readBody(t, response)
	want, err := tickets.QRCodeSVG(token)
	if err != nil {
		t.Fatalf("QRCodeSVG() = unexpected error %v", err)
	}
	if string(body) != string(want) {
		t.Fatalf("qr body = %q, want the SVG of the served token", string(body))
	}
	if strings.Contains(string(body), token) {
		t.Fatal("the served image embeds the token as text")
	}
}

// TestTicketQRRouteAnswersTheSameImageOnEveryRead: the code belongs to the ticket, so
// two reads of one ticket cannot differ.
func TestTicketQRRouteAnswersTheSameImageOnEveryRead(t *testing.T) {
	svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
	ts := mountedTicketServer(t, svc)

	first := readBody(t, getTicketQR(t, ts, fixtureTicket().TicketID))
	second := readBody(t, getTicketQR(t, ts, fixtureTicket().TicketID))

	if len(first) == 0 {
		t.Fatal("the first read answered no image")
	}
	if string(first) != string(second) {
		t.Fatal("two reads of one ticket answered different images")
	}
}

// TestTicketQRRouteAnswers404ForAnUnknownTicket: the image route answers the detail
// route's own problem document, so a wrong guess learns nothing new.
func TestTicketQRRouteAnswers404ForAnUnknownTicket(t *testing.T) {
	reader := &fakeTicketReader{err: tickets.ErrNotFound}
	svc := signedTicketService(t, reader)
	ts := mountedTicketServer(t, svc)

	response := getTicketQR(t, ts, "00000000-0000-0000-0000-0000000000ff")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("qr status = %d, want 404", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want the problem document's application/json", got)
	}
	var problem struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	raw := readBody(t, response)
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "not_found" || problem.Status != http.StatusNotFound {
		t.Fatalf("problem = %+v, want 404 not_found", problem)
	}
}

// TestTicketQRRouteAnswers503ForAnUnreadableTicket: a store that could not be asked is
// not evidence about a ticket, so the answer is retryable and carries no image.
func TestTicketQRRouteAnswers503ForAnUnreadableTicket(t *testing.T) {
	reader := &fakeTicketReader{err: tickets.ErrUnavailable}
	svc := signedTicketService(t, reader)
	ts := mountedTicketServer(t, svc)

	response := getTicketQR(t, ts, fixtureTicket().TicketID)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("qr status = %d, want 503", response.StatusCode)
	}
	var problem struct {
		Code string `json:"code"`
	}
	raw := readBody(t, response)
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "service_unavailable" {
		t.Fatalf("problem code = %q, want service_unavailable", problem.Code)
	}
}

// TestTicketQRRouteIsAbsentWithoutATicketService: a deployment that mounts no ticket
// route must not advertise an image route either.
func TestTicketQRRouteIsAbsentWithoutATicketService(t *testing.T) {
	fakeTickets := publicapi.New(&fakeHoldService{}).Mux()
	server := httptest.NewServer(fakeTickets)
	t.Cleanup(server.Close)

	response := getTicketQR(t, server, fixtureTicket().TicketID)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unmounted qr route status = %d, want 404", response.StatusCode)
	}
	if ct := response.Header.Get("Content-Type"); ct == "image/svg+xml" {
		t.Fatal("an SVG was served without a ticket service")
	}
}
