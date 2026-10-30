// Behaviour tests for the signed QR token (T5, S4).
//
// They pin the whole contract a door scanner depends on, without a database: the
// token is an opaque, canonical, versioned payload plus an HMAC-SHA256 signature
// over those exact payload bytes; it never carries a name, a film, a time or a
// price; and every way of breaking it — a tampered field, a forged signature, a
// truncated token, an unknown version, an unusable key — is one error,
// ErrInvalidToken, that names no claim value.
package tickets_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// qrTestKey is a fixed, throwaway signing key. It is longer than the documented
// minimum so a case can also prove that a 32 byte key is accepted while a shorter
// one is not.
const qrTestKey = "qr-test-key-0123456789abcdef0123456789"

// qrAttackerKey is a different, equally long key: a token sealed with it is a
// forgery, and it must never verify under qrTestKey.
const qrAttackerKey = "qr-attacker-key-0123456789abcdef01234"

// qrTicketTicketID and qrTicketEventID are distinctive so a test can assert that
// neither ever appears in a token or in an error message.
const (
	qrTicketTicketID = "9f1e0f7c-3f0a-4b8e-9a1e-1c2d3e4f5a6b"
	qrTicketEventID  = "3c0b7f2a-8d4e-4c1a-9b2f-5e6d7c8b9a01"
)

// qrScreening is the screening instant of the fixture. It is fixed, never read
// from the clock.
var qrScreening = time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC)

