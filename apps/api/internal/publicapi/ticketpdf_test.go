// Behaviour tests for the ticket PDF download route: GET /api/v1/tickets/{id}/pdf.
//
// They need no bucket and no database: the download service is a fake that answers a
// URL or a failure, so the tests assert the transport contract the confirmation page
// depends on — a short-lived redirect instead of the bytes, a 404 for an unknown
// ticket with the same problem document the detail route answers, a retryable 503 when
// the artefact cannot be produced, and no route at all when nothing is configured to
// store the document.
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

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/pdf"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// fakeTicketPDFs is the download contract as the route sees it: a URL, or the failure
// a real archive would report. It records the ticket it was asked for, so the route
// proves it forwards the path and nothing else.
type fakeTicketPDFs struct {
	url   string
	err   error
	asked []string
}

func (f *fakeTicketPDFs) DownloadURL(_ context.Context, ticketID string) (string, error) {
	f.asked = append(f.asked, ticketID)
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

// signedURL is a stand-in for a pre-signed bucket URL. It carries a signature query
// parameter because the real one does, which is what makes "never echo the URL"
// observable.
const signedURL = "http://127.0.0.1:4566/cinema-m3-tickets/tickets/evt/ticket.pdf?X-Amz-Expires=300&X-Amz-Signature=stub"

// mountedTicketPDFServer wires the public subtree the way cmd/api does, with the ticket
// detail service and the PDF download service attached.
func mountedTicketPDFServer(t *testing.T, ticketService publicapi.TicketService, pdfService publicapi.TicketPDFService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := publicapi.New(&fakeHoldService{}).WithTickets(ticketService).WithTicketPDFs(pdfService).Mux()
	ts := httptest.NewServer(server.WithPublic(handler).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// redirectingClient never follows a redirect: the route's whole answer is the 302, and
// following it would try to download from a bucket the test does not have.
func redirectingClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// getTicketPDF requests the download of one ticket's PDF without following the
// redirect.
func getTicketPDF(t *testing.T, ts *httptest.Server, ticketID string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/tickets/"+ticketID+"/pdf", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := redirectingClient().Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", request.URL, err)
	}
	return response
}

// TestTicketPDFRouteRedirectsToASignedURL pins the download contract: the route answers
// a temporary redirect to a pre-signed bucket URL, not the document itself, so the API
// never streams a ticket and the link stops working on its own.
func TestTicketPDFRouteRedirectsToASignedURL(t *testing.T) {
	pdfs := &fakeTicketPDFs{url: signedURL}
	ts := mountedTicketPDFServer(t, signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}), pdfs)
	ticketID := fixtureTicket().TicketID

	response := getTicketPDF(t, ts, ticketID)
	defer response.Body.Close()

	if response.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 302 (body %s)", response.StatusCode, body)
	}
	if location := response.Header.Get("Location"); location != signedURL {
		t.Fatalf("Location = %q, want the signed URL", location)
	}
	// A redirect is temporary by nature and this one carries a credential, so no cache
	// may keep it.
	if cache := response.Header.Get("Cache-Control"); !strings.Contains(cache, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store: the signed URL expires and is a credential", cache)
	}
	// The URL is a credential: it belongs in the Location header and nowhere else, so it
	// cannot end up in a page body, a log line or a copied link.
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the response body: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("the redirect carries the body %q, want none", body)
	}
	if strings.Contains(string(body), "X-Amz-Signature") || strings.Contains(string(body), signedURL) {
		t.Fatalf("the response body carries the signed URL: %s", body)
	}
	if len(pdfs.asked) != 1 || pdfs.asked[0] != ticketID {
		t.Fatalf("the route asked for %v, want exactly the requested ticket %q", pdfs.asked, ticketID)
	}
}

