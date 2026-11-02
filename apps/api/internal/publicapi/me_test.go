// Behaviour tests for the account's own list: GET /api/v1/me/tickets (T4, S5, D3).
//
// They need no database. What they pin is the transport contract the web relies on when it
// stops keeping a note of ticket identifiers in the browser and starts asking the server:
//
//   - the list is answered for the account behind the presented session, newest first, and
//     each entry is the document the ticket page already renders — not a second, thinner
//     shape a second component would have to understand;
//   - an account that bought nothing answers an empty list, because "no account" (401) and
//     "an account with no orders" (200 with []) are opposite claims and only one of them is
//     true;
//   - every way a session can be unusable — absent, misspelt, expired, signed out, guessed —
//     is the sign-in route's own 401, byte for byte, so a session that just ended and a
//     credential that never existed cannot be told apart from outside;
//   - a store that cannot answer is a retryable 503 and never an empty list, and a session
//     store outage is never read as a sign-out;
//   - the route exists only where the login exists, so a deployment without identity does
//     not advertise "these are your tickets" to nobody.
package publicapi_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/identity"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

const (
	// meTicketsPath is the route under test, relative to the /api/v1 mount point.
	meTicketsPath = "/api/v1/me/tickets"
	// meCustomerID is the account the live session belongs to, and meOtherCustomerID the
	// one a second session names: the route must answer for the account the token proves,
	// so the two are deliberately different strings.
	meCustomerID      = "00000000-0000-4000-8000-0000000000a1"
	meOtherCustomerID = "00000000-0000-4000-8000-0000000000a2"
	// meSessionTTL is the session lifetime these cases mint with. It is long enough that no
	// case expires by accident; the expired case writes an ended row on purpose.
	meSessionTTL = 30 * time.Minute
)

// meSessionStore is the session table in memory: rows keyed by the hash of a token, which
// is the key the real table uses, and an error that makes the store unable to answer at all.
//
// It is a fake table, not a fake rule: the route is wired with the real
// [identity.SessionService] over it, so "expired", "signed out" and "never issued" are
// answered by the domain's own collapsing instead of by a stub that imitates it.
type meSessionStore struct {
	rows map[string]identity.Session
	err  error
}

var _ identity.SessionStore = (*meSessionStore)(nil)

func newMeSessionStore() *meSessionStore {
	return &meSessionStore{rows: make(map[string]identity.Session)}
}

// ended writes a row that already expired, so a case can present a real token whose session
// is over. It is written directly because [identity.SessionService.Open] only mints sessions
// that are still alive.
func (s *meSessionStore) ended(token, customerID string, expiresAt time.Time) {
	s.rows[string(identity.HashSessionToken(token))] = identity.Session{
		ID:         "session-" + customerID,
		CustomerID: customerID,
		CreatedAt:  expiresAt.Add(-meSessionTTL),
		ExpiresAt:  expiresAt,
	}
}

func (s *meSessionStore) Create(_ context.Context, session identity.Session, tokenHash []byte) error {
	if s.err != nil {
		return s.err
	}
	s.rows[string(tokenHash)] = session
	return nil
}

func (s *meSessionStore) Find(_ context.Context, tokenHash []byte, now time.Time) (identity.Session, error) {
	if s.err != nil {
		return identity.Session{}, s.err
	}
	session, ok := s.rows[string(tokenHash)]
	if !ok || !session.ExpiresAt.After(now) {
		return identity.Session{}, identity.ErrNoSession
	}
	return session, nil
}

func (s *meSessionStore) Touch(context.Context, []byte, time.Time) error { return nil }

// Revoke forgets the row. The real store stamps `revoked_at` and keeps it; for the route the
// only fact that matters is that a closed session stops opening anything, which is the
// domain's rule and not this fake's.
func (s *meSessionStore) Revoke(_ context.Context, tokenHash []byte, _ time.Time) error {
	delete(s.rows, string(tokenHash))
	return nil
}

// meAccountTickets is the account's list in memory: it answers the details it was given, in
// the order it was given them — the store is what decides "newest first" — and records which
// account it was asked about.
type meAccountTickets struct {
	details []tickets.TicketDetail
	err     error

	calls      int
	customerID string
}

var _ publicapi.AccountTicketsService = (*meAccountTickets)(nil)

func (f *meAccountTickets) TicketsByCustomer(_ context.Context, customerID string) ([]tickets.TicketDetail, error) {
	f.calls++
	f.customerID = customerID
	if f.err != nil {
		return nil, f.err
	}
	return f.details, nil
}

