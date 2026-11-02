// Behaviour tests for the two identity routes: POST /api/v1/identity/links and
// POST /api/v1/identity/sessions (T2, S1, S2, S3, S6).
//
// The subject here is not the login itself — the domain packages pin that — but what the
// transport is allowed to reveal. Asking for a link answers the same 202 whether one was
// sent, whether the address is inside its allowance and whether the counter could be read at
// all, so the route cannot be used to ask "does this address have an account" or "was I
// throttled". Redeeming a link answers one fixed 401 for a spent, an expired and a forged
// token, so it cannot be used to ask whether a credential was ever real. Everything else —
// a body that is not the documented shape, an address the checkout would refuse, a store or
// a provider that cannot answer — has its own answer, and none of them names an address, a
// token or a key.
//
// Everything is a fake: a fake store, a fake counter and a fake provider behind the real
// domain services. No database, no Redis and no mail server.
package publicapi_test

import (
	"bytes"
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
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/identity"
	"cinema.local/ticketing/apps/api/internal/publicapi"
)

const (
	// identityTestLinkKey signs the login links these cases present. It is longer than the
	// documented minimum and is not one of the other two keys.
	identityTestLinkKey = "identity-route-login-link-key-0123456789abcdef"
	// identityAnotherLinkKey signs the forged token: a link this deployment never minted is
	// the one a prober sends.
	identityAnotherLinkKey = "identity-route-another-link-key-0123456789abcdef"
	// identityLinkPath and identitySessionPath are the two routes under test.
	identityLinkPath    = "/api/v1/identity/links"
	identitySessionPath = "/api/v1/identity/sessions"
	// identityRouteEmail is the address a person types into the login form. It is on a
	// throwaway domain and it is the string that must never appear in an answer or a log.
	identityRouteEmail = "ana@example.test"
	// identitySessionTTL is the session lifetime these cases configure, shorter than the
	// deployment's thirty days so an expiry is easy to read back.
	identitySessionTTL = 30 * time.Minute
)

// identityFakeLinkStore records every link the issuer asked to save, and can refuse.
type identityFakeLinkStore struct {
	saved []identity.AccessLink
	err   error
}

var _ identity.AccessLinkStore = (*identityFakeLinkStore)(nil)

func (s *identityFakeLinkStore) Save(_ context.Context, link identity.AccessLink) error {
	s.saved = append(s.saved, link)
	return s.err
}

// identityFakeMailer records every message the issuer handed a provider, and can refuse.
type identityFakeMailer struct {
	mails []identity.Mail
	err   error
}

var _ identity.Mailer = (*identityFakeMailer)(nil)

func (m *identityFakeMailer) Send(_ context.Context, mail identity.Mail) error {
	if m.err != nil {
		return m.err
	}
	m.mails = append(m.mails, mail)
	return nil
}

// identityFakeLimiter is the allowance in memory, counting per key the way the deployed one
// does. A limit of zero refuses everything, and err is a counter that cannot be read at all.
type identityFakeLimiter struct {
	limit      int
	retryAfter time.Duration
	err        error

	asked  []string
	counts map[string]int
}

var _ identity.LinkLimiter = (*identityFakeLimiter)(nil)

func (l *identityFakeLimiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	l.asked = append(l.asked, key)
	if l.err != nil {
		return false, 0, l.err
	}
	if l.counts == nil {
		l.counts = make(map[string]int)
	}
	l.counts[key]++
	return l.counts[key] <= l.limit, l.retryAfter, nil
}

// identityFakeRedemptionStore records the transaction the redeemer delegated, answers what it
// wrote, and can refuse.
type identityFakeRedemptionStore struct {
	calls      []identity.AccessClaims
	hash       []byte
	redemption identity.Redemption
	err        error
}

var _ identity.RedemptionStore = (*identityFakeRedemptionStore)(nil)

func (s *identityFakeRedemptionStore) Redeem(_ context.Context, claim identity.AccessClaims, session identity.Session, tokenHash []byte) (identity.Redemption, error) {
	s.calls = append(s.calls, claim)
	s.hash = append([]byte(nil), tokenHash...)
	if s.err != nil {
		return identity.Redemption{}, s.err
	}
	return s.redemption, nil
}

// identityFakeIssuer and identityFakeRedeemer are the collaborators a route case does not
// exercise. They exist so a case can mount the surface with one real half and one dummy: the
// routes are mounted as a pair (see TestIdentityRoutesAreNotMountedWithoutBothCollaborators).
type identityFakeIssuer struct{ calls int }

func (i *identityFakeIssuer) Issue(_ context.Context, _ string) error { i.calls++; return nil }

type identityFakeRedeemer struct {
	token   string
	session identity.Session
	claimed int
	err     error
	calls   int
}

