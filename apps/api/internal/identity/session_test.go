// Behaviour tests for the session domain (T3, S1).
//
// A session is an opaque token the client holds and a row the database holds, and the two
// are not the same value: only the hash of the token is stored, so a copy of the table
// cannot be replayed as anybody's session. What is pinned here, with a fake store and no
// database: a token is 32 random bytes spelled as 43 base64url characters and two mints
// differ; the store is handed the SHA-256 of the token and never the token itself; opening a
// session and authenticating with its token answer the same customer and the same expiry;
// an expired session, a revoked one and a token that never existed are one answer
// (ErrNoSession), because a caller must not be able to tell "your session ended" from "that
// token never existed"; authenticating refreshes last_seen_at without moving expires_at, and
// a refresh that fails is bookkeeping rather than a gate; closing is idempotent and quiet;
// and a string that cannot be a token is refused before the store is asked anything.
package identity_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/identity"
)

// sessionTTL is the documented default session lifetime: thirty days. It is long enough to
// be useful and short enough that a stolen cookie is not eternal (S1), which is why the
// cases below assert the expiry it produces instead of a hard-coded instant.
const sessionTTL = 720 * time.Hour

// sessionTokenLength is the only token length this package accepts: base64url without
// padding spells 32 bytes as 43 characters. It is spelled out here rather than imported so
// the test fails if the encoding ever changes shape.
const sessionTokenLength = 43

// sessionURLAlphabet is every character a base64url spelling may use. A token that carried
// anything else would not survive a cookie or a URL header unchanged.
const sessionURLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// A session fixture: one account, so a case reads like the sentence it asserts.
const sessionCustomer = "user-1"

// sessionCreation is one recorded Create: what the service handed the store, kept as the
// store received it (a copied hash, never a reference into the service).
type sessionCreation struct {
	session   identity.Session
	tokenHash []byte
}

// sessionSighting is one recorded Touch or Revoke: the hash and the instant.
type sessionSighting struct {
	tokenHash []byte
	now       time.Time
}

// fakeSessionStore is a session store that remembers what it was asked and answers what the
// case told it to, so the unit suite needs no PostgreSQL. It models the two facts the real
// store's SQL enforces — a revoked row is not found again, and a second revocation matches
// nothing — so the idempotence asserted below is the property the SQL carries, not a
// politeness of the fake.
type fakeSessionStore struct {
	createErr error
	findErr   error
	touchErr  error
	revokeErr error

	created     []sessionCreation
	found       identity.Session
	findHashes  [][]byte
	findNows    []time.Time
	touchCalls  []sessionSighting
	revokeCalls []sessionSighting

	// revoked stands in for `revoked_at IS NOT NULL`.
	revoked bool
}

// compile-time proof that the fake is the contract the service is written against.
var _ identity.SessionStore = (*fakeSessionStore)(nil)

func (f *fakeSessionStore) Create(_ context.Context, session identity.Session, tokenHash []byte) error {
	f.created = append(f.created, sessionCreation{
		session:   session,
		tokenHash: append([]byte(nil), tokenHash...),
	})
	if f.createErr != nil {
		return f.createErr
	}
	// A created session is a session this store holds, exactly as the row would be.
	f.found = session
	return nil
}

func (f *fakeSessionStore) Find(_ context.Context, tokenHash []byte, now time.Time) (identity.Session, error) {
	f.findHashes = append(f.findHashes, append([]byte(nil), tokenHash...))
	f.findNows = append(f.findNows, now)
	if f.findErr != nil {
		return identity.Session{}, f.findErr
	}
	if f.revoked {
		return identity.Session{}, identity.ErrNoSession
	}
	return f.found, nil
}

func (f *fakeSessionStore) Touch(_ context.Context, tokenHash []byte, now time.Time) error {
	f.touchCalls = append(f.touchCalls, sessionSighting{tokenHash: append([]byte(nil), tokenHash...), now: now})
	return f.touchErr
}

func (f *fakeSessionStore) Revoke(_ context.Context, tokenHash []byte, now time.Time) error {
	f.revokeCalls = append(f.revokeCalls, sessionSighting{tokenHash: append([]byte(nil), tokenHash...), now: now})
	if f.revokeErr != nil {
		return f.revokeErr
	}
	if f.revoked {
		// The UPDATE is guarded by `revoked_at IS NULL`, so the second call matches no row.
		return identity.ErrNoSession
	}
	f.revoked = true
	return nil
}

