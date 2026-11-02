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
	"log/slog"
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

// Redemption is what a committed redemption wrote, as far as anybody outside the transaction
// may know: which account the link verified, and how many older orders that address claimed
// (D5). It carries no address on purpose — the identifier is what the log and the caller can
// join on, and the mailbox has already served its purpose by the time this value exists.
//
// Claimed is the rows the claim statement touched, which is why the store reads it from
// `RowsAffected` instead of counting: a count computed here could disagree with what the
// transaction wrote, and the log would then describe something that did not happen.
type Redemption struct {
	// CustomerID is the account the transaction resolved: a new row on a first sign-in, the
	// existing one afterwards.
	CustomerID string
	// Claimed is how many orders the verified address took over. Zero is an ordinary answer —
	// an account with no history — and not a failure.
	Claimed int
}

// RedemptionStore is everything the redeemer needs from storage: one transaction that either
// spends the link and writes everything it proves, or writes nothing at all. It is a single
// method on purpose — a store assembled from separate calls could be committed in halves, which
// is exactly the state this design exists to prevent.
type RedemptionStore interface {
	// Redeem spends the link the claim names, verifies the address it carries, claims the
	// orders placed with that address, and writes the session — all in one transaction, in that
	// order. It answers [ErrLinkSpent] when the link cannot be spent and [ErrLinkUnavailable]
	// when the store could not answer; a failure it reports must leave nothing written, and it
	// then answers no redemption at all.
	Redeem(ctx context.Context, claim AccessClaims, session Session, tokenHash []byte) (Redemption, error)
}

// Redeemer turns one presented link into one session. It holds the signer that owns the key and
// the link lifetime, the store, the session lifetime and the logger the claim is recorded in;
// it holds no clock, so the session begins at the instant of the call.
type Redeemer struct {
	signer *AccessSigner
	store  RedemptionStore
	ttl    time.Duration
	logger *slog.Logger
}

// NewRedeemer builds the redeeming half and refuses a wiring that could not open a session.
//
// The lifetime is the session's, not the link's: a session that inherited the fifteen minutes a
// login step lives would sign everybody out before they finished reading the page. The lifetime
// is not a sentinel error because configuration refuses it before a process starts (see
// API_SESSION_TTL); this guard is for a programmatically built value.
//
// A nil logger is the process's own, the way the email worker treats its own: a redeemer that
// refused to be built without one would turn a missing log into a login nobody can perform.
func NewRedeemer(signer *AccessSigner, store RedemptionStore, ttl time.Duration, logger *slog.Logger) (*Redeemer, error) {
	if signer == nil {
		return nil, errors.New("identity: a redeemer needs a signer, and it was given none")
	}
	if store == nil {
		return nil, errors.New("identity: a redeemer needs a store, and it was given none")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("identity: the session lifetime must be positive, got %s", ttl)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Redeemer{signer: signer, store: store, ttl: ttl, logger: logger}, nil
}

// Redeem verifies one link and answers the session token it opens, once, together with how many
// orders the verified address claimed.
//
// The order is the contract. The token is verified before anything is minted, so a forged or
// expired link cannot even cause a session token to exist, and the expiry is the signer's
// business — a link whose window has closed is refused by the signature's own rules, before a
// row is asked about. Only then is a token minted, which means every reply of this function is
// either a token that was written or no token at all.
//
// The returned session describes what this layer knew when it asked for the write: the row's
// identifier, the instant it was created and when it ends. The account it belongs to is decided
// inside the store's transaction and is empty here — see the file comment. The claimed count is
// the store's answer and not a second count kept beside it, so the line this function writes
// says what the transaction actually wrote.
//
// A successful redemption writes that one line. D5 says the claim of older orders is recorded
// in the log, and the redemption is the only place that knows the whole of it: which account now
// owns them and how many there were. The line carries those two facts and nothing else — the
// address is a person, the link and the token are credentials, and their hashes are what a
// database dump already holds. Zero claimed is still one line: an account with no purchase
// behind it is a fact an operator may need, and a branch that logs only when something was
// claimed is a branch nothing exercises. A refusal writes nothing here at all: it has already
// been accounted for by whoever answered the person, and a line about a link somebody presented
// is a line about a credential.
func (r *Redeemer) Redeem(ctx context.Context, token string) (string, Session, int, error) {
	if r == nil || r.signer == nil || r.store == nil || r.logger == nil {
		return "", Session{}, 0, errors.New("identity: a redeemer needs a signer, a store and a logger, and it was built without one of them")
	}

	now := time.Now().UTC()
	claims, err := r.signer.Verify(token, now)
	if err != nil {
		// Forwarded as it is: the signer already refuses without naming the token, the address
		// or the nonce, and its two answers are the ones the surface can act on.
		return "", Session{}, 0, err
	}

	sessionToken, err := NewSessionToken()
	if err != nil {
		return "", Session{}, 0, err
	}
	session := Session{
		// The project's one UUID minter, so a session id is shaped like every other id here.
		ID:        venue.NewID(),
		CreatedAt: now,
		ExpiresAt: now.Add(r.ttl),
	}
	redemption, err := r.store.Redeem(ctx, claims, session, HashSessionToken(sessionToken))
	if err != nil {
		// The token dies here. The error is wrapped so errors.Is finds ErrLinkSpent or
		// ErrLinkUnavailable, and it never carries the token or the address.
		return "", Session{}, 0, fmt.Errorf("identity: redeem the access link: %w", err)
	}

	r.logger.Info("identity: address verified",
		"customer", redemption.CustomerID, "claimed", redemption.Claimed)
	return sessionToken, session, redemption.Claimed, nil
}