func (r *identityFakeRedeemer) Redeem(_ context.Context, _ string) (string, identity.Session, int, error) {
	r.calls++
	return r.token, r.session, r.claimed, r.err
}

// identitySigner builds the signer of the login links these cases mint and present.
func identitySigner(t *testing.T, key string, ttl time.Duration) *identity.AccessSigner {
	t.Helper()
	signer, err := identity.NewAccessSigner(key, ttl)
	if err != nil {
		t.Fatalf("NewAccessSigner() = unexpected error %v", err)
	}
	return signer
}

// identityIssuer builds the real issuing half over the fakes, so a case exercises the
// domain's own ordering instead of a hand-written stub of it.
func identityIssuer(t *testing.T, store identity.AccessLinkStore, limiter identity.LinkLimiter, mailer identity.Mailer) *identity.Issuer {
	t.Helper()
	issuer, err := identity.NewIssuer(identitySigner(t, identityTestLinkKey, identity.LinkWindow), store, limiter, mailer,
		"https://cinema.test/in/", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewIssuer() = unexpected error %v", err)
	}
	return issuer
}

// identityRedeemer builds the real redeeming half over the fake store. Its log is discarded:
// the line it writes is pinned by the identity package's own suite.
func identityRedeemer(t *testing.T, signer *identity.AccessSigner, store identity.RedemptionStore) *identity.Redeemer {
	t.Helper()
	redeemer, err := identity.NewRedeemer(signer, store, identitySessionTTL,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewRedeemer() = unexpected error %v", err)
	}
	return redeemer
}

// mountedIdentityServer wires the public subtree the way cmd/api does, with the two identity
// routes beside the rest of the customer surface and never behind the administrative guard. A
// nil collaborator is how the "not mounted" case is built.
func mountedIdentityServer(t *testing.T, issuer publicapi.IdentityLinkService, redeemer publicapi.IdentitySessionService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).
		WithIdentity(issuer, redeemer).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// postIdentity asks one of the two routes with a raw body, so a case can send exactly the
// bytes it means to send.
func postIdentity(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return response
}

// readIdentityAnswer asks a route once and answers its status and raw body, so two refusals
// can be compared byte for byte.
func readIdentityAnswer(t *testing.T, ts *httptest.Server, path, body string) (int, []byte) {
	t.Helper()
	response := postIdentity(t, ts, path, body)
	return response.StatusCode, readBody(t, response)
}

// decodedProblem is one problem body, for the cases whose answer must carry a code.
func decodedProblem(t *testing.T, raw []byte) problemBody {
	t.Helper()
	var problem problemBody
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	return problem
}

// T2 (S6): asking for a link answers the same 202 with the same body whether the link was
// sent or the address is inside its allowance. That identity is the whole point: a different
// answer would tell a stranger that this address is being asked about, and a difference in
// timing or wording would be an oracle built out of the refusal.
func TestIdentityLinkRouteAcceptsIdenticallyWhenItIssuedAndWhenItRefused(t *testing.T) {
	store := &identityFakeLinkStore{}
	mailer := &identityFakeMailer{}
	// One request per window, so the second is refused by the counter.
	limiter := &identityFakeLimiter{limit: 1, retryAfter: identity.LinkWindow}
	ts := mountedIdentityServer(t, identityIssuer(t, store, limiter, mailer), &identityFakeRedeemer{})

	issuedStatus, issuedBody := readIdentityAnswer(t, ts, identityLinkPath, `{"email":"`+identityRouteEmail+`"}`)
	throttledStatus, throttledBody := readIdentityAnswer(t, ts, identityLinkPath, `{"email":"`+identityRouteEmail+`"}`)

	for name, got := range map[string]struct {
		status int
		body   []byte
	}{"issued": {issuedStatus, issuedBody}, "throttled": {throttledStatus, throttledBody}} {
		if got.status != http.StatusAccepted {
			t.Fatalf("%s status = %d, want 202 (body %s)", name, got.status, got.body)
		}
		if keys := objectKeys(t, got.body); !equalKeys(keys, []string{"status"}) {
			t.Fatalf("%s answer keys = %v, want only status", name, keys)
		}
		if got := string(objectField(t, got.body, "status")); got != `"accepted"` {
			t.Fatalf("%s status field = %s, want \"accepted\"", name, got)
		}
	}

	// Byte for byte: the refusal is not even distinguishable by a whitespace difference.
	if !bytes.Equal(issuedBody, throttledBody) {
		t.Fatalf("issued answer %s differs from throttled answer %s", issuedBody, throttledBody)
	}
	// And it says nothing about the address: a body that echoed it would confirm the request
	// reached somebody's account, which is exactly what the 202 is meant to withhold.
	if strings.Contains(string(issuedBody), "@") || strings.Contains(string(issuedBody), identityRouteEmail) {
		t.Fatalf("the answer names the address: %s", issuedBody)
	}

	// Exactly one message was handed to the provider, and exactly one link was recorded: the
	// refused request spent neither.
	if len(mailer.mails) != 1 {
		t.Fatalf("the provider was handed %d message(s), want exactly one", len(mailer.mails))
	}
	if len(store.saved) != 1 {
		t.Fatalf("the store was given %d link(s), want exactly one", len(store.saved))
	}
	if mailer.mails[0].To != identityRouteEmail {
		t.Fatalf("the mail was addressed to %q, want %q", mailer.mails[0].To, identityRouteEmail)
	}
}

