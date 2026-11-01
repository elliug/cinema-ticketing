// Behaviour tests for redeeming a login link (T2, S2, S3).
//
// Redeeming is the moment one link becomes one session: the token is verified, a session
// token is minted, and the whole write — spend the link, verify the address, claim the older
// orders, write the session — belongs to the store as one transaction. What is pinned here,
// with a fake store and no database and no clock: a valid link answers a session token that
// hashes to the row that was written and never to the token itself; the session's expiry is
// the session lifetime and not the link's; a link that does not verify opens nothing and
// asks the store nothing; and a link the store refuses to spend answers its one opaque
// refusal, with no token and no session handed back.
package identity_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/identity"
)

// redemptionCall is one recorded write, kept as the store received it: the claim, the
// session and a copied hash, never a reference into the redeemer.
type redemptionCall struct {
	claim     identity.AccessClaims
	session   identity.Session
	tokenHash []byte
}

// fakeRedemptionStore records the transaction the redeemer delegates, and can refuse with a
// link the store will not spend.
type fakeRedemptionStore struct {
	calls []redemptionCall
	err   error
}

var _ identity.RedemptionStore = (*fakeRedemptionStore)(nil)

func (f *fakeRedemptionStore) Redeem(_ context.Context, claim identity.AccessClaims, session identity.Session, tokenHash []byte) error {
	f.calls = append(f.calls, redemptionCall{
		claim:     claim,
		session:   session,
		tokenHash: append([]byte(nil), tokenHash...),
	})
	return f.err
}

// mustRedeemer builds a redeemer over a fake store with the documented session lifetime, and
// answers the signer so a case can mint links.
func mustRedeemer(t *testing.T) (*identity.Redeemer, *identity.AccessSigner, *fakeRedemptionStore) {
	t.Helper()
	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	store := &fakeRedemptionStore{}
	redeemer, err := identity.NewRedeemer(signer, store, sessionTTL)
	if err != nil {
		t.Fatalf("NewRedeemer() = unexpected error %v", err)
	}
	return redeemer, signer, store
}

// S2, S3: a valid link answers a session token that is the hash of what was written, and a
// session whose expiry belongs to the session lifetime rather than to the link's fifteen
// minutes. The token is the only copy that will exist and it is never stored.
func TestRedeemAnswersATokenThatHashesToTheSessionItWrote(t *testing.T) {
	ctx := context.Background()
	redeemer, signer, store := mustRedeemer(t)
	signedAt := time.Now().UTC()
	token := signAccess(t, signer, accessEmail, accessNonce, signedAt)

	before := time.Now().UTC()
	sessionToken, session, err := redeemer.Redeem(ctx, token)
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("Redeem() = unexpected error %v", err)
	}

	if len(store.calls) != 1 {
		t.Fatalf("the redeemer asked the store to write %d times, want exactly one", len(store.calls))
	}
	call := store.calls[0]
	if call.claim.Email != accessEmail || call.claim.Nonce != accessNonce {
		t.Fatalf("the store was handed claim %+v, want the one the token names", call.claim)
	}
	if len(sessionToken) != sessionTokenLength {
		t.Fatalf("Redeem() answered a token of %d characters, want %d", len(sessionToken), sessionTokenLength)
	}
	if !bytes.Equal(call.tokenHash, identity.HashSessionToken(sessionToken)) {
		t.Fatal("the written row is not found by the hash of the returned token, so the token " +
			"the person holds would open nothing")
	}
	if bytes.Equal(call.tokenHash, []byte(sessionToken)) {
		t.Fatal("the store was handed the token itself: the table would hold a live credential")
	}
	if session.ID == "" || session.ID != call.session.ID {
		t.Fatalf("the answered session %q is not the one written (%q)", session.ID, call.session.ID)
	}
	if !call.session.CreatedAt.Equal(session.CreatedAt) || !call.session.ExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("the answered session %+v is not the one written %+v", session, call.session)
	}

	// The session's life is the session's, taken once from the redeemer: a session that
	// inherited the link's fifteen minutes would sign everybody out before they read the
	// message.
	if session.CreatedAt.Before(before) || session.CreatedAt.After(after) {
		t.Fatalf("the session was created at %v, want between %v and %v", session.CreatedAt, before, after)
	}
	expiry := session.CreatedAt.Add(sessionTTL)
	if !session.ExpiresAt.Equal(expiry) {
		t.Fatalf("the session expires at %v, want %v: it uses the session lifetime, not the link's",
			session.ExpiresAt, expiry)
	}
	if !session.ExpiresAt.After(call.claim.ExpiresAt) {
		t.Fatalf("the session expires at %v and the link at %v: the session must outlive the "+
			"step that opened it", session.ExpiresAt, call.claim.ExpiresAt)
	}
}

