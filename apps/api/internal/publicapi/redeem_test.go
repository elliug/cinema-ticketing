// Behaviour tests for the door's admission route: POST /api/v1/tickets/redeem (S1, S3).
//
// The door presents the same signed code the verification route checks, and this route
// answers the whole act: verify the code, then admit the ticket it names exactly once.
// What is pinned here, without a database:
//
//   - a code the server cannot verify is refused with the verification route's own
//     status and body, and the redemption collaborator is never called — that ordering is
//     S3, asserted as a call count rather than as prose;
//   - a fresh admission answers the ticket, its seat, its screening and its instant;
//   - a second presentation is a *success* carrying the first instant and the first label,
//     told apart by a field rather than by a 4xx;
//   - a void ticket and a cancelled screening are two answers, in the reason vocabulary
//     the verification route already uses;
//   - a store that cannot answer is retryable, and a ticket that disappears between the
//     check and the admission keeps the verification route's refusal;
//   - a deployment missing either collaborator mounts no such route.
package publicapi_test

import (
	"context"
	"encoding/json"
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

// doorInstant is when the door admits the fixture ticket: 20:14 UTC, the hour a cashier
// has to be able to read back.
var doorInstant = time.Date(2026, time.May, 4, 20, 14, 0, 0, time.UTC)

// fakeRedeemer is the redemption collaborator. It records whether it was reached at all,
// which is how the tests prove a refused code never gets this far.
type fakeRedeemer struct {
	redemption tickets.Redemption
	err        error

	calls    int
	ticketID string
	label    string
}

func (f *fakeRedeemer) Redeem(_ context.Context, ticketID, label string) (tickets.Redemption, error) {
	f.calls++
	f.ticketID = ticketID
	f.label = label
	if f.err != nil {
		return tickets.Redemption{}, f.err
	}
	return f.redemption, nil
}

// mountedRedeemServer wires the public subtree the way cmd/api does: the admission route
// beside the verification route it shares a code with, never behind the admin guard.
func mountedRedeemServer(t *testing.T, ticketService publicapi.TicketService, redeemer publicapi.RedemptionService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).
		WithTickets(ticketService).WithRedemption(redeemer).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// redeemAnswer submits one admission request and answers its status and raw body.
func redeemAnswer(t *testing.T, ts *httptest.Server, body string) (int, []byte) {
	t.Helper()
	response, err := http.Post(ts.URL+"/api/v1/tickets/redeem", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/tickets/redeem: %v", err)
	}
	return response.StatusCode, readBody(t, response)
}

// redeemBody is the wire contract of the admission route. The screening is answered in
// both time forms for the reason the ticket read answers both, and `alreadyRedeemed` is
// the field a client reads instead of guessing from a status.
type redeemBody struct {
	TicketID        string           `json:"ticketId"`
	TicketStatus    string           `json:"ticketStatus"`
	EventID         string           `json:"eventId"`
	EventStatus     string           `json:"eventStatus"`
	FilmTitle       string           `json:"filmTitle"`
	RoomName        string           `json:"roomName"`
	CinemaName      string           `json:"cinemaName"`
	StartsAt        time.Time        `json:"startsAt"`
	StartsAtLocal   string           `json:"startsAtLocal"`
	Seats           []ticketSeatBody `json:"seats"`
	RedeemedAt      time.Time        `json:"redeemedAt"`
	RedeemedBy      string           `json:"redeemedBy"`
	AlreadyRedeemed bool             `json:"alreadyRedeemed"`
}

// freshRedeemer is the collaborator a deployment answers with when this call admitted the
// ticket: the instant it wrote and the label the door gave itself.
func freshRedeemer() *fakeRedeemer {
	return &fakeRedeemer{redemption: tickets.Redemption{
		TicketID:   fixtureTicket().TicketID,
		Outcome:    tickets.RedemptionRedeemed,
		RedeemedAt: doorInstant,
		RedeemedBy: "door-north",
	}}
}

// T7 (S1): a valid, unredeemed ticket is admitted, and the answer carries what the door
// has to show — the ticket, its seat, its screening and the instant it was admitted.
func TestTicketRedemptionAdmitsAValidTicket(t *testing.T) {
	svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
	redeemer := freshRedeemer()
	ts := mountedRedeemServer(t, svc, redeemer)

	status, raw := redeemAnswer(t, ts, fmt.Sprintf(`{"token":%q,"label":"door-north"}`,
		servedToken(t, svc, fixtureTicket().TicketID)))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, raw)
	}
	var body redeemBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body %s: %v", raw, err)
	}

	if body.TicketID != fixtureTicket().TicketID || body.TicketStatus != tickets.TicketStatusIssued {
		t.Fatalf("ticket = %q / %q, want the fixture ticket", body.TicketID, body.TicketStatus)
	}
	if len(body.Seats) != 1 || body.Seats[0] != (ticketSeatBody{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"}) {
		t.Fatalf("seats = %+v, want the ticket's own seat", body.Seats)
	}
	if body.EventID != fixtureTicket().Event.ID || body.FilmTitle != "La película" ||
		body.RoomName != "Sala 1" || body.CinemaName != "Cine Centro" || body.EventStatus != "on_sale" {
		t.Fatalf("screening = %+v, want the stored screening", body)
	}
	// Both time forms are answered, and the local one is the venue's hour, not the
	// machine's: 18:30 UTC is 20:30 in Madrid.
	if !body.StartsAt.Equal(ticketScreening) || body.StartsAtLocal != "2026-05-04T20:30:00+02:00" {
		t.Fatalf("screening times = %s / %q, want the stored instant and the venue's hour",
			body.StartsAt, body.StartsAtLocal)
	}
	if !body.RedeemedAt.Equal(doorInstant) || body.RedeemedBy != "door-north" || body.AlreadyRedeemed {
		t.Fatalf("admission = %+v, want a fresh admission at %s by this door", body, doorInstant)
	}

	// The collaborator was asked about the ticket the *code* names, never about an
	// identifier a client could have sent, and it was given this door's own label.
	if redeemer.calls != 1 || redeemer.ticketID != fixtureTicket().TicketID || redeemer.label != "door-north" {
		t.Fatalf("Redeem() = %d call(s) about %q as %q, want one about the verified ticket",
			redeemer.calls, redeemer.ticketID, redeemer.label)
	}

	// The key names are the door's contract, so they are pinned as a set.
	keys := objectKeys(t, raw)
	sort.Strings(keys)
	want := []string{"alreadyRedeemed", "cinemaName", "eventId", "eventStatus", "filmTitle",
		"redeemedAt", "redeemedBy", "roomName", "seats", "startsAt", "startsAtLocal",
		"ticketId", "ticketStatus"}
	if !equalKeys(keys, want) {
		t.Fatalf("redeem keys = %v, want %v", keys, want)
	}
}

