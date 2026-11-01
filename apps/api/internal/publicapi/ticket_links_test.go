// Behaviour tests for the signed ticket link route: GET /api/v1/ticket-links/{token}
// (T4, S3).
//
// The link is what a purchase email carries, so it is the one route whose token a
// stranger holds long after the sale. What is pinned here, without a database:
//
//   - a genuine link answers the order's ticket list, in one deterministic order, and the
//     instant it stops opening;
//   - a forged, malformed, tampered or expired link is the *same* opaque 404 body, byte
//     for byte, so a caller cannot tell an aged-out email from a guess;
//   - only a paid order opens: an unpaid, unknown or ticketless reservation is that same
//     404, and a store that cannot answer is a retryable 503 instead;
//   - the token never appears in a body, and the route is mounted only when both halves
//     it needs are wired.
package publicapi_test

import (
	"encoding/json"
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
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// ticketLinkRouteKey is the throwaway key the route tests sign links with. It is longer
// than the documented minimum and distinct from any other key in these tests on purpose:
// the link key is never the QR key.
const ticketLinkRouteKey = "ticket-link-route-key-0123456789abcdef"

// ticketLinkTTL is the lifetime the route tests sign with. Seven days is the documented
// demo default, so the answer's expiry is also the production spelling.
const ticketLinkTTL = 7 * 24 * time.Hour

// ticketLinkReservation is the order the link names in every fixture.
const ticketLinkReservation = "00000000-0000-0000-0000-00000000f4a1"

// mustLinkSigner builds the signer a deployment mounts and fails the test when it is
// refused.
func mustLinkSigner(t *testing.T) *tickets.LinkSigner {
	t.Helper()
	signer, err := tickets.NewLinkSigner(ticketLinkRouteKey, ticketLinkTTL)
	if err != nil {
		t.Fatalf("NewLinkSigner() = unexpected error %v", err)
	}
	return signer
}

// tripTicketLink is the link of one reservation, signed now, and the expiry the answer
// must repeat. The instant is read once, so a test never compares against a second call
// to the clock.
func tripTicketLink(t *testing.T, signer *tickets.LinkSigner, reservationID string) (string, string) {
	t.Helper()
	signedAt := time.Now()
	token, err := signer.Sign(reservationID, signedAt)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	// The signer truncates the computed expiry to whole seconds, which is the spelling
	// the answer must carry: RFC 3339 in UTC, with no fraction.
	return token, signedAt.Add(ticketLinkTTL).UTC().Truncate(time.Second).Format(time.RFC3339)
}

// mountedTicketLinkServer wires the public subtree the way cmd/api does: the link route
// beside the ticket and reservation reads it depends on, never behind the admin guard.
func mountedTicketLinkServer(t *testing.T, reader publicapi.ReservationService, signer *tickets.LinkSigner) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).
		WithTickets(signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})).
		WithReservations(reader).WithTicketLinks(signer).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// getTicketLink asks the link route for one token.
func getTicketLink(t *testing.T, ts *httptest.Server, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/ticket-links/"+token, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", request.URL, err)
	}
	return response
}

// ticketLinkBody is the wire contract of the opened link.
type ticketLinkBody struct {
	ReservationID string   `json:"reservationId"`
	ExpiresAt     string   `json:"expiresAt"`
	TicketIDs     []string `json:"ticketIds"`
}

// fixtureTicketLinkOrder is the paid order a genuine link opens. Its ticket list is
// deliberately not in ascending order, because the answer's order is the route's own
// promise and not something the store happens to hand over sorted.
func fixtureTicketLinkOrder() catalog.ReservationDetail {
	return catalog.ReservationDetail{
		ID:     ticketLinkReservation,
		Status: "paid",
		TicketIDs: []string{
			"00000000-0000-0000-0000-00000000f4c3",
			"00000000-0000-0000-0000-00000000f4c1",
			"00000000-0000-0000-0000-00000000f4c2",
		},
	}
}

