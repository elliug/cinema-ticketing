// S11: the catalogue's page request and its cursor, tested without a database.
//
// Three rules live here, and each of them is a rule the store would otherwise be
// trusted to remember:
//
//   - a page request is refused when the limit is outside the documented range, naming
//     the field, so a client is never silently given a different page than it asked
//     for;
//   - `from` and `to` are ordered and timezone-normalized, refused by naming the field
//     when `to` precedes `from`;
//   - the cursor is an opaque encoding of the pair the catalogue's ORDER BY fixes
//     (`starts_at, id`), and anything this build did not issue is refused by naming the
//     field. An empty page would read as "no more screenings", which is the one wrong
//     answer a client walking pages cannot detect.
//
// The walk itself — each screening seen exactly once while the list changes under the
// reader — needs PostgreSQL and lives in the gated tests next to this file.
package catalog_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/catalog"
)

// S11: the range is enforced rather than clamped, and a request that names no limit is
// not the read model's problem — it is refused, so no caller can ask for "everything" by
// leaving a field at its zero value.
func TestListQueryEnforcesTheDocumentedLimit(t *testing.T) {
	if catalog.DefaultEventLimit != 20 || catalog.MaxEventLimit != 100 {
		t.Fatalf("the documented page size changed to %d/%d; update the wire tests with it",
			catalog.DefaultEventLimit, catalog.MaxEventLimit)
	}

	for _, limit := range []int{1, 2, 50, catalog.MaxEventLimit} {
		query, err := catalog.ListQuery{Limit: limit}.Normalize()
		if err != nil {
			t.Fatalf("Normalize() of limit %d = unexpected error %v", limit, err)
		}
		if query.Limit != limit {
			t.Fatalf("Normalize() of limit %d answered %d", limit, query.Limit)
		}
	}

	for _, limit := range []int{0, -1, catalog.MaxEventLimit + 1, 10000} {
		_, err := catalog.ListQuery{Limit: limit}.Normalize()
		requireQueryRefusal(t, err, "limit")
		// The refusal has to be recognisable by a transport without matching strings.
		if !errors.Is(err, catalog.ErrInvalidQuery) {
			t.Fatalf("Normalize() of limit %d = %v, want ErrInvalidQuery", limit, err)
		}
	}
}

// S11: the bounds are an ordered pair of instants, normalized to UTC so the instant a
// client sent and the instant the keyset compares are the same one.
func TestListQueryOrdersAndNormalizesTheBounds(t *testing.T) {
	madrid := time.FixedZone("CEST", 2*60*60)
	from := time.Date(2026, time.May, 4, 20, 30, 0, 0, madrid)
	to := time.Date(2026, time.May, 5, 20, 30, 0, 0, madrid)

	query, err := catalog.ListQuery{Limit: 10, From: &from, To: &to}.Normalize()
	if err != nil {
		t.Fatalf("Normalize() of an ordered pair = unexpected error %v", err)
	}
	if !query.From.Equal(from) || !query.To.Equal(to) {
		t.Fatalf("Normalize() answered %v..%v, want the same instants %v..%v",
			query.From, query.To, from, to)
	}
	if query.From.Location() != time.UTC || query.To.Location() != time.UTC {
		t.Fatalf("Normalize() answered the bounds in %v, want them in UTC",
			query.From.Location())
	}
	// A pair that names the same instant is one screening, not a contradiction.
	same, err := catalog.ListQuery{Limit: 10, From: &from, To: &from}.Normalize()
	if err != nil {
		t.Fatalf("Normalize() of one instant as both bounds = unexpected error %v", err)
	}
	if !same.From.Equal(*same.To) {
		t.Fatalf("Normalize() of one instant answered %v..%v", same.From, same.To)
	}

	// The same pair the other way round is a page that can never exist, and the refusal
	// names both fields so the caller can fix the one it got wrong.
	_, err = catalog.ListQuery{Limit: 10, From: &to, To: &from}.Normalize()
	requireQueryRefusal(t, err, "to", "from")
}

