// Behaviour tests for the signed access link (T2, S2, S3).
//
// The link is the one URL a login email carries, so it is signed, it names one address and
// it carries its own short expiry: a mailbox proves control of an address for as long as the
// message exists, and a permanent key in it would be a key to an account. What is pinned
// here, without a clock and without a database: the token is the canonical
// `v1|access|<email>|<nonce>|<exp>` payload plus an HMAC over exactly those bytes; a
// different key, a flipped signature byte, a non-canonical spelling of `exp`, an address
// that is not the normalized one and a padded base64 are all refusals; expiry is its own
// observable outcome (ErrAccessLinkExpired) while every other failure stays the opaque
// ErrInvalidToken; and normalization happens in the signer, so two spellings of one address
// are one payload and one account.
package identity_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/identity"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// The fixture facts of the access link tests. Both keys are throwaway and longer than the
// documented minimum, so a case can also prove that a 32 byte key is accepted.
const (
	accessEmail       = "ana@example.test"
	accessNonce       = "3f8a1c62-9d47-4f0e-8a11-2c6b5d7e9f01"
	accessTestKey     = "access-test-key-0123456789abcdef01234567"
	accessAttackerKey = "access-attacker-key-0123456789abcdef01"
)

// accessTTL is the documented demo lifetime of an access link: fifteen minutes, so a case
// can move the signing instant around it by a second and still be inside the window.
const accessTTL = 15 * time.Minute

// accessNow is when every case signs or verifies. It is fixed and never read from the clock,
// so expiry is asserted exactly instead of slept for.
var accessNow = time.Date(2026, time.May, 4, 10, 0, 0, 0, time.UTC)

// mustAccessSigner builds a signer and fails the test when the key or lifetime is refused.
func mustAccessSigner(t *testing.T, key string, ttl time.Duration) *identity.AccessSigner {
	t.Helper()
	signer, err := identity.NewAccessSigner(key, ttl)
	if err != nil {
		t.Fatalf("NewAccessSigner() = unexpected error %v", err)
	}
	return signer
}

// signAccess signs one address and nonce at one instant and fails the test on error.
func signAccess(t *testing.T, signer *identity.AccessSigner, email, nonce string, now time.Time) string {
	t.Helper()
	token, err := signer.Sign(email, nonce, now)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	return token
}

// accessPayload is the canonical payload the signer must produce for one instant: the
// version, the scope, the normalized address, the nonce and the expiry in RFC 3339 UTC,
// which is the format the rest of the project parses and prints.
func accessPayload(email, nonce string, now time.Time) string {
	return "v1|access|" + email + "|" + nonce + "|" +
		now.Add(accessTTL).UTC().Truncate(time.Second).Format(time.RFC3339)
}

// accessPayloadPart decodes the payload half of a token without verifying it, which is
// exactly what anybody holding the URL from a mailbox can do.
func accessPayloadPart(t *testing.T, token string) string {
	t.Helper()
	payloadPart, _, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token %q carries no signature part", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		t.Fatalf("decode the payload of %q: %v", token, err)
	}
	return string(payload)
}

// resignAccess rebuilds a token from a new payload but keeps the original signature over
// the old payload bytes: the shape of a tampered link.
func resignAccess(t *testing.T, token, payload string) string {
	t.Helper()
	_, signature, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token %q carries no signature part", token)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + signature
}

// sealAccess builds a token exactly as the format prescribes, over an arbitrary payload and
// with an arbitrary key. It lets a case construct a payload that is not canonical, or a
// token signed by somebody else, with a correct signature.
func sealAccess(payload, key string) string {
	mac := hmacSHA256(key, payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac)
}