// meWiring is everything the account list is mounted with. A nil field is how the "not
// mounted" cases are built: the route needs the login surface, the session read and the
// list, so any missing piece leaves it absent.
type meWiring struct {
	issuer   publicapi.IdentityLinkService
	redeemer publicapi.IdentitySessionService
	sessions publicapi.SessionAuthenticator
	account  publicapi.AccountTicketsService
	tickets  publicapi.TicketService
}

// mountedMeServer wires the public subtree the way cmd/api does, with the account route and
// the ticket route beside the hold route and never behind the administrative guard.
func mountedMeServer(t *testing.T, wiring meWiring) *httptest.Server {
	t.Helper()
	server := httpserver.New(config.Config{}, health.New(health.ProcessProbe{}),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := publicapi.New(&fakeHoldService{}).
		WithTickets(wiring.tickets).
		WithIdentity(wiring.issuer, wiring.redeemer).
		WithIdentityTickets(wiring.sessions, wiring.account)
	ts := httptest.NewServer(server.WithPublic(handler.Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// meSessionService is the real session service over the in-memory store, so the route's
// refusals come from the domain's rules and not from a stub that guesses them.
func meSessionService(t *testing.T, store identity.SessionStore) *identity.SessionService {
	t.Helper()
	sessions, err := identity.NewSessionService(store, meSessionTTL)
	if err != nil {
		t.Fatalf("NewSessionService() = unexpected error %v", err)
	}
	return sessions
}

// meOpenedSession mints one live session for customerID and answers its token, which is the
// credential the route is presented with.
func meOpenedSession(t *testing.T, sessions *identity.SessionService, customerID string) string {
	t.Helper()
	token, _, err := sessions.Open(context.Background(), customerID)
	if err != nil {
		t.Fatalf("Open(%q) = unexpected error %v", customerID, err)
	}
	return token
}

// meTicket is one ticket of a fixture list, carrying the signed code its own ticket page
// serves: the list must answer the same document, so the code is set here exactly as the
// ticket service sets it when the page reads the ticket on its own.
func meTicket(t *testing.T, ticketID, reservationID string, issuedAt time.Time) tickets.TicketDetail {
	t.Helper()
	detail := fixtureTicket()
	detail.TicketID = ticketID
	detail.ReservationID = reservationID
	detail.IssuedAt = issuedAt
	signer, err := tickets.NewSigner(ticketRouteQRKey)
	if err != nil {
		t.Fatalf("NewSigner() = unexpected error %v", err)
	}
	token, err := signer.Sign(detail)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	detail.QRToken = token
	return detail
}

// getMeTickets asks the account route with the given Authorization header, or with none
// when it is empty.
func getMeTickets(t *testing.T, ts *httptest.Server, authorization string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, ts.URL+meTicketsPath, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", meTicketsPath, err)
	}
	return response
}

// T4 (S5, D3): a live session answers the account's tickets, newest first, and every entry
// is byte for byte the document the ticket route serves for that same ticket. The second
// assertion is the point of the route: the web renders one ticket component for the list and
// for the page, so the two answers cannot be two shapes that drift apart.
func TestMyTicketsRouteAnswersTheAccountsTicketsNewestFirst(t *testing.T) {
	now := ticketScreening
	// The identifiers are chosen so the newest ticket sorts after the older one as text: a
	// handler that ordered the list by anything but the order the store answered it in
	// cannot pass by accident, and neither can one that reversed it.
	newest := meTicket(t, "00000000-0000-4000-8000-0000000000b9", fixtureTicket().ReservationID, now)
	older := meTicket(t, "00000000-0000-4000-8000-0000000000b1", "00000000-0000-4000-8000-0000000000b0", now.Add(-time.Hour))

	sessions := meSessionService(t, newMeSessionStore())
	token := meOpenedSession(t, sessions, meCustomerID)
	account := &meAccountTickets{details: []tickets.TicketDetail{newest, older}}
	reader := &fakeTicketReader{detail: newest}
	ts := mountedMeServer(t, meWiring{
		issuer: &identityFakeIssuer{}, redeemer: &identityFakeRedeemer{},
		sessions: sessions, account: account,
		tickets: signedTicketService(t, reader),
	})

	response := getMeTickets(t, ts, "Bearer "+token)
	if response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("GET %s = %d, want 200 (body %s)", meTicketsPath, response.StatusCode, body)
	}
	raw := readBody(t, response)

	// The account is the one the session proves, and it is asked exactly once.
	if account.calls != 1 || account.customerID != meCustomerID {
		t.Fatalf("the list was asked for account %q in %d call(s), want %q once",
			account.customerID, account.calls, meCustomerID)
	}

	if keys := objectKeys(t, raw); !equalKeys(keys, []string{"tickets"}) {
		t.Fatalf("answer keys = %v, want only tickets", keys)
	}
	entries := arrayField(t, raw, "tickets")
	if len(entries) != 2 {
		t.Fatalf("the list answered %d ticket(s), want the account's two", len(entries))
	}
	for position, want := range []string{newest.TicketID, older.TicketID} {
		if got := string(objectField(t, entries[position], "ticketId")); got != `"`+want+`"` {
			t.Fatalf("ticket %d = %s, want %q: the list must answer the order the store did, newest first",
				position, got, want)
		}
	}

	// The same ticket, asked for the way the ticket page asks for it, answers the same
	// bytes: one projection, one spelling of it.
	served := bytes.TrimSpace(readBody(t, getTicket(t, ts, newest.TicketID)))
	if !bytes.Equal(entries[0], served) {
		t.Fatalf("the list entry %s differs from the ticket route's %s", entries[0], served)
	}
}

// T4 (S5): an account that bought nothing answers 200 with an empty list. It is not a 401 —
// the session is real — and it is not a 404 — the account exists and simply has no orders.
// The empty array is the whole claim, so it is asserted exactly, `[]` and not `null`.
func TestMyTicketsRouteAnswersAnEmptyListForAnAccountWithNoOrders(t *testing.T) {
	sessions := meSessionService(t, newMeSessionStore())
	token := meOpenedSession(t, sessions, meCustomerID)
	account := &meAccountTickets{}
	ts := mountedMeServer(t, meWiring{
		issuer: &identityFakeIssuer{}, redeemer: &identityFakeRedeemer{},
		sessions: sessions, account: account,
	})

	response := getMeTickets(t, ts, "Bearer "+token)
	raw := readBody(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (body %s)", meTicketsPath, response.StatusCode, raw)
	}
	if got := strings.TrimSpace(string(raw)); got != `{"tickets":[]}` {
		t.Fatalf("answer = %s, want an empty tickets array", got)
	}
	if account.customerID != meCustomerID {
		t.Fatalf("the list was asked for account %q, want %q", account.customerID, meCustomerID)
	}
}

// T4 (S1, S5): every way a session can be unusable is one answer, and it is the sign-in
// route's own 401, byte for byte. A session that just expired and a credential that was
// guessed must be indistinguishable from outside, which is why the body is compared with the
// login route's rather than with a literal written here.
func TestMyTicketsRouteRefusesEveryUnusableTokenIdentically(t *testing.T) {
	store := newMeSessionStore()
	sessions := meSessionService(t, store)
	live := meOpenedSession(t, sessions, meCustomerID)

	// A session that was signed out: the same service opens it and closes it again.
	signedOut := meOpenedSession(t, sessions, meCustomerID)
	if err := sessions.Close(context.Background(), signedOut); err != nil {
		t.Fatalf("Close() = unexpected error %v", err)
	}
	// A session whose instant has passed: the row is present and it is over.
	expiredToken, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() = unexpected error %v", err)
	}
	store.ended(expiredToken, meCustomerID, time.Now().Add(-time.Minute))
	// A well-shaped token this deployment never minted: the guess the route must not confirm.
	guessed, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() = unexpected error %v", err)
	}

	// The reference refusal is the login route's own: a spent sign-in link answers it.
	spentSigner := identitySigner(t, identityTestLinkKey, identity.LinkWindow)
	spentLink, err := spentSigner.Sign(identityRouteEmail, "9a1f7b2c-0000-4000-8000-0000000000c1", time.Now())
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	ts := mountedMeServer(t, meWiring{
		issuer: &identityFakeIssuer{},
		redeemer: identityRedeemer(t, spentSigner,
			&identityFakeRedemptionStore{err: identity.ErrLinkSpent}),
		sessions: sessions, account: &meAccountTickets{},
	})
	referenceStatus, referenceBody := readIdentityAnswer(t, ts, identitySessionPath, `{"token":"`+spentLink+`"}`)
	if referenceStatus != http.StatusUnauthorized {
		t.Fatalf("the login route's refusal = %d, want 401 (body %s)", referenceStatus, referenceBody)
	}

	cases := []struct {
		name          string
		authorization string
	}{
		{"no Authorization header", ""},
		{"an empty bearer", "Bearer "},
		{"a token that is not shaped like one", "Bearer not-a-session-token"},
		{"a header that is not a bearer", "Basic " + guessed},
		{"an expired session", "Bearer " + expiredToken},
		{"a signed-out session", "Bearer " + signedOut},
		{"a guessed token", "Bearer " + guessed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := getMeTickets(t, ts, testCase.authorization)
			raw := readBody(t, response)
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", response.StatusCode, raw)
			}
			if !bytes.Equal(raw, referenceBody) {
				t.Fatalf("answer = %s, want the login route's %s", raw, referenceBody)
			}
			for _, credential := range []string{live, expiredToken, signedOut, guessed, "not-a-session-token"} {
				if strings.Contains(string(raw), credential) {
					t.Fatalf("the refusal carries the presented credential %q: %s", credential, raw)
				}
			}
		})
	}

	// Preserve: the live session still answers after every refusal above, so nothing closed
	// the session it proves and no refusal leaked into the next request.
	if response := getMeTickets(t, ts, "Bearer "+live); response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("the live session = %d after the refusals, want 200 (body %s)", response.StatusCode, body)
	}
}

