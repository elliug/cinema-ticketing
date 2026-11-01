// The signed ticket link: the one URL a purchase email carries, and the reason it is not
// a permanent key in a mailbox.
//
// # Why a link that closes
//
// A ticket email is not read once. It is forwarded to the person who will actually go,
// printed, archived by a provider, backed up, and read again months later from a mailbox
// nobody curates. The ticket's own URL is a permanent capability — see the tickets
// package documentation — so putting one in a message would put a key to the order in
// every mailbox the message ever reached, for as long as it exists, with no way to take
// it back. The link therefore carries its own expiry: it is signed with HMAC and an
// explicit `exp`, the server refuses it from that instant on, and a buyer who comes back
// a month later opens their tickets from the page instead of replaying an old email. An
// email is a notification that a sale happened; it is not a credential that never dies.
//
// # The format, and why it is the QR format
//
// The machinery is deliberately the same as [Signer]'s: two base64url halves separated by
// one dot, the signature computed over the payload *bytes as transmitted*, and a
// verifier that refuses any payload which does not re-encode to itself. The payload is
// the canonical, versioned, pipe-joined statement of exactly two facts:
//
//	v1|<reservationID>|<exp>
//
// `exp` is RFC 3339 in UTC, which is the instant format the rest of this project parses
// and prints. Because the canonical spelling is the one the signer produced, a payload
// whose `exp` carries an offset or a fractional second is refused rather than read as the
// same instant: two spellings of one expiry would be two different links for one window.
//
// # A separate key
//
// The link is signed with a key of its own ([LinkSigner] never reads the QR key). A QR
// code is printed on a physical document and a link lives in a mailbox; rotating one must
// not invalidate the other, and a leak of the mailbox key must not let anybody mint a
// door code. Sharing one key would tie two unrelated lifetimes together.
//
// # What this scheme does not do
//
// The link proves that a reservation and an expiry were signed by this deployment, and
// the expiry is only as good as the two clocks: a verifier whose clock is far enough
// behind will still accept an aged-out link. It says nothing about who holds the email,
// because the address is never verified (S2) and a forwarded message is valid by design.
// What it does guarantee is the thing that matters for a link sitting in a mailbox: it
// stops working on its own date, without anybody having to revoke it.

package tickets

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// linkVersion is the format version of the signed link payload. It is inside the
// signature for the reason the QR version is: it is a statement the signature protects,
// not a label a reader trusts, so a later format can never be read under these rules.
const linkVersion = "v1"

// ErrLinkExpired reports that a link verified but its window has closed. It exists for
// logs and tests only: the route answers it exactly like a forged token, so a caller can
// never tell "this email is old" from "this link was never ours". It is distinct from
// [ErrInvalidToken] so the two are distinguishable where nobody can probe for them.
var ErrLinkExpired = errors.New("tickets: the ticket link has expired")

// LinkClaims is what a verified link says: the order it opens and the instant it stops
// opening. It carries no address and no buyer, so it is safe to log and useless to a
// reader of the URL.
type LinkClaims struct {
	// ReservationID is the order the link names (`cinema.reservations.id`).
	ReservationID string
	// ExpiresAt is the instant the link stops opening anything, as the signature spelled
	// it: whole seconds, UTC.
	ExpiresAt time.Time
}

// LinkSigner mints and verifies the links a purchase email carries. It holds the HMAC
// key and the lifetime; the key is copied in and never logged or echoed.
type LinkSigner struct {
	key []byte
	ttl time.Duration
}

// NewLinkSigner builds a signer from the deployment's own link key and the lifetime its
// links live.
//
// The key rules mirror [NewSigner]'s, for the same reason: a blank key, a key shorter than
// [MinKeyLength] bytes, and a lifetime that would mint a link which is already closed are
// all refused at startup, and the key never reaches an error message. The lifetime is not
// a sentinel error because config refuses it before a process starts (see
// API_TICKET_LINK_TTL); this guard is for a programmatically built value, like the one
// [NewSigner] applies to a key nobody validated.
func NewLinkSigner(key string, ttl time.Duration) (*LinkSigner, error) {
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%w: the key is blank", ErrInvalidKey)
	}
	// The length is reported in bytes and the value never is: a wrong key is a secret too.
	if len(key) < MinKeyLength {
		return nil, fmt.Errorf("%w: the key must be at least %d bytes, got %d",
			ErrInvalidKey, MinKeyLength, len(key))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("tickets: the ticket link lifetime must be positive, got %s", ttl)
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	return &LinkSigner{key: copied, ttl: ttl}, nil
}