// qrDetail is the ticket a QR token is signed for: one ticket, one seat, one
// order — everything the projection really carries.
func qrDetail() tickets.TicketDetail {
	return tickets.TicketDetail{
		TicketID:      qrTicketTicketID,
		Status:        tickets.TicketStatusIssued,
		IssuedAt:      qrScreening.Add(-48 * time.Hour),
		ReservationID: "00000000-0000-4000-8000-0000000000aa",
		Event: tickets.EventDetail{
			ID:         qrTicketEventID,
			FilmTitle:  "La película secreta",
			StartsAt:   qrScreening,
			RoomName:   "Sala 1",
			CinemaName: "Cine Centro",
			Timezone:   "Europe/Madrid",
			Status:     "on_sale",
		},
		Seats:            []tickets.Seat{{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"}},
		OrderAmountCents: 1998,
	}
}

// mustSigner builds a signer and fails the test if the key is refused.
func mustSigner(t *testing.T, key string) *tickets.Signer {
	t.Helper()
	signer, err := tickets.NewSigner(key)
	if err != nil {
		t.Fatalf("NewSigner() = unexpected error %v", err)
	}
	return signer
}

// signTicket signs the fixture and fails the test on error.
func signTicket(t *testing.T, signer *tickets.Signer, detail tickets.TicketDetail) string {
	t.Helper()
	token, err := signer.Sign(detail)
	if err != nil {
		t.Fatalf("Sign() = unexpected error %v", err)
	}
	return token
}

// tokenPayload decodes the payload half of a token without verifying it, which is
// exactly what an attacker holding a printed code can do.
func tokenPayload(t *testing.T, token string) string {
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

// resignPayload rebuilds a token from a new payload but keeps the original
// signature over the old payload bytes: the shape of a tampered code.
func resignPayload(t *testing.T, token, payload string) string {
	t.Helper()
	_, signature, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token %q carries no signature part", token)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + signature
}

// seal builds a token exactly as the format prescribes, over an arbitrary payload
// and with an arbitrary key. It lets a case construct a payload that is not
// canonical, or a token signed by somebody else, with a correct signature.
func seal(payload, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// T5 (S4): the token is two base64url halves, the payload is the canonical
// versioned identity of the ticket, and verifying it answers exactly that identity.
func TestSignProducesAVersionedOpaqueTokenThatVerifies(t *testing.T) {
	signer := mustSigner(t, qrTestKey)
	detail := qrDetail()
	token := signTicket(t, signer, detail)

	if strings.Count(token, ".") != 1 {
		t.Fatalf("token %q does not separate payload and signature with exactly one dot", token)
	}
	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	for _, part := range []string{payloadPart, signaturePart} {
		if strings.ContainsAny(part, "+/=") {
			t.Fatalf("token part %q is not URL-safe base64 without padding", part)
		}
	}
	if _, err := base64.RawURLEncoding.DecodeString(signaturePart); err != nil {
		t.Fatalf("decode the signature of %q: %v", token, err)
	}

	// The payload is the canonical, versioned identity: the ticket, its event and
	// its seat ids ascending. Nothing else.
	if got, want := tokenPayload(t, token), "v1|"+qrTicketTicketID+"|"+qrTicketEventID+"|21"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}

	claims, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("Verify() = unexpected error %v", err)
	}
	if claims.TicketID != qrTicketTicketID || claims.EventID != qrTicketEventID {
		t.Fatalf("claims = %+v, want ticket %q and event %q", claims, qrTicketTicketID, qrTicketEventID)
	}
	if len(claims.SeatIDs) != 1 || claims.SeatIDs[0] != 21 {
		t.Fatalf("claim seat ids = %v, want [21]", claims.SeatIDs)
	}

	// A photograph of the code must expose nothing: no ticket detail, no buyer, no
	// movie, no time, no money.
	decoded, _ := base64.RawURLEncoding.DecodeString(payloadPart)
	for _, leaked := range []string{
		"La película secreta", "Sala 1", "Cine Centro", "Europe/Madrid",
		"19.98", "1998", "2026-05-04", detail.ReservationID, "A1",
	} {
		if strings.Contains(string(decoded), leaked) {
			t.Fatalf("the payload contains %q: %s", leaked, decoded)
		}
	}
	if strings.Contains(token, qrTicketTicketID) || strings.Contains(token, qrTicketEventID) {
		t.Fatalf("the token carries an identifier in the clear: %q", token)
	}

	// The same ticket always produces the same code, so the web page and the PDF of
	// one ticket cannot show two different codes.
	if second := signTicket(t, signer, detail); second != token {
		t.Fatalf("signing the same ticket twice produced %q and %q", token, second)
	}
}

// T5 (S4): the seat list is signed in ascending order, so the payload has one
// canonical form whatever order the projection happens to carry.
func TestSignWritesTheSeatIdentifiersAscending(t *testing.T) {
	signer := mustSigner(t, qrTestKey)
	detail := qrDetail()
	detail.Seats = []tickets.Seat{
		{EventSeatID: 9, RowLabel: "B", SeatNumber: "2"},
		{EventSeatID: 2, RowLabel: "A", SeatNumber: "1"},
		{EventSeatID: 5, RowLabel: "A", SeatNumber: "3"},
	}
	token := signTicket(t, signer, detail)

	if got, want := tokenPayload(t, token), "v1|"+qrTicketTicketID+"|"+qrTicketEventID+"|2,5,9"; got != want {
		t.Fatalf("payload = %q, want the seat ids ascending %q", got, want)
	}
	claims, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("Verify() = unexpected error %v", err)
	}
	if len(claims.SeatIDs) != 3 || claims.SeatIDs[0] != 2 || claims.SeatIDs[1] != 5 || claims.SeatIDs[2] != 9 {
		t.Fatalf("claim seat ids = %v, want [2 5 9]", claims.SeatIDs)
	}
}

// T5 (S4): every field of the signed payload is covered by the signature, so
// rewriting any one of them in the printed code breaks the token.
func TestVerifyRejectsATamperedPayloadForEverySignedField(t *testing.T) {
	signer := mustSigner(t, qrTestKey)
	detail := qrDetail()
	twoSeats := detail
	twoSeats.Seats = []tickets.Seat{
		{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"},
		{EventSeatID: 22, RowLabel: "A", SeatNumber: "2"},
	}

	cases := []struct {
		name    string
		detail  tickets.TicketDetail
		payload string
	}{
		{
			name:    "another ticket id",
			detail:  detail,
			payload: "v1|00000000-0000-4000-8000-00000000dead|" + qrTicketEventID + "|21",
		},
		{
			name:    "another event id",
			detail:  detail,
			payload: "v1|" + qrTicketTicketID + "|00000000-0000-4000-8000-0000000000bb|21",
		},
		{
			name:    "another seat",
			detail:  detail,
			payload: "v1|" + qrTicketTicketID + "|" + qrTicketEventID + "|22",
		},
		{
			name:    "an added seat",
			detail:  twoSeats,
			payload: "v1|" + qrTicketTicketID + "|" + qrTicketEventID + "|21,22,23",
		},
		{
			name:    "a dropped seat",
			detail:  twoSeats,
			payload: "v1|" + qrTicketTicketID + "|" + qrTicketEventID + "|21",
		},
		{
			name:    "the seat order reversed",
			detail:  twoSeats,
			payload: "v1|" + qrTicketTicketID + "|" + qrTicketEventID + "|22,21",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			token := signTicket(t, signer, testCase.detail)
			tampered := resignPayload(t, token, testCase.payload)
			claims, err := signer.Verify(tampered)
			if !errors.Is(err, tickets.ErrInvalidToken) {
				t.Fatalf("Verify(tampered %s) = %+v, %v; want ErrInvalidToken", testCase.name, claims, err)
			}
			for _, leaked := range []string{qrTicketTicketID, qrTicketEventID, "21", "22"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("the error names the claim value %q: %v", leaked, err)
				}
			}
		})
	}
}

