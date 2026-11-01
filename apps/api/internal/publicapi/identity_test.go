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

// identityFakeRedemptionStore records the transaction the redeemer delegated, and can refuse.
type identityFakeRedemptionStore struct {
	calls []identity.AccessClaims
	hash  []byte
	err   error
}

var _ identity.RedemptionStore = (*identityFakeRedemptionStore)(nil)

func (s *identityFakeRedemptionStore) Redeem(_ context.Context, claim identity.AccessClaims, session identity.Session, tokenHash []byte) error {
	s.calls = append(s.calls, claim)
	s.hash = append([]byte(nil), tokenHash...)
	return s.err
}

// identityFakeIssuer and identityFakeRedeemer are the collaborators a route case does not
// exercise. They exist so a case can mount the surface with one real half and one dummy: the
// routes are mounted as a pair (see TestIdentityRoutesAreNotMountedWithoutBothCollaborators).
type identityFakeIssuer struct{ calls int }

func (i *identityFakeIssuer) Issue(_ context.Context, _ string) error { i.calls++; return nil }

type identityFakeRedeemer struct {
	token   string
	session identity.Session
	err     error
	calls   int
}

func (r *identityFakeRedeemer) Redeem(_ context.Context, _ string) (string, identity.Session, error) {
	r.calls++
	return r.token, r.session, r.err
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

// identityRedeemer builds the real redeeming half over the fake store.
func identityRedeemer(t *testing.T, signer *identity.AccessSigner, store identity.RedemptionStore) *identity.Redeemer {
	t.Helper()
	redeemer, err := identity.NewRedeemer(signer, store, identitySessionTTL)
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
	store := &identityFakeRedemptionStore{}
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