// T2 (S2): the token is two base64url halves over the canonical, versioned payload, and
// verifying it answers the normalized address, the nonce it was minted with and the instant
// it stops opening.
func TestAccessLinkSignProducesAVersionedTokenThatVerifies(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	token := signAccess(t, signer, accessEmail, accessNonce, accessNow)

	if strings.Count(token, ".") != 1 {
		t.Fatalf("token %q does not separate payload and signature with exactly one dot", token)
	}
	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	for _, part := range []string{payloadPart, signaturePart} {
		if strings.ContainsAny(part, "+/=") {
			t.Fatalf("token part %q is not URL-safe base64 without padding", part)
		}
	}

	// The expiry is spelled the way the rest of the project spells an instant, and it is the
	// one the signing moment plus the configured lifetime names.
	want := accessPayload(accessEmail, accessNonce, accessNow)
	if got := accessPayloadPart(t, token); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if !strings.HasSuffix(want, "2026-05-04T10:15:00Z") {
		t.Fatalf("payload = %q, want the expiry fifteen minutes after the signing instant", want)
	}

	claims, err := signer.Verify(token, accessNow)
	if err != nil {
		t.Fatalf("Verify() = unexpected error %v", err)
	}
	if claims.Email != accessEmail {
		t.Fatalf("claims email = %q, want %q", claims.Email, accessEmail)
	}
	if claims.Nonce != accessNonce {
		t.Fatalf("claims nonce = %q, want %q", claims.Nonce, accessNonce)
	}
	if wantExpiry := accessNow.Add(accessTTL); !claims.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("claims expiry = %s, want %s", claims.ExpiresAt, wantExpiry)
	}
	if claims.ExpiresAt.Location() != time.UTC {
		t.Fatalf("claims expiry is in %s, want UTC", claims.ExpiresAt.Location())
	}

	// The same address, nonce and instant always produce the same link, so a retried send
	// of one login step is not a second token for the same nonce.
	if second := signAccess(t, signer, accessEmail, accessNonce, accessNow); second != token {
		t.Fatalf("signing the same step twice produced %q and %q", token, second)
	}
	// Sub-second precision is truncated, so a clock that ticks mid-signing cannot mint a
	// second token for the same window.
	if rounded := signAccess(t, signer, accessEmail, accessNonce, accessNow.Add(500*time.Millisecond)); rounded != token {
		t.Fatalf("signing with a fractional instant produced %q, want the truncated %q", rounded, token)
	}
	// The address and the nonce are signed, not printed: the token is opaque.
	for _, leaked := range []string{accessEmail, accessNonce} {
		if strings.Contains(token, leaked) {
			t.Fatalf("the token carries %q in the clear: %q", leaked, token)
		}
	}
}

// T2 (S3): the address is the account, so the signer is what makes one person out of two
// spellings of one mailbox. Both casings produce the same payload and the same token for the
// same nonce and instant, and either one verifies to the normalized address.
func TestAccessLinkSignNormalizesTheAddressIntoOnePayload(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)

	canonical := signAccess(t, signer, "ana@x.com", accessNonce, accessNow)
	for _, spelling := range []string{"Ana@X.com", "ANA@X.COM", "  Ana@X.com\t"} {
		t.Run(spelling, func(t *testing.T) {
			token := signAccess(t, signer, spelling, accessNonce, accessNow)
			if token != canonical {
				t.Fatalf("Sign(%q) = %q, want the canonical token %q", spelling, token, canonical)
			}
			if got := accessPayloadPart(t, token); !strings.Contains(got, "|ana@x.com|") {
				t.Fatalf("payload = %q, want the normalized address", got)
			}
			claims, err := signer.Verify(token, accessNow)
			if err != nil {
				t.Fatalf("Verify() = unexpected error %v", err)
			}
			if claims.Email != "ana@x.com" {
				t.Fatalf("claims email = %q, want the normalized %q", claims.Email, "ana@x.com")
			}
		})
	}
}

// NormalizeEmail is exported because the caller that records the address and the caller that
// signs it have to agree on the spelling; this pins what that spelling is.
func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
		want  string
	}{
		{"an address already normalized", "ana@x.com", "ana@x.com"},
		{"upper case is lowered", "ANA@X.COM", "ana@x.com"},
		{"mixed case is lowered", "Ana@X.com", "ana@x.com"},
		{"surrounding whitespace is trimmed", "  Ana@X.com \t\r\n", "ana@x.com"},
		{"an empty address stays empty", "", ""},
		{"a blank address normalizes to nothing", "   \t", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := identity.NormalizeEmail(testCase.email); got != testCase.want {
				t.Fatalf("NormalizeEmail(%q) = %q, want %q", testCase.email, got, testCase.want)
			}
		})
	}
}

