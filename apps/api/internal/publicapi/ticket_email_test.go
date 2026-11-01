// Behaviour tests for the resend route: POST /api/v1/tickets/{ticketId}/email (T5, S2, S3,
// S5).
//
// It is the API half of the ticket page's "send it to this address" action: the same
// purchase email the checkout sends, to an address typed at that moment, and never stored as
// a second address of the order. What that makes possible is a relay — anybody holding a
// ticket identifier could post any mailbox to it — so the limit, the refusals and the fact
// that the address is not persisted are the subject here, not the happy path.
//
// Everything is a fake: a fake provider, a fake counter and the fakes the ticket and
// reservation routes already use. No database, no Redis and no mail server.
package publicapi_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"cinema.local/ticketing/apps/api/internal/email"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

const (
	// ticketEmailLinkKey signs the link the resend mails. It is longer than the documented
	// minimum and never the QR key: the two have different lifetimes.
	ticketEmailLinkKey = "ticket-email-route-link-key-0123456789abcdef"
	// ticketEmailWebOrigin is the public web origin the mailed link is built from.
	ticketEmailWebOrigin = "https://cinema.test"
	// ticketEmailRecipient is the address a buyer types on the ticket page. It is on a
	// throwaway domain and it is deliberately different from any stored address: the whole
	// point of the route is that this one is not the order's.
	ticketEmailRecipient = "colleague@example.com"
)

// ticketEmailLinks builds the link the mailed message must carry, with a real signer, so a
// test can verify what the recipient would receive instead of trusting a fixture.
func ticketEmailLinks(t *testing.T) (*tickets.LinkSigner, email.LinkBuilder) {
	t.Helper()
	signer, err := tickets.NewLinkSigner(ticketEmailLinkKey, time.Hour)
	if err != nil {
		t.Fatalf("NewLinkSigner() = unexpected error %v", err)
	}
	return signer, func(reservationID string) (string, error) {
		token, err := signer.Sign(reservationID, time.Now())
		if err != nil {
			return "", err
		}
		return ticketEmailWebOrigin + "/t/" + token, nil
	}
}

// ticketEmailFakeSender records every message the action asked a provider to send. It is the
// one place the address is allowed to appear, which is what the "nothing is stored" test
// asserts against.
type ticketEmailFakeSender struct {
	messages []email.Message
	failWith error
}

func (s *ticketEmailFakeSender) Send(_ context.Context, message email.Message) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.messages = append(s.messages, message)
	return nil
}

// ticketEmailFakeLimiter is the anti-relay bound in memory, counting per reservation the way
// the deployed one does.
type ticketEmailFakeLimiter struct {
	limit      int
	retryAfter time.Duration
	err        error

	asked  []string
	counts map[string]int
}

func (l *ticketEmailFakeLimiter) Allow(_ context.Context, reservationID string) (bool, time.Duration, error) {
	l.asked = append(l.asked, reservationID)
	if l.err != nil {
		return false, 0, l.err
	}
	if l.counts == nil {
		l.counts = make(map[string]int)
	}
	l.counts[reservationID]++
	return l.counts[reservationID] <= l.limit, l.retryAfter, nil
}

// ticketEmailFakeAction answers whatever one test configures, so a mapping no real wiring can
// reach from a test — a deployment with no mail provider — is still pinned at the edge.
type ticketEmailFakeAction struct {
	err   error
	calls int
}

func (a *ticketEmailFakeAction) Resend(_ context.Context, _, _ string) error {
	a.calls++
	return a.err
}

// mustResender builds the action over the fakes and the shared link builder.
func mustResender(t *testing.T, sender email.Sender, limiter email.ResendLimiter) *email.Resender {
	t.Helper()
	_, link := ticketEmailLinks(t)
	resender, err := email.NewResender(sender, link, limiter)
	if err != nil {
		t.Fatalf("NewResender() = unexpected error %v", err)
	}
	return resender
}

