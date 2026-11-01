// Behaviour tests for the signed ticket link (T4, S3).
//
// The link is the one URL a purchase email carries, so it is signed and it carries its
// own expiry: an email is forwarded, archived and read long after the purchase, and a
// permanent key in a mailbox would be a key to the order for as long as the mailbox
// exists. What is pinned here, without a clock and without a database: the token is the
// canonical `v1|<reservationID>|<exp>` payload plus an HMAC over exactly those bytes; a
// different key, a flipped signature byte, a non-canonical spelling of `exp` and a
// padded base64 are all refusals; and expiry is its own observable outcome
// (ErrLinkExpired) while every other failure stays the opaque ErrInvalidToken, so a log
// can say "this link aged out" without telling a caller which part of a token failed.
package tickets_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// The fixture facts of the link tests. Both keys are throwaway and longer than the
// documented minimum, so a case can also prove that a 32 byte key is accepted.
const (
	linkReservationID = "00000000-0000-4000-8000-00000000e1a1"
	linkTestKey       = "link-test-key-0123456789abcdef0123456789"
	linkAttackerKey   = "link-attacker-key-0123456789abcdef0123"
)

// linkTTL is a demo lifetime of seven days: long enough that a case can move the
// signing instant around it by a second and still be inside the window.
const linkTTL = 7 * 24 * time.Hour

// linkNow is when every case sign or verifies. It is fixed and never read from the
// clock, so expiry is asserted exactly instead of slept for.
var linkNow = time.Date(2026, time.May, 4, 10, 0, 0, 0, time.UTC)

// mustLinkSigner builds a signer and fails the test when the key or lifetime is refused.
func mustLinkSigner(t *testing.T, key string, ttl time.Duration) *tickets.LinkSigner {
	t.Helper()
	signer, err := tickets.NewLinkSigner(key, ttl)
	if err != nil {
		t.Fatalf("NewLinkSigner() = unexpected error %v", err)
	}
	return signer
}

// signLink signs one reservation at one instant and fails the test on error.
func signLink(t *testing.T, signer *tickets.LinkSigner, reservationID string, now time.Time) string {
	t.Helper()
	token, err := signer.Sign(reservationID, now)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	return token
}

// linkPayload is the canonical payload the signer must produce for one instant: the
// version, the reservation and the expiry in RFC 3339 UTC, which is the format the rest
// of the project parses and prints.
func linkPayload(reservationID string, now time.Time) string {
	return "v1|" + reservationID + "|" + now.Add(linkTTL).UTC().Truncate(time.Second).Format(time.RFC3339)
}

// T4 (S3): the token is two base64url halves over the canonical, versioned payload, and
// verifying it answers the reservation and the instant it stops opening.
func TestLinkSignProducesAVersionedTokenThatVerifies(t *testing.T) {
	signer := mustLinkSigner(t, linkTestKey, linkTTL)
	token := signLink(t, signer, linkReservationID, linkNow)

	if strings.Count(token, ".") != 1 {
		t.Fatalf("token %q does not separate payload and signature with exactly one dot", token)
	}
	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	for _, part := range []string{payloadPart, signaturePart} {
		if strings.ContainsAny(part, "+/=") {
			t.Fatalf("token part %q is not URL-safe base64 without padding", part)
		}
	}

	// The expiry is spelled the way the rest of the project spells an instant, and it is
	// the one the signing moment plus the configured lifetime names.
	want := linkPayload(linkReservationID, linkNow)
	if got := tokenPayload(t, token); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if !strings.HasSuffix(want, "2026-05-11T10:00:00Z") {
		t.Fatalf("payload = %q, want the expiry seven days after the signing instant", want)
	}

	claims, err := signer.Verify(token, linkNow)
	if err != nil {
		t.Fatalf("Verify() = unexpected error %v", err)
	}
	if claims.ReservationID != linkReservationID {
		t.Fatalf("claims reservation = %q, want %q", claims.ReservationID, linkReservationID)
	}
	if wantExpiry := linkNow.Add(linkTTL); !claims.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("claims expiry = %s, want %s", claims.ExpiresAt, wantExpiry)
	}
	if claims.ExpiresAt.Location() != time.UTC {
		t.Fatalf("claims expiry is in %s, want UTC", claims.ExpiresAt.Location())
	}

	// The same reservation at the same instant always produces the same link: an old
	// email and a new one name the same window, so neither is a second spelling.
	if second := signLink(t, signer, linkReservationID, linkNow); second != token {
		t.Fatalf("signing the same reservation twice produced %q and %q", token, second)
	}
	// Sub-second precision is truncated, so a clock that ticks mid-signing cannot mint a
	// second token for the same window.
	if rounded := signLink(t, signer, linkReservationID, linkNow.Add(500*time.Millisecond)); rounded != token {
		t.Fatalf("signing with a fractional instant produced %q, want the truncated %q", rounded, token)
	}
	// The reservation identifier is signed, not printed: the token is opaque.
	if strings.Contains(token, linkReservationID) {
		t.Fatalf("the token carries the reservation identifier in the clear: %q", token)
	}
}

