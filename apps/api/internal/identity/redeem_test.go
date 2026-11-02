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
//
// The one line a successful redemption writes is pinned here too (D5): how many older orders
// the verified address claimed, and for which account — never the address, never a token and
// never a hash — and nothing at all when the redemption was refused.
package identity_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/identity"
)

// redeemedCustomer is the account the fake store reports: the row the transaction resolved,
// which is what the log line has to name.
const redeemedCustomer = "6b1f0c4e-6a5d-4c31-9d2f-0a7b3e5c8d42"

// redemptionCall is one recorded write, kept as the store received it: the claim, the
// session and a copied hash, never a reference into the redeemer.
type redemptionCall struct {
	claim     identity.AccessClaims
	session   identity.Session
	tokenHash []byte
}

// fakeRedemptionStore records the transaction the redeemer delegates, answers what that
// transaction wrote, and can refuse with a link the store will not spend.
type fakeRedemptionStore struct {
	calls      []redemptionCall
	redemption identity.Redemption
	err        error
}

var _ identity.RedemptionStore = (*fakeRedemptionStore)(nil)

func (f *fakeRedemptionStore) Redeem(_ context.Context, claim identity.AccessClaims, session identity.Session, tokenHash []byte) (identity.Redemption, error) {
	f.calls = append(f.calls, redemptionCall{
		claim:     claim,
		session:   session,
		tokenHash: append([]byte(nil), tokenHash...),
	})
	if f.err != nil {
		return identity.Redemption{}, f.err
	}
	return f.redemption, nil
}

