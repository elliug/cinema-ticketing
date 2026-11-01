// Package identity owns the identity and access concerns of a person who buys a ticket:
// the signed links a login email carries, and later the sessions those links open.
//
// This file is the access link only. It mints and verifies one signed URL for one address;
// the nonce it stamps becomes a database row and the session that row opens belongs to the
// tasks that follow, so nothing here touches a database, a route or a cookie.
//
// # Why a mailbox needs a link that closes in minutes
//
// A ticket email is read once and kept; a login email is read within the minute it was
// requested. An access link proves control of a mailbox (S3), which is exactly as valuable
// as the mailbox itself: whoever holds the URL can become that account, and a copy left in a
// forwarded thread, a provider's archive or a shared inbox stays valid for as long as the
// message exists. The window is therefore minutes and not days (S2) — a stolen copy of a
// step is worth far less than a stolen copy of a key — and the link carries its own expiry,
// so the server refuses it from that instant on rather than when somebody remembers to
// revoke it. The caller also stamps a nonce, so one login step is one row and one use
// instead of a token that keeps working until it ages out.
//
// # The format, and why it is the ticket link's format
//
// The machinery is deliberately [tickets.LinkSigner]'s: two base64url halves separated by
// one dot, the signature computed over the payload *bytes as transmitted*, and a verifier
// that refuses any payload which does not re-encode to itself. The payload is the canonical,
// versioned, pipe-joined statement of exactly four facts:
//
//	v1|access|<normalized email>|<nonce>|<exp>
//
// `exp` is RFC 3339 in UTC, which is the instant format the rest of this project parses and
// prints. Because the canonical spelling is the one the signer produced, a payload whose
// `exp` carries an offset or a fractional second is refused rather than read as the same
// instant: two spellings of one expiry would be two windows for one login step. The `access`
// word is a scope inside the signature, so a future step (a password reset, say) can be a
// different statement under the same key instead of an ambiguity a reader has to guess at.
//
// # A separate key
//
// The link is signed with a key of its own ([AccessSigner] never reads the ticket link key or
// the QR key). A ticket link proves a purchase and an access link proves a mailbox; the two
// are different claims about different things, they live for different lengths of time, and
// they sit in the same inbox. Rotating one must not invalidate the other, and a leak of the
// mailbox key must not let anybody mint a link into somebody's order — nor the other way
// round. Sharing one key would tie two unrelated lifetimes and two unrelated blasts together.
//
// # Why the address is normalized here
//
// [NormalizeEmail] is applied when signing and when verifying, so `Ana@X.com` and `ana@x.com`
// are one payload, one link and one account. It is in the signer rather than at the edge
// because the address is the account: two callers that normalize differently — one trimming,
// one not, one preserving case for display — would create two accounts and two verified
// claims for one person. Normalizing in one place makes the rule part of what is signed
// instead of a convention each caller has to remember.
//
// # What this scheme does not do
//
// The link proves that an address, a nonce and an expiry were signed by this deployment, and
// the expiry is only as good as the two clocks: a verifier whose clock is far enough behind
// will still accept an aged-out link. It says nothing about who read the mailbox — that is
// the definition of a magic link and also its limit, since a shared inbox is a shared
// account. Single use is not a property of the signature either: the nonce makes a second
// use *recognisable*, and the row that records it is what makes it *refused*. What the
// signature guarantees is the part a database cannot recover once the URL is out: the link
// names one address, was minted by this deployment, and stops working on its own date.
package identity

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

// accessVersion is the format version of the signed access link payload. It is inside the
// signature for the reason the ticket link's version is: it is a statement the signature
// protects, not a label a reader trusts, so a later format can never be read under these
// rules.
const accessVersion = "v1"

// accessScope names the kind of step this payload is. It is the second field and it is
// inside the signature, so an access link and any later signed step can share a key without
// either being readable as the other.
const accessScope = "access"

// MinKeyLength is the shortest access link key this package accepts, in bytes. It is the
// number the other two signers use, for the same reason: an HMAC-SHA256 key is only as
// strong as its entropy, and 32 bytes matches the length of a SHA-256 digest so nobody has to
// remember two numbers.
const MinKeyLength = 32

