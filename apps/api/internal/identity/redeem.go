// The redeeming half of the login link: turn one presented link into one session.
//
// This file is the domain only, and it is short for a reason. Verifying the link is
// [AccessSigner]'s job (the expiry included), minting the credential is [NewSessionToken]'s,
// and the whole write — spend the link, verify the address, claim the orders that were placed
// with it, write the session — happens inside one transaction in [RedemptionStore]. What is
// left here is the order of those three and the rule that no token survives a failure.
//
// # Why the session row is written by the store and not by [SessionService]
//
// [SessionService.Open] is the ordinary way a session is created, and it cannot be used here.
// A session belongs to an account, and at this instant the account may not exist yet: it is
// created by the same transaction that verifies the address, and the session has to be written
// in that transaction or not at all — a session that committed before the account, or after it,
// would be a window in which somebody is signed in as nobody, or in which a verified address
// has bought a session that was never written. So this file builds the [Session] with the
// identifier, the instant and the lifetime it knows, hands it to the store with the hash of the
// token, and the store resolves the account inside the transaction and writes the row there.
//
// One consequence is stated rather than hidden: the [Session] this file returns carries no
// customer identifier, because the account is decided inside the transaction. The caller learns
// which account the session belongs to when it authenticates the token, which is the next
// request anyway. Nothing here guesses it.
//
// # What the token is, and is not
//
// The session token is returned exactly once, to the caller that will put it in the cookie. It
// is never stored — the row holds the hash — and it is never logged, never wrapped into an
// error, and not part of the [Session] that is returned beside it. A failure on the way answers
// no token at all: the token minted for a transaction that rolled back dies with this call
// instead of being handed to somebody whose link was spent.
//
// # Every way a link fails to redeem
//
// The signature's own refusals are forwarded, because the signer distinguishes them where nobody
// can probe for them ([ErrInvalidToken], [ErrAccessLinkExpired]). Everything the store can find
// wrong with the row — spent, never issued, expired — is one opaque [ErrLinkSpent], so the login
// surface can answer all three with the same sentence: "this link cannot be used". Telling them
// apart would confirm which links this deployment once issued, which is the one fact a one-use
// token exists to withhold.
package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// The failures a redemption reports beyond the signer's own.
var (
	// ErrLinkSpent reports that a link cannot be spent, whatever the reason: it was already
	// used, it was never issued, or the row that records it has expired. It is deliberately one
	// error for three states, so a caller cannot tell "somebody used this" from "this was never
	// ours" — the second is the question a forged token is trying to ask.
	ErrLinkSpent = errors.New("identity: this access link cannot be redeemed")

	// ErrLinkUnavailable reports that the store could not answer, which is not the same answer
	// as "this link is spent": a database that is down must never look to a person like a link
	// that was already used. The surface above this maps it to a server failure.
	ErrLinkUnavailable = errors.New("identity: the identity store did not answer")
)

// RedemptionStore is everything the redeemer needs from storage: one transaction that either
// spends the link and writes everything it proves, or writes nothing at all. It is a single
// method on purpose — a store assembled from separate calls could be committed in halves, which
// is exactly the state this design exists to prevent.
type RedemptionStore interface {
	// Redeem spends the link the claim names, verifies the address it carries, claims the
	// orders placed with that address, and writes the session — all in one transaction, in that
	// order. It answers [ErrLinkSpent] when the link cannot be spent and [ErrLinkUnavailable]
	// when the store could not answer; a failure it reports must leave nothing written.
	Redeem(ctx context.Context, claim AccessClaims, session Session, tokenHash []byte) error
}

// Redeemer turns one presented link into one session. It holds the signer that owns the key and
// the link lifetime, the store, and the session lifetime; it holds no clock, so the session
// begins at the instant of the call.
type Redeemer struct {
	signer *AccessSigner
	store  RedemptionStore
	ttl    time.Duration
}

// NewRedeemer builds the redeeming half and refuses a wiring that could not open a session.
//
// The lifetime is the session's, not the link's: a session that inherited the fifteen minutes a
// login step lives would sign everybody out before they finished reading the page. The lifetime
// is not a sentinel error because configuration refuses it before a process starts (see
// API_SESSION_TTL); this guard is for a programmatically built value.
func NewRedeemer(signer *AccessSigner, store RedemptionStore, ttl time.Duration) (*Redeemer, error) {
	if signer == nil {
		return nil, errors.New("identity: a redeemer needs a signer, and it was given none")
	}
	if store == nil {
		return nil, errors.New("identity: a redeemer needs a store, and it was given none")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("identity: the session lifetime must be positive, got %s", ttl)
	}
	return &Redeemer{signer: signer, store: store, ttl: ttl}, nil
}

// Redeem verifies one link and answers the session token it opens, once.
//
// The order is the contract. The token is verified before anything is minted, so a forged or
// expired link cannot even cause a session token to exist, and the expiry is the signer's
// business — a link whose window has closed is refused by the signature's own rules, before a
// row is asked about. Only then is a token minted, which means every reply of this function is
// either a token that was written or no token at all.
//
// The returned session describes what this layer knew when it asked for the write: the row's
// identifier, the instant it was created and when it ends. The account it belongs to is decided
// inside the store's transaction and is empty here — see the file comment.
func (r *Redeemer) Redeem(ctx context.Context, token string) (string, Session, error) {
	if r == nil || r.signer == nil || r.store == nil {
		return "", Session{}, errors.New("identity: a redeemer needs a signer and a store, and it was built without them")
	}

	now := time.Now().UTC()
	claims, err := r.signer.Verify(token, now)
	if err != nil {
		// Forwarded as it is: the signer already refuses without naming the token, the address
		// or the nonce, and its two answers are the ones the surface can act on.
		return "", Session{}, err
	}

	sessionToken, err := NewSessionToken()
	if err != nil {
		return "", Session{}, err
	}
	session := Session{
		// The project's one UUID minter, so a session id is shaped like every other id here.
		ID:        venue.NewID(),
		CreatedAt: now,
		ExpiresAt: now.Add(r.ttl),
	}
	if err := r.store.Redeem(ctx, claims, session, HashSessionToken(sessionToken)); err != nil {
		// The token dies here. The error is wrapped so errors.Is finds ErrLinkSpent or
		// ErrLinkUnavailable, and it never carries the token or the address.
		return "", Session{}, fmt.Errorf("identity: redeem the access link: %w", err)
	}
	return sessionToken, session, nil
}
