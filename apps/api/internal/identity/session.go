// The session: the opaque credential a signed-in browser holds, the hash of it that the
// database keeps, and the service that opens, reads and closes one.
//
// This file is the domain only. It has no HTTP route, no cookie and no handler — the
// transport that carries the token belongs to the tasks that follow — and it knows nothing
// about PostgreSQL: [SessionStore] is the whole of what it needs from storage, so every rule
// here is provable with a fake and no database.
//
// # The token is the secret; the row is its hash
//
// A session is a random string the client holds that nothing else can derive (S1). The
// database stores the SHA-256 of it and never the token itself, so the commonest way a
// project like this leaks — a dump of the table, a backup, a read replica — yields no
// credential anybody can present. The lookup works because a hash is deterministic: the
// client hands over the token, and the server looks up its digest. What a dump does reveal is
// that a session existed and to which account; what it does not reveal is how to be it.
//
// Two consequences follow and both are implemented here rather than left to callers:
//
//   - The token is returned exactly once, by [SessionService.Open], and is never part of the
//     [Session] it returns, never an argument to the store, and never logged anywhere in this
//     file. A function that had it in a message could put it in a log; there is no such
//     function.
//   - A token that cannot be one is refused before any query. A session token is 32 random
//     bytes spelled in canonical URL-safe base64 without padding, which is 43 characters of
//     one alphabet with one spelling, so a string of any other shape cannot name a session.
//     Refusing it early is not an optimisation: the check is what keeps an attacker from
//     turning the store into an oracle by feeding it spellings of a guess.
//
// # Every way a session ends is one answer
//
// An expired session, a revoked one and a token that never existed all answer [ErrNoSession],
// and the service collapses every reason the store might offer into that one sentinel
// instead of forwarding it. The reason is not cosmetic. The three states are told apart by
// information only the server has, and a caller who can distinguish them learns something it
// is not owed: whether a token was ever real, and — for a token somebody is watching — when
// its owner signed out. A signed-out browser and a forged cookie must look identical from
// outside, so they are identical here. Expiry and revocation remain distinguishable where
// nobody can probe for them: in the row, in the store's own return values, and in the tests.
//
// The same rule decides what [SessionService.Close] answers, and it is the one human decision
// this file had to take: closing never reports that a token was unknown. Closing an unknown
// token, an already closed one or a string that cannot be a token all answer nil, and only a
// store that could not be reached is an error. The reason is that sign-out must not be a
// question: if Close answered ErrNoSession for an unknown token, any caller could use it to
// ask "did this credential ever exist?" — a question the login surface is built to refuse —
// and a client whose row was already revoked would be told its sign-out failed, which is the
// one message that would make it believe it is still signed in. Closing is therefore
// idempotent, and idempotent here means total.
//
// # A session is not renewed silently
//
// [SessionService.Authenticate] refreshes `last_seen_at`, because a session that is being used
// should say so for the audit that will one day be asked for, and it does not touch
// `expires_at`, because a session that extends itself is a key that never ends. A person who
// uses the site for a month signs in again at the end of it; that is the documented behaviour
// (S1, and the study's own list of limits), not an oversight.
//
// The refresh is also bookkeeping rather than a gate: if stamping the row fails, the session
// still authenticates. The alternative — refusing a valid session because an UPDATE timed out
// — would sign somebody out over a column that changes nothing about the credential, and the
// only thing lost is one sighting in an audit that records the others.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// sessionTokenBytes is how much entropy a session token carries. It is 32 bytes because that
// is the size a guess cannot reach: 2^256 is not a number a machine enumerates, and it is the
// same number the signing keys in this package use, so nobody has to remember two.
const sessionTokenBytes = 32

// sessionTokenLength is the only token length this package accepts: 32 bytes in URL-safe
// base64 without padding is 43 characters. The number is derived rather than guessed, so a
// future change to the byte count cannot leave the shape check behind.
const sessionTokenLength = (sessionTokenBytes*8 + 5) / 6

// The failures this package reports.
var (
	// ErrNoSession reports that a presented token opens nothing: it never existed, it has
	// expired, or it was revoked. It is deliberately one error for three states — see the
	// file comment — so a caller cannot tell "your session ended" from "that token was never
	// ours". It never names the token or the account.
	ErrNoSession = errors.New("identity: there is no session for this token")

	// ErrSessionUnavailable reports that the store could not answer, which is not the same
	// answer as "there is no session": a database that is down must never look to every
	// signed-in person like a silent sign-out. The transport above this maps it to a server
	// failure, never to a refusal of the credential.
	ErrSessionUnavailable = errors.New("identity: the session store did not answer")
)