// The failures this package reports.
var (
	// ErrInvalidKey reports that a signing key cannot be used: it is blank, blank
	// whitespace, or shorter than [MinKeyLength]. It is a configuration failure, and the
	// error never echoes the key.
	ErrInvalidKey = errors.New("identity: the access link signing key is not usable")

	// ErrInvalidToken reports that a presented link does not verify. Every way of failing is
	// this one error: malformed, truncated, wrong version or scope, wrong key, tampered
	// payload, tampered signature. An identity link failing must never be reported as a
	// ticket failure, so it is this package's own sentinel and not tickets'.
	ErrInvalidToken = errors.New("identity: the access link does not verify")

	// ErrUnsignable reports that a link cannot be minted from what it was given: no address
	// that normalizes to something, or no nonce. Signing one of those would mint a link that
	// names less than a login step — no account, or no single use — so the sender fails
	// instead of mailing it.
	ErrUnsignable = errors.New("identity: the access link cannot be signed")

	// ErrAccessLinkExpired reports that a link verified but its window has closed. It exists
	// for logs and tests only, exactly as the ticket link's expiry does: the route answers it
	// like a forged token, so a caller can never tell "this email is old" from "this link was
	// never ours". It is distinct from [ErrInvalidToken] so the two are distinguishable where
	// nobody can probe for them.
	ErrAccessLinkExpired = errors.New("identity: the access link has expired")
)

// AccessClaims is what a verified link says: the address it opens, the nonce the sender
// recorded when it minted the link, and the instant it stops opening.
type AccessClaims struct {
	// Email is the address the link was minted for, normalized (see [NormalizeEmail]) so it
	// is the same string the account lookup uses.
	Email string
	// Nonce is the single-use identifier the sender supplied and recorded. It is returned so
	// the caller can find the row it wrote and refuse a second use of this exact token.
	Nonce string
	// ExpiresAt is the instant the link stops opening anything, as the signature spelled it:
	// whole seconds, UTC.
	ExpiresAt time.Time
}

// AccessSigner mints and verifies the links a login email carries. It holds the HMAC key and
// the lifetime; the key is copied in and never logged or echoed.
type AccessSigner struct {
	key []byte
	ttl time.Duration
}

// NewAccessSigner builds a signer from the deployment's own identity link key and the
// lifetime its links live.
//
// The key rules mirror the other two signers', for the same reason: a blank key, a key
// shorter than [MinKeyLength] bytes, and a lifetime that would mint a link which is already
// closed are all refused at startup, and the key never reaches an error message. The lifetime
// is not a sentinel error because config refuses it before a process starts (see
// API_IDENTITY_LINK_TTL); this guard is for a programmatically built value.
func NewAccessSigner(key string, ttl time.Duration) (*AccessSigner, error) {
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%w: the key is blank", ErrInvalidKey)
	}
	// The length is reported in bytes and the value never is: a wrong key is a secret too.
	if len(key) < MinKeyLength {
		return nil, fmt.Errorf("%w: the key must be at least %d bytes, got %d",
			ErrInvalidKey, MinKeyLength, len(key))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("identity: the access link lifetime must be positive, got %s", ttl)
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	return &AccessSigner{key: copied, ttl: ttl}, nil
}

// Sign renders the access link of one address, valid for this signer's lifetime from now.
//
// The expiry is computed here rather than passed in, so a sender cannot mint a link with a
// window of its own: one deployment has one lifetime, and every login email it sends agrees
// about it. The address is normalized first, so the payload carries the one spelling the
// verifier will recognize.
//
// The nonce is not generated here. It becomes a database row the caller writes and later
// looks up to refuse a second use, so a signer that invented its own randomness could
// neither be recorded nor be tested at a fixed instant.
//
// Neither the token, the address, the nonce nor the key is ever logged or echoed: the address
// is a person and the nonce is a one-use secret of the login step.
func (s *AccessSigner) Sign(email, nonce string, now time.Time) (string, error) {
	if s == nil || len(s.key) < MinKeyLength {
		return "", fmt.Errorf("%w: the signer was built without a key", ErrInvalidKey)
	}
	if s.ttl <= 0 {
		return "", fmt.Errorf("identity: the access link lifetime must be positive, got %s", s.ttl)
	}

	normalized := NormalizeEmail(email)
	if normalized == "" {
		return "", fmt.Errorf("%w: the link names no address", ErrUnsignable)
	}
	// The address and the nonce are two of five pipe-joined fields of one URL segment, so a
	// separator or a whitespace byte inside either would let a caller sign a payload a
	// verifier reads as a different statement. A nonce is also refused when it is blank: a
	// link with no nonce could not be spent once.
	if !payloadCarries(normalized) {
		return "", fmt.Errorf("%w: the address is not one the payload can carry", ErrUnsignable)
	}
	if strings.TrimSpace(nonce) == "" {
		return "", fmt.Errorf("%w: the link carries no nonce, so it could not be spent once", ErrUnsignable)
	}
	if !payloadCarries(nonce) {
		return "", fmt.Errorf("%w: the nonce is not one the payload can carry", ErrUnsignable)
	}
	return tokenFor(accessPayload(normalized, nonce, now.Add(s.ttl)), s.key), nil
}

