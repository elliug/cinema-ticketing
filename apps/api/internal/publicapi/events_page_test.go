// S11: the catalogue list is a page, and the answer says how to ask for the next one.
//
// These are the wire rules the catalogue screen (T5b) and any other client read:
//
//   - a page is bounded by default and by a documented maximum, and a limit outside
//     that range is refused by naming the field instead of being clamped, so a client
//     that asked for something the catalogue cannot answer learns it;
//   - the `events` array stays exactly where it was — the screen that already reads it
//     keeps working — and a sibling object describes the page: the limit that was
//     applied, whether there is a next page, and the cursor that reaches it (`null`
//     when there is none);
//   - `from` and `to` are optional RFC 3339 bounds on the screening start, refused by
//     naming the field when they do not parse or when `to` precedes `from`;
//   - a cursor the catalogue did not issue is refused by naming the field, never
//     answered with an empty page that would read as "no more screenings".
//
// The numbers below are the documented promise, written out here on purpose: a test
// that asked the implementation what the limits were could not notice the promise
// changing. The keyset walk itself needs a database and lives in the catalogue
// package's gated tests.
package publicapi_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/catalog"
)

const (
	// cataloguePageLimitDefault is the page size a request that names no limit gets.
	cataloguePageLimitDefault = 20
	// cataloguePageLimitMax is the largest page the catalogue answers.
	cataloguePageLimitMax = 100
)

// cataloguePageBody is the wire shape of a paged catalogue answer: the events array the
// screen already reads, and the sibling object that describes the page.
type cataloguePageBody struct {
	Events []catalogueEntryBody `json:"events"`
	Page   struct {
		Limit      int     `json:"limit"`
		HasMore    bool    `json:"hasMore"`
		NextCursor *string `json:"nextCursor"`
	} `json:"page"`
}

// cataloguePageKeys is the pinned key set of the page object.
var cataloguePageKeys = []string{"hasMore", "limit", "nextCursor"}

// S11: a request that names no limit is answered with a bounded page, and the answer
// says which limit was applied so a client can reason about what it received.
func TestCataloguePageIsBoundedByDefault(t *testing.T) {
	reader := &fakeCatalogService{screenings: fixtureCatalogue()}
	raw := readBody(t, getEvents(t, mountedCatalogServer(t, reader)))

	var body cataloguePageBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode catalogue body %s: %v", raw, err)
	}

	// The read was asked for the documented default and for no cursor: the route does not
	// read the whole catalogue and then slice it, it asks for one page.
	if reader.calls != 1 || len(reader.queries) != 1 {
		t.Fatalf("the route asked the reader %d times, want once", reader.calls)
	}
	if reader.queries[0].Limit != cataloguePageLimitDefault {
		t.Fatalf("the reader was asked for a limit of %d, want the documented default %d",
			reader.queries[0].Limit, cataloguePageLimitDefault)
	}
	if reader.queries[0].After != nil {
		t.Fatalf("the first page was asked for from the position %+v, want no cursor", reader.queries[0].After)
	}

	// The events array is still there, at the root, with the entries it already had.
	entries := arrayField(t, raw, "events")
	if len(entries) != len(fixtureCatalogue()) {
		t.Fatalf("events array = %s, want the %d entries the reader answered", entries, len(fixtureCatalogue()))
	}
	// Its sibling describes the page, and says which limit was applied: the documented
	// default, not "everything".
	if body.Page.Limit != cataloguePageLimitDefault {
		t.Fatalf("page.limit = %d, want the documented default %d", body.Page.Limit, cataloguePageLimitDefault)
	}
	// Two screenings fit in one page, so there is no next one, and the key is present
	// and null rather than missing: a client can tell "no more" from "not answered".
	if body.Page.HasMore {
		t.Fatal("page.hasMore = true for a page shorter than the limit")
	}
	if body.Page.NextCursor != nil {
		t.Fatalf("page.nextCursor = %q, want null when there is no next page", *body.Page.NextCursor)
	}

	// The key sets are the contract: one added, renamed or dropped key is a change a
	// client would notice.
	if got := objectKeys(t, raw); !equalKeys(got, []string{"events", "page"}) {
		t.Fatalf("catalogue keys = %v, want events beside page", got)
	}
	if got := objectKeys(t, objectField(t, raw, "page")); !equalKeys(got, cataloguePageKeys) {
		t.Fatalf("page keys = %v, want %v", got, cataloguePageKeys)
	}
}