// T2 (S6): a counter that cannot be read refuses the send and is answered exactly like a
// sent link, because "the limit is unknown" is not something the caller is owed and a 503
// here would tell a stranger the deployment was asking about this address.
func TestIdentityLinkRouteAcceptsIdenticallyWhenTheLimitCannotBeRead(t *testing.T) {
	store := &identityFakeLinkStore{}
	mailer := &identityFakeMailer{}
	limiter := &identityFakeLimiter{err: errors.New("redis is down")}
	ts := mountedIdentityServer(t, identityIssuer(t, store, limiter, mailer), &identityFakeRedeemer{})

	status, body := readIdentityAnswer(t, ts, identityLinkPath, `{"email":"`+identityRouteEmail+`"}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want the same 202 a sent link answers (body %s)", status, body)
	}
	if keys := objectKeys(t, body); !equalKeys(keys, []string{"status"}) {
		t.Fatalf("answer keys = %v, want only status", keys)
	}
	// Failing closed means nothing was minted, recorded or mailed.
	if len(store.saved) != 0 || len(mailer.mails) != 0 {
		t.Fatalf("an unreadable counter still recorded %d link(s) and mailed %d message(s)",
			len(store.saved), len(mailer.mails))
	}
}

// T2 (S2): a body that is not exactly `{"email": "…"}`, or an address the checkout's own
// rule refuses, is a 400 — and it costs neither a counter increment nor a message, so a
// rejected request cannot become a way to spend somebody's allowance.
func TestIdentityLinkRouteRefusesAnUnusableRequest(t *testing.T) {
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
		{"an unknown field beside the address", `{"email":"` + identityRouteEmail + `","name":"Ana"}`},
		{"a body that is not JSON", `mail me`},
		{"a body larger than the route reads", `{"email":"` + strings.Repeat("a", 2<<10) + `@example.test"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &identityFakeLinkStore{}
			mailer := &identityFakeMailer{}
			limiter := &identityFakeLimiter{limit: identity.LinkLimit}
			ts := mountedIdentityServer(t, identityIssuer(t, store, limiter, mailer), &identityFakeRedeemer{})

			status, raw := readIdentityAnswer(t, ts, identityLinkPath, testCase.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", status, raw)
			}
			problem := decodedProblem(t, raw)
			if problem.Code != "invalid_input" || problem.Status != http.StatusBadRequest {
				t.Fatalf("problem = %+v, want 400 invalid_input", problem)
			}
			if strings.Contains(problem.Detail, identityRouteEmail) {
				t.Fatalf("the refusal echoes the address: %q", problem.Detail)
			}
			if len(limiter.asked) != 0 {
				t.Fatalf("the counter was asked about %q for a refused request", limiter.asked)
			}
			if len(mailer.mails) != 0 {
				t.Fatalf("the provider was handed %d message(s), want none", len(mailer.mails))
			}
		})
	}
}