// Session is one stored session: the row, minus the credential. It never carries the token or
// its hash — the hash is the store's business, and the token exists in the client's cookie and
// in the return value of [SessionService.Open] and nowhere else.
type Session struct {
	// ID is the row's own identifier, for logs, joins and a future "sign out everywhere".
	ID string
	// CustomerID is the account the session belongs to.
	CustomerID string
	// CreatedAt is when the session was opened.
	CreatedAt time.Time
	// LastSeenAt is the last instant the token was presented, or nil when it has not been
	// presented since the act that created it. Nil rather than a sentinel timestamp, so
	// "never" is not spelled as an instant that could be real.
	LastSeenAt *time.Time
	// ExpiresAt is when the session stops opening anything. It is set once, by Open, and
	// nothing in this package moves it afterwards.
	ExpiresAt time.Time
}

// SessionStore is everything the service needs from storage: write a row, read a live one,
// record a sighting, close one. It is an interface here rather than a concrete store so the
// rules above are provable without PostgreSQL, and it is deliberately narrow so the store
// cannot become a second place where the rules are written.
type SessionStore interface {
	// Create writes one session row keyed by tokenHash.
	Create(ctx context.Context, s Session, tokenHash []byte) error
	// Find answers the session a hash belongs to, but only while it is live: revoked and
	// expired rows must answer ErrNoSession, not a session the caller has to filter.
	Find(ctx context.Context, tokenHash []byte, now time.Time) (Session, error)
	// Touch records that the session was presented at now. It changes nothing else.
	Touch(ctx context.Context, tokenHash []byte, now time.Time) error
	// Revoke closes the session at now without deleting the row. A hash that matches no open
	// row answers ErrNoSession, without saying why.
	Revoke(ctx context.Context, tokenHash []byte, now time.Time) error
}

// SessionService opens, authenticates and closes sessions for one lifetime. It holds the
// store and the lifetime; it holds no clock, so every instant it uses is `time.Now` at the
// moment of the call and nothing is renewed behind the caller's back.
type SessionService struct {
	store SessionStore
	ttl   time.Duration
}

// NewSessionService builds a service over a store and a lifetime.
//
// Both arguments are refused when they cannot work: a nil store has nowhere to write a
// session, and a non-positive lifetime would mint sessions that are already over. The
// lifetime is not a sentinel error because config refuses it before a process starts (see
// API_SESSION_TTL); this guard is for a programmatically built value.
func NewSessionService(store SessionStore, ttl time.Duration) (*SessionService, error) {
	if store == nil {
		return nil, errors.New("identity: a session service needs a store, and it was given none")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("identity: the session lifetime must be positive, got %s", ttl)
	}
	return &SessionService{store: store, ttl: ttl}, nil
}

// Open mints a session for one account and answers the token, once.
//
// The token is the only copy that will ever exist: it is hashed for the row, returned to the
// caller, and never stored, logged or echoed here. The expiry is this service's lifetime from
// now, computed here rather than passed in, so one deployment's sessions all live the same
// length of time. The creation instant is also set here and written explicitly, so the
// session handed back describes the row that was written instead of a zero instant the
// database later filled in.
//
// An account is required: the row's foreign key is NOT NULL, so an empty identifier is a
// programming mistake and is refused before a token is minted or a row is written.
func (s *SessionService) Open(ctx context.Context, customerID string) (string, Session, error) {
	if s == nil || s.store == nil {
		return "", Session{}, errors.New("identity: a session service needs a store, and it was given none")
	}
	if strings.TrimSpace(customerID) == "" {
		// The identifier is not echoed: it is an account, and a caller that passed garbage
		// does not need it copied into a log to find out.
		return "", Session{}, errors.New("identity: a session must belong to a customer, and it was given no identifier")
	}

	token, err := NewSessionToken()
	if err != nil {
		return "", Session{}, err
	}
	now := time.Now().UTC()
	session := Session{
		// The identifier is the project's one UUID minter, the same one the reservation and
		// checkout services use, so a session id is shaped like every other id here.
		ID:         venue.NewID(),
		CustomerID: customerID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.ttl),
	}
	if err := s.store.Create(ctx, session, HashSessionToken(token)); err != nil {
		// The failure is wrapped and the token is not in it. A caller learns that no session
		// was opened, which is exactly what happened.
		return "", Session{}, fmt.Errorf("identity: open a session: %w", err)
	}
	return token, session, nil
}