// T5 (S4): a code sealed with somebody else's key, or a signature with a single
// changed byte, is a forgery and verifies as nothing.
func TestVerifyRejectsAForgedOrCorruptedSignature(t *testing.T) {
	signer := mustSigner(t, qrTestKey)
	detail := qrDetail()
	token := signTicket(t, signer, detail)
	payload := tokenPayload(t, token)

	// The same payload, sealed by a key this deployment does not hold.
	forged := seal(payload, qrAttackerKey)
	if claims, err := signer.Verify(forged); !errors.Is(err, tickets.ErrInvalidToken) {
		t.Fatalf("Verify() of a token forged with another key = %+v, %v; want ErrInvalidToken", claims, err)
	}

	// One changed byte of the signature: the comparison must be over the whole
	// signature, not over a prefix.
	signature, err := base64.RawURLEncoding.DecodeString(token[strings.Index(token, ".")+1:])
	if err != nil {
		t.Fatalf("decode the signature: %v", err)
	}
	for _, index := range []int{0, len(signature) / 2, len(signature) - 1} {
		corrupted := append([]byte(nil), signature...)
		corrupted[index] ^= 0x01
		candidate := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
			base64.RawURLEncoding.EncodeToString(corrupted)
		if claims, err := signer.Verify(candidate); !errors.Is(err, tickets.ErrInvalidToken) {
			t.Fatalf("Verify() with signature byte %d changed = %+v, %v; want ErrInvalidToken", index, claims, err)
		}
	}
}

// T5 (S4): a token that is not the canonical signed identity of a ticket does not
// verify: truncated, padded, unparseable, of another version, or in a shape this
// build does not sign. Everything that fails is ErrInvalidToken.
func TestVerifyRejectsAnyTokenThatIsNotTheCanonicalSignedIdentity(t *testing.T) {
	signer := mustSigner(t, qrTestKey)
	token := signTicket(t, signer, qrDetail())
	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	canonical := tokenPayload(t, token)

	cases := []struct {
		name  string
		token string
	}{
		{"an empty token", ""},
		{"only a dot", "."},
		{"no dot", payloadPart + signaturePart},
		{"an empty payload", "." + signaturePart},
		{"an empty signature", payloadPart + "."},
		{"a truncated signature", payloadPart + "." + signaturePart[:len(signaturePart)-2]},
		{"an extra dot", token + "." + signaturePart},
		{"a payload that is not base64", "not base64!!." + signaturePart},
		{"a signature that is not base64", payloadPart + ".not base64!!"},
		{"a padded payload", payloadPart + "=." + signaturePart},
		{"a payload this build does not sign", seal("v2|"+qrTicketTicketID+"|"+qrTicketEventID+"|21", qrTestKey)},
		{"a payload with three fields", seal("v1|"+qrTicketTicketID+"|"+qrTicketEventID, qrTestKey)},
		{"a payload with five fields", seal(canonical+"|extra", qrTestKey)},
		{"a payload with no seat", seal("v1|"+qrTicketTicketID+"|"+qrTicketEventID+"|", qrTestKey)},
		{"a payload with a non numeric seat", seal("v1|"+qrTicketTicketID+"|"+qrTicketEventID+"|A1", qrTestKey)},
		{"a payload with a zero seat", seal("v1|"+qrTicketTicketID+"|"+qrTicketEventID+"|0", qrTestKey)},
		{"a payload with a negative seat", seal("v1|"+qrTicketTicketID+"|"+qrTicketEventID+"|-21", qrTestKey)},
		{"a payload with a duplicated seat", seal("v1|"+qrTicketTicketID+"|"+qrTicketEventID+"|21,21", qrTestKey)},
		{"a payload with an empty ticket id", seal("v1||"+qrTicketEventID+"|21", qrTestKey)},
		{"a payload with an empty event id", seal("v1|"+qrTicketTicketID+"||21", qrTestKey)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			claims, err := signer.Verify(testCase.token)
			if !errors.Is(err, tickets.ErrInvalidToken) {
				t.Fatalf("Verify() = %+v, %v; want ErrInvalidToken", claims, err)
			}
			if claims.TicketID != "" || claims.EventID != "" || len(claims.SeatIDs) != 0 {
				t.Fatalf("a refused token answered claims: %+v", claims)
			}
		})
	}
}