// T2 (S2): an address the purchase form accepts but the signed payload cannot carry — the
// separator the payload joins its fields with — is still caller input, so it is a 400 and
// nothing is mailed. The counter was already spent by the domain's own order (it counts
// before it signs), which is the conservative mistake it makes on purpose.
func TestIdentityLinkRouteRefusesAnAddressThePayloadCannotCarry(t *testing.T) {
	mailer := &identityFakeMailer{}
	ts := mountedIdentityServer(t,
		identityIssuer(t, &identityFakeLinkStore{}, &identityFakeLimiter{limit: identity.LinkLimit}, mailer),
		&identityFakeRedeemer{})

	status, raw := readIdentityAnswer(t, ts, identityLinkPath, `{"email":"ana|admin@example.test"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, raw)
	}
	if problem := decodedProblem(t, raw); problem.Code != "invalid_input" {
		t.Fatalf("problem = %+v, want invalid_input", problem)
	}
	if len(mailer.mails) != 0 {
		t.Fatalf("the provider was handed %d message(s), want none", len(mailer.mails))
	}
}

// T2 (S2, S6): a store or a provider that cannot be reached is a retryable 503, never a 202
// and never a verdict on the address: the person is told to try again, and nothing about
// whether this mailbox has an account is confirmed either way.
func TestIdentityLinkRouteAnswersUnavailableWhenItCannotSend(t *testing.T) {
	cases := []struct {
		name   string
		store  *identityFakeLinkStore
		mailer *identityFakeMailer
	}{
		{"the store cannot record the link", &identityFakeLinkStore{err: errors.New("connection refused")}, &identityFakeMailer{}},
		{"the provider cannot send", &identityFakeLinkStore{}, &identityFakeMailer{err: errors.New("smtp refused the message")}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ts := mountedIdentityServer(t,
				identityIssuer(t, testCase.store, &identityFakeLimiter{limit: identity.LinkLimit}, testCase.mailer),
				&identityFakeRedeemer{})

			status, raw := readIdentityAnswer(t, ts, identityLinkPath, `{"email":"`+identityRouteEmail+`"}`)
			if status != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (body %s)", status, raw)
			}
			problem := decodedProblem(t, raw)
			if problem.Code != "service_unavailable" || problem.Status != http.StatusServiceUnavailable {
				t.Fatalf("problem = %+v, want 503 service_unavailable", problem)
			}
			if strings.Contains(string(raw), "@") {
				t.Fatalf("the failure answer names the address: %s", raw)
			}
		})
	}
}

// T2 (S6): the one log line names the outcome and nothing else. The address is a person and
// the token is a credential: an operator needs to know a request was served, refused or
// could not be sent, and needs neither of those two things in a log that outlives them.
func TestIdentityLinkRouteLogsTheOutcomeWithoutTheAddressTokenOrKey(t *testing.T) {
	cases := []struct {
		name    string
		store   *identityFakeLinkStore
		limiter *identityFakeLimiter
		want    string
	}{
		{"issued", &identityFakeLinkStore{}, &identityFakeLimiter{limit: identity.LinkLimit}, "issued"},
		{"rate_limited", &identityFakeLinkStore{}, &identityFakeLimiter{limit: 0, retryAfter: time.Minute}, "rate_limited"},
		{"unavailable", &identityFakeLinkStore{err: errors.New("connection refused")}, &identityFakeLimiter{limit: identity.LinkLimit}, "unavailable"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var logged bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			ts := mountedIdentityServer(t,
				identityIssuer(t, testCase.store, testCase.limiter, &identityFakeMailer{}),
				&identityFakeRedeemer{})
			status, raw := readIdentityAnswer(t, ts, identityLinkPath, `{"email":"`+identityRouteEmail+`"}`)
			if status != http.StatusAccepted && status != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 202 or 503 (body %s)", status, raw)
			}

			lines := 0
			for _, line := range strings.Split(strings.TrimSpace(logged.String()), "\n") {
				if strings.Contains(line, "identity link") {
					lines++
					if !strings.Contains(line, "outcome="+testCase.want) {
						t.Fatalf("log line = %q, want outcome=%s", line, testCase.want)
					}
				}
			}
			if lines != 1 {
				t.Fatalf("logged %d identity link line(s), want exactly one: %q", lines, logged.String())
			}
			for _, leaked := range []string{identityRouteEmail, "@", identityTestLinkKey, "/in/"} {
				if strings.Contains(logged.String(), leaked) {
					t.Fatalf("the log leaked %q: %q", leaked, logged.String())
				}
			}
		})
	}
}

// T2 (S1, S2, S3): redeeming a link answers 201 with the session token and the instant it
// ends, and the row the store wrote is keyed by that token's hash — never by the token. The
// access link itself is a credential and is not echoed back in the answer.
func TestIdentitySessionRouteAnswersTheSessionTokenOnce(t *testing.T) {
	signer := identitySigner(t, identityTestLinkKey, identity.LinkWindow)
	// The claim of D5 wrote three orders for this account, and the answer must not mention it:
	// how much history an address brought is not a fact the caller asked for and not one the
	// body has any business carrying beside a token.
	store := &identityFakeRedemptionStore{redemption: identity.Redemption{
		CustomerID: "9a1f7b2c-0000-4000-8000-0000000000c1", Claimed: 3}}
	ts := mountedIdentityServer(t, &identityFakeIssuer{}, identityRedeemer(t, signer, store))

	linkToken, err := signer.Sign(identityRouteEmail, "9a1f7b2c-0000-4000-8000-000000000001", time.Now())
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}

	status, raw := readIdentityAnswer(t, ts, identitySessionPath, `{"token":"`+linkToken+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", status, raw)
	}
	if keys := objectKeys(t, raw); !equalKeys(keys, []string{"expiresAt", "token"}) {
		t.Fatalf("answer keys = %v, want the token and its expiry", keys)
	}
	// The count of orders the address claimed is the operator's fact, not the caller's.
	for _, forbidden := range []string{"claimed", "count", "orders", "customer"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the answer names the claim (%q): %s", forbidden, raw)
		}
	}
	if strings.Contains(string(raw), linkToken) {
		t.Fatalf("the answer echoes the access link: %s", raw)
	}

	var sessionToken string
	if err := json.Unmarshal(objectField(t, raw, "token"), &sessionToken); err != nil {
		t.Fatalf("decode token %s: %v", raw, err)
	}
	if sessionToken == "" || sessionToken == linkToken {
		t.Fatalf("session token = %q, want a fresh credential", sessionToken)
	}
	var expiresAt time.Time
	if err := json.Unmarshal(objectField(t, raw, "expiresAt"), &expiresAt); err != nil {
		t.Fatalf("decode expiresAt %s: %v", raw, err)
	}
	if until := time.Until(expiresAt); until <= 0 || until > identitySessionTTL+time.Minute {
		t.Fatalf("expiresAt = %s (%s from now), want the session lifetime of %s", expiresAt, until, identitySessionTTL)
	}

	// The row holds the hash of the token it answered, and the token is nowhere in it.
	if len(store.calls) != 1 {
		t.Fatalf("the store was asked to redeem %d time(s), want exactly one", len(store.calls))
	}
	if !bytes.Equal(store.hash, identity.HashSessionToken(sessionToken)) {
		t.Fatal("the store was not given the hash of the token this route answered")
	}
	if bytes.Equal(store.hash, []byte(sessionToken)) {
		t.Fatal("the store was given the token itself instead of its hash")
	}
	// The address it verified is the one the link names, normalized.
	if got := store.calls[0].Email; got != identityRouteEmail {
		t.Fatalf("the redemption verified %q, want %q", got, identityRouteEmail)
	}
}