// T7 (S1): presenting an admitted ticket again is not an error. It answers the *first*
// instant and the *first* label, and a client tells the two apart by a field, so a
// cashier can say "used at 20:14" instead of "invalid".
func TestTicketRedemptionAnswersAnAlreadyRedeemedTicketWithTheFirstInstant(t *testing.T) {
	svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
	ts := mountedRedeemServer(t, svc, &fakeRedeemer{redemption: tickets.Redemption{
		TicketID:   fixtureTicket().TicketID,
		Outcome:    tickets.RedemptionAlreadyRedeemed,
		RedeemedAt: doorInstant,
		RedeemedBy: "door-north",
	}})

	status, raw := redeemAnswer(t, ts, fmt.Sprintf(`{"token":%q,"label":"door-south"}`,
		servedToken(t, svc, fixtureTicket().TicketID)))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: a second presentation is an answer, not a refusal (body %s)",
			status, raw)
	}
	var body redeemBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body %s: %v", raw, err)
	}
	if !body.AlreadyRedeemed {
		t.Fatal("alreadyRedeemed = false, so a client cannot tell this from a fresh admission")
	}
	if !body.RedeemedAt.Equal(doorInstant) || body.RedeemedBy != "door-north" {
		t.Fatalf("admission = %s / %q, want the first redemption %s by the first door",
			body.RedeemedAt, body.RedeemedBy, doorInstant)
	}
	if body.TicketID != fixtureTicket().TicketID || len(body.Seats) != 1 {
		t.Fatalf("an already-redeemed ticket lost its facts: %+v", body)
	}
}