// T5 (S4): a ticket detail that does not name a ticket, an event and at least one
// distinct positive seat cannot be signed at all, and the signer never answers a
// token for it.
func TestSignRefusesADetailItCannotEncode(t *testing.T) {
	signer := mustSigner(t, qrTestKey)

	cases := []struct {
		name   string
		mutate func(*tickets.TicketDetail)
	}{
		{"no ticket id", func(detail *tickets.TicketDetail) { detail.TicketID = "" }},
		{"no event id", func(detail *tickets.TicketDetail) { detail.Event.ID = "" }},
		{"no seat", func(detail *tickets.TicketDetail) { detail.Seats = nil }},
		{"a zero seat", func(detail *tickets.TicketDetail) {
			detail.Seats = []tickets.Seat{{EventSeatID: 0, RowLabel: "A", SeatNumber: "1"}}
		}},
		{"the same seat twice", func(detail *tickets.TicketDetail) {
			detail.Seats = []tickets.Seat{
				{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"},
				{EventSeatID: 21, RowLabel: "A", SeatNumber: "1"},
			}
		}},
		{"an identifier that would break the payload", func(detail *tickets.TicketDetail) {
			detail.TicketID = "ticket|29"
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			detail := qrDetail()
			testCase.mutate(&detail)
			token, err := signer.Sign(detail)
			if err == nil {
				t.Fatalf("Sign() = %q, want an error", token)
			}
			if token != "" {
				t.Fatalf("Sign() answered %q next to an error", token)
			}
			// A signing failure is not a statement about a token: it is a ticket the
			// deployment cannot serve a code for.
			if !errors.Is(err, tickets.ErrUnsignable) {
				t.Fatalf("Sign() error = %v, want ErrUnsignable", err)
			}
		})
	}
}

// T5 (S4): a key that is blank, all whitespace, or shorter than the documented
// minimum cannot sign anything, and the refusal never echoes the key.
func TestNewSignerRefusesAnUnusableKey(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"an empty key", "", true},
		{"a blank key", "                                  ", true}, // 34 spaces
		{"a short key", "too-short-to-sign-anything", true},
		{"one byte below the minimum", strings.Repeat("k", 31), true},
		{"exactly the minimum", strings.Repeat("k", 32), false},
		{"a comfortably long key", qrTestKey, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			signer, err := tickets.NewSigner(testCase.key)
			if testCase.wantErr {
				if !errors.Is(err, tickets.ErrInvalidKey) {
					t.Fatalf("NewSigner() = %v, %v; want ErrInvalidKey", signer, err)
				}
				if strings.Contains(err.Error(), testCase.key) && testCase.key != "" {
					t.Fatalf("the error echoes the key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSigner() = unexpected error %v", err)
			}
			// The accepted key really signs: a constructed but unusable signer would
			// pass this test otherwise.
			if _, err := signer.Sign(qrDetail()); err != nil {
				t.Fatalf("Sign() with an accepted key = unexpected error %v", err)
			}
		})
	}
}

// T5 (S4): a signer that was never built from a key signs and verifies nothing,
// so a wiring mistake fails as a configuration error instead of minting codes.
func TestZeroSignerRefusesToSignOrVerify(t *testing.T) {
	signer := new(tickets.Signer)

	if token, err := signer.Sign(qrDetail()); !errors.Is(err, tickets.ErrInvalidKey) {
		t.Fatalf("Sign() = %q, %v; want ErrInvalidKey", token, err)
	}
	if claims, err := signer.Verify(seal("v1|a|b|1", qrTestKey)); !errors.Is(err, tickets.ErrInvalidKey) {
		t.Fatalf("Verify() = %+v, %v; want ErrInvalidKey", claims, err)
	}
}
