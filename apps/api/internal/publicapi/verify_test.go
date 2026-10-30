// Behaviour tests for the public ticket verification route: POST
// /api/v1/tickets/verify.
//
// The door scanner has no administrative token, so this route is public, and it is
// the only place a presented code becomes an answer. What these tests pin, without a
// database, is the answer per case: a code that does not verify is one 400
// `invalid_token` that names nothing; a code that verifies but disagrees with the
// stored ticket is the same 400, so probing cannot confirm that an identifier exists;
// a valid code answers the ticket, its statuses and a `valid` flag with a
// machine-readable reason when the ticket no longer admits its holder; an unreadable
// database is a retryable 503 rather than a forgery; and the submitted token never
// appears in a response.
package publicapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// postVerify submits a verification request body and returns the response.
func postVerify(t *testing.T, ts *httptest.Server, body string) *http.Response {
	t.Helper()
	response, err := http.Post(ts.URL+"/api/v1/tickets/verify", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/tickets/verify: %v", err)
	}
	return response
}

// verifyBody is the wire contract of the verification endpoint: what a door scanner
// reads. `reason` is answered only for a ticket that does not admit its holder.
type verifyBody struct {
	TicketID     string           `json:"ticketId"`
	EventID      string           `json:"eventId"`
	Seats        []ticketSeatBody `json:"seats"`
	TicketStatus string           `json:"ticketStatus"`
	EventStatus  string           `json:"eventStatus"`
	Valid        bool             `json:"valid"`
	Reason       string           `json:"reason"`
}

// servedToken reads one ticket through the service and answers the code that was
// served with it, which is exactly what the buyer would present at the door.
func servedToken(t *testing.T, svc publicapi.TicketService, ticketID string) string {
	t.Helper()
	detail, err := svc.Detail(context.Background(), ticketID)
	if err != nil {
		t.Fatalf("Detail() = unexpected error %v", err)
	}
	if detail.QRToken == "" {
		t.Fatal("the served detail carries no QR token")
	}
	return detail.QRToken
}

// decodeResponseBody reads and decodes the response into T.
func decodeResponseBody[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	raw := readBody(t, response)
	var body T
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body %s: %v", raw, err)
	}
	return body
}

// T5 (S4): a genuine code answers the ticket it names — the identifiers, the seats,
// both statuses and the validity — so the door can let the holder in.
func TestTicketVerificationAnswersAValidTicket(t *testing.T) {
	reader := &fakeTicketReader{detail: fixtureTicket()}
	svc := signedTicketService(t, reader)
	ts := mountedTicketServer(t, svc)
	token := servedToken(t, svc, fixtureTicket().TicketID)

	response := postVerify(t, ts, fmt.Sprintf(`{"token":%q}`, token))
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("verify status = %d, want 200 (body %s)", response.StatusCode, body)
	}

	raw := readBody(t, response)
	var body verifyBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode verify body %s: %v", raw, err)
	}
	if body.TicketID != fixtureTicket().TicketID {
		t.Fatalf("ticketId = %q, want %q", body.TicketID, fixtureTicket().TicketID)
	}
	if body.EventID != fixtureTicket().Event.ID {
		t.Fatalf("eventId = %q, want %q", body.EventID, fixtureTicket().Event.ID)
	}
	if len(body.Seats) != 1 || body.Seats[0] != (ticketSeatBody{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"}) {
		t.Fatalf("seats = %+v, want the ticket's own seat", body.Seats)
	}
	if body.TicketStatus != tickets.TicketStatusIssued || body.EventStatus != "on_sale" {
		t.Fatalf("statuses = %q / %q, want issued / on_sale", body.TicketStatus, body.EventStatus)
	}
	if !body.Valid {
		t.Fatal("a live issued ticket verified as not valid")
	}
	if body.Reason != "" {
		t.Fatalf("reason = %q, want no reason for a valid ticket", body.Reason)
	}

	// The key names are the door's contract, so they are pinned as a set: a valid
	// ticket answers no `reason` at all, and nothing else may be added silently.
	keys := objectKeys(t, raw)
	sort.Strings(keys)
	if got, want := keys, []string{"eventId", "eventStatus", "seats", "ticketId", "ticketStatus", "valid"}; !equalKeys(got, want) {
		t.Fatalf("verify keys = %v, want %v", got, want)
	}
}