// T7 (S3): verification and redemption are one answer. A code the server cannot verify is
// refused with the verification route's own status and body, and — the point of this
// route — the redemption collaborator is never reached, so nothing is written and nothing
// is ever told "already redeemed".
func TestTicketRedemptionRefusesUnverifiableCodesWithoutReachingTheStore(t *testing.T) {
	genuine := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
	token := servedToken(t, genuine, fixtureTicket().TicketID)
	forged := token[:strings.Index(token, ".")+1] + strings.Repeat("A", 43)

	disagreeing := fixtureTicket()
	disagreeing.Event.ID = "00000000-0000-0000-0000-00000000f0ff"

	cases := []struct {
		name string
		svc  publicapi.TicketService
		body string
	}{
		{"a token that is not a token", genuine, `{"token":"not-a-token"}`},
		{"a token with a forged signature", genuine, fmt.Sprintf(`{"token":%q}`, forged)},
		{"a token with a tampered payload", genuine, fmt.Sprintf(`{"token":%q}`, tamperPayload(t, token))},
		{"an empty token", genuine, `{"token":""}`},
		{"a token that names no stored ticket",
			signedTicketService(t, &fakeTicketReader{err: fmt.Errorf("%w: gone", tickets.ErrNotFound)}),
			fmt.Sprintf(`{"token":%q}`, token)},
		{"a token whose signed event disagrees with the stored ticket",
			signedTicketService(t, &fakeTicketReader{detail: disagreeing}),
			fmt.Sprintf(`{"token":%q}`, token)},
		{"a body that is not a token", genuine, `token=abc`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			redeemer := freshRedeemer()
			ts := mountedRedeemServer(t, testCase.svc, redeemer)
			// The same request against the verification route: the answer the door must not
			// be able to tell apart from this one.
			verifyStatus, verifyRaw := verificationAnswer(t, testCase.svc, testCase.body)

			status, raw := redeemAnswer(t, ts, testCase.body)
			if status != verifyStatus || string(raw) != string(verifyRaw) {
				t.Fatalf("redeem answered %d %s, want the verification route's %d %s",
					status, raw, verifyStatus, verifyRaw)
			}
			// S3 as a call count: the code never verified, so the redemption was never
			// offered the chance to write or to answer "already redeemed".
			if redeemer.calls != 0 {
				t.Fatalf("Redeem() calls = %d, want 0 for a code that does not verify", redeemer.calls)
			}
		})
	}
}

// T7 (S1): a void ticket and a cancelled screening are their own answers, in the reason
// vocabulary the verification route already uses, rather than one generic failure — a
// cashier acts differently on each.
func TestTicketRedemptionRefusesVoidTicketsAndCancelledScreeningsDistinctly(t *testing.T) {
	cases := []struct {
		name     string
		outcome  tickets.RedemptionOutcome
		wantCode string
	}{
		{"a void ticket", tickets.RedemptionRefusedVoided, tickets.InvalidReasonVoided},
		{"a cancelled screening", tickets.RedemptionRefusedEventCancelled, tickets.InvalidReasonEventCancelled},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
			ts := mountedRedeemServer(t, svc, &fakeRedeemer{redemption: tickets.Redemption{
				TicketID: fixtureTicket().TicketID,
				Outcome:  testCase.outcome,
			}})

			_, raw := redeemAnswer(t, ts, fmt.Sprintf(`{"token":%q}`,
				servedToken(t, svc, fixtureTicket().TicketID)))
			var problem problemBody
			if err := json.Unmarshal(raw, &problem); err != nil {
				t.Fatalf("decode problem %s: %v", raw, err)
			}
			if problem.Status != http.StatusConflict || problem.Code != testCase.wantCode {
				t.Fatalf("problem = %+v, want 409 %s", problem, testCase.wantCode)
			}
		})
	}
}