// mustSessionService builds a service over a fake and fails the test when the constructor
// refuses it. The fake is returned too, because every case asserts on what the store saw.
func mustSessionService(t *testing.T) (*identity.SessionService, *fakeSessionStore) {
	t.Helper()
	store := &fakeSessionStore{}
	service, err := identity.NewSessionService(store, sessionTTL)
	if err != nil {
		t.Fatalf("NewSessionService() = unexpected error %v", err)
	}
	return service, store
}

// openSession opens one session for the fixture account and fails the test on error.
func openSession(t *testing.T, service *identity.SessionService) (string, identity.Session) {
	t.Helper()
	token, session, err := service.Open(context.Background(), sessionCustomer)
	if err != nil {
		t.Fatalf("Open() = unexpected error %v", err)
	}
	return token, session
}

// TestSessionTokensAreOpaqueAndRandom pins the shape of the credential (S1): 32 bytes of
// crypto/rand in the URL-safe base64 alphabet without padding, and two mints differ. A
// token is also not a UUID and carries no structure a client could read or guess.
func TestSessionTokensAreOpaqueAndRandom(t *testing.T) {
	first, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() = unexpected error %v", err)
	}
	second, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() = unexpected error %v", err)
	}

	if len(first) != sessionTokenLength {
		t.Fatalf("the token is %d characters, want %d: 32 bytes in base64url without padding",
			len(first), sessionTokenLength)
	}
	for _, r := range first {
		if !strings.ContainsRune(sessionURLAlphabet, r) {
			t.Fatalf("the token carries %q, which is not in the base64url alphabet", r)
		}
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(first)
	if err != nil {
		t.Fatalf("the token does not decode as canonical base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("the token decodes to %d bytes, want 32", len(decoded))
	}
	if first == second {
		t.Fatal("two mints produced the same token: a credential must not be a constant")
	}
}

// TestHashingIsStableAndNotTheToken pins the one-way half of S1: the stored value is the
// SHA-256 of the token, not the token, because the token lives only in the client's cookie.
func TestHashingIsStableAndNotTheToken(t *testing.T) {
	first, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() = unexpected error %v", err)
	}
	second, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() = unexpected error %v", err)
	}

	hash := identity.HashSessionToken(first)
	if len(hash) != 32 {
		t.Fatalf("the hash is %d bytes, want 32 (SHA-256)", len(hash))
	}
	if !bytes.Equal(hash, identity.HashSessionToken(first)) {
		t.Fatal("hashing one token twice answered two values: a lookup could not find its own row")
	}
	if bytes.Equal(hash, identity.HashSessionToken(second)) {
		t.Fatal("two distinct tokens hashed to one value")
	}
	if bytes.Equal(hash, []byte(first)) {
		t.Fatal("the hash is the token: a database dump would be a set of live credentials")
	}
}

// TestOpenStoresTheHashAndNotTheToken is the assertion S1 exists for: what the store
// receives is the SHA-256 of the token, and the token itself never reaches it — not as the
// credential, not as a field of the row. It is checked on what the fake recorded, so it is
// the real value that crossed the boundary and not a value the test built.
func TestOpenStoresTheHashAndNotTheToken(t *testing.T) {
	service, store := mustSessionService(t)
	token, _ := openSession(t, service)

	if len(store.created) != 1 {
		t.Fatalf("Open() created %d rows, want 1", len(store.created))
	}
	recorded := store.created[0].tokenHash
	if !bytes.Equal(recorded, identity.HashSessionToken(token)) {
		t.Fatal("the store did not receive the SHA-256 of the token")
	}
	if bytes.Equal(recorded, []byte(token)) {
		t.Fatal("the store received the token itself: the table must hold only its hash")
	}
	// Nothing else the store was handed may carry the token either.
	if strings.Contains(fmt.Sprintf("%+v", store.created[0].session), token) {
		t.Fatal("the session handed to the store carries the token")
	}
}