// mountedTicketEmailServer wires the public subtree the way cmd/api does: the resend route
// beside the ticket and reservation reads it resolves the order through, never behind the
// administrative guard. A nil collaborator is how the "not mounted" cases are built.
func mountedTicketEmailServer(t *testing.T, action publicapi.TicketEmailService,
	reader publicapi.ReservationService, ticketService publicapi.TicketService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).
		WithTickets(ticketService).WithReservations(reader).WithTicketEmail(action).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// postTicketEmail asks the route to mail the ticket's link to whatever body says.
func postTicketEmail(t *testing.T, ts *httptest.Server, ticketID, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/tickets/"+ticketID+"/email",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", request.URL, err)
	}
	return response
}

// readTicketEmailAnswer asks the route once and answers its status and raw body, so two
// refusals can be compared byte for byte.
func readTicketEmailAnswer(t *testing.T, ts *httptest.Server, ticketID, body string) (int, []byte) {
	t.Helper()
	response := postTicketEmail(t, ts, ticketID, body)
	return response.StatusCode, readBody(t, response)
}

// fixtureTicketEmailOrder is the paid order the fixture ticket was sold in. Its identifier is
// the ticket's own reservation, because that is the order the route resolves and mails.
func fixtureTicketEmailOrder() catalog.ReservationDetail {
	return catalog.ReservationDetail{
		ID:        fixtureTicket().ReservationID,
		Status:    "paid",
		TicketIDs: []string{fixtureTicket().TicketID},
	}
}

// T5 (S2, S3): one valid request mails exactly one message — the same email the checkout
// sends, carrying a link that verifies and names the ticket's own order — and answers 202,
// because the send has already happened by the time the answer is written.
func TestTicketEmailRouteSendsTheSignedLinkOnce(t *testing.T) {
	signer, link := ticketEmailLinks(t)
	sender := &ticketEmailFakeSender{}
	resender, err := email.NewResender(sender, link, &ticketEmailFakeLimiter{limit: email.ResendLimit})
	if err != nil {
		t.Fatalf("NewResender() = unexpected error %v", err)
	}
	ts := mountedTicketEmailServer(t, resender, &fakeReservationReader{detail: fixtureTicketEmailOrder()},
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

	response := postTicketEmail(t, ts, fixtureTicket().TicketID, `{"email":"`+ticketEmailRecipient+`"}`)
	raw := readBody(t, response)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", response.StatusCode, raw)
	}
	if got := objectKeys(t, raw); !equalKeys(got, []string{"sent"}) {
		t.Fatalf("answer keys = %v, want only sent", got)
	}
	if got := string(objectField(t, raw, "sent")); got != "true" {
		t.Fatalf("sent = %s, want true", got)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("the provider was handed %d message(s), want exactly one", len(sender.messages))
	}
	sent := sender.messages[0]
	if sent.To != ticketEmailRecipient {
		t.Fatalf("To = %q, want the address this request named", sent.To)
	}
	if sent.Subject != email.Subject {
		t.Fatalf("subject = %q, want the purchase email's %q", sent.Subject, email.Subject)
	}

	// The body carries a link this deployment signed, and it opens the ticket's order: a
	// link that opened something else would mail the buyer somebody else's tickets.
	mailLink := ""
	for _, line := range strings.Split(sent.Body, "\n") {
		if strings.HasPrefix(line, ticketEmailWebOrigin+"/t/") {
			mailLink = line
		}
	}
	if mailLink == "" {
		t.Fatalf("body = %q, want a line with the signed link", sent.Body)
	}
	claims, err := signer.Verify(strings.TrimPrefix(mailLink, ticketEmailWebOrigin+"/t/"), time.Now())
	if err != nil {
		t.Fatalf("the mailed link does not verify: %v", err)
	}
	if claims.ReservationID != fixtureTicketEmailOrder().ID {
		t.Fatalf("the mailed link names reservation %q, want %q", claims.ReservationID, fixtureTicketEmailOrder().ID)
	}
	// The link is a credential: it is mailed, never echoed back to the caller.
	if strings.Contains(string(raw), "/t/") {
		t.Fatalf("the answer echoes the link: %s", raw)
	}
}