// T5 (S4): a ticket that no longer admits its holder still verifies — the code is
// genuine — and the answer says why in a machine-readable reason, so the door can
// tell a cancelled ticket from a forgery.
func TestTicketVerificationExplainsWhyATicketIsNotValid(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*tickets.TicketDetail)
		wantReason string
		wantTicket string
		wantEvent  string
	}{
		{
			name:       "a voided ticket",
			mutate:     func(detail *tickets.TicketDetail) { detail.Status = tickets.TicketStatusVoid },
			wantReason: tickets.InvalidReasonVoided,
			wantTicket: tickets.TicketStatusVoid,
			wantEvent:  "on_sale",
		},
		{
			name: "a ticket of a cancelled screening",
			mutate: func(detail *tickets.TicketDetail) {
				detail.Event.Status = tickets.EventStatusCancelled
			},
			wantReason: tickets.InvalidReasonEventCancelled,
			wantTicket: tickets.TicketStatusIssued,
			wantEvent:  tickets.EventStatusCancelled,
		},
		{
			name: "a voided ticket of a cancelled screening",
			mutate: func(detail *tickets.TicketDetail) {
				detail.Status = tickets.TicketStatusVoid
				detail.Event.Status = tickets.EventStatusCancelled
			},
			wantReason: tickets.InvalidReasonVoided,
			wantTicket: tickets.TicketStatusVoid,
			wantEvent:  tickets.EventStatusCancelled,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			detail := fixtureTicket()
			testCase.mutate(&detail)
			svc := signedTicketService(t, &fakeTicketReader{detail: detail})
			ts := mountedTicketServer(t, svc)
			token := servedToken(t, svc, detail.TicketID)

			response := postVerify(t, ts, fmt.Sprintf(`{"token":%q}`, token))
			if response.StatusCode != http.StatusOK {
				body := readBody(t, response)
				t.Fatalf("status = %d, want 200: a genuine code is a successful answer (body %s)",
					response.StatusCode, body)
			}
			body := decodeResponseBody[verifyBody](t, response)
			if body.Valid {
				t.Fatalf("valid = true for %s, want false", testCase.name)
			}
			if body.Reason != testCase.wantReason {
				t.Fatalf("reason = %q, want %q", body.Reason, testCase.wantReason)
			}
			// The answer still carries the stored facts, so the door can show the
			// usher what the ticket was.
			if body.TicketStatus != testCase.wantTicket || body.EventStatus != testCase.wantEvent {
				t.Fatalf("statuses = %q / %q, want %q / %q",
					body.TicketStatus, body.EventStatus, testCase.wantTicket, testCase.wantEvent)
			}
			if body.TicketID == "" || body.EventID == "" || len(body.Seats) != 1 {
				t.Fatalf("an invalid ticket lost its identifiers: %+v", body)
			}
		})
	}
}

// T5 (S4): every code that is not a ticket of ours gets one 400 `invalid_token`, and
// the same one whether or not the ticket it names exists: a forged signature, a
// tampered payload, an unknown ticket and a ticket whose stored event disagrees all
// answer the same 400, and none of them echoes the submitted token.
func TestTicketVerificationRejectsEveryCodeThatIsNotATicket(t *testing.T) {
	genuine := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
	token := servedToken(t, genuine, fixtureTicket().TicketID)
	forged := token[:strings.Index(token, ".")+1] + strings.Repeat("A", 43)

	// The stored ticket of the mismatch case is the same ticket with a different
	// screening: the code is genuine, the sale it names is not the one stored.
	disagreeing := fixtureTicket()
	disagreeing.Event.ID = "00000000-0000-0000-0000-00000000f0ff"

	cases := []struct {
		name string
		svc  publicapi.TicketService
		body string
	}{
		{
			name: "a token that is not a token",
			svc:  genuine,
			body: `{"token":"not-a-token"}`,
		},
		{
			name: "a token with a forged signature",
			svc:  genuine,
			body: fmt.Sprintf(`{"token":%q}`, forged),
		},
		{
			name: "a token with a tampered payload",
			svc:  genuine,
			body: fmt.Sprintf(`{"token":%q}`, tamperPayload(t, token)),
		},
		{
			name: "a token that names no stored ticket",
			svc:  signedTicketService(t, &fakeTicketReader{err: fmt.Errorf("%w: gone", tickets.ErrNotFound)}),
			body: fmt.Sprintf(`{"token":%q}`, token),
		},
		{
			name: "a token whose signed event disagrees with the stored ticket",
			svc:  signedTicketService(t, &fakeTicketReader{detail: disagreeing}),
			body: fmt.Sprintf(`{"token":%q}`, token),
		},
		{
			name: "an empty token",
			svc:  genuine,
			body: `{"token":""}`,
		},
	}

	var reference string
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := postVerify(t, mountedTicketServer(t, testCase.svc), testCase.body)
			if response.StatusCode != http.StatusBadRequest {
				body := readBody(t, response)
				t.Fatalf("status = %d, want 400 (body %s)", response.StatusCode, body)
			}
			raw := readBody(t, response)
			var problem problemBody
			if err := json.Unmarshal(raw, &problem); err != nil {
				t.Fatalf("decode problem %s: %v", raw, err)
			}
			if problem.Code != "invalid_token" || problem.Status != http.StatusBadRequest {
				t.Fatalf("problem = %+v, want 400 invalid_token", problem)
			}

			// The answer is the same for every case, so a caller cannot learn from the
			// shape of the refusal that a ticket exists.
			if reference == "" {
				reference = string(raw)
			} else if string(raw) != reference {
				t.Fatalf("the refusal differs per case:\n%s\nwant\n%s", raw, reference)
			}

			// The submitted token, and anything a token carries, is never echoed.
			presented := presentedToken(t, testCase.body)
			if presented != "" && strings.Contains(string(raw), presented) {
				t.Fatalf("the response echoed the submitted token: %s", raw)
			}
		})
	}
}