// A link that does not verify opens no session and never reaches the store: a forged token
// must not even be looked up, and an expired one is the signature's own refusal.
func TestALinkThatDoesNotVerifyOpensNoSession(t *testing.T) {
	ctx := context.Background()
	redeemer, _, store := mustRedeemer(t)
	now := time.Now().UTC()

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"not a token at all", "not-a-token", identity.ErrInvalidToken},
		{"signed by somebody else", sealAccess(accessPayload(accessEmail, accessNonce, now), accessAttackerKey), identity.ErrInvalidToken},
		{"a payload respelled under its own signature", resignAccess(t, signAccess(t, mustAccessSigner(t, accessTestKey, accessTTL), accessEmail, accessNonce, now), accessPayload("other@example.test", accessNonce, now)), identity.ErrInvalidToken},
		{"expired", signAccess(t, mustAccessSigner(t, accessTestKey, accessTTL), accessEmail, accessNonce, now.Add(-time.Hour)), identity.ErrAccessLinkExpired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store.calls = nil
			sessionToken, session, err := redeemer.Redeem(ctx, c.token)
			if !errors.Is(err, c.want) {
				t.Fatalf("Redeem() = %v, want %v", err, c.want)
			}
			if sessionToken != "" || session != (identity.Session{}) {
				t.Fatalf("a refused link answered token %q and session %+v, want neither", sessionToken, session)
			}
			if len(store.calls) != 0 {
				t.Fatalf("a refused link reached the store %d times, want none", len(store.calls))
			}
			assertDoesNotName(t, err.Error(), accessNonce, accessEmail)
		})
	}
}

// S2: spent, unknown and expired-row links are one refusal from the store, and the redeemer
// hands back no token at all — the token it minted dies with the transaction, so a link that
// could not be spent cannot leave a credential behind.
func TestASpentLinkAnswersNoTokenAndTheStoresOneError(t *testing.T) {
	ctx := context.Background()
	redeemer, signer, store := mustRedeemer(t)
	token := signAccess(t, signer, accessEmail, accessNonce, time.Now().UTC())
	store.err = identity.ErrLinkSpent

	sessionToken, session, err := redeemer.Redeem(ctx, token)
	if !errors.Is(err, identity.ErrLinkSpent) {
		t.Fatalf("Redeem() of a spent link = %v, want ErrLinkSpent", err)
	}
	if sessionToken != "" || session != (identity.Session{}) {
		t.Fatalf("a spent link answered token %q and session %+v, want neither", sessionToken, session)
	}
	if len(store.calls) != 1 {
		t.Fatalf("the store answered a spent link after %d calls, want exactly one", len(store.calls))
	}
	assertDoesNotName(t, err.Error(), accessNonce, accessEmail)
	if strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "already") {
		t.Fatalf("the refusal explains itself: %q", err.Error())
	}
}

// A redeemer that cannot verify or cannot write is refused at construction.
func TestNewRedeemerRefusesWhatCannotWork(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	store := &fakeRedemptionStore{}

	if redeemer, err := identity.NewRedeemer(nil, store, sessionTTL); err == nil {
		t.Fatalf("NewRedeemer() without a signer = %v, nil; want a refusal", redeemer)
	}
	if redeemer, err := identity.NewRedeemer(signer, nil, sessionTTL); err == nil {
		t.Fatalf("NewRedeemer() without a store = %v, nil; want a refusal", redeemer)
	}
	if redeemer, err := identity.NewRedeemer(signer, store, 0); err == nil {
		t.Fatalf("NewRedeemer() with no lifetime = %v, nil; want a refusal", redeemer)
	}
}