// TestOpenAndAuthenticateAgree pins the round trip: the token Open returned opens the session
// Open stored, with the same account and the same expiry, and the expiry is this service's
// lifetime from now (never the store's guess).
func TestOpenAndAuthenticateAgree(t *testing.T) {
	service, _ := mustSessionService(t)
	before := time.Now().Add(-time.Second)
	token, opened := openSession(t, service)
	after := time.Now().Add(time.Second)

	if opened.CustomerID != sessionCustomer {
		t.Fatalf("the opened session belongs to %q, want %q", opened.CustomerID, sessionCustomer)
	}
	if opened.LastSeenAt != nil {
		t.Fatalf("a session that has not been presented once reports last_seen_at = %v, want none",
			opened.LastSeenAt)
	}
	if !opened.ExpiresAt.After(before) || opened.ExpiresAt.After(after.Add(sessionTTL)) {
		t.Fatalf("ExpiresAt = %v, want now + %v", opened.ExpiresAt, sessionTTL)
	}
	if opened.ID == "" {
		t.Fatal("the opened session carries no identifier")
	}

	authenticated, err := service.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate() = unexpected error %v", err)
	}
	if authenticated.CustomerID != opened.CustomerID {
		t.Fatalf("Authenticate() answered customer %q, want %q", authenticated.CustomerID, opened.CustomerID)
	}
	if !authenticated.ExpiresAt.Equal(opened.ExpiresAt) {
		t.Fatalf("Authenticate() answered expiry %v, want %v: authenticating must not move it",
			authenticated.ExpiresAt, opened.ExpiresAt)
	}
}

// TestEveryDeadSessionAnswersOneError is S1's opacity rule: expired, revoked and never
// existed are one error, so a caller cannot learn from an answer whether a token was once
// real. The store is made to say *why* in each case, which is the whole point — the service
// collapses every reason into the same sentinel rather than forwarding the store's words.
func TestEveryDeadSessionAnswersOneError(t *testing.T) {
	cases := []struct {
		name     string
		storeErr error
	}{
		{"expired", fmt.Errorf("%w: the row expired at noon", identity.ErrNoSession)},
		{"revoked", fmt.Errorf("%w: the row was closed by its owner", identity.ErrNoSession)},
		{"unknown", fmt.Errorf("%w: no row holds that hash", identity.ErrNoSession)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, store := mustSessionService(t)
			token, _ := openSession(t, service)
			store.findErr = testCase.storeErr

			session, err := service.Authenticate(context.Background(), token)
			if err != identity.ErrNoSession {
				t.Fatalf("Authenticate() = %v, want exactly ErrNoSession for every way a session ends: "+
					"a caller must not be able to tell them apart", err)
			}
			if session != (identity.Session{}) {
				t.Fatalf("a refused authentication answered a session: %+v", session)
			}
			if len(store.touchCalls) != 0 {
				t.Fatal("a refused authentication refreshed a session that is not a session")
			}
		})
	}
}

// TestAStoreFailureIsNotReportedAsASignedOutUser pins the other half of the opacity rule: "I
// cannot answer" must never be disguised as "that token never existed", because a database
// outage would then look to every signed-in person like a silent sign-out.
func TestAStoreFailureIsNotReportedAsASignedOutUser(t *testing.T) {
	service, store := mustSessionService(t)
	token, _ := openSession(t, service)
	store.findErr = errors.New("the database is unreachable")

	if _, err := service.Authenticate(context.Background(), token); err == nil {
		t.Fatal("Authenticate() with an unreachable store = nil, want an error")
	} else if errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("Authenticate() reported a store failure as ErrNoSession: %v", err)
	}
}