// T4 (S3): the key is the whole of the link's security, so a token sealed with another
// key verifies as nothing — and a single flipped byte of the signature is a forgery
// too, which only a whole-signature comparison refuses.
func TestLinkVerifyRefusesAForeignKeyOrAFlippedSignatureByte(t *testing.T) {
	signer := mustLinkSigner(t, linkTestKey, linkTTL)
	token := signLink(t, signer, linkReservationID, linkNow)

	forged := seal(tokenPayload(t, token), linkAttackerKey)
	if claims, err := signer.Verify(forged, linkNow); !errors.Is(err, tickets.ErrInvalidToken) {
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
		if claims, err := signer.Verify(candidate, linkNow); !errors.Is(err, tickets.ErrInvalidToken) {
			t.Fatalf("Verify() with signature byte %d changed = %+v, %v; want ErrInvalidToken", index, claims, err)
		}
	}
}

// T4 (S3): a signature over a payload this deployment never produced is not a link of
// ours. The payload has one canonical spelling, so an offset-bearing or fractional
// `exp`, a relabelled version, a padded base64 half and the wrong number of fields are
// all refused as ErrInvalidToken — never as expired, which would describe a token that
// verified.
func TestLinkVerifyRefusesAnyTokenThatIsNotTheCanonicalSignedForm(t *testing.T) {
	signer := mustLinkSigner(t, linkTestKey, linkTTL)
	token := signLink(t, signer, linkReservationID, linkNow)
	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	expiry := linkNow.Add(linkTTL).UTC().Format(time.RFC3339)

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
		{"an expiry with an offset instead of UTC", seal("v1|"+linkReservationID+"|2026-05-11T12:00:00+02:00", linkTestKey)},
		{"an expiry with a fractional second", seal("v1|"+linkReservationID+"|2026-05-11T10:00:00.500Z", linkTestKey)},
		{"an expiry that is not an instant", seal("v1|"+linkReservationID+"|tomorrow", linkTestKey)},
		{"a version this build does not sign", seal("v2|"+linkReservationID+"|"+expiry, linkTestKey)},
		{"a version this build does not spell", seal("V1|"+linkReservationID+"|"+expiry, linkTestKey)},
		{"a payload without a version", seal(linkReservationID+"|"+expiry, linkTestKey)},
		{"a payload with one field too many", seal("v1|"+linkReservationID+"|"+expiry+"|", linkTestKey)},
		{"a payload with an empty reservation", seal("v1||"+expiry, linkTestKey)},
		{"a payload naming another reservation", resignPayload(t, token, "v1|"+linkReservationID+"x|"+expiry)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			claims, err := signer.Verify(testCase.token, linkNow)
			if !errors.Is(err, tickets.ErrInvalidToken) {
				t.Fatalf("Verify() = %+v, %v; want ErrInvalidToken", claims, err)
			}
			// A refusal is never a statement about what the token named.
			for _, leaked := range []string{linkReservationID, expiry} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("the error names a claim value %q: %v", leaked, err)
				}
			}
			if claims.ReservationID != "" || !claims.ExpiresAt.IsZero() {
				t.Fatalf("a refused token answered claims: %+v", claims)
			}
		})
	}
}