// mustRedeemer builds a redeemer over a fake store with the documented session lifetime, and
// answers the signer so a case can mint links. The log goes nowhere: a case that reads the
// log builds its own logger.
func mustRedeemer(t *testing.T) (*identity.Redeemer, *identity.AccessSigner, *fakeRedemptionStore) {
	t.Helper()
	return mustRedeemerWithLogger(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// mustRedeemerWithLogger is the same build with the log a case can read back.
func mustRedeemerWithLogger(t *testing.T, logger *slog.Logger) (*identity.Redeemer, *identity.AccessSigner, *fakeRedemptionStore) {
	t.Helper()
	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	store := &fakeRedemptionStore{redemption: identity.Redemption{CustomerID: redeemedCustomer}}
	redeemer, err := identity.NewRedeemer(signer, store, sessionTTL, logger)
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
	sessionToken, session, _, err := redeemer.Redeem(ctx, token)
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
			sessionToken, session, claimed, err := redeemer.Redeem(ctx, c.token)
			if !errors.Is(err, c.want) {
				t.Fatalf("Redeem() = %v, want %v", err, c.want)
			}
			if sessionToken != "" || session != (identity.Session{}) {
				t.Fatalf("a refused link answered token %q and session %+v, want neither", sessionToken, session)
			}
			if len(store.calls) != 0 {
				t.Fatalf("a refused link reached the store %d times, want none", len(store.calls))
			}
			if claimed != 0 {
				t.Fatalf("a refused link answered claimed=%d, want no claim at all", claimed)
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

	sessionToken, session, claimed, err := redeemer.Redeem(ctx, token)
	if !errors.Is(err, identity.ErrLinkSpent) {
		t.Fatalf("Redeem() of a spent link = %v, want ErrLinkSpent", err)
	}
	if sessionToken != "" || session != (identity.Session{}) {
		t.Fatalf("a spent link answered token %q and session %+v, want neither", sessionToken, session)
	}
	if claimed != 0 {
		t.Fatalf("a spent link answered claimed=%d, want no claim at all", claimed)
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if redeemer, err := identity.NewRedeemer(nil, store, sessionTTL, logger); err == nil {
		t.Fatalf("NewRedeemer() without a signer = %v, nil; want a refusal", redeemer)
	}
	if redeemer, err := identity.NewRedeemer(signer, nil, sessionTTL, logger); err == nil {
		t.Fatalf("NewRedeemer() without a store = %v, nil; want a refusal", redeemer)
	}
	if redeemer, err := identity.NewRedeemer(signer, store, 0, logger); err == nil {
		t.Fatalf("NewRedeemer() with no lifetime = %v, nil; want a refusal", redeemer)
	}
}

// D5 (S3): a successful redemption writes one line saying what it claimed and for which
// account. "Se anota en el log" is the whole of the record a claim leaves, so it has to say
// enough to be worth having — and it is the one line that outlives the person, so it may not
// name the address, a link, the token or a hash of it.
func TestASuccessfulRedemptionLogsTheAccountAndWhatItClaimed(t *testing.T) {
	ctx := context.Background()
	var logged bytes.Buffer
	redeemer, signer, store := mustRedeemerWithLogger(t, slog.New(slog.NewTextHandler(&logged, nil)))
	store.redemption = identity.Redemption{CustomerID: redeemedCustomer, Claimed: 3}
	link := signAccess(t, signer, accessEmail, accessNonce, time.Now().UTC())

	sessionToken, _, claimed, err := redeemer.Redeem(ctx, link)
	if err != nil {
		t.Fatalf("Redeem() = unexpected error %v", err)
	}
	if claimed != 3 {
		t.Fatalf("Redeem() answered claimed=%d, want the store's 3", claimed)
	}

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("a successful redemption wrote %d log line(s), want exactly one: %q", len(lines), logged.String())
	}
	line := lines[0]
	for _, want := range []string{"identity: address verified", "customer=" + redeemedCustomer, "claimed=3"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %q does not carry %q", line, want)
		}
	}

	// What a log is judged by: the address is a person, the link and the token are
	// credentials, and the hash is the credential a database dump holds.
	if len(store.calls) != 1 {
		t.Fatalf("the store was asked to write %d times, want exactly one", len(store.calls))
	}
	leaks := []string{accessEmail, link, sessionToken, hex.EncodeToString(store.calls[0].tokenHash)}
	for _, leaked := range leaks {
		if leaked != "" && strings.Contains(line, leaked) {
			t.Fatalf("the log line carries %q: %q", leaked, line)
		}
	}
}

// The other half of the same rule: a new account with no history is a fact worth having, so
// zero claimed is still one line — a branch that logs only when something was claimed is a
// branch nobody ever reads.
func TestARedemptionThatClaimedNothingStillLogsOnce(t *testing.T) {
	ctx := context.Background()
	var logged bytes.Buffer
	redeemer, signer, store := mustRedeemerWithLogger(t, slog.New(slog.NewTextHandler(&logged, nil)))
	store.redemption = identity.Redemption{CustomerID: redeemedCustomer}
	link := signAccess(t, signer, accessEmail, accessNonce, time.Now().UTC())

	if _, _, claimed, err := redeemer.Redeem(ctx, link); err != nil || claimed != 0 {
		t.Fatalf("Redeem() = %d, %v; want 0, nil", claimed, err)
	}

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("a redemption that claimed nothing wrote %d log line(s), want exactly one: %q",
			len(lines), logged.String())
	}
	if !strings.Contains(lines[0], "claimed=0") {
		t.Fatalf("log line %q does not carry claimed=0", lines[0])
	}
}

// A refused redemption writes nothing here: the refusal has already been accounted for by the
// surface that answered it, and a line about a link that was spent would be a line about a
// credential somebody presented.
func TestARefusedRedemptionWritesNoLogLine(t *testing.T) {
	ctx := context.Background()
	var logged bytes.Buffer
	redeemer, signer, store := mustRedeemerWithLogger(t, slog.New(slog.NewTextHandler(&logged, nil)))
	store.err = identity.ErrLinkSpent
	link := signAccess(t, signer, accessEmail, accessNonce, time.Now().UTC())

	if _, _, _, err := redeemer.Redeem(ctx, link); !errors.Is(err, identity.ErrLinkSpent) {
		t.Fatalf("Redeem() = %v, want ErrLinkSpent", err)
	}
	if logged.Len() != 0 {
		t.Fatalf("a refused redemption logged %q, want nothing", logged.String())
	}
}

// A logger is not a requirement, and a redeemer built without one must not take a login down:
// nil is the process's own logger, the way the email worker treats its own.
func TestARedeemerWithoutALoggerStillRedeems(t *testing.T) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	store := &fakeRedemptionStore{redemption: identity.Redemption{CustomerID: redeemedCustomer, Claimed: 1}}
	redeemer, err := identity.NewRedeemer(signer, store, sessionTTL, nil)
	if err != nil {
		t.Fatalf("NewRedeemer() with no logger = unexpected error %v", err)
	}
	link := signAccess(t, signer, accessEmail, accessNonce, time.Now().UTC())

	if _, _, claimed, err := redeemer.Redeem(context.Background(), link); err != nil || claimed != 1 {
		t.Fatalf("Redeem() = %d, %v; want the store's 1 and no error", claimed, err)
	}
}
