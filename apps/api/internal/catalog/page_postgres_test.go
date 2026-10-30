// PostgreSQL-only tests for the paged catalogue read: the page bound, the keyset walk and
// the two filters. They need a migrated isolated database and are gated on
// M3_CHECKOUT_DATABASE_URL exactly the way the other read-model suites are, so the default
// unit suite needs no database at all.
//
// What only PostgreSQL can prove is here, because all of it is a property of the statement
// and not of the Go code around it:
//
//   - a request that names no more than the documented page is answered with that many
//     screenings and a next position, so `GET /events` no longer answers the whole
//     programme;
//   - walking the pages with the cursor sees every screening exactly once, in screening
//     order, and the last page says there is no next one;
//   - a screening inserted **while a client is walking** neither repeats an entry nor skips
//     one. This is the property that chose a keyset over an offset, so it is the one that
//     has to be measured: an offset is a count of rows before the page, so a row inserted
//     earlier in the catalogue pushes the client's next window down and repeats an entry it
//     already saw, and a row removed pulls it up and steps over one it never saw;
//   - `from` and `to` bound the page on the screening start, inclusive at both ends.
//
// The database is shared with the other read-model suites, which have on-sale screenings of
// their own. Every query here is therefore bounded to the fixture's own window of days: the
// catalogue is a global list, and a test that read it globally would be asserting something
// about its neighbours.
package catalog_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/catalog"
)

// The paging fixture's own venue and film, far from every other suite's identifiers.
const (
	pagedCinema = "00000000-0000-0000-0000-00000000f4a1"
	pagedRoom   = "00000000-0000-0000-0000-00000000f4a2"
	pagedLayout = "00000000-0000-0000-0000-00000000f4a3"
	pagedRow    = "00000000-0000-0000-0000-00000000f4a4"
	pagedSeat   = "00000000-0000-0000-0000-00000000f4a5"
	pagedFilm   = "00000000-0000-0000-0000-00000000f4b1"

	// pagedEventCount is more than the documented default page, so "the default page is
	// bounded" is a fact this fixture can show instead of a number the test asserts about
	// itself.
	pagedEventCount = 22

	// The two screenings this test inserts in the middle of a walk: one that sorts before
	// the position the walk has reached, and one that sorts after it.
	pagedInsertedEarlier = "00000000-0000-0000-0000-00000000f4c1"
	pagedInsertedLater   = "00000000-0000-0000-0000-00000000f4c2"
)

// pagedStartsAt is the first screening's instant, in the future for the whole life of this
// milestone, so every screening here is on sale when the test runs. The screenings run
// forward one hour apiece.
var pagedStartsAt = time.Date(2037, time.February, 1, 12, 0, 0, 0, time.UTC)

// pagedWindow is the span every query in this file is bounded by: the fixture's own days,
// with room around them for the two screenings the walk inserts. Nothing of another suite
// is on sale inside it.
func pagedWindow() (time.Time, time.Time) {
	return pagedStartsAt.Add(-2 * time.Hour), pagedStartsAt.Add(48 * time.Hour)
}

// pagedEventID is the identifier of the screening in one position of the fixture. The
// position is written as three decimal digits in the identifier's last hex group, which is
// the same ordering as the times, so a page sorted by the wrong one is visible.
func pagedEventID(position int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-000000000%03d", position)
}

// pagedStartsAtIn is the instant of the screening in one position of the fixture.
func pagedStartsAtIn(position int) time.Time {
	return pagedStartsAt.Add(time.Duration(position) * time.Hour)
}

// pagedQuery is a page request bounded to the fixture's own window, so a page in this test
// only ever answers this fixture's screenings.
func pagedQuery(limit int) catalog.ListQuery {
	from, to := pagedWindow()
	return catalog.ListQuery{Limit: limit, From: &from, To: &to}
}