// T5 (S5): a provider that fails is never reported as sent. A 202 for a message that was
// never accepted is the one lie this route must not tell, because the caller's next step is
// to close the page and tell somebody to check their inbox.
func TestTicketEmailRouteNeverReportsAFailedSendAsSent(t *testing.T) {
	sender := &ticketEmailFakeSender{failWith: fmt.Errorf("%w: smtp refused the message", email.ErrUnavailable)}
	ts := mountedTicketEmailServer(t, mustResender(t, sender, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
		&fakeReservationReader{detail: fixtureTicketEmailOrder()},
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

	response := postTicketEmail(t, ts, fixtureTicket().TicketID, `{"email":"`+ticketEmailRecipient+`"}`)
	raw := readBody(t, response)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, raw)
	}
	if strings.Contains(string(raw), `"sent"`) {
		t.Fatalf("the failure answer claims success: %s", raw)
	}
	var problem problemBody
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "service_unavailable" || problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("problem = %+v, want 503 service_unavailable", problem)
	}
}

// T5 (S5): the fourth request inside the window is refused with a 429 and the wait it has
// left, and nothing is sent. The Retry-After header is the part a client can act on, so it
// carries the counter's own remaining time in whole seconds.
func TestTicketEmailRouteRefusesOverTheLimitWithRetryAfter(t *testing.T) {
	limiter := &ticketEmailFakeLimiter{limit: email.ResendLimit, retryAfter: 42 * time.Minute}
	sender := &ticketEmailFakeSender{}
	ts := mountedTicketEmailServer(t, mustResender(t, sender, limiter),
		&fakeReservationReader{detail: fixtureTicketEmailOrder()},
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

	for attempt := 1; attempt <= email.ResendLimit; attempt++ {
		status, raw := readTicketEmailAnswer(t, ts, fixtureTicket().TicketID, `{"email":"`+ticketEmailRecipient+`"}`)
		if status != http.StatusAccepted {
			t.Fatalf("request %d of %d = %d, want 202 (body %s)", attempt, email.ResendLimit, status, raw)
		}
	}

	response := postTicketEmail(t, ts, fixtureTicket().TicketID, `{"email":"`+ticketEmailRecipient+`"}`)
	raw := readBody(t, response)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %s)", response.StatusCode, raw)
	}
	wantRetryAfter := "2520" // 42 minutes, in seconds
	if got := response.Header.Get("Retry-After"); got != wantRetryAfter {
		t.Fatalf("Retry-After = %q, want %q", got, wantRetryAfter)
	}
	var problem problemBody
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "rate_limited" || problem.Status != http.StatusTooManyRequests {
		t.Fatalf("problem = %+v, want 429 rate_limited", problem)
	}
	// The body says how long to wait, because a client that only reads the body still has
	// to be able to tell the buyer something useful.
	if !strings.Contains(problem.Detail, wantRetryAfter) {
		t.Fatalf("detail = %q, want it to say how long to wait (%s seconds)", problem.Detail, wantRetryAfter)
	}
	if len(sender.messages) != email.ResendLimit {
		t.Fatalf("the provider was handed %d message(s), want exactly the %d allowed",
			len(sender.messages), email.ResendLimit)
	}
}

// T5 (S5): a counter that cannot be read refuses the send. Redis being down must not turn
// this route into an unthrottled mail sender, so the failure is a retryable 503 and the
// provider is never called.
func TestTicketEmailRouteFailsClosedWhenTheLimitCannotBeChecked(t *testing.T) {
	limiter := &ticketEmailFakeLimiter{limit: email.ResendLimit, err: fmt.Errorf("redis is down")}
	sender := &ticketEmailFakeSender{}
	ts := mountedTicketEmailServer(t, mustResender(t, sender, limiter),
		&fakeReservationReader{detail: fixtureTicketEmailOrder()},
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

	response := postTicketEmail(t, ts, fixtureTicket().TicketID, `{"email":"`+ticketEmailRecipient+`"}`)
	raw := readBody(t, response)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, raw)
	}
	var problem problemBody
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "service_unavailable" {
		t.Fatalf("problem = %+v, want service_unavailable", problem)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("the provider was handed %d message(s), want none", len(sender.messages))
	}
}