// S11: the maximum is enforced — not clamped — and the boundaries themselves are
// answered.
func TestCataloguePageRefusesALimitOutsideTheDocumentedRange(t *testing.T) {
	refused := []string{"0", "-1", "101", "1000", "not-a-number", ""}
	for _, limit := range refused {
		t.Run("limit="+limit, func(t *testing.T) {
			reader := &fakeCatalogService{screenings: fixtureCatalogue()}
			response := getPath(t, mountedCatalogServer(t, reader), "/api/v1/events?limit="+limit)
			requireInvalidInput(t, response, "limit")
			if reader.calls != 0 {
				t.Fatalf("the route asked the reader %d times for a refused limit, want 0", reader.calls)
			}
		})
	}

	accepted := []int{1, 2, cataloguePageLimitMax}
	for _, limit := range accepted {
		t.Run("limit="+strconv.Itoa(limit), func(t *testing.T) {
			reader := &fakeCatalogService{screenings: fixtureCatalogue()}
			raw := readBody(t, getPath(t, mountedCatalogServer(t, reader),
				"/api/v1/events?limit="+strconv.Itoa(limit)))
			var body cataloguePageBody
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode catalogue body %s: %v", raw, err)
			}
			// The applied limit is echoed verbatim, so a client that asked for 1 or for the
			// maximum knows what it got.
			if body.Page.Limit != limit {
				t.Fatalf("page.limit = %d, want the requested %d", body.Page.Limit, limit)
			}
		})
	}
}

// S11: a cursor the catalogue did not issue — malformed, or issued by a scheme this
// build does not read — is refused by naming the field. An empty page would read as
// "no more screenings", which is the one wrong answer a walker cannot detect.
func TestCataloguePageRefusesACursorItDidNotIssue(t *testing.T) {
	cases := map[string]string{
		"garbage":            "not-a-cursor",
		"base64 of nothing":  base64.RawURLEncoding.EncodeToString([]byte("")),
		"not a known format": base64.RawURLEncoding.EncodeToString([]byte("foo")),
		// A cursor from a previous encoding: the version prefix is what makes it stale
		// rather than malformed, and both are refused.
		"a previous scheme": base64.RawURLEncoding.EncodeToString(
			[]byte("v2:2026-05-04T18:30:00Z:00000000-0000-0000-0000-00000000f1a1")),
		"no usable instant": base64.RawURLEncoding.EncodeToString(
			[]byte("v1:yesterday:00000000-0000-0000-0000-00000000f1a1")),
		"no screening": base64.RawURLEncoding.EncodeToString(
			[]byte("v1:2026-05-04T18:30:00Z:not-a-screening")),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			reader := &fakeCatalogService{screenings: fixtureCatalogue()}
			response := getPath(t, mountedCatalogServer(t, reader), "/api/v1/events?cursor="+cursor)
			requireInvalidInput(t, response, "cursor")
			if reader.calls != 0 {
				t.Fatalf("the route asked the reader %d times for a refused cursor, want 0", reader.calls)
			}
		})
	}

	// A cursor parameter that is present and empty is the same refusal, not the first
	// page: the field was named, so it has to be usable.
	response := getPath(t, mountedCatalogServer(t, &fakeCatalogService{}), "/api/v1/events?cursor=")
	requireInvalidInput(t, response, "cursor")
}

// S11: the two filters are optional RFC 3339 bounds on the screening start, and each is
// refused by its own name — `to` before `from` names both, because the pair is the
// problem.
func TestCataloguePageRefusesBoundsItCannotUse(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		fields []string
	}{
		{"a bound that is not an instant", "?from=2026-05-04", []string{"from"}},
		{"a bound that does not exist", "?from=2026-13-04T18:30:00Z", []string{"from"}},
		{"an unparseable to", "?to=tomorrow", []string{"to"}},
		{"an empty to", "?to=", []string{"to"}},
		{"to before from", "?from=2026-05-05T18:30:00Z&to=2026-05-04T18:30:00Z", []string{"to", "from"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reader := &fakeCatalogService{screenings: fixtureCatalogue()}
			response := getPath(t, mountedCatalogServer(t, reader), "/api/v1/events"+testCase.query)
			requireInvalidInput(t, response, testCase.fields...)
			if reader.calls != 0 {
				t.Fatalf("the route asked the reader %d times for a refused query, want 0", reader.calls)
			}
		})
	}
}

// S11: a page shorter than the limit says there is no next page, and a page with a
// cursor says how to ask for the next one. Both are answered from the reader's own
// answer: the transport neither counts screenings nor guesses where a page ends.
func TestCataloguePageSaysWhetherThereIsANextPage(t *testing.T) {
	short := &fakeCatalogService{screenings: fixtureCatalogue()}
	raw := readBody(t, getPath(t, mountedCatalogServer(t, short), "/api/v1/events?limit=5"))
	var body cataloguePageBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode catalogue body %s: %v", raw, err)
	}
	if body.Page.HasMore {
		t.Fatal("page.hasMore = true for a first page with fewer results than the limit")
	}
	if body.Page.NextCursor != nil {
		t.Fatalf("page.nextCursor = %q for the last page, want null", *body.Page.NextCursor)
	}
	if body.Page.Limit != 5 {
		t.Fatalf("page.limit = %d, want the applied 5", body.Page.Limit)
	}
}