// TestAStringThatCannotBeATokenNeverReachesTheStore pins the cheap refusal: a token is 43
// characters of canonical base64url, so anything else cannot name a session and is answered
// before a query is spent on it — with the same opaque error, so the shape of a token is not
// a hint a caller can probe for.
func TestAStringThatCannotBeATokenNeverReachesTheStore(t *testing.T) {
	cases := []struct{ name, token string }{
		{"empty", ""},
		{"too short", strings.Repeat("a", sessionTokenLength-1)},
		{"too long", strings.Repeat("a", sessionTokenLength+1)},
		{"a character outside the alphabet", "!" + strings.Repeat("a", sessionTokenLength-1)},
		{"base64 padding", strings.Repeat("a", sessionTokenLength-1) + "="},
		{"standard base64 characters", strings.Repeat("+", sessionTokenLength)},
		// 43 characters whose final character carries bits the 32 bytes do not have: one
		// token has one spelling, and a second spelling would be a second hash.
		{"a non-canonical final character", strings.Repeat("A", sessionTokenLength-1) + "B"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, store := mustSessionService(t)

			if _, err := service.Authenticate(context.Background(), testCase.token); err != identity.ErrNoSession {
				t.Fatalf("Authenticate(%q) = %v, want exactly ErrNoSession", testCase.token, err)
			}
			if len(store.findHashes) != 0 {
				t.Fatalf("Authenticate(%q) asked the store about a string that cannot be a token",
					testCase.token)
			}
			if err := service.Close(context.Background(), testCase.token); err != nil {
				t.Fatalf("Close(%q) = %v, want nil", testCase.token, err)
			}
			if len(store.revokeCalls) != 0 {
				t.Fatalf("Close(%q) asked the store about a string that cannot be a token",
					testCase.token)
			}
		})
	}
}

// TestAuthenticateRefreshesLastSeenButNotExpiry pins the two halves of S1's no-silent-renewal
// rule: a session that is being used says so, and using it never pushes its own end further
// out. A session is not a rolling window.
func TestAuthenticateRefreshesLastSeenButNotExpiry(t *testing.T) {
	service, store := mustSessionService(t)
	token, opened := openSession(t, service)

	before := time.Now().Add(-time.Second)
	authenticated, err := service.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate() = unexpected error %v", err)
	}
	after := time.Now().Add(time.Second)

	if len(store.touchCalls) != 1 {
		t.Fatalf("Authenticate() refreshed the row %d times, want 1", len(store.touchCalls))
	}
	sighting := store.touchCalls[0]
	if !bytes.Equal(sighting.tokenHash, identity.HashSessionToken(token)) {
		t.Fatal("the refresh was addressed by something other than the token's hash")
	}
	if sighting.now.Before(before) || sighting.now.After(after) {
		t.Fatalf("the refresh was stamped %v, want an instant inside this call", sighting.now)
	}

	if authenticated.LastSeenAt == nil {
		t.Fatal("Authenticate() answered no last_seen_at after a refresh that succeeded")
	}
	if !authenticated.LastSeenAt.Equal(sighting.now) {
		t.Fatalf("Authenticate() answered last_seen_at %v, want the instant it recorded, %v",
			*authenticated.LastSeenAt, sighting.now)
	}
	if !authenticated.ExpiresAt.Equal(opened.ExpiresAt) {
		t.Fatalf("authenticating moved the expiry from %v to %v: a session is not renewed silently",
			opened.ExpiresAt, authenticated.ExpiresAt)
	}
	if !store.found.ExpiresAt.Equal(opened.ExpiresAt) {
		t.Fatalf("the stored row's expiry moved to %v, want %v", store.found.ExpiresAt, opened.ExpiresAt)
	}
}

// TestAFailedRefreshStillAuthenticates pins that the refresh is bookkeeping and not a gate:
// a session is usable when the row cannot be stamped, because refusing here would sign
// somebody out over an update that changes nothing about the credential.
func TestAFailedRefreshStillAuthenticates(t *testing.T) {
	service, store := mustSessionService(t)
	token, opened := openSession(t, service)
	store.touchErr = errors.New("the update timed out")

	authenticated, err := service.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate() with a failed refresh = %v, want the session: the refresh is "+
			"bookkeeping, not a gate", err)
	}
	if authenticated.CustomerID != opened.CustomerID {
		t.Fatalf("Authenticate() answered customer %q, want %q", authenticated.CustomerID, opened.CustomerID)
	}
	if authenticated.LastSeenAt != nil {
		t.Fatalf("Authenticate() reported last_seen_at = %v after a refresh that failed, "+
			"want none: the answer must not claim a sighting that was not recorded",
			authenticated.LastSeenAt)
	}
}