// T2 (S2): a spent, an expired and a forged link are one refusal with one status and one
// body. Telling them apart would confirm which links this deployment once issued, and — for
// a token somebody is watching — when its owner signed in with it.
func TestIdentitySessionRouteCannotTellSpentExpiredAndForgedApart(t *testing.T) {
	now := time.Now()

	// Spent: the link verifies, and the store refuses to spend it a second time.
	spentSigner := identitySigner(t, identityTestLinkKey, identity.LinkWindow)
	spentToken, err := spentSigner.Sign(identityRouteEmail, "9a1f7b2c-0000-4000-8000-000000000002", now)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	spent := mountedIdentityServer(t, &identityFakeIssuer{},
		identityRedeemer(t, spentSigner, &identityFakeRedemptionStore{err: identity.ErrLinkSpent}))

	// Expired: the signature is ours, and its window closed at the second it was minted.
	expiredSigner := identitySigner(t, identityTestLinkKey, time.Nanosecond)
	expiredToken, err := expiredSigner.Sign(identityRouteEmail, "9a1f7b2c-0000-4000-8000-000000000003", now)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	expired := mountedIdentityServer(t, &identityFakeIssuer{},
		identityRedeemer(t, expiredSigner, &identityFakeRedemptionStore{}))

	// Forged: a link this deployment never minted, because another key signed it.
	forgedSigner := identitySigner(t, identityAnotherLinkKey, identity.LinkWindow)
	forgedToken, err := forgedSigner.Sign(identityRouteEmail, "9a1f7b2c-0000-4000-8000-000000000004", now)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	forged := mountedIdentityServer(t, &identityFakeIssuer{},
		identityRedeemer(t, identitySigner(t, identityTestLinkKey, identity.LinkWindow), &identityFakeRedemptionStore{}))

	referenceStatus, referenceBody := readIdentityAnswer(t, spent, identitySessionPath, `{"token":"`+spentToken+`"}`)
	if referenceStatus != http.StatusUnauthorized {
		t.Fatalf("spent status = %d, want 401 (body %s)", referenceStatus, referenceBody)
	}
	if problem := decodedProblem(t, referenceBody); problem.Code != "unauthorized" {
		t.Fatalf("problem = %+v, want unauthorized", problem)
	}

	for name, got := range map[string]struct {
		ts   *httptest.Server
		body string
	}{
		"expired":           {expired, `{"token":"` + expiredToken + `"}`},
		"forged":            {forged, `{"token":"` + forgedToken + `"}`},
		"not a link at all": {forged, `{"token":"v1.not-a-signed-link"}`},
	} {
		status, raw := readIdentityAnswer(t, got.ts, identitySessionPath, got.body)
		if status != referenceStatus {
			t.Fatalf("%s status = %d, want %d (body %s)", name, status, referenceStatus, raw)
		}
		if !bytes.Equal(raw, referenceBody) {
			t.Fatalf("%s answer = %s, want the spent link's %s", name, raw, referenceBody)
		}
	}

	// The three answers never carry a credential, and never carry the address.
	for _, leaked := range []string{spentToken, expiredToken, forgedToken, identityRouteEmail} {
		if strings.Contains(string(referenceBody), leaked) {
			t.Fatalf("the refusal carries %q: %s", leaked, referenceBody)
		}
	}
}