// S11: the filters are accepted when they are usable, and the page they produce is a
// 200 with the array in the same place: a filter is a narrower page, not a different
// answer shape.
func TestCataloguePageAcceptsUsableBounds(t *testing.T) {
	reader := &fakeCatalogService{screenings: fixtureCatalogue()}
	raw := readBody(t, getPath(t, mountedCatalogServer(t, reader),
		"/api/v1/events?from=2026-05-04T00:00:00Z&to=2026-05-05T00:00:00Z&limit=3"))
	var body cataloguePageBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode catalogue body %s: %v", raw, err)
	}
	if got := len(arrayField(t, raw, "events")); got != len(fixtureCatalogue()) {
		t.Fatalf("events array = %d entries, want the reader's %d", got, len(fixtureCatalogue()))
	}
	if body.Page.Limit != 3 {
		t.Fatalf("page.limit = %d, want the applied 3", body.Page.Limit)
	}

	// The reader received exactly the instants the caller named, in the one spelling the
	// keyset compares: parsed by the transport, normalized by the read model, never
	// re-derived from a string further down.
	if len(reader.queries) != 1 {
		t.Fatalf("the route asked the reader %d times, want once", len(reader.queries))
	}
	asked := reader.queries[0]
	wantFrom := time.Date(2026, time.May, 4, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, time.May, 5, 0, 0, 0, 0, time.UTC)
	if asked.From == nil || !asked.From.Equal(wantFrom) {
		t.Fatalf("the reader was asked from %v, want %v", asked.From, wantFrom)
	}
	if asked.To == nil || !asked.To.Equal(wantTo) {
		t.Fatalf("the reader was asked to %v, want %v", asked.To, wantTo)
	}
	if asked.From.Location() != time.UTC || asked.To.Location() != time.UTC {
		t.Fatalf("the reader was asked for the bounds in %v/%v, want both in UTC",
			asked.From.Location(), asked.To.Location())
	}
}

// S11: the transport answers the cursor the read handed it, and hands a cursor back to the
// read unchanged. The round trip is the whole point: a cursor that is not the one the read
// answered would start the next page somewhere the first page did not end.
func TestCataloguePagePassesTheCursorThrough(t *testing.T) {
	position := catalog.Cursor{
		StartsAt: time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC),
		ID:       "00000000-0000-0000-0000-00000000f1a2",
	}
	reader := &fakeCatalogService{screenings: fixtureCatalogue(), next: &position}
	raw := readBody(t, getEvents(t, mountedCatalogServer(t, reader)))

	var body cataloguePageBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode catalogue body %s: %v", raw, err)
	}
	if !body.Page.HasMore {
		t.Fatal("page.hasMore = false although the reader answered a next position")
	}
	if body.Page.NextCursor == nil {
		t.Fatal("page.nextCursor = null although the reader answered a next position")
	}
	if *body.Page.NextCursor != position.String() {
		t.Fatalf("page.nextCursor = %q, want the reader's own position %q",
			*body.Page.NextCursor, position.String())
	}
	// The cursor a client receives is the cursor the catalogue accepts: handing it back
	// asks for the page that follows, and the read is told exactly where to continue.
	decoded, err := catalog.ParseCursor(*body.Page.NextCursor)
	if err != nil {
		t.Fatalf("the answered cursor does not parse: %v", err)
	}
	if decoded.ID != position.ID || !decoded.StartsAt.Equal(position.StartsAt) {
		t.Fatalf("the answered cursor decodes to %+v, want %+v", decoded, position)
	}

	second := &fakeCatalogService{screenings: fixtureCatalogue()}
	if response := getPath(t, mountedCatalogServer(t, second), "/api/v1/events?cursor="+*body.Page.NextCursor); response.StatusCode != http.StatusOK {
		body := readBody(t, response)
		t.Fatalf("walking to the second page status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if len(second.queries) != 1 || second.queries[0].After == nil {
		t.Fatalf("the second page was read without a position: %+v", second.queries)
	}
	if second.queries[0].After.ID != position.ID || !second.queries[0].After.StartsAt.Equal(position.StartsAt) {
		t.Fatalf("the second page was read from %+v, want %+v", second.queries[0].After, position)
	}
}

// requireInvalidInput asserts the shared 400 problem document, with a detail naming each
// of the given fields, and that nothing of a catalogue leaked into the refusal.
func requireInvalidInput(t *testing.T, response *http.Response, fields ...string) {
	t.Helper()
	if response.StatusCode != http.StatusBadRequest {
		body := readBody(t, response)
		t.Fatalf("status = %d, want 400 (body %s)", response.StatusCode, body)
	}
	raw := readBody(t, response)
	var problem problemBody
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatalf("decode problem %s: %v", raw, err)
	}
	if problem.Code != "invalid_input" || problem.Status != http.StatusBadRequest {
		t.Fatalf("problem = %+v, want a 400 invalid_input document", problem)
	}
	for _, field := range fields {
		if !strings.Contains(problem.Detail, field) {
			t.Fatalf("detail %q does not name the field %q", problem.Detail, field)
		}
	}
	for _, leaked := range []string{"events", "filmTitle", "availableSeats"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("the refusal carries %q: %s", leaked, raw)
		}
	}
}