// TestCloseIsIdempotentAndQuiet pins the decision this task had to take: closing answers nil
// whether the token was live, already closed, never issued or not even a token. Sign-out
// cannot fail in a way that tells a client its token used to exist, and it cannot leave a
// client believing it is still signed in because a row was already gone.
func TestCloseIsIdempotentAndQuiet(t *testing.T) {
	service, store := mustSessionService(t)
	token, _ := openSession(t, service)

	if err := service.Close(context.Background(), token); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}
	if !store.revoked {
		t.Fatal("Close() did not reach the store: the row is still live")
	}
	if err := service.Close(context.Background(), token); err != nil {
		t.Fatalf("second Close() = %v, want nil: revoking twice is not an error", err)
	}
	if len(store.revokeCalls) != 2 {
		t.Fatalf("the store was asked to revoke %d times, want 2", len(store.revokeCalls))
	}
	for _, call := range store.revokeCalls {
		if !bytes.Equal(call.tokenHash, identity.HashSessionToken(token)) {
			t.Fatal("a revocation was addressed by something other than the token's hash")
		}
	}

	// A string that could be a token but was never issued answers the same quiet nil.
	unknown, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() = unexpected error %v", err)
	}
	if err := service.Close(context.Background(), unknown); err != nil {
		t.Fatalf("Close() of a token that was never issued = %v, want nil: a client must not be "+
			"able to learn whether it existed", err)
	}
	if _, err := service.Authenticate(context.Background(), token); err != identity.ErrNoSession {
		t.Fatalf("Authenticate() after Close() = %v, want ErrNoSession", err)
	}
}

// TestAClosedSessionDoesNotAuthenticate is the other side of T3: closing is not decoration.
func TestAClosedSessionDoesNotAuthenticate(t *testing.T) {
	service, _ := mustSessionService(t)
	token, _ := openSession(t, service)

	if err := service.Close(context.Background(), token); err != nil {
		t.Fatalf("Close() = unexpected error %v", err)
	}
	if _, err := service.Authenticate(context.Background(), token); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("Authenticate() after Close() = %v, want ErrNoSession", err)
	}
}

// TestCloseReportsAStoreFailure pins that quiet is about "nothing matched" and not about
// everything: a revocation the database could not perform is reported, because the session
// is still open and the caller has to know.
func TestCloseReportsAStoreFailure(t *testing.T) {
	service, store := mustSessionService(t)
	token, _ := openSession(t, service)
	store.revokeErr = errors.New("the database is unreachable")

	err := service.Close(context.Background(), token)
	if err == nil {
		t.Fatal("Close() with an unreachable store = nil, want an error: the row is still live")
	}
	if errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("Close() reported a store failure as ErrNoSession: %v", err)
	}
}

// TestNewSessionServiceRefusesWhatCannotWork pins the constructor guards: a service with no
// store cannot open or read anything, and a non-positive lifetime would mint sessions that
// are already over.
func TestNewSessionServiceRefusesWhatCannotWork(t *testing.T) {
	if _, err := identity.NewSessionService(nil, sessionTTL); err == nil {
		t.Fatal("NewSessionService(nil, ...) = nil, want an error")
	}
	if _, err := identity.NewSessionService(&fakeSessionStore{}, 0); err == nil {
		t.Fatal("NewSessionService(store, 0) = nil, want an error")
	} else if !strings.Contains(err.Error(), "positive") {
		t.Fatalf("a zero lifetime was refused without saying it must be positive: %v", err)
	}
	if _, err := identity.NewSessionService(&fakeSessionStore{}, -time.Minute); err == nil {
		t.Fatal("NewSessionService(store, -1m) = nil, want an error")
	}
}

// TestOpenRefusesACustomerThatCannotBeOne pins that a session names an account: the row's
// foreign key is NOT NULL, so a blank identifier can only be a programming mistake, and it
// is refused before a token is minted or a row is written.
func TestOpenRefusesACustomerThatCannotBeOne(t *testing.T) {
	service, store := mustSessionService(t)

	if _, _, err := service.Open(context.Background(), "  "); err == nil {
		t.Fatal("Open() of a blank customer = nil, want an error")
	}
	if len(store.created) != 0 {
		t.Fatal("Open() wrote a session for a blank customer")
	}
}