// TestCataloguePagesWithAKeysetCursor is the whole paging contract against a real database.
func TestCataloguePagesWithAKeysetCursor(t *testing.T) {
	ctx := context.Background()
	pool, url := cataloguePool(t, ctx)
	seedPagedCatalogue(t, ctx, pool)
	t.Cleanup(func() {
		cleanupPagedCatalogue(pool)
		pool.Close()
	})

	store, err := catalog.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the catalogue store: %v", err)
	}
	defer store.Close()

	t.Run("a page is bounded by the documented default and says there is more", func(t *testing.T) {
		page, err := store.Events(ctx, pagedQuery(catalog.DefaultEventLimit))
		if err != nil {
			t.Fatalf("Events() = unexpected error %v", err)
		}
		if len(page.Screenings) != catalog.DefaultEventLimit {
			t.Fatalf("the default page answered %d screenings, want the documented %d of %d",
				len(page.Screenings), catalog.DefaultEventLimit, pagedEventCount)
		}
		// The page is the beginning of the catalogue, in screening order, not an arbitrary
		// subset of it.
		for position, screening := range page.Screenings {
			if screening.ID != pagedEventID(position) {
				t.Fatalf("entry %d = %s, want %s: the page is the catalogue's own order",
					position, screening.ID, pagedEventID(position))
			}
		}
		if page.Next == nil {
			t.Fatal("the first page says there is no next page, but the fixture has more screenings")
		}
		// The next page starts after the last screening this one answered, so the two pages
		// share no entry.
		last := page.Screenings[len(page.Screenings)-1]
		if page.Next.ID != last.ID || !page.Next.StartsAt.Equal(last.StartsAt) {
			t.Fatalf("the next page starts after %+v, want the last entry of this page (%s at %v)",
				page.Next, last.ID, last.StartsAt)
		}
	})

	t.Run("the pages walk the catalogue once, in order, and end", func(t *testing.T) {
		wantOrder := make([]string, 0, pagedEventCount)
		for position := 0; position < pagedEventCount; position++ {
			wantOrder = append(wantOrder, pagedEventID(position))
		}
		// Several page sizes, because a page boundary in the middle of the list is where a
		// walk either repeats an entry or drops one.
		for _, limit := range []int{1, 5, catalog.DefaultEventLimit, catalog.MaxEventLimit} {
			seen := walkPagedCatalogue(t, ctx, store, pagedQuery(limit))
			requireEachExactlyOnce(t, fmt.Sprintf("limit %d", limit), seen, wantOrder)
		}
	})

	t.Run("a screening inserted mid-walk neither repeats one nor skips one", func(t *testing.T) {
		// Page one of a walk, then the list changes under the reader: one screening is
		// inserted before the position the walk has reached and one after it.
		first, err := store.Events(ctx, pagedQuery(5))
		if err != nil {
			t.Fatalf("Events() of the first page = unexpected error %v", err)
		}
		if len(first.Screenings) != 5 || first.Next == nil {
			t.Fatalf("the first page answered %d screenings with next %+v, want 5 and a position",
				len(first.Screenings), first.Next)
		}
		insertPagedScreening(t, ctx, pool, pagedInsertedEarlier, pagedStartsAt.Add(-time.Hour))
		insertPagedScreening(t, ctx, pool, pagedInsertedLater, pagedStartsAtIn(4).Add(30*time.Minute))
		// These two are this case's own rows, and the cases after it walk the catalogue the
		// fixture seeded; remove them when this case ends.
		t.Cleanup(func() { deletePagedScreenings(pool, pagedInsertedEarlier, pagedInsertedLater) })

		seen := make([]string, 0, pagedEventCount+1)
		for _, screening := range first.Screenings {
			seen = append(seen, screening.ID)
		}
		seen = append(seen, walkPagedCatalogue(t, ctx, store, pagedQueryAfter(5, first.Next))...)

		// Every screening the catalogue had before the insert, exactly once: the insert
		// behind the cursor neither repeats the entry already read nor steps over the next
		// one, which is exactly what an offset would have done.
		for position := 0; position < pagedEventCount; position++ {
			id := pagedEventID(position)
			if count := countOf(seen, id); count != 1 {
				t.Fatalf("screening %s was answered %d times across the walk, want exactly once (walk %v)",
					id, count, seen)
			}
		}
		// The screening inserted ahead of the cursor is a screening the catalogue now sells,
		// and the walk reaches it exactly once: a page continues from a position, so an entry
		// ahead of it has not been seen yet.
		if count := countOf(seen, pagedInsertedLater); count != 1 {
			t.Fatalf("the screening inserted ahead of the cursor was answered %d times, want once (walk %v)",
				count, seen)
		}
		// The screening inserted behind the cursor is not answered: a walk reports the
		// catalogue as it changes, and it never goes back.
		if count := countOf(seen, pagedInsertedEarlier); count != 0 {
			t.Fatalf("the screening inserted behind the cursor was answered %d times, want never (walk %v)",
				count, seen)
		}
	})

	t.Run("the bounds bound the page on the screening start, both ends inclusive", func(t *testing.T) {
		_, windowTo := pagedWindow()
		from := pagedStartsAtIn(2)
		to := pagedStartsAtIn(4)

		// A window of three screenings: both bounds are inclusive, so the screening that
		// starts exactly at `to` is in the page.
		page, err := store.Events(ctx, catalog.ListQuery{Limit: catalog.MaxEventLimit, From: &from, To: &to})
		if err != nil {
			t.Fatalf("Events() with bounds = unexpected error %v", err)
		}
		want := []string{pagedEventID(2), pagedEventID(3), pagedEventID(4)}
		got := make([]string, 0, len(page.Screenings))
		for _, screening := range page.Screenings {
			got = append(got, screening.ID)
		}
		requireEachExactlyOnce(t, "the bounded window", got, want)
		// Everything fits in one page, so there is no next one: a filter narrows the
		// catalogue, it does not page it.
		if page.Next != nil {
			t.Fatalf("the bounded page answered a next position %+v, want none", page.Next)
		}

		// Each bound alone still bounds: `from` is a floor and `to` is a ceiling.
		afterFloor := walkPagedCatalogue(t, ctx, store,
			catalog.ListQuery{Limit: catalog.MaxEventLimit, From: &from, To: &windowTo})
		if len(afterFloor) != pagedEventCount-2 {
			t.Fatalf("the page from screening 2 answered %d entries, want %d",
				len(afterFloor), pagedEventCount-2)
		}
		windowFrom, _ := pagedWindow()
		upToCeiling := walkPagedCatalogue(t, ctx, store,
			catalog.ListQuery{Limit: catalog.MaxEventLimit, From: &windowFrom, To: &to})
		if len(upToCeiling) != 5 {
			t.Fatalf("the page up to screening 4 answered %d entries, want 5", len(upToCeiling))
		}

		// A window the catalogue has nothing in answers an empty list and no next page: the
		// end of the catalogue, told apart from a refusal by being an answered page.
		past := pagedStartsAt.Add(90 * 24 * time.Hour)
		pastEnd := past.Add(24 * time.Hour)
		empty, err := store.Events(ctx, catalog.ListQuery{Limit: 5, From: &past, To: &pastEnd})
		if err != nil {
			t.Fatalf("Events() of an empty window = unexpected error %v", err)
		}
		if empty.Screenings == nil || len(empty.Screenings) != 0 {
			t.Fatalf("an empty window answered %+v, want an empty list", empty.Screenings)
		}
		if empty.Next != nil {
			t.Fatalf("an empty window answered a next position %+v, want none", empty.Next)
		}
	})

	t.Run("a request the catalogue cannot mean is refused instead of answered", func(t *testing.T) {
		for _, limit := range []int{0, -1, catalog.MaxEventLimit + 1} {
			if _, err := store.Events(ctx, pagedQuery(limit)); err == nil {
				t.Fatalf("Events() with limit %d answered a page, want a refusal", limit)
			}
		}
	})
}