// T4 (S3): expiry is its own answer, and the boundary is exact. A link whose `exp` is
// the verification instant is closed (the expiry is the last instant it is unusable
// from), one second later is still open, and one that was tampered with at the same
// moment is a verification failure rather than an expiry — a distinction for logs, never
// for a caller.
func TestLinkVerifyAnswersExpirySeparatelyFromAVerificationFailure(t *testing.T) {
	signer := mustLinkSigner(t, linkTestKey, linkTTL)

	cases := []struct {
		name       string
		signedAt   time.Time
		wantExpiry time.Time
		wantErr    error
	}{
		{"a link that expired an hour ago", linkNow.Add(-time.Hour).Add(-linkTTL), linkNow.Add(-time.Hour), tickets.ErrLinkExpired},
		{"a link that expires exactly now", linkNow.Add(-linkTTL), linkNow, tickets.ErrLinkExpired},
		{"a link that expires one second from now", linkNow.Add(time.Second).Add(-linkTTL), linkNow.Add(time.Second), nil},
		{"a link that expires in the configured lifetime", linkNow, linkNow.Add(linkTTL), nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			token := signLink(t, signer, linkReservationID, testCase.signedAt)

			claims, err := signer.Verify(token, linkNow)
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("Verify() = %+v, %v; want %v", claims, err, testCase.wantErr)
				}
				// An expiry is not a verification failure: the token is genuine and its
				// window closed, which is what a log and a test need to tell apart.
				if errors.Is(err, tickets.ErrInvalidToken) {
					t.Fatalf("an expired link answered ErrInvalidToken: %v", err)
				}
				if claims.ReservationID != "" {
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
	tampered := resignPayload(t, signLink(t, signer, linkReservationID, linkNow), "v1|"+linkReservationID+"|2026-05-11T10:00:01Z")
	if _, err := signer.Verify(tampered, linkNow); !errors.Is(err, tickets.ErrInvalidToken) ||
		errors.Is(err, tickets.ErrLinkExpired) {
		t.Fatalf("Verify() of a tampered token = %v; want ErrInvalidToken and not ErrLinkExpired", err)
	}
}

// T4 (S3): a key that cannot sign anything and a lifetime that would mint links that are
// already closed are both refused at construction. The key is never echoed.
func TestNewLinkSignerRefusesAnUnusableKeyOrLifetime(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		ttl     time.Duration
		wantErr bool
	}{
		{"an empty key", "", linkTTL, true},
		{"a blank key", strings.Repeat(" ", 40), linkTTL, true},
		{"a short key", "too-short-to-sign", linkTTL, true},
		{"one byte below the minimum", strings.Repeat("k", 31), linkTTL, true},
		{"exactly the minimum", strings.Repeat("k", 32), linkTTL, false},
		{"a zero lifetime", linkTestKey, 0, true},
		{"a negative lifetime", linkTestKey, -time.Hour, true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signer, err := tickets.NewLinkSigner(testCase.key, testCase.ttl)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("NewLinkSigner() = %v, want an error", signer)
				}
				if signer != nil {
					t.Fatalf("NewLinkSigner() answered a signer next to an error: %v", signer)
				}
				if testCase.key != "" && strings.Contains(err.Error(), testCase.key) {
					t.Fatalf("the error echoes the key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewLinkSigner() = unexpected error %v", err)
			}
			// The accepted pair really signs and verifies: a constructed but unusable
			// signer would pass the rest of this test otherwise.
			token := signLink(t, signer, linkReservationID, linkNow)
			if _, err := signer.Verify(token, linkNow); err != nil {
				t.Fatalf("Verify() with an accepted signer = unexpected error %v", err)
			}
		})
	}
}

// T4 (S3): a signer that was never built from a key refuses both operations, so a wiring
// mistake fails as a configuration error instead of minting links nobody can check.
func TestZeroLinkSignerRefusesToSignOrVerify(t *testing.T) {
	signer := new(tickets.LinkSigner)

	if token, err := signer.Sign(linkReservationID, linkNow); !errors.Is(err, tickets.ErrInvalidKey) {
		t.Fatalf("Sign() = %q, %v; want ErrInvalidKey", token, err)
	}
	if claims, err := signer.Verify(seal("v1|"+linkReservationID+"|"+linkNow.Format(time.RFC3339), linkTestKey),
		linkNow); !errors.Is(err, tickets.ErrInvalidKey) {
		t.Fatalf("Verify() = %+v, %v; want ErrInvalidKey", claims, err)
	}
}

// T4 (S3): a reservation identifier the payload cannot carry is not signed at all. A
// token naming a second reservation would be a link to somebody else's order.
func TestLinkSignRefusesAReservationItCannotEncode(t *testing.T) {
	signer := mustLinkSigner(t, linkTestKey, linkTTL)

	for _, reservationID := range []string{"", "   ", "reservation|2026", "reservation\n"} {
		t.Run(reservationID, func(t *testing.T) {
			token, err := signer.Sign(reservationID, linkNow)
			if err == nil {
				t.Fatalf("Sign(%q) = %q, want an error", reservationID, token)
			}
			if token != "" {
				t.Fatalf("Sign(%q) answered %q next to an error", reservationID, token)
			}
			if !errors.Is(err, tickets.ErrUnsignable) {
				t.Fatalf("Sign(%q) error = %v, want ErrUnsignable", reservationID, err)
			}
		})
	}
}
