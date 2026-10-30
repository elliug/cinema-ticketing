// The signed QR token: the code printed on the web page and on the PDF, and the
// only thing a door scanner is asked to believe.
//
// # What the code is
//
// A token is two base64url halves separated by one dot:
//
//	base64url(payload) "." base64url(HMAC-SHA256(key, payload))
//
// The signature is computed over the payload **bytes exactly as transmitted**, so
// verification recomputes it over what it received. It never re-encodes what it
// parsed first: a verifier that re-serialized its own reading of the payload would
// accept spellings the signer never produced, and two encodings of one ticket would
// then verify as one.
//
// # What the payload carries, and what it does not
//
// The payload is a canonical, versioned, pipe-joined string of exactly three facts:
//
//	v1|<ticketID>|<eventID>|<seatID>,<seatID>,...
//
// The seat identifiers are signed ascending, which is the one canonical spelling:
// [Signer.Verify] re-encodes what it parsed and refuses anything that is not
// byte-identical to its canonical form, so "21,22" and "22,21" cannot both be
// tokens for one order.
//
// There is deliberately nothing else in it. No buyer name, no email, no film
// title, no screening time, no price, no row or seat label. A QR code is a
// photograph waiting to happen: the buyer photographs the code, the door
// photographs the code, the usher's phone shows it to the person behind them. So
// the code carries identifiers the server can look up, and every readable fact is
// fetched server side after the code verifies. The identifiers are what makes the
// later cashier workflow (M4) possible: a ticket id, the event it admits to and the
// seat it admits, checked against the stored ticket rather than trusted from the
// client.
//
// # Why the version is inside the signed payload
//
// The version is a field of the payload, not a prefix beside it, because it is a
// statement the signature has to protect. A version beside the MAC ("v2." +
// payload + "." + signature) is a label anybody can rewrite: an old payload
// relabelled as a new version would be parsed under rules nobody signed it for, and
// every verifier that trusted the label would be the attack surface. Inside the
// payload, changing the version invalidates the signature, so a verifier knows the
// format it is reading was chosen by whoever holds the key. That also makes a later
// format a distinguishable thing rather than an ambiguity: this build understands
// "v1" and refuses everything else as [ErrInvalidToken], so a token minted after an
// upgrade can never be read as if it were the old shape.
//
// # What this scheme does not do
//
// The token proves that a ticket id, an event id and a set of seats were signed by
// this deployment. It does not prove that nobody photographed the code, and it does
// not know whether the ticket has already been let in. Both are deliberate: a code
// is copyable by nature, and single use is a state change on the ticket that M4's
// cashier workflow owns (the signed token names the ticket; the ticket's own row
// says whether it was redeemed). What M3 can promise is that a photographed code
// exposes no personal data and that a forged, tampered or unknown code is refused
// server side.

package tickets

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// tokenVersion is the format version of the signed payload. It is inside the
// signature, so it is the signer's statement rather than a label a reader trusts.
const tokenVersion = "v1"

// MinKeyLength is the shortest signing key this package accepts, in bytes. An
// HMAC-SHA256 key is only as strong as its entropy, and a key that ships in a
// repository signs nothing at all: 32 bytes is the shortest length at which a
// deployment can reasonably hold a real random key, and it matches the length of a
// SHA-256 digest so nobody has to remember two numbers.
const MinKeyLength = 32

// The failures this file reports.
var (
	// ErrInvalidKey reports that a signing key cannot be used: it is blank, blank
	// whitespace, or shorter than [MinKeyLength]. It is a configuration failure, and
	// the error never echoes the key.
	ErrInvalidKey = errors.New("tickets: the ticket QR signing key is not usable")

	// ErrInvalidToken reports that a presented token does not verify. Every way of
	// failing is this one error: malformed, truncated, wrong version, wrong key,
	// tampered payload, tampered signature. The door is told one thing ("this code
	// is not a ticket of ours") and the error never names a claim value, so a
	// refusal cannot be used to confirm that a ticket id exists.
	ErrInvalidToken = errors.New("tickets: the ticket QR token does not verify")

	// ErrUnsignable reports that a ticket detail cannot be encoded as a signed
	// identity: no ticket id, no event id, no seat, or seats that cannot form a
	// canonical list. Serving a ticket whose code names less than the ticket would
	// hand the door a document it cannot check.
	ErrUnsignable = errors.New("tickets: the ticket detail cannot be signed")
)

// Claims is what a verified token says: the ticket it names, the screening it
// admits to, and the seats it admits, ascending. It carries no readable fact about
// the buyer or the film, so it is safe to compare against the stored ticket and
// never a substitute for it.
type Claims struct {
	// TicketID is the ticket the code names (`cinema.tickets.id`).
	TicketID string
	// EventID is the screening the ticket admits to (`cinema.events.id`).
	EventID string
	// SeatIDs are the event seats the ticket admits to, ascending.
	SeatIDs []int64
}

// Signer mints and verifies the ticket QR tokens of one deployment. It holds the
// HMAC key and nothing else; the key is copied in and never logged or echoed.
type Signer struct {
	key []byte
}

// NewSigner builds a signer from the deployment's key. The key must be at least
// [MinKeyLength] bytes of entropy-bearing text and must not be blank, so a
// deployment cannot start signing with a placeholder it forgot to replace.
//
// The key is a secret: it is never logged, never returned, and never echoed in an
// error, including when it is refused.
func NewSigner(key string) (*Signer, error) {
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%w: the key is blank", ErrInvalidKey)
	}
	// The length is reported in bytes and the value never is: a wrong key is a
	// secret too.
	if len(key) < MinKeyLength {
		return nil, fmt.Errorf("%w: the key must be at least %d bytes, got %d",
			ErrInvalidKey, MinKeyLength, len(key))
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	return &Signer{key: copied}, nil
}