// T4 (S3): a genuine link opens a paid order and answers exactly what the email promised
// the buyer sees: the order, its tickets in one stable order, and when the link stops
// working.
func TestTicketLinkRouteOpensAPaidOrder(t *testing.T) {
	signer := mustLinkSigner(t)
	reader := &fakeReservationReader{detail: fixtureTicketLinkOrder()}
	ts := mountedTicketLinkServer(t, reader, signer)
	token, wantExpiry := tripTicketLink(t, signer, ticketLinkReservation)

	response := getTicketLink(t, ts, token)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}

	raw := readBody(t, response)
	var body ticketLinkBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body %s: %v", raw, err)
	}
	if body.ReservationID != ticketLinkReservation {
		t.Fatalf("reservationId = %q, want %q", body.ReservationID, ticketLinkReservation)
	}
	// The ticket list is answered ascending, so two readers of the same order never see
	// two orders.
	want := []string{
		"00000000-0000-0000-0000-00000000f4c1",
		"00000000-0000-0000-0000-00000000f4c2",
		"00000000-0000-0000-0000-00000000f4c3",
	}
	if len(body.TicketIDs) != len(want) {
		t.Fatalf("ticketIds = %v, want %v", body.TicketIDs, want)
	}
	for index := range want {
		if body.TicketIDs[index] != want[index] {
			t.Fatalf("ticketIds = %v, want the ascending %v", body.TicketIDs, want)
		}
	}
	// The expiry is the one inside the signature, spelled the way the rest of the API
	// spells an instant: RFC 3339 in UTC.
	if body.ExpiresAt != wantExpiry {
		t.Fatalf("expiresAt = %q, want %q", body.ExpiresAt, wantExpiry)
	}

	// The store was asked about the reservation the *token* names, once, and the route
	// answered no key of its own: the set is the web page's contract.
	if got := atomic.LoadInt32(&reader.calls); got != 1 || reader.reservationID != ticketLinkReservation {
		t.Fatalf("Reservation() = %d call(s) about %q, want one about %q",
			got, reader.reservationID, ticketLinkReservation)
	}
	if got := objectKeys(t, raw); !equalKeys(got, []string{"expiresAt", "reservationId", "ticketIds"}) {
		t.Fatalf("link keys = %v, want expiresAt, reservationId and ticketIds", got)
	}
	// The token is a credential in a URL: it is never repeated into a body.
	if strings.Contains(string(raw), token) {
		t.Fatalf("the answer echoes the token: %s", raw)
	}
}

// T4 (S3): only a paid sale opens. An unpaid or ticketless order, an unknown one and an
// identifier no reservation could carry are all the same opaque 404 — the answer an
// expired or forged link gets — so the route never says which of them happened.
func TestTicketLinkRouteOnlyOpensAPaidOrder(t *testing.T) {
	signer := mustLinkSigner(t)
	genuine := mountedTicketLinkServer(t, &fakeReservationReader{detail: fixtureTicketLinkOrder()}, signer)
	link, _ := tripTicketLink(t, signer, ticketLinkReservation)
	forged := link[:strings.Index(link, ".")+1] + strings.Repeat("A", 43)
	forgedResponse := getTicketLink(t, genuine, forged)
	forgedStatus, forgedBody := forgedResponse.StatusCode, readBody(t, forgedResponse)
	if forgedStatus != http.StatusNotFound {
		t.Fatalf("forged link status = %d, want 404 (body %s)", forgedStatus, forgedBody)
	}

	cases := []struct {
		name   string
		reader *fakeReservationReader
	}{
		{"a hold that was never paid", &fakeReservationReader{detail: catalog.ReservationDetail{
			ID: ticketLinkReservation, Status: "held"}}},
		{"a payment still under way", &fakeReservationReader{detail: catalog.ReservationDetail{
			ID: ticketLinkReservation, Status: "payment_pending"}}},
		{"a reservation that expired unpaid", &fakeReservationReader{detail: catalog.ReservationDetail{
			ID: ticketLinkReservation, Status: "expired"}}},
		{"a reservation cancelled before paying", &fakeReservationReader{detail: catalog.ReservationDetail{
			ID: ticketLinkReservation, Status: "cancelled"}}},
		{"a paid order without tickets", &fakeReservationReader{detail: catalog.ReservationDetail{
			ID: ticketLinkReservation, Status: "paid"}}},
		{"a reservation this database does not know",
			&fakeReservationReader{err: fmt.Errorf("%w: gone", catalog.ErrNotFound)}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ts := mountedTicketLinkServer(t, testCase.reader, signer)
			status, raw := readTicketLinkAnswer(t, ts, link)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", status, raw)
			}
			// Byte-identical to the forged link's refusal: a caller cannot tell an unpaid
			// order from a guess, and neither can it tell an expired email from one.
			if string(raw) != string(forgedBody) || status != forgedStatus {
				t.Fatalf("answer = %d %s, want the forged link's %d %s",
					status, raw, forgedStatus, forgedBody)
			}
		})
	}
}