// Sign renders the link of one reservation, valid for this signer's lifetime from now.
//
// The expiry is computed here rather than passed in, so a sender cannot mint a link with a
// window of its own: one deployment has one lifetime, and every email it sends agrees
// about it.
func (s *LinkSigner) Sign(reservationID string, now time.Time) (string, error) {
	if s == nil || len(s.key) < MinKeyLength {
		return "", fmt.Errorf("%w: the signer was built without a key", ErrInvalidKey)
	}
	if s.ttl <= 0 {
		return "", fmt.Errorf("tickets: the ticket link lifetime must be positive, got %s", s.ttl)
	}
	if strings.TrimSpace(reservationID) == "" {
		return "", fmt.Errorf("%w: the link names no reservation", ErrUnsignable)
	}
	// The identifier is one of three pipe-joined fields of one URL segment, so a separator
	// or a whitespace byte inside it would let a caller sign a payload a verifier reads as
	// two different statements.
	if strings.IndexFunc(reservationID, func(r rune) bool {
		return r == '|' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return "", fmt.Errorf("%w: the reservation identifier is not one the payload can carry", ErrUnsignable)
	}
	return tokenFor(linkPayload(reservationID, now.Add(s.ttl)), s.key), nil
}

// Verify checks a presented link and answers what it names.
//
// The order of the checks is the contract. The signature is compared with hmac.Equal over
// the payload bytes as received, so nothing is parsed before it is authenticated; then the
// payload must re-encode to itself, so a signature over a spelling this deployment never
// produced is not a link of ours; only then is the expiry read, and only after all of that
// can a link be reported as expired. Every failure other than expiry is [ErrInvalidToken],
// which never says which part failed.
func (s *LinkSigner) Verify(token string, now time.Time) (LinkClaims, error) {
	if s == nil || len(s.key) < MinKeyLength {
		return LinkClaims{}, fmt.Errorf("%w: the signer was built without a key", ErrInvalidKey)
	}

	payloadPart, signaturePart, ok := strings.Cut(token, ".")
	if !ok {
		return LinkClaims{}, invalidToken("the link is not a payload and a signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return LinkClaims{}, invalidToken("the link payload is not URL-safe base64 without padding")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return LinkClaims{}, invalidToken("the link signature is not URL-safe base64 without padding")
	}

	// hmac.Equal is constant time and compares the whole signature, so no prefix of a valid
	// signature passes and no early exit leaks how much of it matched.
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return LinkClaims{}, invalidToken("the link signature does not match its payload")
	}

	claims, err := decodeLinkClaims(payload)
	if err != nil {
		return LinkClaims{}, err
	}
	// The expiry is the last instant the link is unusable from, so an `exp` equal to now is
	// already closed. Both sides are UTC: a comparison in two zones would depend on the
	// machine the process runs on.
	if !claims.ExpiresAt.After(now.UTC()) {
		return LinkClaims{}, fmt.Errorf("%w: the link closed at %s",
			ErrLinkExpired, claims.ExpiresAt.Format(time.RFC3339))
	}
	return claims, nil
}

// linkPayload renders the one canonical payload of a link: the version, the reservation and
// the expiry in RFC 3339 UTC, whole seconds. Both the signer and the canonical re-encode
// check use it, so the two cannot disagree about the spelling.
func linkPayload(reservationID string, expiresAt time.Time) []byte {
	return []byte(linkVersion + "|" + reservationID + "|" +
		expiresAt.UTC().Truncate(time.Second).Format(time.RFC3339))
}

// decodeLinkClaims parses a payload whose signature already checked out, and refuses it
// unless it is byte-identical to its own canonical form.
func decodeLinkClaims(payload []byte) (LinkClaims, error) {
	fields := strings.Split(string(payload), "|")
	if len(fields) != 3 {
		return LinkClaims{}, invalidToken("the link payload does not carry three fields")
	}
	if fields[0] != linkVersion {
		return LinkClaims{}, invalidToken("the link payload was signed in a version this build does not understand")
	}
	if fields[1] == "" {
		return LinkClaims{}, invalidToken("the link payload names no reservation")
	}
	expiresAt, err := time.Parse(time.RFC3339, fields[2])
	if err != nil {
		return LinkClaims{}, invalidToken("the link expiry is not an RFC 3339 instant")
	}

	claims := LinkClaims{ReservationID: fields[1], ExpiresAt: expiresAt.UTC()}
	if !bytes.Equal(linkPayload(claims.ReservationID, claims.ExpiresAt), payload) {
		return LinkClaims{}, invalidToken("the link payload is not in canonical form")
	}
	return claims, nil
}