// TestTicketPDFRouteAnswersAnUnknownTicketWith404 pins that an unknown ticket is the
// same 404 the detail route answers: the identifier is the capability, so a wrong guess
// and an identifier that never existed must be indistinguishable, and no link is signed
// for one.
func TestTicketPDFRouteAnswersAnUnknownTicketWith404(t *testing.T) {
	const unknown = "00000000-0000-0000-0000-00000000ffff"
	notFound := fmt.Errorf("%w: %s", tickets.ErrNotFound, unknown)

	ts := mountedTicketPDFServer(t,
		signedTicketService(t, &fakeTicketReader{err: notFound}),
		&fakeTicketPDFs{err: notFound})

	detail := getTicket(t, ts, unknown)
	detailBody := readBody(t, detail)
	if detail.StatusCode != http.StatusNotFound {
		t.Fatalf("detail status = %d, want 404 (body %s)", detail.StatusCode, detailBody)
	}

	response := getTicketPDF(t, ts, unknown)
	pdfBody := readBody(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("pdf status = %d, want 404 (body %s)", response.StatusCode, pdfBody)
	}
	if location := response.Header.Get("Location"); location != "" {
		t.Fatalf("an unknown ticket answered the redirect %q", location)
	}
	// The two routes answer one problem document, so a client needs one error shape.
	if string(pdfBody) != string(detailBody) {
		t.Fatalf("the pdf route answers %s, want the detail route's document %s", pdfBody, detailBody)
	}
	if strings.Contains(string(pdfBody), unknown) {
		t.Fatalf("the problem detail echoed the identifier: %s", pdfBody)
	}
}

// TestTicketPDFRouteAnswersAStorageFailureWith503 pins the failure contract: an
// artefact that cannot be produced is a retryable 503, never a redirect to a link that
// would not work and never an empty document.
func TestTicketPDFRouteAnswersAStorageFailureWith503(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"the object store is unavailable", fmt.Errorf("%w: bucket unreachable", pdf.ErrUnavailable)},
		{"the archive is not configured", fmt.Errorf("%w: no bucket", pdf.ErrNotConfigured)},
		{"an unclassified failure", errors.New("unexpected")},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ts := mountedTicketPDFServer(t,
				signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}),
				&fakeTicketPDFs{err: testCase.err})

			response := getTicketPDF(t, ts, fixtureTicket().TicketID)
			raw := readBody(t, response)
			if response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, raw)
			}
			if location := response.Header.Get("Location"); location != "" {
				t.Fatalf("a failed download answered the redirect %q", location)
			}
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
			// A failure never leaks the artefact: no URL, no document, no ticket fields.
			for _, leaked := range []string{"X-Amz-Signature", "%PDF-", "qrToken", "orderAmountCents"} {
				if strings.Contains(string(raw), leaked) {
					t.Fatalf("the 503 body carries %q: %s", leaked, raw)
				}
			}
		})
	}
}

// TestTicketPDFRouteIsNotMountedWithoutArtefactStorage pins the deployment rule: the
// route is mounted only when both the ticket service and the object store are
// configured. A deployment without storage must not advertise a download it cannot
// serve, while the ticket detail route keeps working.
func TestTicketPDFRouteIsNotMountedWithoutArtefactStorage(t *testing.T) {
	cases := []struct {
		name  string
		mount func(*publicapi.Handler) *publicapi.Handler
	}{
		{
			name: "a ticket service without an artefact store",
			mount: func(h *publicapi.Handler) *publicapi.Handler {
				return h.WithTickets(signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))
			},
		},
		{
			name: "an artefact store without a ticket service",
			mount: func(h *publicapi.Handler) *publicapi.Handler {
				return h.WithTicketPDFs(&fakeTicketPDFs{url: signedURL})
			},
		},
		{
			name:  "neither of them",
			mount: func(h *publicapi.Handler) *publicapi.Handler { return h },
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			healthSvc := health.New(health.ProcessProbe{})
			server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
			handler := testCase.mount(publicapi.New(&fakeHoldService{}))
			ts := httptest.NewServer(server.WithPublic(handler.Mux()).Handler())
			t.Cleanup(ts.Close)

			response := getTicketPDF(t, ts, fixtureTicket().TicketID)
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("download route status = %d, want 404 when the artefact store is not configured",
					response.StatusCode)
			}

			// Preserve: the routes next to it are unaffected.
			hold := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
			hold.Body.Close()
			if hold.StatusCode == http.StatusNotFound {
				t.Fatal("the hold route disappeared with the missing download route")
			}
		})
	}
}

// TestTicketPDFRouteIsReadOnly pins the method: the route answers a GET, and anything
// else is refused by the mux rather than reaching a handler that only reads.
func TestTicketPDFRouteIsReadOnly(t *testing.T) {
	ts := mountedTicketPDFServer(t,
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}),
		&fakeTicketPDFs{url: signedURL})

	request, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/tickets/"+fixtureTicket().TicketID+"/pdf", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := redirectingClient().Do(request)
	if err != nil {
		t.Fatalf("POST the download route: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.StatusCode)
	}
}