// T7 (S3): a store that cannot answer is retryable, never a verdict on the code — and a
// ticket that disappears between the check and the admission keeps the verification
// route's refusal rather than inventing a vocabulary of its own.
func TestTicketRedemptionAnswersStoreFailuresHonestly(t *testing.T) {
	unavailable := fmt.Errorf("%w: conn closed", tickets.ErrUnavailable)
	cases := []struct {
		name        string
		reader      *fakeTicketReader
		redeemer    *fakeRedeemer
		wantStatus  int
		wantCode    string
		wantNoWrite bool
	}{
		{"the code cannot be checked", &fakeTicketReader{err: unavailable}, freshRedeemer(),
			http.StatusServiceUnavailable, "service_unavailable", true},
		{"the admission cannot be written", &fakeTicketReader{detail: fixtureTicket()},
			&fakeRedeemer{err: unavailable}, http.StatusServiceUnavailable, "service_unavailable", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			svc := signedTicketService(t, testCase.reader)
			ts := mountedRedeemServer(t, svc, testCase.redeemer)

			status, raw := redeemAnswer(t, ts, fmt.Sprintf(`{"token":%q}`, fixtureToken(t)))
			var problem problemBody
			if err := json.Unmarshal(raw, &problem); err != nil {
				t.Fatalf("decode problem %s: %v", raw, err)
			}
			if status != testCase.wantStatus || problem.Code != testCase.wantCode {
				t.Fatalf("answer = %d %+v, want %d %s", status, problem, testCase.wantStatus, testCase.wantCode)
			}
			if testCase.wantNoWrite && testCase.redeemer.calls != 0 {
				t.Fatalf("Redeem() calls = %d, want 0 when the code could not be checked", testCase.redeemer.calls)
			}
		})
	}

	t.Run("the ticket no longer exists", func(t *testing.T) {
		svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
		ts := mountedRedeemServer(t, svc, &fakeRedeemer{err: fmt.Errorf("%w: gone", tickets.ErrNotFound)})
		body := fmt.Sprintf(`{"token":%q}`, fixtureToken(t))

		// A code naming no stored ticket is the verification route's own refusal, and a
		// ticket that disappeared a moment ago answers the same way.
		gone := signedTicketService(t, &fakeTicketReader{err: fmt.Errorf("%w: gone", tickets.ErrNotFound)})
		verifyStatus, verifyRaw := verificationAnswer(t, gone, body)
		status, raw := redeemAnswer(t, ts, body)
		if status != http.StatusBadRequest || string(raw) != string(verifyRaw) {
			t.Fatalf("answer = %d %s, want the verification route's %d %s",
				status, raw, verifyStatus, verifyRaw)
		}
	})
}

// T7 (S3): the route is mounted only when both collaborators are present, and the routes
// next to it keep working without it.
func TestTicketRedemptionIsNotMountedWithoutItsCollaborators(t *testing.T) {
	svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})

	// Without a redemption collaborator the admission handler is not registered at all. The
	// path is still claimed by GET /tickets/{ticketId}, so the mux answers 405 for the POST
	// it does not serve — never one of the admission route's own answers.
	noRedeemer := mountedTicketServer(t, svc)
	if status, _ := redeemAnswer(t, noRedeemer, `{"token":"whatever"}`); status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 without a redemption collaborator", status)
	}
	if response := postVerify(t, noRedeemer, fmt.Sprintf(`{"token":%q}`,
		servedToken(t, svc, fixtureTicket().TicketID))); response.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d, want the route next to it to keep working", response.StatusCode)
	}

	// Without the ticket service nothing verifies a code, so no admission is advertised.
	noTickets := mountedRedeemServer(t, nil, freshRedeemer())
	if status, _ := redeemAnswer(t, noTickets, `{"token":"whatever"}`); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without a ticket service", status)
	}
}

// Preserve: mounting the admission route changes nothing about the verification route next
// to it, and verification still never admits anybody.
func TestTicketVerificationStillOnlyVerifiesBesideTheRedeemRoute(t *testing.T) {
	svc := signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()})
	redeemer := freshRedeemer()
	ts := mountedRedeemServer(t, svc, redeemer)

	response := postVerify(t, ts, fmt.Sprintf(`{"token":%q}`, servedToken(t, svc, fixtureTicket().TicketID)))
	body := decodeResponseBody[verifyBody](t, response)
	if response.StatusCode != http.StatusOK || !body.Valid || body.TicketID != fixtureTicket().TicketID {
		t.Fatalf("verify answered %d %+v, want the valid ticket", response.StatusCode, body)
	}
	if redeemer.calls != 0 {
		t.Fatalf("Redeem() calls = %d, want 0: verification never admits anybody", redeemer.calls)
	}
}

// verificationAnswer asks the verification route the same request and answers its status
// and raw body, so a comparison with the admission route's refusal is exact.
func verificationAnswer(t *testing.T, svc publicapi.TicketService, body string) (int, []byte) {
	t.Helper()
	response := postVerify(t, mountedTicketServer(t, svc), body)
	return response.StatusCode, readBody(t, response)
}

// fixtureToken signs the fixture ticket directly, so a test can present a genuine code to
// a service whose store is deliberately broken.
func fixtureToken(t *testing.T) string {
	t.Helper()
	signer, err := tickets.NewSigner(ticketRouteQRKey)
	if err != nil {
		t.Fatalf("NewSigner() = unexpected error %v", err)
	}
	token, err := signer.Sign(fixtureTicket())
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	return token
}

var _ publicapi.RedemptionService = (*fakeRedeemer)(nil)