// T5 (S5, S2): a deployment with no mail provider says so instead of pretending. The route
// is only mounted where a provider exists, and the action's own refusal is the second half:
// a deployment that cannot send must never answer 202.
func TestTicketEmailRouteAnswersNoMailProviderHonestly(t *testing.T) {
	action := &ticketEmailFakeAction{err: fmt.Errorf("%w: no mail provider is wired", email.ErrNotConfigured)}
	ts := mountedTicketEmailServer(t, action, &fakeReservationReader{detail: fixtureTicketEmailOrder()},
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

	response := postTicketEmail(t, ts, fixtureTicket().TicketID, `{"email":"`+ticketEmailRecipient+`"}`)
	raw := readBody(t, response)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, raw)
	}
	var problem problemBody
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "service_unavailable" {
		t.Fatalf("problem = %+v, want service_unavailable", problem)
	}
	// It says what is true: this deployment does not send mail. "Try again later" would be
	// a promise nobody configured.
	if !strings.Contains(problem.Detail, "does not send mail") {
		t.Fatalf("detail = %q, want it to say this deployment does not send mail", problem.Detail)
	}
}

// T5 (S2): an unknown ticket and a ticket of an order that was never paid are the same
// refusal, byte for byte, and it is the ticket route's own unknown-ticket 404. A caller
// must not learn whether an identifier exists, and a hold that was never paid sold nothing
// to mail anyway.
func TestTicketEmailRouteCannotTellAnUnknownTicketFromAnUnpaidOrder(t *testing.T) {
	unknown := mountedTicketEmailServer(t,
		mustResender(t, &ticketEmailFakeSender{}, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
		&fakeReservationReader{detail: fixtureTicketEmailOrder()},
		signedTicketService(t, &fakeTicketReader{err: fmt.Errorf("%w: gone", tickets.ErrNotFound)}))

	// The reference: what the ticket detail route beside it answers for a ticket it does
	// not know. The resend must not spell "there is nothing here" any differently.
	reference := getTicket(t, unknown, fixtureTicket().TicketID)
	referenceStatus := reference.StatusCode
	referenceBody := readBody(t, reference)
	if referenceStatus != http.StatusNotFound {
		t.Fatalf("the ticket route answered %d for an unknown ticket, want 404 (body %s)",
			referenceStatus, referenceBody)
	}

	unknownStatus, unknownBody := readTicketEmailAnswer(t, unknown, fixtureTicket().TicketID,
		`{"email":"`+ticketEmailRecipient+`"}`)
	if unknownStatus != referenceStatus {
		t.Fatalf("unknown ticket status = %d, want %d", unknownStatus, referenceStatus)
	}
	if string(unknownBody) != string(referenceBody) {
		t.Fatalf("unknown ticket answer = %s, want the ticket route's %s", unknownBody, referenceBody)
	}

	// A ticket that exists but whose order was never paid: the same answer, so a prober
	// learns nothing from the difference between "no such ticket" and "not sold".
	for _, status := range []string{"held", "payment_pending", "expired", "cancelled"} {
		t.Run("an order that is "+status, func(t *testing.T) {
			unpaid := mountedTicketEmailServer(t,
				mustResender(t, &ticketEmailFakeSender{}, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
				&fakeReservationReader{detail: catalog.ReservationDetail{
					ID: fixtureTicket().ReservationID, Status: status}},
				signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

			gotStatus, gotBody := readTicketEmailAnswer(t, unpaid, fixtureTicket().TicketID,
				`{"email":"`+ticketEmailRecipient+`"}`)
			if gotStatus != referenceStatus || string(gotBody) != string(referenceBody) {
				t.Fatalf("answer = %d %s, want the unknown-ticket %d %s",
					gotStatus, gotBody, referenceStatus, referenceBody)
			}
		})
	}

	// A reservation read that cannot answer is our failure, retryable, and is never a
	// verdict on the ticket.
	broken := mountedTicketEmailServer(t,
		mustResender(t, &ticketEmailFakeSender{}, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
		&fakeReservationReader{err: fmt.Errorf("%w: conn closed", catalog.ErrUnavailable)},
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))
	if status, body := readTicketEmailAnswer(t, broken, fixtureTicket().TicketID,
		`{"email":"`+ticketEmailRecipient+`"}`); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", status, body)
	}
}

// T5 (S2): an address the API's own rule refuses is a 400, and nothing is sent. The rule is
// the checkout's — imported, not copied — so the route cannot start accepting an address the
// purchase form would reject, and an unknown field is refused rather than dropped.
func TestTicketEmailRouteRefusesAnUnusableAddress(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no body at all", ""},
		{"an empty object", `{}`},
		{"an empty address", `{"email":""}`},
		{"an address with no at-sign", `{"email":"not-an-address"}`},
		{"a domain with no dot", `{"email":"someone@localhost"}`},
		{"an address with whitespace inside", `{"email":"some one@example.com"}`},
		{"an address longer than the RFC allows", `{"email":"` + strings.Repeat("a", 320) + `@example.com"}`},
		{"an unknown field beside the address", `{"email":"` + ticketEmailRecipient + `","name":"Ana"}`},
		{"a body that is not JSON", `mail me`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sender := &ticketEmailFakeSender{}
			limiter := &ticketEmailFakeLimiter{limit: email.ResendLimit}
			ts := mountedTicketEmailServer(t, mustResender(t, sender, limiter),
				&fakeReservationReader{detail: fixtureTicketEmailOrder()},
				signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

			response := postTicketEmail(t, ts, fixtureTicket().TicketID, testCase.body)
			raw := readBody(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", response.StatusCode, raw)
			}
			var problem problemBody
			if err := json.Unmarshal(raw, &problem); err != nil {
				t.Fatalf("decode problem %s: %v", raw, err)
			}
			if problem.Code != "invalid_input" || problem.Status != http.StatusBadRequest {
				t.Fatalf("problem = %+v, want 400 invalid_input", problem)
			}
			if len(sender.messages) != 0 {
				t.Fatalf("the provider was handed %d message(s), want none", len(sender.messages))
			}
			// The refusal happens before the counter, so a rejected request never costs the
			// order one of its three sends an hour.
			if len(limiter.asked) != 0 {
				t.Fatalf("the counter was asked about %q for a refused address", limiter.asked)
			}
		})
	}

	// And the refusal comes before any read, so it cannot become a probe for identifiers: a
	// bad address is refused the same way for a ticket this deployment knows and for one it
	// does not, without the ticket read ever being asked.
	reader := &fakeTicketReader{detail: fixtureTicket()}
	unknown := mountedTicketEmailServer(t,
		mustResender(t, &ticketEmailFakeSender{}, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
		&fakeReservationReader{detail: fixtureTicketEmailOrder()}, signedTicketService(t, reader))
	if status, raw := readTicketEmailAnswer(t, unknown, fixtureTicket().TicketID,
		`{"email":"not-an-address"}`); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want the address refusal to come before any read (body %s)", status, raw)
	}
	if reader.calls != 0 {
		t.Fatalf("the ticket was read %d time(s) for an address that was already refused", reader.calls)
	}
}

// T5 (S2): the address is never stored. It appears in exactly one place — the message handed
// to the provider — and the log line names the order and the ticket instead, because a
// second address of the order would be PII the buyer never asked the product to keep.
func TestTicketEmailRouteStoresNoAddress(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	sender := &ticketEmailFakeSender{}
	limiter := &ticketEmailFakeLimiter{limit: email.ResendLimit}
	reader := &fakeReservationReader{detail: fixtureTicketEmailOrder()}
	ts := mountedTicketEmailServer(t, mustResender(t, sender, limiter), reader,
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))

	status, raw := readTicketEmailAnswer(t, ts, fixtureTicket().TicketID, `{"email":"`+ticketEmailRecipient+`"}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", status, raw)
	}

	// The message is the only record of the address, and it is the provider's, not ours.
	if len(sender.messages) != 1 || sender.messages[0].To != ticketEmailRecipient {
		t.Fatalf("the provider was handed %v, want one message to %q", sender.messages, ticketEmailRecipient)
	}
	// Nothing durable was given it: the reads and the counter were asked about the
	// reservation, by identifier, and about nothing else.
	if got := reader.reservationID; got != fixtureTicketEmailOrder().ID {
		t.Fatalf("the reservation read was given %q, want the order's identifier", got)
	}
	for _, asked := range limiter.asked {
		if strings.Contains(asked, "@") {
			t.Fatalf("the counter was asked about %q, which looks like an address", asked)
		}
		if asked != fixtureTicketEmailOrder().ID {
			t.Fatalf("the counter was asked about %q, want the order's identifier", asked)
		}
	}

	// The request log line names the reservation and the ticket, so an operator can follow a
	// resend — and carries neither the address nor the link, which are the two things a log
	// is not allowed to keep.
	line := logged.String()
	if !strings.Contains(line, "ticket email resent") {
		t.Fatalf("log = %q, want a line about the resend", line)
	}
	if !strings.Contains(line, fixtureTicketEmailOrder().ID) || !strings.Contains(line, fixtureTicket().TicketID) {
		t.Fatalf("log = %q, want it to name reservation %q and ticket %q",
			line, fixtureTicketEmailOrder().ID, fixtureTicket().TicketID)
	}
	for _, secret := range []string{ticketEmailRecipient, "/t/"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log = %q, want no %q in it", line, secret)
		}
	}
}

// T5 (S5): the route needs the three pieces it is built from — the action, the ticket read
// and the order read — so a deployment missing any of them advertises nothing, and the
// routes next to it keep answering.
func TestTicketEmailRouteIsNotMountedWithoutItsCollaborators(t *testing.T) {
	body := `{"email":"` + ticketEmailRecipient + `"}`
	cases := []struct {
		name string
		ts   *httptest.Server
	}{
		{"without the action", mountedTicketEmailServer(t, nil,
			&fakeReservationReader{detail: fixtureTicketEmailOrder()},
			signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))},
		{"without the ticket read", mountedTicketEmailServer(t,
			mustResender(t, &ticketEmailFakeSender{}, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
			&fakeReservationReader{detail: fixtureTicketEmailOrder()}, nil)},
		{"without the order read", mountedTicketEmailServer(t,
			mustResender(t, &ticketEmailFakeSender{}, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
			nil, signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if status, raw := readTicketEmailAnswer(t, testCase.ts, fixtureTicket().TicketID, body); status != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 like a missing route (body %s)", status, raw)
			}
		})
	}

	// Preserve: with the route mounted, the ticket detail route beside it still answers, and
	// the resend changed nothing about it.
	mounted := mountedTicketEmailServer(t,
		mustResender(t, &ticketEmailFakeSender{}, &ticketEmailFakeLimiter{limit: email.ResendLimit}),
		&fakeReservationReader{detail: fixtureTicketEmailOrder()},
		signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}))
	response := getTicket(t, mounted, fixtureTicket().TicketID)
	if response.StatusCode != http.StatusOK {
		raw := readBody(t, response)
		t.Fatalf("ticket detail status = %d, want 200 beside the resend route (body %s)", response.StatusCode, raw)
	}
}