// T4 (S3): an expired link and a forged one are the same answer, byte for byte. This is
// the whole point of the expiry: it closes the link without telling anybody which of the
// two refusals happened, so probing cannot confirm that a reservation exists.
func TestTicketLinkRouteCannotTellAnExpiredLinkFromAForgedOne(t *testing.T) {
	signer := mustLinkSigner(t)
	ts := mountedTicketLinkServer(t, &fakeReservationReader{detail: fixtureTicketLinkOrder()}, signer)

	// Signed two lifetimes ago, so its window closed an hour into the past — no sleep and
	// no dependence on the clock beyond the day it runs on.
	expired, err := signer.Sign(ticketLinkReservation, time.Now().Add(-2*ticketLinkTTL))
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	genuine, _ := tripTicketLink(t, signer, ticketLinkReservation)
	// One signature byte rewritten into a different one: the payload still decodes, the
	// signature is the wrong one. The replacement is chosen so the tampered character is
	// never the one that was already there, which would leave a genuine link.
	dot := strings.Index(genuine, ".")
	replacement := "A"
	if genuine[dot+1] == 'A' {
		replacement = "B"
	}
	tampered := genuine[:dot+1] + replacement + genuine[dot+2:]

	expiredStatus, expiredBody := readTicketLinkAnswer(t, ts, expired)
	if expiredStatus != http.StatusNotFound {
		t.Fatalf("expired link status = %d, want 404 (body %s)", expiredStatus, expiredBody)
	}

	refusals := map[string]string{"the expired link": expired}
	for name, token := range map[string]string{
		"a forged link":                     genuine[:strings.Index(genuine, ".")+1] + strings.Repeat("A", 43),
		"a tampered link":                   tampered,
		"a malformed link":                  "not-a-token",
		"a link-shaped token with two dots": genuine + "." + genuine,
	} {
		status, body := readTicketLinkAnswer(t, ts, token)
		if status != expiredStatus || string(body) != string(expiredBody) {
			t.Fatalf("%s answered %d %s, want the expired link's %d %s",
				name, status, body, expiredStatus, expiredBody)
		}
		refusals[name] = token
	}

	// The refusal is a problem document in the vocabulary the rest of the public API uses,
	// and it repeats nothing of what was sent.
	var problem problemBody
	if err := json.Unmarshal(expiredBody, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", expiredBody, err)
	}
	if problem.Status != http.StatusNotFound || problem.Code == "" || problem.Type == "" {
		t.Fatalf("problem = %+v, want a complete 404 problem document", problem)
	}
	for name, token := range refusals {
		if strings.Contains(string(expiredBody), token) {
			t.Fatalf("%s: the refusal echoed the token", name)
		}
	}
	if strings.Contains(string(expiredBody), ticketLinkReservation) {
		t.Fatalf("the refusal names the reservation: %s", expiredBody)
	}
}

// T4 (S3): a store that cannot answer is retryable, never a verdict on the link. A link
// that happens to be expired must not be reported as an outage either: the signature and
// its expiry are checked before any read.
func TestTicketLinkRouteAnswersAStoreFailureWith503(t *testing.T) {
	signer := mustLinkSigner(t)
	reader := &fakeReservationReader{err: fmt.Errorf("%w: conn closed", catalog.ErrUnavailable)}
	ts := mountedTicketLinkServer(t, reader, signer)
	token, _ := tripTicketLink(t, signer, ticketLinkReservation)

	response := getTicketLink(t, ts, token)
	raw := readBody(t, response)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, raw)
	}
	var problem problemBody
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "service_unavailable" || problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("problem = %+v, want 503 service_unavailable", problem)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "ticketIds") {
		t.Fatalf("the failure answer carries link or ticket data: %s", raw)
	}

	// An expired link is refused before the store is consulted at all, so an outage can
	// never turn an aged-out email into a retry invitation.
	expired, err := signer.Sign(ticketLinkReservation, time.Now().Add(-2*ticketLinkTTL))
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	before := atomic.LoadInt32(&reader.calls)
	if status, body := readTicketLinkAnswer(t, ts, expired); status != http.StatusNotFound {
		t.Fatalf("expired link status = %d, want 404 (body %s)", status, body)
	}
	if after := atomic.LoadInt32(&reader.calls); after != before {
		t.Fatalf("Reservation() calls = %d, want the expired link to be refused without a read", after)
	}
}

// T4 (S3): the route needs both halves it is built from — a signer to check the token and
// the reservation read to resolve the tickets — and mounting it changes nothing about the
// routes next to it.
func TestTicketLinkRouteIsNotMountedWithoutItsCollaborators(t *testing.T) {
	signer := mustLinkSigner(t)
	_, link := tripTicketLink(t, signer, ticketLinkReservation)

	// Without a signer nothing can be verified, so no link route is advertised.
	noSigner := mountedTicketLinkServer(t, &fakeReservationReader{detail: fixtureTicketLinkOrder()}, nil)
	if status, _ := readTicketLinkAnswer(t, noSigner, link); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without a link signer", status)
	}

	// Without the reservation read the link could verify and resolve nothing.
	noReader := mountedTicketLinkServer(t, nil, signer)
	if status, _ := readTicketLinkAnswer(t, noReader, link); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without a reservation read", status)
	}

	// Preserve: with the link route mounted, the reservation read and the ticket detail
	// route next to it still answer.
	reader := &fakeReservationReader{detail: fixturePaidReservation()}
	mounted := mountedTicketLinkServer(t, reader, signer)
	if response := getReservation(t, mounted, fixturePaidReservation().ID); response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("reservation read status = %d, want 200 beside the link route (body %s)", response.StatusCode, body)
	}
	if response := getTicket(t, mounted, fixtureTicket().TicketID); response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("ticket read status = %d, want 200 beside the link route (body %s)", response.StatusCode, body)
	}
}

// readTicketLinkAnswer asks the link route for one token and answers its status and raw
// body, so two refusals can be compared byte for byte.
func readTicketLinkAnswer(t *testing.T, ts *httptest.Server, token string) (int, []byte) {
	t.Helper()
	response := getTicketLink(t, ts, token)
	return response.StatusCode, readBody(t, response)
}

var _ publicapi.ReservationService = (*fakeReservationReader)(nil)