// Verify checks a presented link and answers what it names.
//
// The order of the checks is the contract. The signature is compared with hmac.Equal over the
// payload bytes as received, so nothing is parsed before it is authenticated; then the
// payload must re-encode to itself, so a signature over a spelling this deployment never
// produced — a non-canonical expiry, or an address that was not normalized — is not a link of
// ours; only then is the expiry read, and only after all of that can a link be reported as
// expired. Every failure other than expiry is [ErrInvalidToken], which never says which part
// failed and never names the address or the nonce.
func (s *AccessSigner) Verify(token string, now time.Time) (AccessClaims, error) {
	if s == nil || len(s.key) < MinKeyLength {
		return AccessClaims{}, fmt.Errorf("%w: the signer was built without a key", ErrInvalidKey)
	}

	payloadPart, signaturePart, ok := strings.Cut(token, ".")
	if !ok {
		return AccessClaims{}, invalidToken("the link is not a payload and a signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return AccessClaims{}, invalidToken("the link payload is not URL-safe base64 without padding")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return AccessClaims{}, invalidToken("the link signature is not URL-safe base64 without padding")
	}

	// hmac.Equal is constant time and compares the whole signature, so no prefix of a valid
	// signature passes and no early exit leaks how much of it matched.
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return AccessClaims{}, invalidToken("the link signature does not match its payload")
	}

	claims, err := decodeAccessClaims(payload)
	if err != nil {
		return AccessClaims{}, err
	}
	// The expiry is the last instant the link is unusable from, so an `exp` equal to now is
	// already closed. Both sides are UTC: a comparison in two zones would depend on the
	// machine the process runs on.
	if !claims.ExpiresAt.After(now.UTC()) {
		return AccessClaims{}, fmt.Errorf("%w: the link closed at %s",
			ErrAccessLinkExpired, claims.ExpiresAt.Format(time.RFC3339))
	}
	return claims, nil
}

// NormalizeEmail renders the one spelling of an address this project accounts for: trimmed
// and lower-cased.
//
// It lives beside the signer and not at the edge because the address is the account (D5,
// S3). Two callers that normalize differently — one comparing the trimmed form, one storing
// what the person typed — are two accounts and two verified claims for one mailbox, and
// neither of them is wrong on its own. Doing it once, in the code that both signs and reads
// the link, makes the rule part of what the signature protects instead of a convention every
// caller has to remember.
//
// It deliberately does not validate the shape of an address: whether a mailbox exists is the
// provider's answer, and a login link to a nonexistent one is worthless anyway. What it does
// guarantee is that one person's address is compared one way.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// accessPayload renders the one canonical payload of a link: the version, the scope, the
// normalized address, the nonce and the expiry in RFC 3339 UTC, whole seconds. Both the
// signer and the canonical re-encode check use it, so the two cannot disagree about the
// spelling.
func accessPayload(email, nonce string, expiresAt time.Time) []byte {
	return []byte(accessVersion + "|" + accessScope + "|" + email + "|" + nonce + "|" +
		expiresAt.UTC().Truncate(time.Second).Format(time.RFC3339))
}

// decodeAccessClaims parses a payload whose signature already checked out, and refuses it
// unless it is byte-identical to its own canonical form.
func decodeAccessClaims(payload []byte) (AccessClaims, error) {
	fields := strings.Split(string(payload), "|")
	if len(fields) != 5 {
		return AccessClaims{}, invalidToken("the link payload does not carry five fields")
	}
	if fields[0] != accessVersion {
		return AccessClaims{}, invalidToken("the link payload was signed in a version this build does not understand")
	}
	if fields[1] != accessScope {
		return AccessClaims{}, invalidToken("the link payload was signed for another kind of step")
	}
	if fields[2] == "" {
		return AccessClaims{}, invalidToken("the link payload names no address")
	}
	if fields[3] == "" {
		return AccessClaims{}, invalidToken("the link payload carries no nonce")
	}
	expiresAt, err := time.Parse(time.RFC3339, fields[4])
	if err != nil {
		return AccessClaims{}, invalidToken("the link expiry is not an RFC 3339 instant")
	}

	claims := AccessClaims{Email: NormalizeEmail(fields[2]), Nonce: fields[3], ExpiresAt: expiresAt.UTC()}
	// The re-encode is also the normalization check: a payload carrying "Ana@X.com" answers a
	// different canonical form, so it is refused instead of being read as the same person.
	if !bytes.Equal(accessPayload(claims.Email, claims.Nonce, claims.ExpiresAt), payload) {
		return AccessClaims{}, invalidToken("the link payload is not in canonical form")
	}
	return claims, nil
}

// payloadCarries reports whether a value can be one pipe-joined field of the payload. A
// separator, a space or a control byte would let one field be read as two.
func payloadCarries(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return r == '|' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

// tokenFor renders payload and signature in the transport form.
func tokenFor(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// invalidToken builds one of the fixed refusals. The reason is a sentence about the shape of
// the link and never a claim value, so a refusal cannot confirm that an address exists or
// that a nonce was ever issued.
func invalidToken(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidToken, reason)
}