// pagedQueryAfter is the fixture-bounded request that continues from a position.
func pagedQueryAfter(limit int, after *catalog.Cursor) catalog.ListQuery {
	query := pagedQuery(limit)
	query.After = after
	return query
}

// walkPagedCatalogue follows the cursor to the end of the catalogue and answers every
// screening id it was given, in the order the pages answered them. It fails if the walk
// does not end or if a page repeats an entry the walk already has.
func walkPagedCatalogue(t *testing.T, ctx context.Context, store *catalog.PostgresStore,
	query catalog.ListQuery) []string {
	t.Helper()
	var seen []string
	// The fixture is small; a bound on the number of pages keeps a cursor that never ends
	// from hanging the suite.
	for page := 0; page < pagedEventCount+5; page++ {
		answered, err := store.Events(ctx, query)
		if err != nil {
			t.Fatalf("Events() of page %d = unexpected error %v", page, err)
		}
		if len(answered.Screenings) > query.Limit {
			t.Fatalf("page %d answered %d screenings, over the limit %d",
				page, len(answered.Screenings), query.Limit)
		}
		for _, screening := range answered.Screenings {
			if countOf(seen, screening.ID) != 0 {
				t.Fatalf("page %d repeated the screening %s (walk %v)", page, screening.ID, seen)
			}
			seen = append(seen, screening.ID)
		}
		if answered.Next == nil {
			return seen
		}
		query.After = answered.Next
	}
	t.Fatalf("the walk did not end within %d pages (walk %v)", pagedEventCount+5, seen)
	return nil
}