// T2 (S1): a store that cannot answer is a retryable 503 and never a sign-out, and a signer
// that cannot be used is the same: our failure, not a verdict on the link in somebody's
// inbox.
func TestIdentitySessionRouteAnswersUnavailableWhenItCannotAnswer(t *testing.T) {
	// A link this deployment did sign, so the store is actually reached: the signature is
	// checked first, and a forged token would never get as far as the store.
	signer := identitySigner(t, identityTestLinkKey, identity.LinkWindow)
	signedToken, err := signer.Sign(identityRouteEmail, "9a1f7b2c-0000-4000-8000-000000000005", time.Now())
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}

	cases := []struct {
		name     string
		redeemer publicapi.IdentitySessionService
		token    string
	}{
		{"the store cannot answer", identityRedeemer(t, signer,
			&identityFakeRedemptionStore{err: fmt.Errorf("%w: conn closed", identity.ErrLinkUnavailable)}), signedToken},
		{"the signer cannot be used", &identityFakeRedeemer{err: fmt.Errorf("%w: the key is blank", identity.ErrInvalidKey)},
			strings.Repeat("a", 43)},
		{"an unexplained failure", &identityFakeRedeemer{err: errors.New("something else entirely")},
			strings.Repeat("a", 43)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ts := mountedIdentityServer(t, &identityFakeIssuer{}, testCase.redeemer)
			status, raw := readIdentityAnswer(t, ts, identitySessionPath, `{"token":"`+testCase.token+`"}`)
			if status != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (body %s)", status, raw)
			}
			problem := decodedProblem(t, raw)
			if problem.Code != "service_unavailable" || problem.Status != http.StatusServiceUnavailable {
				t.Fatalf("problem = %+v, want 503 service_unavailable", problem)
			}
		})
	}
}