// T5 (S4): an unreadable database is a retryable 503, not a verdict on the code: a
// door that cannot check anything must not be told the ticket is a forgery.
func TestTicketVerificationAnswersAnUnreadableStoreWith503(t *testing.T) {
	// The code is genuine, so the service gets past the signature and asks the store —
	// which cannot answer.
	signer, err := tickets.NewSigner(ticketRouteQRKey)
	if err != nil {
		t.Fatalf("NewSigner() = unexpected error %v", err)
	}
	token, err := signer.Sign(fixtureTicket())
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	body := fmt.Sprintf(`{"token":%q}`, token)

	svc := signedTicketService(t, &fakeTicketReader{err: fmt.Errorf("%w: conn closed", tickets.ErrUnavailable)})
	response := postVerify(t, mountedTicketServer(t, svc), body)
	if response.StatusCode != http.StatusServiceUnavailable {
		raw := readBody(t, response)
		t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, raw)
	}
	problem := decodeResponseBody[problemBody](t, response)
	if problem.Code != "service_unavailable" || problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("problem = %+v, want 503 service_unavailable", problem)
	}

	// A deployment whose signer was never built cannot check a code either, and that
	// is our failure rather than a forged ticket.
	unconfigured := tickets.NewService(&fakeTicketReader{detail: fixtureTicket()}, nil)
	response = postVerify(t, mountedTicketServer(t, unconfigured), body)
	if response.StatusCode != http.StatusServiceUnavailable {
		raw := readBody(t, response)
		t.Fatalf("status with no signer = %d, want 503 (body %s)", response.StatusCode, raw)
	}
}

// T5 (S4): the request is bounded and its shape is fixed. A body that is not JSON
// carrying a token, or a body larger than the bound, is a client error that echoes
// nothing of what was sent; the bound exists because this route is public.
func TestTicketVerificationRejectsAMalformedOrOversizedRequest(t *testing.T) {
	svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
	ts := mountedTicketServer(t, svc)

	oversized := strings.Repeat("t", 8<<10)
	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"an empty body", "", "invalid_request"},
		{"a body that is not JSON", "token=abc", "invalid_request"},
		{"a token that is not a string", `{"token":42}`, "invalid_request"},
		{"an oversized token", fmt.Sprintf(`{"token":%q}`, oversized), "invalid_request"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := postVerify(t, ts, testCase.body)
			if response.StatusCode != http.StatusBadRequest {
				body := readBody(t, response)
				t.Fatalf("status = %d, want 400 (body %s)", response.StatusCode, body)
			}
			raw := readBody(t, response)
			var problem problemBody
			if err := json.Unmarshal(raw, &problem); err != nil {
				t.Fatalf("decode problem %s: %v", raw, err)
			}
			if problem.Code != testCase.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, testCase.wantCode)
			}
			if strings.Contains(string(raw), oversized) {
				t.Fatalf("the response echoed the oversized body: %s", raw)
			}
		})
	}
}

// Preserve: a deployment without a ticket service mounts no verification route, and
// the routes next to it keep working.
func TestTicketVerificationIsNotMountedWithoutATicketService(t *testing.T) {
	ts := mountedTicketServer(t, nil)

	response := postVerify(t, ts, `{"token":"whatever"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("verify status = %d, want 404 without a ticket service", response.StatusCode)
	}

	hold := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
	hold.Body.Close()
	if hold.StatusCode == http.StatusNotFound {
		t.Fatal("the hold route disappeared when the ticket service was not wired")
	}
}

// tamperPayload rewrites the ticket id inside a genuine token and keeps its
// signature, which is what a photograph of an edited code looks like.
func tamperPayload(t *testing.T, token string) string {
	t.Helper()
	payloadPart, signature, _ := strings.Cut(token, ".")
	decoded, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		t.Fatalf("decode the payload of %q: %v", token, err)
	}
	fields := strings.Split(string(decoded), "|")
	if len(fields) != 4 {
		t.Fatalf("payload %q does not carry four fields", decoded)
	}
	fields[1] = "00000000-0000-0000-0000-00000000f0ff"
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, "|"))) + "." + signature
}

// presentedToken extracts the token a request body carries, so a test can assert the
// response never contains it.
func presentedToken(t *testing.T, body string) string {
	t.Helper()
	var request struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		return ""
	}
	return request.Token
}