// requireEachExactlyOnce asserts the walk answered exactly the expected screenings, each
// once, in the expected order.
func requireEachExactlyOnce(t *testing.T, what string, seen, want []string) {
	t.Helper()
	if len(seen) != len(want) {
		t.Fatalf("%s answered %d screenings, want %d (walk %v)", what, len(seen), len(want), seen)
	}
	for position := range want {
		if seen[position] != want[position] {
			t.Fatalf("%s answered %s in position %d, want %s (walk %v)",
				what, seen[position], position, want[position], seen)
		}
		if count := countOf(seen, want[position]); count != 1 {
			t.Fatalf("%s answered the screening %s %d times, want once (walk %v)",
				what, want[position], count, seen)
		}
	}
}

// countOf counts an identifier in a walk.
func countOf(seen []string, id string) int {
	count := 0
	for _, got := range seen {
		if got == id {
			count++
		}
	}
	return count
}

// seedPagedCatalogue creates the venue once and then the screenings, each reset on every
// run so a run a previous case changed cannot decide what this one asserts.
func seedPagedCatalogue(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	cleanupPagedCatalogue(pool)

	var layoutExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, pagedLayout).
		Scan(&layoutExists); err != nil {
		t.Fatalf("inspect the paged venue fixture: %v", err)
	}
	if !layoutExists {
		// The layout is published before the event exists, because M1 materializes event
		// seats from a published layout and refuses a draft one, and a published layout is
		// immutable afterwards — which is why this venue is reused across runs.
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + pagedCinema + `','Cine Paged','Europe/Madrid')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + pagedRoom + `','` + pagedCinema + `','Sala Paged')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + pagedLayout + `','` + pagedRoom + `',1)`},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('` + pagedRow + `','` + pagedLayout + `','A',1)`},
			{"seat", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES ('` + pagedSeat + `','` + pagedRow + `','1',1,'standard',true)`},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = '` + pagedLayout + `'`},
		} {
			if _, err := pool.Exec(ctx, statement.sql); err != nil {
				t.Fatalf("seed %s: %v", statement.what, err)
			}
		}
	}

	var filmExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.films WHERE id = $1)`, pagedFilm).
		Scan(&filmExists); err != nil {
		t.Fatalf("inspect the paged film fixture: %v", err)
	}
	if !filmExists {
		if _, err := pool.Exec(ctx,
			`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1,'Paged Fixture',90,'PG')`,
			pagedFilm); err != nil {
			t.Fatalf("seed film: %v", err)
		}
	}

	for position := 0; position < pagedEventCount; position++ {
		insertPagedScreening(t, ctx, pool, pagedEventID(position), pagedStartsAtIn(position))
	}
}

// insertPagedScreening stores one screening of the paged venue at the given instant, which
// is what "the catalogue changed while a client was walking it" means.
func insertPagedScreening(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, startsAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status, price_cents)
VALUES ($1, $2, $3, $4, $5, 'on_sale', 900)
ON CONFLICT (id) DO UPDATE SET starts_at = EXCLUDED.starts_at, status = EXCLUDED.status`,
		id, pagedRoom, pagedFilm, pagedLayout, startsAt); err != nil {
		t.Fatalf("insert the screening %s at %v: %v", id, startsAt, err)
	}
}

// deletePagedScreenings removes screenings the fixture created, so one case's change to the
// catalogue cannot decide what the next one asserts. The venue stays: M1 makes a published
// layout immutable, so it can never be deleted and re-inserted.
func deletePagedScreenings(pool *pgxpool.Pool, ids ...string) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.event_seats WHERE event_id::text = ANY($1)`, ids)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.events WHERE id::text = ANY($1)`, ids)
}

// cleanupPagedCatalogue removes the rows the paging fixture creates.
func cleanupPagedCatalogue(pool *pgxpool.Pool) {
	ids := make([]string, 0, pagedEventCount+2)
	for position := 0; position < pagedEventCount; position++ {
		ids = append(ids, pagedEventID(position))
	}
	deletePagedScreenings(pool, append(ids, pagedInsertedEarlier, pagedInsertedLater)...)
}