// T2 (S1): the redeem body is bounded and refuses an unknown field exactly like the link
// request does, and a request that names no token never reaches the redeemer.
func TestIdentitySessionRouteRefusesAnUnusableRequest(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no body at all", ""},
		{"an empty object", `{}`},
		{"an empty token", `{"token":""}`},
		{"an unknown field beside the token", `{"token":"abc","email":"ana@example.test"}`},
		{"a body that is not JSON", `redeem me`},
		{"a body larger than the route reads", `{"token":"` + strings.Repeat("a", 2<<10) + `"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			redeemer := &identityFakeRedeemer{}
			ts := mountedIdentityServer(t, &identityFakeIssuer{}, redeemer)

			status, raw := readIdentityAnswer(t, ts, identitySessionPath, testCase.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", status, raw)
			}
			if problem := decodedProblem(t, raw); problem.Code != "invalid_input" {
				t.Fatalf("problem = %+v, want invalid_input", problem)
			}
			if redeemer.calls != 0 {
				t.Fatalf("the redeemer was asked %d time(s) for a request with no token", redeemer.calls)
			}
		})
	}
}

// T2 (S2): the two routes are the two halves of one act, so they are mounted as a pair. A
// deployment with only one collaborator advertises neither: a link that could be asked for
// and never redeemed, or a redeem endpoint with no way to obtain a link, is worse than an
// absent surface, and neither half can be answered honestly alone.
func TestIdentityRoutesAreNotMountedWithoutBothCollaborators(t *testing.T) {
	cases := []struct {
		name     string
		issuer   publicapi.IdentityLinkService
		redeemer publicapi.IdentitySessionService
	}{
		{"no issuer", nil, &identityFakeRedeemer{}},
		{"no redeemer", &identityFakeIssuer{}, nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ts := mountedIdentityServer(t, testCase.issuer, testCase.redeemer)
			for _, path := range []string{identityLinkPath, identitySessionPath} {
				status, raw := readIdentityAnswer(t, ts, path, `{"email":"`+identityRouteEmail+`","token":"`+strings.Repeat("a", 43)+`"}`)
				if status != http.StatusNotFound {
					t.Fatalf("POST %s = %d, want 404 like a missing route (body %s)", path, status, raw)
				}
			}
		})
	}
}

// ── Signing out (T3, S1) ────────────────────────────────────────────────────
//
// DELETE /api/v1/identity/sessions is the other end of the two routes above: it presents the
// session token this browser holds to [identity.SessionService.Close], so the row behind it
// stops working now instead of at its expiry.
//
// Its whole contract is one answer. `204 No Content` for a live token, an expired one, a
// revoked one, a forged one and a request with no Authorization header at all, because Close is
// deliberately quiet — a client told its sign-out failed believes it is still signed in — and
// because a route that answered 401 to an unknown token would let a caller ask whether a
// credential ever existed, which is the one question this surface refuses to answer everywhere
// else. A no-op is a truthful answer for a token there is nothing to close.

// identitySignOutToken is a string of the one shape a session token has. It is not a session
// some login minted; it is the credential these cases present on the wire.
var identitySignOutToken = strings.Repeat("s", 43)

// The session service the process wires is what closes a session through this route: this is
// the line that says the login's own service *is* the sign-out collaborator, rather than a
// second one that could disagree with the store the sessions live in.
var _ publicapi.IdentitySessionCloser = (*identity.SessionService)(nil)

// identityFakeSignOut is the collaborator a wire case presents to: it records the token it was
// given, which is what proves the header reached Close, and can refuse like a store that did
// not answer.
type identityFakeSignOut struct {
	tokens []string
	err    error
}

var _ publicapi.IdentitySessionCloser = (*identityFakeSignOut)(nil)

func (c *identityFakeSignOut) Close(_ context.Context, token string) error {
	c.tokens = append(c.tokens, token)
	return c.err
}

// identityFakeSessionStore is the session store in memory, behind the real service. Find answers
// ErrNoSession — every token opens nothing here, which is the one answer the real store gives for
// an expired row, a revoked one and one that never existed alike — and Revoke records the hash it
// was given, which is what lets a case show that a malformed credential never reached the store.
type identityFakeSessionStore struct {
	revoked [][]byte
	err     error
}

var _ identity.SessionStore = (*identityFakeSessionStore)(nil)

func (s *identityFakeSessionStore) Create(context.Context, identity.Session, []byte) error {
	return nil
}

func (s *identityFakeSessionStore) Find(context.Context, []byte, time.Time) (identity.Session, error) {
	return identity.Session{}, identity.ErrNoSession
}

func (s *identityFakeSessionStore) Touch(context.Context, []byte, time.Time) error { return nil }

func (s *identityFakeSessionStore) Revoke(_ context.Context, tokenHash []byte, _ time.Time) error {
	s.revoked = append(s.revoked, append([]byte(nil), tokenHash...))
	return s.err
}

// identitySessionService builds the real session service over the fake store, so a route case
// exercises Close's own contract instead of a stub of it.
func identitySessionService(t *testing.T, store identity.SessionStore) *identity.SessionService {
	t.Helper()
	sessions, err := identity.NewSessionService(store, identitySessionTTL)
	if err != nil {
		t.Fatalf("NewSessionService() = unexpected error %v", err)
	}
	return sessions
}

// mountedSignOutServer wires the public subtree with the login and its sign-out, the way
// cmd/api does. A nil closer is how "this deployment has no sign-out" is built.
func mountedSignOutServer(t *testing.T, signOut publicapi.IdentitySessionCloser) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(&fakeHoldService{}).
		WithIdentity(&identityFakeIssuer{}, &identityFakeRedeemer{}).
		WithIdentitySignOut(signOut).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// deleteIdentitySessions asks the sign-out route with whatever Authorization header the case
// means to present, and answers the status and the raw body.
func deleteIdentitySessions(t *testing.T, ts *httptest.Server, authorization string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodDelete, ts.URL+identitySessionPath, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("DELETE %s: %v", identitySessionPath, err)
	}
	return response.StatusCode, readBody(t, response)
}

// T3 (S1): the bearer token the header carries is what reaches Close, and the answer is
// `204 No Content` with no body at all — there is nothing to say about a credential that was
// just closed, and nothing about it in the response.
func TestIdentitySignOutRouteClosesTheSessionTheHeaderCarries(t *testing.T) {
	signOut := &identityFakeSignOut{}
	ts := mountedSignOutServer(t, signOut)

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	status, raw := deleteIdentitySessions(t, ts, "Bearer "+identitySignOutToken)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", status, raw)
	}
	if len(raw) != 0 {
		t.Fatalf("body = %s, want no body at all", raw)
	}

	// The credential reached the service, unchanged and trimmed: the transport does not
	// re-shape a token, because the domain owns what a token is.
	if len(signOut.tokens) != 1 || signOut.tokens[0] != identitySignOutToken {
		t.Fatalf("Close() was presented %q, want exactly the token the header carried", signOut.tokens)
	}

	// One line, naming the outcome and nothing else: the token is a credential and its hash is
	// the form a dump of the table would reveal.
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(logged.String()), "\n") {
		if strings.Contains(line, "identity session closed") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("logged %d sign-out line(s), want exactly one: %q", lines, logged.String())
	}
	for _, leaked := range []string{identitySignOutToken, "Bearer",
		fmt.Sprintf("%x", identity.HashSessionToken(identitySignOutToken))} {
		if strings.Contains(logged.String(), leaked) {
			t.Fatalf("the log leaked %q: %q", leaked, logged.String())
		}
	}
}

// T3 (S1): every credential that cannot be closed is answered exactly like one that was — a
// revoked row, a forged token, a string that is not a token at all and a request that presented
// no credential. The domain is what decides, so this case runs the real service: a malformed
// token is refused before the store is asked, and nothing at all is refused to the client.
func TestIdentitySignOutRouteAnswersTheSameNoContentForEveryTokenItCannotClose(t *testing.T) {
	store := &identityFakeSessionStore{}
	ts := mountedSignOutServer(t, identitySessionService(t, store))

	referenceStatus, referenceBody := deleteIdentitySessions(t, ts, "Bearer "+identitySignOutToken)
	if referenceStatus != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", referenceStatus, referenceBody)
	}
	if len(referenceBody) != 0 {
		t.Fatalf("body = %s, want no body at all", referenceBody)
	}

	cases := map[string]struct {
		authorization string
		// reachesTheStore is true when this case got far enough to be hashed and looked up: a
		// well-shaped token is, and a string that cannot be one is refused before any query.
		reachesTheStore bool
	}{
		"a token the store has no live row for": {"Bearer " + strings.Repeat("A", 43), true},
		"a string that cannot be a token":       {"Bearer not-a-token", false},
		"a header that is not a bearer":         {"Basic " + identitySignOutToken, false},
		"no Authorization header at all":        {"", false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			before := len(store.revoked)
			status, raw := deleteIdentitySessions(t, ts, testCase.authorization)
			if status != referenceStatus {
				t.Fatalf("status = %d, want the same %d a closed session answers (body %s)", status, referenceStatus, raw)
			}
			// Byte for byte: the answer is not even distinguishable by a whitespace difference.
			if !bytes.Equal(raw, referenceBody) {
				t.Fatalf("answer = %s, want the closed session's %s", raw, referenceBody)
			}
			if reached := len(store.revoked) > before; reached != testCase.reachesTheStore {
				if testCase.reachesTheStore {
					t.Fatal("a token of the documented shape was refused before the store was asked about it")
				}
				t.Fatal("a credential that cannot be a token reached the store, which would make it an oracle")
			}
		})
	}
}

// T3 (S1): a store that could not answer still answers 204 — the client's sign-out is not a
// question — and the one log line says so, because the row is in fact still open and the
// operator is the only one who can be told.
func TestIdentitySignOutRouteAnswersNoContentWhenTheStoreCannotAnswer(t *testing.T) {
	store := &identityFakeSessionStore{err: errors.New("connection refused")}
	ts := mountedSignOutServer(t, identitySessionService(t, store))

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	status, raw := deleteIdentitySessions(t, ts, "Bearer "+identitySignOutToken)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 even when the store did not answer (body %s)", status, raw)
	}
	if len(raw) != 0 {
		t.Fatalf("body = %s, want no body at all", raw)
	}
	if len(store.revoked) != 1 {
		t.Fatalf("the store was asked %d time(s), want exactly one", len(store.revoked))
	}
	if !strings.Contains(logged.String(), "identity session closed") {
		t.Fatalf("log = %q, want the one sign-out line", logged.String())
	}
	// The outcome is the only thing this line can say: Close refuses to distinguish a token it
	// just closed from one there was nothing to close, so the route says the one thing it can —
	// that the row was *not* closed, which is the fact an operator needs.
	if !strings.Contains(logged.String(), "outcome=unavailable") {
		t.Fatalf("log = %q, want the outcome of a store that did not answer", logged.String())
	}
	if strings.Contains(logged.String(), identitySignOutToken) {
		t.Fatalf("the log leaked the token: %q", logged.String())
	}
}

// T3 (S1): the route is part of the login surface, so a deployment without identity never
// advertises it — a sign-out that could close nothing would be a button that lies.
func TestIdentitySignOutRouteIsNotMountedWithoutTheLoginSurface(t *testing.T) {
	ts := mountedIdentityServer(t, nil, nil)

	status, raw := deleteIdentitySessions(t, ts, "Bearer "+identitySignOutToken)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 like a route this deployment does not serve (body %s)", status, raw)
	}
}