// T2 (S2): the key is the whole of the link's security, so a token sealed with another key
// verifies as nothing — and a single flipped byte of the signature is a forgery too, which
// only a whole-signature comparison refuses.
func TestAccessLinkVerifyRefusesAForeignKeyOrAFlippedSignatureByte(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	token := signAccess(t, signer, accessEmail, accessNonce, accessNow)

	forged := sealAccess(accessPayloadPart(t, token), accessAttackerKey)
	if claims, err := signer.Verify(forged, accessNow); !errors.Is(err, identity.ErrInvalidToken) {
		t.Fatalf("Verify() of a token signed with another key = %+v, %v; want ErrInvalidToken", claims, err)
	}

	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		t.Fatalf("decode the signature: %v", err)
	}
	for _, index := range []int{0, len(signature) / 3, len(signature) - 1} {
		corrupted := append([]byte(nil), signature...)
		corrupted[index] ^= 0x01
		candidate := payloadPart + "." + base64.RawURLEncoding.EncodeToString(corrupted)
		if claims, err := signer.Verify(candidate, accessNow); !errors.Is(err, identity.ErrInvalidToken) {
			t.Fatalf("Verify() with signature byte %d changed = %+v, %v; want ErrInvalidToken", index, claims, err)
		}
	}
}

// T2 (S2, S3): a signature over a payload this deployment never produced is not a link of
// ours. The payload has one canonical spelling, so an offset-bearing or fractional `exp`, an
// address that is not the normalized one, a relabelled version or scope, a padded base64 half
// and the wrong number of fields are all refused as ErrInvalidToken — never as expired, which
// would describe a token that verified.
func TestAccessLinkVerifyRefusesAnyTokenThatIsNotTheCanonicalSignedForm(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	token := signAccess(t, signer, accessEmail, accessNonce, accessNow)
	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	expiry := accessNow.Add(accessTTL).UTC().Format(time.RFC3339)

	cases := []struct {
		name  string
		token string
	}{
		{"an empty token", ""},
		{"only a dot", "."},
		{"no dot", payloadPart + signaturePart},
		{"a payload that is not base64", "not base64!!." + signaturePart},
		{"a signature that is not base64", payloadPart + ".not base64!!"},
		{"a padded payload", payloadPart + "=." + signaturePart},
		{"a padded signature", payloadPart + "." + signaturePart + "="},
		{"an expiry with an offset instead of UTC", sealAccess("v1|access|"+accessEmail+"|"+accessNonce+"|2026-05-04T12:15:00+02:00", accessTestKey)},
		{"an expiry with a fractional second", sealAccess("v1|access|"+accessEmail+"|"+accessNonce+"|2026-05-04T10:15:00.500Z", accessTestKey)},
		{"an expiry that is not an instant", sealAccess("v1|access|"+accessEmail+"|"+accessNonce+"|soon", accessTestKey)},
		{"a version this build does not sign", sealAccess("v2|access|"+accessEmail+"|"+accessNonce+"|"+expiry, accessTestKey)},
		{"a version this build does not spell", sealAccess("V1|access|"+accessEmail+"|"+accessNonce+"|"+expiry, accessTestKey)},
		{"a scope this build does not sign", sealAccess("v1|ticket|"+accessEmail+"|"+accessNonce+"|"+expiry, accessTestKey)},
		{"a payload without a version", sealAccess("access|"+accessEmail+"|"+accessNonce+"|"+expiry, accessTestKey)},
		{"a payload with one field too many", sealAccess("v1|access|"+accessEmail+"|"+accessNonce+"|"+expiry+"|", accessTestKey)},
		{"an address that is not normalized", sealAccess("v1|access|Ana@X.com|"+accessNonce+"|"+expiry, accessTestKey)},
		{"a payload with an empty address", sealAccess("v1|access||"+accessNonce+"|"+expiry, accessTestKey)},
		{"a payload with an empty nonce", sealAccess("v1|access|"+accessEmail+"||"+expiry, accessTestKey)},
		{"a payload naming another address", resignAccess(t, token, "v1|access|other@example.test|"+accessNonce+"|"+expiry)},
		{"a payload naming another nonce", resignAccess(t, token, "v1|access|"+accessEmail+"|another-nonce|"+expiry)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			claims, err := signer.Verify(testCase.token, accessNow)
			if !errors.Is(err, identity.ErrInvalidToken) {
				t.Fatalf("Verify() = %+v, %v; want ErrInvalidToken", claims, err)
			}
			// A refusal is never a statement about what the token named, and it is never a
			// ticket failure: the two links answer to the package that minted them.
			for _, leaked := range []string{accessEmail, accessNonce, expiry} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("the error names a claim value %q: %v", leaked, err)
				}
			}
			if errors.Is(err, tickets.ErrInvalidToken) {
				t.Fatalf("an access link refusal answered tickets.ErrInvalidToken: %v", err)
			}
			if claims.Email != "" || claims.Nonce != "" || !claims.ExpiresAt.IsZero() {
				t.Fatalf("a refused token answered claims: %+v", claims)
			}
		})
	}
}