// Authenticate answers the session a presented token opens, or [ErrNoSession].
//
// The token is hashed and the hash is looked up, so the value the database holds is the only
// value that has to match. A string that cannot be a token is refused before the store is
// asked anything, and with the same opaque error: the shape of a credential is not a hint a
// caller can probe for.
//
// Every way a session ends answers [ErrNoSession] — see the file comment — and a store that
// could not answer answers [ErrSessionUnavailable] instead, so an outage is never disguised
// as a sign-out.
//
// On success the sighting is recorded, and a refresh that fails does not refuse the session:
// it is bookkeeping, not a gate. The returned session reports the sighting only when it was
// actually written, because an answer that claimed one that failed would be a small lie in
// the one place an audit is read.
func (s *SessionService) Authenticate(ctx context.Context, token string) (Session, error) {
	if s == nil || s.store == nil {
		return Session{}, errors.New("identity: a session service needs a store, and it was given none")
	}
	tokenHash, ok := sessionTokenHash(token)
	if !ok {
		return Session{}, ErrNoSession
	}

	now := time.Now().UTC()
	session, err := s.store.Find(ctx, tokenHash, now)
	if errors.Is(err, ErrNoSession) {
		// The store may know which of the three states it was; the caller may not.
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrSessionUnavailable, err)
	}

	if err := s.store.Touch(ctx, tokenHash, now); err != nil {
		return session, nil
	}
	session.LastSeenAt = &now
	return session, nil
}

// Close ends a session. It is idempotent, and quiet about what it found.
//
// Closing a live token revokes its row; closing one that is already revoked, was never issued
// or is not even shaped like a token answers nil, because a client must not be able to learn
// from a sign-out whether its credential existed, and because a sign-out that reported failure
// would leave the client believing it is still signed in. Only a store that could not be
// reached is an error, since then the row really is still open.
func (s *SessionService) Close(ctx context.Context, token string) error {
	if s == nil || s.store == nil {
		return errors.New("identity: a session service needs a store, and it was given none")
	}
	tokenHash, ok := sessionTokenHash(token)
	if !ok {
		// There is no session this string could close, so there is nothing to do and nothing
		// to tell the client.
		return nil
	}

	err := s.store.Revoke(ctx, tokenHash, time.Now().UTC())
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNoSession):
		// Nothing matched: the row is gone, already closed, or was never here. All three are
		// "you are signed out", which is what the caller asked for.
		return nil
	default:
		return fmt.Errorf("%w: %w", ErrSessionUnavailable, err)
	}
}

// NewSessionToken mints a session token: 32 bytes from crypto/rand in URL-safe base64 without
// padding.
//
// crypto/rand and not math/rand, for the reason any credential uses it: a predictable token is
// not a secret at all. Base64url and not a UUID, because a UUID is 122 bits dressed as a
// credential — it is a name, and this value is a key. An error is returned rather than
// swallowed, because a deployment whose kernel entropy is unavailable must fail instead of
// issuing a token nobody can trust.
func NewSessionToken() (string, error) {
	var raw [sessionTokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("identity: no entropy for a session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// HashSessionToken answers the SHA-256 of a token, which is the value the database stores and
// looks sessions up by.
//
// A plain hash rather than a password hash (bcrypt, argon2) on purpose: those exist to make
// guessing a low-entropy human secret expensive, and a token is 256 bits of machine
// randomness. What the hash has to do here is make a stolen table useless, and SHA-256 does
// that against an input no dictionary reaches. It also has to be fast, because it runs on
// every request a signed-in browser makes.
func HashSessionToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// sessionTokenHash answers the hash of a token that could be one, and refuses a string that
// could not.
//
// One token has one spelling, and the check enforces both halves of that: the length is the
// one 32 bytes encode to, and the decoding is strict, so a final character carrying bits the
// bytes do not have is refused rather than read as the same credential with a second hash.
func sessionTokenHash(token string) ([]byte, bool) {
	if len(token) != sessionTokenLength {
		return nil, false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != sessionTokenBytes {
		return nil, false
	}
	return HashSessionToken(token), true
}