// T4 (S5): a store that cannot answer is a retryable 503 and never an empty list. An empty
// list is a claim about the account — "you bought nothing" — and a database that is down has
// not earned the right to make it. A session store outage is not a sign-out either.
func TestMyTicketsRouteAnswersUnavailableInsteadOfAnEmptyList(t *testing.T) {
	sessions := meSessionService(t, newMeSessionStore())
	token := meOpenedSession(t, sessions, meCustomerID)

	cases := []struct {
		name     string
		storeErr error
		// unreachable builds the server over a session store that cannot answer at all.
		unreachable bool
	}{
		{"the list cannot be read", tickets.ErrUnavailable, false},
		{"the list fails in a way nobody classified", errors.New("something else entirely"), false},
		{"the list does not know the account", tickets.ErrNotFound, false},
		{"the session store cannot answer", nil, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			presented := "Bearer " + token
			account := &meAccountTickets{err: testCase.storeErr}
			serverSessions := publicapi.SessionAuthenticator(sessions)
			if testCase.unreachable {
				serverSessions = meSessionService(t, &meSessionStore{err: errors.New("connection refused")})
			}
			ts := mountedMeServer(t, meWiring{
				issuer: &identityFakeIssuer{}, redeemer: &identityFakeRedeemer{},
				sessions: serverSessions, account: account,
			})

			response := getMeTickets(t, ts, presented)
			raw := readBody(t, response)
			if response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (body %s)", response.StatusCode, raw)
			}
			problem := decodedProblem(t, raw)
			if problem.Code != "service_unavailable" || problem.Status != http.StatusServiceUnavailable {
				t.Fatalf("problem = %+v, want 503 service_unavailable", problem)
			}
			// A failure is never a list, empty or otherwise, and never a refusal of the
			// credential: those are two different answers and this case is the first.
			if keys := objectKeys(t, raw); !equalKeys(keys, []string{"code", "detail", "status", "title", "type"}) {
				t.Fatalf("the failure body carries %v, want only a problem document", keys)
			}
		})
	}
}