// Sign renders the code for one ticket: the canonical versioned identity of the
// ticket, signed with this deployment's key.
//
// It signs the ticket's own seat list, ascending, and nothing else. A detail that
// cannot name a ticket, an event and at least one distinct positive seat is
// [ErrUnsignable] rather than a token that names less than the ticket: the renderer
// must fail instead of printing a code the door cannot check.
func (s *Signer) Sign(detail TicketDetail) (string, error) {
	if s == nil || len(s.key) < MinKeyLength {
		return "", fmt.Errorf("%w: the signer was built without a key", ErrInvalidKey)
	}

	claims := Claims{TicketID: detail.TicketID, EventID: detail.Event.ID}
	claims.SeatIDs = make([]int64, 0, len(detail.Seats))
	for _, seat := range detail.Seats {
		claims.SeatIDs = append(claims.SeatIDs, seat.EventSeatID)
	}
	// The projection may carry the seats in any order (it orders them by row and
	// position), so signing sorts them into the one canonical spelling instead of
	// trusting the reader.
	sort.Slice(claims.SeatIDs, func(i, j int) bool { return claims.SeatIDs[i] < claims.SeatIDs[j] })

	payload, err := encodeClaims(claims)
	if err != nil {
		return "", err
	}
	return tokenFor(payload, s.key), nil
}

// Verify checks a presented token and answers the identity it carries. Anything
// that does not verify is [ErrInvalidToken]: a malformed token, a truncated one, a
// version this build does not understand, a signature made with another key, a
// tampered payload or a tampered signature.
//
// The signature is compared with hmac.Equal, over the payload bytes as received.
func (s *Signer) Verify(token string) (Claims, error) {
	if s == nil || len(s.key) < MinKeyLength {
		return Claims{}, fmt.Errorf("%w: the signer was built without a key", ErrInvalidKey)
	}

	payloadPart, signaturePart, ok := strings.Cut(token, ".")
	if !ok {
		return Claims{}, invalidToken("the token is not a payload and a signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return Claims{}, invalidToken("the payload is not URL-safe base64 without padding")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return Claims{}, invalidToken("the signature is not URL-safe base64 without padding")
	}

	// hmac.Equal is constant time and compares the whole signature, so no prefix of
	// a valid signature passes and no early exit leaks how much of it matched.
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return Claims{}, invalidToken("the signature does not match the payload")
	}

	return decodeClaims(payload)
}

// tokenFor renders payload and signature in the transport form.
func tokenFor(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// invalidToken builds one of the fixed refusals. The reason is a sentence about the
// shape of the token and never a claim value, so a refusal cannot confirm that a
// ticket or a seat exists.
func invalidToken(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidToken, reason)
}

// encodeClaims renders the one canonical payload of a claims set: the version, the
// ticket id, the event id and the seat ids joined by one comma, in the order given.
// The caller is responsible for sorting, and this function is what enforces that the
// result is unambiguous.
func encodeClaims(claims Claims) ([]byte, error) {
	if claims.TicketID == "" {
		return nil, fmt.Errorf("%w: the detail names no ticket", ErrUnsignable)
	}
	if claims.EventID == "" {
		return nil, fmt.Errorf("%w: the detail names no event", ErrUnsignable)
	}
	if strings.ContainsAny(claims.TicketID, "|,") || strings.ContainsAny(claims.EventID, "|,") {
		return nil, fmt.Errorf("%w: an identifier carries the separator", ErrUnsignable)
	}
	if len(claims.SeatIDs) == 0 {
		return nil, fmt.Errorf("%w: the detail names no seat", ErrUnsignable)
	}
	seats := make([]string, 0, len(claims.SeatIDs))
	for index, id := range claims.SeatIDs {
		if id <= 0 {
			return nil, fmt.Errorf("%w: a seat identifier is not positive", ErrUnsignable)
		}
		// Strictly ascending is the canonical order: a repeated or reversed list
		// would be a second spelling of the same ticket.
		if index > 0 && claims.SeatIDs[index-1] >= id {
			return nil, fmt.Errorf("%w: the seat identifiers are not distinct and ascending", ErrUnsignable)
		}
		seats = append(seats, strconv.FormatInt(id, 10))
	}
	return []byte(tokenVersion + "|" + claims.TicketID + "|" + claims.EventID + "|" +
		strings.Join(seats, ",")), nil
}

// decodeClaims parses a payload whose signature already checked out, and refuses it
// unless it is byte-identical to its own canonical form. A signature over a
// non-canonical payload is a signature over a spelling this deployment never
// produced, so it is not a ticket of ours.
func decodeClaims(payload []byte) (Claims, error) {
	fields := strings.Split(string(payload), "|")
	if len(fields) != 4 {
		return Claims{}, invalidToken("the payload does not carry four fields")
	}
	if fields[0] != tokenVersion {
		return Claims{}, invalidToken("the payload was signed in a version this build does not understand")
	}

	claims := Claims{TicketID: fields[1], EventID: fields[2]}
	for _, part := range strings.Split(fields[3], ",") {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return Claims{}, invalidToken("a seat identifier is not a whole number")
		}
		claims.SeatIDs = append(claims.SeatIDs, id)
	}
	sort.Slice(claims.SeatIDs, func(i, j int) bool { return claims.SeatIDs[i] < claims.SeatIDs[j] })

	canonical, err := encodeClaims(claims)
	if err != nil {
		return Claims{}, invalidToken("the payload is not the identity of a ticket")
	}
	if !bytes.Equal(canonical, payload) {
		return Claims{}, invalidToken("the payload is not in canonical form")
	}
	return claims, nil
}