// T2 (S2): expiry is its own answer, and the boundary is exact. A link whose `exp` is the
// verification instant is closed (the expiry is the last instant it is unusable from), one
// second later is still open, and one that was tampered with at the same moment is a
// verification failure rather than an expiry — a distinction for logs and tests, never for a
// caller of the route.
func TestAccessLinkVerifyAnswersExpirySeparatelyFromAVerificationFailure(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)

	cases := []struct {
		name       string
		signedAt   time.Time
		wantExpiry time.Time
		wantErr    error
	}{
		{"a link that expired an hour ago", accessNow.Add(-time.Hour).Add(-accessTTL), accessNow.Add(-time.Hour), identity.ErrAccessLinkExpired},
		{"a link that expires exactly now", accessNow.Add(-accessTTL), accessNow, identity.ErrAccessLinkExpired},
		{"a link that expires one second from now", accessNow.Add(time.Second).Add(-accessTTL), accessNow.Add(time.Second), nil},
		{"a link that expires in the configured lifetime", accessNow, accessNow.Add(accessTTL), nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			token := signAccess(t, signer, accessEmail, accessNonce, testCase.signedAt)

			claims, err := signer.Verify(token, accessNow)
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("Verify() = %+v, %v; want %v", claims, err, testCase.wantErr)
				}
				// An expiry is not a verification failure: the token is genuine and its
				// window closed, which is what a log and a test need to tell apart.
				if errors.Is(err, identity.ErrInvalidToken) {
					t.Fatalf("an expired link answered ErrInvalidToken: %v", err)
				}
				if errors.Is(err, tickets.ErrLinkExpired) {
					t.Fatalf("an expired access link answered tickets.ErrLinkExpired: %v", err)
				}
				if claims.Email != "" || claims.Nonce != "" {
					t.Fatalf("an expired link answered claims: %+v", claims)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify() = unexpected error %v", err)
			}
			if !claims.ExpiresAt.Equal(testCase.wantExpiry) {
				t.Fatalf("claims expiry = %s, want %s", claims.ExpiresAt, testCase.wantExpiry)
			}
		})
	}

	// The same instant, an invalid token: tampering is not an expiry, and the two are
	// distinguishable errors even though no caller of the route may tell them apart.
	tampered := resignAccess(t, signAccess(t, signer, accessEmail, accessNonce, accessNow),
		"v1|access|"+accessEmail+"|"+accessNonce+"|2026-05-04T10:15:01Z")
	if _, err := signer.Verify(tampered, accessNow); !errors.Is(err, identity.ErrInvalidToken) ||
		errors.Is(err, identity.ErrAccessLinkExpired) {
		t.Fatalf("Verify() of a tampered token = %v; want ErrInvalidToken and not ErrAccessLinkExpired", err)
	}
}

// T2 (S2): a key that cannot sign anything and a lifetime that would mint links that are
// already closed are both refused at construction. The key is never echoed.
func TestNewAccessSignerRefusesAnUnusableKeyOrLifetime(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		ttl      time.Duration
		wantErr  bool
		wantKeyE bool
	}{
		{"an empty key", "", accessTTL, true, true},
		{"a blank key", strings.Repeat(" ", 40), accessTTL, true, true},
		{"a short key", "too-short-to-sign", accessTTL, true, true},
		{"one byte below the minimum", strings.Repeat("k", identity.MinKeyLength-1), accessTTL, true, true},
		{"exactly the minimum", strings.Repeat("k", identity.MinKeyLength), accessTTL, false, false},
		{"a zero lifetime", accessTestKey, 0, true, false},
		{"a negative lifetime", accessTestKey, -time.Minute, true, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signer, err := identity.NewAccessSigner(testCase.key, testCase.ttl)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("NewAccessSigner() = %v, want an error", signer)
				}
				if signer != nil {
					t.Fatalf("NewAccessSigner() answered a signer next to an error: %v", signer)
				}
				if testCase.wantKeyE && !errors.Is(err, identity.ErrInvalidKey) {
					t.Fatalf("NewAccessSigner() error = %v, want ErrInvalidKey", err)
				}
				if testCase.key != "" && strings.Contains(err.Error(), testCase.key) {
					t.Fatalf("the error echoes the key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAccessSigner() = unexpected error %v", err)
			}
			// The accepted pair really signs and verifies: a constructed but unusable
			// signer would pass the rest of this test otherwise.
			token := signAccess(t, signer, accessEmail, accessNonce, accessNow)
			if _, err := signer.Verify(token, accessNow); err != nil {
				t.Fatalf("Verify() with an accepted signer = unexpected error %v", err)
			}
		})
	}
}