// T4 (S5): the route is mounted only where the login is, and only with both halves it is
// built from. A deployment without identity has no signed-in account to answer for, so
// advertising "these are your tickets" there would advertise a route that can only refuse.
func TestMyTicketsRouteIsMountedOnlyWithTheIdentitySurface(t *testing.T) {
	sessions := meSessionService(t, newMeSessionStore())
	token := meOpenedSession(t, sessions, meCustomerID)
	account := &meAccountTickets{}

	cases := []struct {
		name   string
		wiring meWiring
	}{
		{"without the login routes", meWiring{sessions: sessions, account: account}},
		{"without the session read", meWiring{
			issuer: &identityFakeIssuer{}, redeemer: &identityFakeRedeemer{}, account: account}},
		{"without the account read", meWiring{
			issuer: &identityFakeIssuer{}, redeemer: &identityFakeRedeemer{}, sessions: sessions}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ts := mountedMeServer(t, testCase.wiring)
			response := getMeTickets(t, ts, "Bearer "+token)
			raw := readBody(t, response)
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 like a missing route (body %s)", response.StatusCode, raw)
			}
			if account.calls != 0 {
				t.Fatalf("an unmounted route read the account %d time(s)", account.calls)
			}
		})
	}

	// Preserve: the routes beside the account list are untouched by it being mounted — the
	// ticket route it answers with still serves a ticket, and the hold route still holds.
	ts := mountedMeServer(t, meWiring{
		issuer: &identityFakeIssuer{}, redeemer: &identityFakeRedeemer{},
		sessions: sessions, account: account,
		tickets: signedTicketService(t, &fakeTicketReader{detail: fixtureTicket()}),
	})
	served := getTicket(t, ts, fixtureTicket().TicketID)
	served.Body.Close()
	if served.StatusCode != http.StatusOK {
		t.Fatalf("the ticket route = %d, want 200: the list is additive", served.StatusCode)
	}
	hold := postHold(t, ts, "me-key-1", `{"eventId":"evt-1","seatIds":[21]}`)
	hold.Body.Close()
	if hold.StatusCode == http.StatusNotFound {
		t.Fatal("the hold route disappeared when the account route was mounted")
	}
}