// S11: the cursor encodes the pair the ORDER BY fixes, and it is opaque: a client cannot
// read a position out of it or write one into it, so the catalogue can change how a page
// continues without breaking the clients already holding one.
func TestCursorIsAnOpaqueEncodingOfTheScreeningPair(t *testing.T) {
	cursor := catalog.Cursor{
		StartsAt: time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC),
		ID:       "00000000-0000-0000-0000-00000000f1a1",
	}
	encoded := cursor.String()
	if encoded == "" {
		t.Fatal("String() answered an empty cursor")
	}
	if strings.Contains(encoded, cursor.ID) || strings.Contains(encoded, "2026-05-04") {
		t.Fatalf("the cursor %q spells out the position it encodes", encoded)
	}
	// It is URL-safe, so a client can hand it back in a query string unchanged.
	if strings.ContainsAny(encoded, "+/= ") {
		t.Fatalf("the cursor %q is not URL-safe", encoded)
	}

	decoded, err := catalog.ParseCursor(encoded)
	if err != nil {
		t.Fatalf("ParseCursor() of a cursor this build issued = unexpected error %v", err)
	}
	if !decoded.StartsAt.Equal(cursor.StartsAt) || decoded.ID != cursor.ID {
		t.Fatalf("ParseCursor() answered %+v, want the position %+v", decoded, cursor)
	}

	// The instant survives with its precision: two screenings in the same second are told
	// apart by the identifier, but the time itself must not be rounded.
	precise := catalog.Cursor{
		StartsAt: time.Date(2026, time.May, 4, 18, 30, 0, 123456789, time.UTC),
		ID:       cursor.ID,
	}
	roundTrip, err := catalog.ParseCursor(precise.String())
	if err != nil {
		t.Fatalf("ParseCursor() of a precise cursor = unexpected error %v", err)
	}
	if !roundTrip.StartsAt.Equal(precise.StartsAt) {
		t.Fatalf("ParseCursor() answered %v, want %v", roundTrip.StartsAt, precise.StartsAt)
	}

	// A position handed in from another zone names the same instant, in UTC.
	other := catalog.Cursor{
		StartsAt: cursor.StartsAt.In(time.FixedZone("CEST", 2*60*60)),
		ID:       strings.ToUpper(cursor.ID),
	}
	normalized, err := catalog.ParseCursor(other.String())
	if err != nil {
		t.Fatalf("ParseCursor() of a cursor built from another zone = unexpected error %v", err)
	}
	if !normalized.StartsAt.Equal(cursor.StartsAt) || normalized.StartsAt.Location() != time.UTC {
		t.Fatalf("ParseCursor() answered %v, want the same instant in UTC", normalized.StartsAt)
	}
	// The identifier is canonicalized, so the text comparison the keyset makes compares
	// one spelling and not the case a client happened to use.
	if normalized.ID != cursor.ID {
		t.Fatalf("ParseCursor() answered the identifier %q, want the canonical %q", normalized.ID, cursor.ID)
	}
}

// S11: anything this build did not issue is refused by naming the field. A stale cursor
// (one from a previous scheme) is not "malformed" — it is a position this build cannot
// read — and both are refusals rather than an empty page.
func TestParseCursorRefusesAnythingItDidNotIssue(t *testing.T) {
	encode := func(plain string) string { return base64.RawURLEncoding.EncodeToString([]byte(plain)) }
	cases := map[string]string{
		"empty":               "",
		"blank":               "   ",
		"not base64":          "not-a-cursor",
		"base64 of nothing":   encode(""),
		"one field":           encode("v1"),
		"two fields":          encode("v1:2026-05-04T18:30:00Z"),
		"no version":          encode("2026-05-04T18:30:00Z:00000000-0000-0000-0000-00000000f1a1"),
		"a future version":    encode("v2:2026-05-04T18:30:00Z:00000000-0000-0000-0000-00000000f1a1"),
		"an unusable instant": encode("v1:yesterday:00000000-0000-0000-0000-00000000f1a1"),
		"no identifier":       encode("v1:2026-05-04T18:30:00Z:not-a-screening"),
		"an empty identifier": encode("v1:2026-05-04T18:30:00Z:"),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			cursor, err := catalog.ParseCursor(text)
			requireQueryRefusal(t, err, "cursor")
			if !errors.Is(err, catalog.ErrInvalidQuery) {
				t.Fatalf("ParseCursor(%q) = %v, want ErrInvalidQuery", text, err)
			}
			if cursor.ID != "" || !cursor.StartsAt.IsZero() {
				t.Fatalf("ParseCursor(%q) answered the position %+v next to its error", text, cursor)
			}
		})
	}
}

// S11: a page request carries a validated cursor, so the store can trust the pair it
// compares and never receives a position it would have to repair.
func TestListQueryValidatesTheCursor(t *testing.T) {
	valid := catalog.Cursor{
		StartsAt: time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC),
		ID:       "00000000-0000-0000-0000-00000000f1a1",
	}
	query, err := catalog.ListQuery{Limit: 10, After: &valid}.Normalize()
	if err != nil {
		t.Fatalf("Normalize() with a cursor = unexpected error %v", err)
	}
	if query.After == nil || query.After.ID != valid.ID || !query.After.StartsAt.Equal(valid.StartsAt) {
		t.Fatalf("Normalize() answered the cursor %+v, want %+v", query.After, valid)
	}

	// A position built in code rather than parsed is validated on the way in, so a
	// second caller cannot bypass the encoding rule.
	broken := catalog.Cursor{StartsAt: valid.StartsAt, ID: "not-a-screening"}
	if _, err := (catalog.ListQuery{Limit: 10, After: &broken}).Normalize(); err == nil {
		t.Fatal("Normalize() accepted a cursor that names no screening")
	} else {
		requireQueryRefusal(t, err, "cursor")
	}
	// A zero position is not a position: the first page is asked for by naming no
	// cursor at all.
	empty := catalog.Cursor{}
	if _, err := (catalog.ListQuery{Limit: 10, After: &empty}).Normalize(); err == nil {
		t.Fatal("Normalize() accepted an empty cursor instead of refusing it")
	}
}

// requireQueryRefusal asserts the refusal names the given fields and nothing of the
// storage layer leaks into it.
func requireQueryRefusal(t *testing.T, err error, fields ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the query was accepted, want a refusal naming %v", fields)
	}
	for _, field := range fields {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("the refusal %q does not name the field %q", err, field)
		}
	}
	for _, leaked := range []string{"pgx", "SQL", "pq:"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("the refusal %q leaks %q", err, leaked)
		}
	}
}