// T2 (S2): a signer that was never built from a key refuses both operations, so a wiring
// mistake fails as a configuration error instead of minting links nobody can check.
func TestZeroAccessSignerRefusesToSignOrVerify(t *testing.T) {
	signer := new(identity.AccessSigner)

	if token, err := signer.Sign(accessEmail, accessNonce, accessNow); !errors.Is(err, identity.ErrInvalidKey) {
		t.Fatalf("Sign() = %q, %v; want ErrInvalidKey", token, err)
	}
	if claims, err := signer.Verify(sealAccess(accessPayload(accessEmail, accessNonce, accessNow), accessTestKey),
		accessNow); !errors.Is(err, identity.ErrInvalidKey) {
		t.Fatalf("Verify() = %+v, %v; want ErrInvalidKey", claims, err)
	}
}

// T2 (S2): the nonce is the caller's single-use record and the address is the account, so
// neither is invented and neither is signed when it cannot be carried faithfully. A link with
// no nonce could not be spent once, and a link whose address the payload cannot hold would be
// a second spelling of somebody else's account.
func TestAccessLinkSignRefusesAnAddressOrNonceItCannotEncode(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)

	cases := []struct {
		name    string
		email   string
		nonce   string
		wantErr error
	}{
		{"an empty address", "", accessNonce, identity.ErrUnsignable},
		{"a blank address", "   \t", accessNonce, identity.ErrUnsignable},
		{"an address carrying the separator", "ana|x@example.test", accessNonce, identity.ErrUnsignable},
		{"an address carrying a space", "ana x@example.test", accessNonce, identity.ErrUnsignable},
		{"an address carrying a control byte", "ana@exa\nmple.test", accessNonce, identity.ErrUnsignable},
		{"an empty nonce", accessEmail, "", identity.ErrUnsignable},
		{"a blank nonce", accessEmail, "   ", identity.ErrUnsignable},
		{"a nonce carrying the separator", accessEmail, "spent|once", identity.ErrUnsignable},
		{"a nonce carrying a control byte", accessEmail, "spent-once\n", identity.ErrUnsignable},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			token, err := signer.Sign(testCase.email, testCase.nonce, accessNow)
			if err == nil {
				t.Fatalf("Sign(%q, %q) = %q, want an error", testCase.email, testCase.nonce, token)
			}
			if token != "" {
				t.Fatalf("Sign(%q, %q) answered %q next to an error", testCase.email, testCase.nonce, token)
			}
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Sign(%q, %q) error = %v, want %v", testCase.email, testCase.nonce, err, testCase.wantErr)
			}
			// Neither the address nor the nonce reaches the error: the address is a person
			// and the nonce is a one-use secret of the login step.
			for _, leaked := range []string{strings.TrimSpace(testCase.email), strings.TrimSpace(testCase.nonce)} {
				if leaked != "" && strings.Contains(err.Error(), leaked) {
					t.Fatalf("the error names %q: %v", leaked, err)
				}
			}
		})
	}
}

// T2 (S3): an access link that fails must never be reported as a ticket failure, so the
// identity package carries its own sentinels rather than borrowing the tickets package's. A
// caller mapping errors to HTTP answers would otherwise tell a person their login was a bad
// ticket, and a log would file one incident under the other.
func TestIdentityFailuresAreNotTicketFailures(t *testing.T) {
	pairs := []struct {
		identity error
		ticket   error
	}{
		{identity.ErrInvalidKey, tickets.ErrInvalidKey},
		{identity.ErrInvalidToken, tickets.ErrInvalidToken},
		{identity.ErrUnsignable, tickets.ErrUnsignable},
		{identity.ErrAccessLinkExpired, tickets.ErrLinkExpired},
	}
	for _, pair := range pairs {
		if errors.Is(pair.identity, pair.ticket) || errors.Is(pair.ticket, pair.identity) {
			t.Fatalf("%v and %v are the same failure", pair.identity, pair.ticket)
		}
		if pair.identity.Error() == pair.ticket.Error() {
			t.Fatalf("%v and %v share a message", pair.identity, pair.ticket)
		}
	}
}

// hmacSHA256 is the signature half of the format, spelled here so a case can seal a payload
// with a key of its own choosing.
func hmacSHA256(key, payload string) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}
