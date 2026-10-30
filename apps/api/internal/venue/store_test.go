// PostgreSQL-only tests for the administrative screening and film reads and the price and
// status write. They need a migrated isolated database and are gated on
// M3_CHECKOUT_DATABASE_URL, so the default unit suite needs no database at all.
//
// What only PostgreSQL can prove is here: that the update write is one statement whose
// guard is in WHERE (an unknown event is not found instead of an invented success), that
// the stored event comes back as the database holds it, that the columns refuse a negative
// price and an unknown status even when the application validation is bypassed, that
// changing the price leaves the event's room, film, layout, start and status untouched (and
// the other way round), that a draft becomes on_sale, that both administrative screening
// reads name the film, the room and the cinema in the same statement, and that the film
// list answers the stored films in title order.
package venue_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// venueDatabaseURLEnv names the isolated database these tests use: the same throwaway
// M3 database the other gated suites run against, because the price column it writes
// arrived with 0005_checkout. The default unit suite skips them; the gated run points
// it at a fresh database migrated through 0006.
const venueDatabaseURLEnv = "M3_CHECKOUT_DATABASE_URL"

// Fixture identifiers are fixed so a failed run can be cleaned up by the next one. The
// venue is reused across runs because M1 makes a published layout immutable and
// requires it before an event exists.
const (
	fixtureCinema = "00000000-0000-0000-0000-0000000008a1"
	fixtureRoom   = "00000000-0000-0000-0000-0000000008a2"
	fixtureLayout = "00000000-0000-0000-0000-0000000008a3"
	fixtureRow    = "00000000-0000-0000-0000-0000000008a4"
	fixtureSeat   = "00000000-0000-0000-0000-0000000008a5"
	fixtureFilm   = "00000000-0000-0000-0000-0000000008b1"
	// fixtureFilmAlt is the second film: its title sorts before the first one's, so a list
	// that answered insertion order or identifier order would be wrong here.
	fixtureFilmAlt = "00000000-0000-0000-0000-0000000008b2"
	fixtureEarly   = "00000000-0000-0000-0000-0000000008c1"
	fixtureLate    = "00000000-0000-0000-0000-0000000008c2"
	fixtureAbsent  = "00000000-0000-0000-0000-0000000008cf"
	// fixtureCreated and fixtureCreatedDefault are the screenings the one-request create stores:
	// one that names a price and a status, one that names neither.
	fixtureCreated        = "00000000-0000-0000-0000-0000000008c3"
	fixtureCreatedDefault = "00000000-0000-0000-0000-0000000008c4"
)

var (
	fixtureEarlyAt = time.Date(2040, time.March, 1, 18, 0, 0, 0, time.UTC)
	fixtureLateAt  = time.Date(2040, time.March, 3, 21, 0, 0, 0, time.UTC)
)

// TestEventPriceWriteAndAdministrativeReads walks the whole surface the administration
// needs: the screening reads carry the stored price, the write sets it, the column refuses
// a negative value, an unknown event is not found, and the other columns are untouched.
func TestEventPriceWriteAndAdministrativeReads(t *testing.T) {
	ctx := context.Background()
	pool, url := venueTestPool(t, ctx)

	seedEventPriceFixture(t, ctx, pool)
	t.Cleanup(func() { cleanupEventPriceFixture(pool) })

	store, err := venue.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the venue store: %v", err)
	}
	defer store.Close()

	t.Run("the reads answer the stored price and the event's own columns", func(t *testing.T) {
		events, err := store.ListEvents(ctx)
		if err != nil {
			t.Fatalf("ListEvents() = unexpected error %v", err)
		}
		mine := make([]venue.Event, 0, 2)
		for _, event := range events {
			if event.ID == fixtureEarly || event.ID == fixtureLate {
				mine = append(mine, event)
			}
		}
		if len(mine) != 2 || mine[0].ID != fixtureEarly || mine[1].ID != fixtureLate {
			t.Fatalf("the administrative list answered %v, want the fixture's two screenings in start order", mine)
		}
		if mine[0].StartsAt.Before(mine[1].StartsAt) == false {
			t.Fatalf("the list is not ordered by start: %v then %v", mine[0].StartsAt, mine[1].StartsAt)
		}

		early, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() = unexpected error %v", err)
		}
		if early.ID != fixtureEarly || early.RoomID != fixtureRoom || early.FilmID != fixtureFilm ||
			early.LayoutVersionID != fixtureLayout || !early.StartsAt.Equal(fixtureEarlyAt) ||
			early.Status != "draft" || early.PriceCents != 0 {
			t.Fatalf("event = %+v, want the stored screening with price zero", early)
		}
	})

	t.Run("setting a price returns the event as stored", func(t *testing.T) {
		before, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() before the write = unexpected error %v", err)
		}
		stored, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{PriceCents: ptrInt64(1250)})
		if err != nil {
			t.Fatalf("UpdateEvent(price only) = unexpected error %v", err)
		}
		if stored.PriceCents != 1250 {
			t.Fatalf("returned price = %d, want 1250", stored.PriceCents)
		}
		// The write returned the stored row, not a value assembled from its arguments.
		if stored.ID != before.ID || stored.RoomID != before.RoomID || stored.FilmID != before.FilmID ||
			stored.LayoutVersionID != before.LayoutVersionID || !stored.StartsAt.Equal(before.StartsAt) ||
			stored.Status != before.Status {
			t.Fatalf("the write changed more than the price: before %+v, after %+v", before, stored)
		}
		read, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() after the write = unexpected error %v", err)
		}
		if read.PriceCents != 1250 {
			t.Fatalf("stored price = %d, want 1250", read.PriceCents)
		}
		if read.RoomID != fixtureRoom || read.FilmID != fixtureFilm || read.LayoutVersionID != fixtureLayout ||
			!read.StartsAt.Equal(fixtureEarlyAt) || read.Status != "draft" {
			t.Fatalf("the update touched another column: %+v", read)
		}
	})

	t.Run("an explicit zero is stored rather than refused", func(t *testing.T) {
		stored, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{PriceCents: ptrInt64(0)})
		if err != nil {
			t.Fatalf("UpdateEvent(explicit zero) = unexpected error %v", err)
		}
		if stored.PriceCents != 0 {
			t.Fatalf("returned price = %d, want zero", stored.PriceCents)
		}
		read, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() = unexpected error %v", err)
		}
		if read.PriceCents != 0 {
			t.Fatalf("stored price = %d, want zero", read.PriceCents)
		}
	})

	t.Run("the column refuses a negative price and changes nothing", func(t *testing.T) {
		if _, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{PriceCents: ptrInt64(-1)}); !errors.Is(err, venue.ErrConflict) {
			t.Fatalf("UpdateEvent(-1) = %v, want the check violation as ErrConflict", err)
		}
		read, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() after the refused write = unexpected error %v", err)
		}
		if read.PriceCents != 0 {
			t.Fatalf("the refused write left price %d, want the previous zero", read.PriceCents)
		}
	})

	t.Run("the int4 maximum is accepted", func(t *testing.T) {
		if _, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{PriceCents: ptrInt64(venue.MaxPriceCents)}); err != nil {
			t.Fatalf("UpdateEvent(%d) = unexpected error %v", venue.MaxPriceCents, err)
		}
		read, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() = unexpected error %v", err)
		}
		if read.PriceCents != venue.MaxPriceCents {
			t.Fatalf("stored price = %d, want %d", read.PriceCents, venue.MaxPriceCents)
		}
	})

	t.Run("an unknown event is not found instead of a claimed success", func(t *testing.T) {
		if _, err := store.GetEvent(ctx, fixtureAbsent); !errors.Is(err, venue.ErrNotFound) {
			t.Fatalf("GetEvent(unknown) = %v, want ErrNotFound", err)
		}
		if _, err := store.UpdateEvent(ctx, fixtureAbsent, venue.EventUpdate{PriceCents: ptrInt64(500)}); !errors.Is(err, venue.ErrNotFound) {
			t.Fatalf("UpdateEvent(unknown) = %v, want ErrNotFound", err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.events WHERE id = $1`, fixtureAbsent).Scan(&count); err != nil {
			t.Fatalf("count the unknown event: %v", err)
		}
		if count != 0 {
			t.Fatalf("the guarded write inserted %d rows for an unknown event", count)
		}
	})
}

// TestEventUpdateWritesTheStatusAndThePriceInOneGuardedStatement walks what the
// administration needs to put a screening on sale: a draft that becomes on_sale, a price
// and a status changed by one request, and the column the request did not name left as it
// was. The identifier stays the guard, so an unknown screening writes nothing.
func TestEventUpdateWritesTheStatusAndThePriceInOneGuardedStatement(t *testing.T) {
	ctx := context.Background()
	pool, url := venueTestPool(t, ctx)

	seedEventPriceFixture(t, ctx, pool)
	t.Cleanup(func() { cleanupEventPriceFixture(pool) })

	store, err := venue.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the venue store: %v", err)
	}
	defer store.Close()

	// Every case starts from a known stored screening, so no case can pass only because an
	// earlier one left the columns where it happened to need them.
	resetFixture := func(t *testing.T, priceCents int64, status string) {
		t.Helper()
		if _, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{
			PriceCents: ptrInt64(priceCents), Status: ptrString(status),
		}); err != nil {
			t.Fatalf("reset the fixture screening to %s at %d: %v", status, priceCents, err)
		}
	}
	requireStored := func(t *testing.T, wantPrice int64, wantStatus string) {
		t.Helper()
		read, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() = unexpected error %v", err)
		}
		if read.Status != wantStatus || read.PriceCents != wantPrice {
			t.Fatalf("stored event = status %q price %d, want status %q price %d",
				read.Status, read.PriceCents, wantStatus, wantPrice)
		}
		// The update writes two columns and no others.
		if read.RoomID != fixtureRoom || read.FilmID != fixtureFilm ||
			read.LayoutVersionID != fixtureLayout || !read.StartsAt.Equal(fixtureEarlyAt) {
			t.Fatalf("the update touched another column: %+v", read)
		}
	}

	t.Run("a draft becomes on sale and keeps the price it had", func(t *testing.T) {
		resetFixture(t, 1250, venue.EventDraft)
		stored, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{Status: ptrString(venue.EventOnSale)})
		if err != nil {
			t.Fatalf("UpdateEvent(status only) = unexpected error %v", err)
		}
		if stored.Status != venue.EventOnSale {
			t.Fatalf("returned status = %q, want on_sale", stored.Status)
		}
		if stored.PriceCents != 1250 {
			t.Fatalf("returned price = %d, want the untouched 1250", stored.PriceCents)
		}
		requireStored(t, 1250, venue.EventOnSale)
	})

	t.Run("a price and a status are written by one request", func(t *testing.T) {
		resetFixture(t, 0, venue.EventDraft)
		stored, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{
			PriceCents: ptrInt64(1750), Status: ptrString(venue.EventOnSale),
		})
		if err != nil {
			t.Fatalf("UpdateEvent(both fields) = unexpected error %v", err)
		}
		if stored.Status != venue.EventOnSale || stored.PriceCents != 1750 {
			t.Fatalf("returned event = status %q price %d, want on_sale at 1750", stored.Status, stored.PriceCents)
		}
		// RETURNING cannot join, so the write answers the stored columns and no name.
		if stored.FilmTitle != "" || stored.RoomName != "" || stored.CinemaName != "" || stored.CinemaTimezone != "" {
			t.Fatalf("names = %q / %q / %q / %q, want the write to answer only the stored columns",
				stored.FilmTitle, stored.RoomName, stored.CinemaName, stored.CinemaTimezone)
		}
		requireStored(t, 1750, venue.EventOnSale)
	})

	t.Run("a price-only request leaves the status untouched", func(t *testing.T) {
		resetFixture(t, 900, venue.EventOnSale)
		stored, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{PriceCents: ptrInt64(1000)})
		if err != nil {
			t.Fatalf("UpdateEvent(price only) = unexpected error %v", err)
		}
		if stored.Status != venue.EventOnSale || stored.PriceCents != 1000 {
			t.Fatalf("returned event = status %q price %d, want the status kept and the price set to 1000",
				stored.Status, stored.PriceCents)
		}
		requireStored(t, 1000, venue.EventOnSale)
	})

	t.Run("an unknown event is not found instead of a claimed success", func(t *testing.T) {
		for name, update := range map[string]venue.EventUpdate{
			"status only": {Status: ptrString(venue.EventOnSale)},
			"price only":  {PriceCents: ptrInt64(500)},
			"both":        {PriceCents: ptrInt64(500), Status: ptrString(venue.EventOnSale)},
		} {
			if _, err := store.UpdateEvent(ctx, fixtureAbsent, update); !errors.Is(err, venue.ErrNotFound) {
				t.Fatalf("%s: UpdateEvent(unknown) = %v, want ErrNotFound", name, err)
			}
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cinema.events WHERE id = $1`, fixtureAbsent).Scan(&count); err != nil {
			t.Fatalf("count the unknown event: %v", err)
		}
		if count != 0 {
			t.Fatalf("the guarded write inserted %d rows for an unknown event", count)
		}
	})

	t.Run("the column refuses a status the schema does not allow", func(t *testing.T) {
		resetFixture(t, 1250, venue.EventDraft)
		if _, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{Status: ptrString("sold_out")}); !errors.Is(err, venue.ErrConflict) {
			t.Fatalf("UpdateEvent(sold_out) = %v, want the check violation as ErrConflict", err)
		}
		requireStored(t, 1250, venue.EventDraft)
	})

	t.Run("an update that names no field is refused before SQL", func(t *testing.T) {
		if _, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{}); err == nil {
			t.Fatalf("UpdateEvent(empty) = nil error, want a refusal instead of an UPDATE without a SET")
		}
	})
}

// venueTestPool opens the gated isolated database, skipping the default unit suite and
// refusing a URL that names the development database.
func venueTestPool(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(venueDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set: this test needs a migrated isolated database", venueDatabaseURLEnv)
	}
	requireIsolatedVenueDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open %s pool: %v", venueDatabaseURLEnv, err)
	}
	// Register the close first so it runs last: the fixture cleanups below need the
	// pool, and a closed pool would silently leave the fixture rows behind.
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("%s is not reachable: %v", venueDatabaseURLEnv, err)
	}
	return pool, url
}

// requireIsolatedVenueDatabase refuses the development database. The test writes real
// rows, so the name has to say it is disposable.
func requireIsolatedVenueDatabase(t *testing.T, url string) {
	t.Helper()
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if name == "cinema" {
		t.Fatalf("%s must never point at the development database", venueDatabaseURLEnv)
	}
	if !strings.HasPrefix(name, "cinema_m3") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m3, got %q", venueDatabaseURLEnv, name)
	}
}

// seedEventPriceFixture creates the venue once, then resets the two screenings on every
// run so a run that changed a price cannot decide what this one asserts. A published
// layout is immutable, so its geometry is inserted only when the layout is new.
func seedEventPriceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	cleanupEventPriceFixture(pool)

	var layoutExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, fixtureLayout).
		Scan(&layoutExists); err != nil {
		t.Fatalf("inspect the fixture layout: %v", err)
	}
	if !layoutExists {
		for _, statement := range []struct {
			what string
			sql  string
			args []any
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ($1,'Cine Admin','UTC')`, []any{fixtureCinema}},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ($1,$2,'Sala Admin')`, []any{fixtureRoom, fixtureCinema}},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ($1,$2,1)`, []any{fixtureLayout, fixtureRoom}},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ($1,$2,'A',1)`, []any{fixtureRow, fixtureLayout}},
			{"seat", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES ($1,$2,'1',1,'standard',true)`, []any{fixtureSeat, fixtureRow}},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = $1`, []any{fixtureLayout}},
		} {
			if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
				t.Fatalf("seed %s: %v", statement.what, err)
			}
		}
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES
 ($1,'Admin Fixture',90,'PG'),
 ($2,'Aardvark Fixture',77,'12')
ON CONFLICT DO NOTHING`, fixtureFilm, fixtureFilmAlt); err != nil {
		t.Fatalf("seed films: %v", err)
	}

	for _, screening := range []struct {
		id       string
		startsAt time.Time
	}{
		{fixtureEarly, fixtureEarlyAt},
		{fixtureLate, fixtureLateAt},
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status, price_cents)
VALUES ($1, $2, $3, $4, $5, 'draft', 0)
ON CONFLICT (id) DO UPDATE SET starts_at = EXCLUDED.starts_at, status = EXCLUDED.status,
                               price_cents = EXCLUDED.price_cents`,
			screening.id, fixtureRoom, fixtureFilm, fixtureLayout, screening.startsAt); err != nil {
			t.Fatalf("seed screening %s: %v", screening.id, err)
		}
	}
}

// cleanupEventPriceFixture removes the screenings and their materialized seats. The
// published layout is left in place: M1 makes it immutable, and the throwaway database
// is dropped after the run.
func cleanupEventPriceFixture(pool *pgxpool.Pool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.event_seats WHERE event_id = ANY($1::uuid[])`, fixtureEvents())
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.events WHERE id = ANY($1::uuid[])`, fixtureEvents())
}

func fixtureEvents() []string {
	return []string{fixtureEarly, fixtureLate, fixtureAbsent, fixtureCreated, fixtureCreatedDefault}
}

// TestAdministrativeReadsNameWhatAScreeningIsAndListTheFilms proves the two things a
// scheduling screen needs beside an identifier: the film, the room and the cinema every
// screening is against, and the films it may schedule one against. Both are read in one
// statement each, so a film list of n entries costs one query and never an n+1 of films.
func TestAdministrativeReadsNameWhatAScreeningIsAndListTheFilms(t *testing.T) {
	ctx := context.Background()
	pool, url := venueTestPool(t, ctx)

	seedEventPriceFixture(t, ctx, pool)
	t.Cleanup(func() { cleanupEventPriceFixture(pool) })

	store, err := venue.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the venue store: %v", err)
	}
	defer store.Close()

	t.Run("both screening reads name the film, the room and the cinema", func(t *testing.T) {
		early, err := store.GetEvent(ctx, fixtureEarly)
		if err != nil {
			t.Fatalf("GetEvent() = unexpected error %v", err)
		}
		if early.FilmTitle != "Admin Fixture" || early.RoomName != "Sala Admin" || early.CinemaName != "Cine Admin" {
			t.Fatalf("names = %q / %q / %q, want the film, the room and the cinema that were stored",
				early.FilmTitle, early.RoomName, early.CinemaName)
		}
		// The venue's zone is joined too: it is what the administrative read renders the
		// screening's own hour from, and an identifier-less hour needs no second query.
		if early.CinemaTimezone != "UTC" {
			t.Fatalf("timezone = %q, want the venue's stored UTC", early.CinemaTimezone)
		}
		// The names are additions: every stored column is still answered beside them.
		if early.ID != fixtureEarly || early.RoomID != fixtureRoom || early.FilmID != fixtureFilm ||
			early.LayoutVersionID != fixtureLayout || !early.StartsAt.Equal(fixtureEarlyAt) ||
			early.Status != "draft" {
			t.Fatalf("event = %+v, want the stored screening and its names", early)
		}

		// The list entry is the single read's answer: the two reads share one projection,
		// so a second query or a second scan would show up here as a difference.
		events, err := store.ListEvents(ctx)
		if err != nil {
			t.Fatalf("ListEvents() = unexpected error %v", err)
		}
		var listed venue.Event
		for _, event := range events {
			if event.ID == fixtureEarly {
				listed = event
			}
		}
		if listed.ID != fixtureEarly {
			t.Fatalf("the administrative list did not answer %s", fixtureEarly)
		}
		if listed.FilmTitle != early.FilmTitle || listed.RoomName != early.RoomName ||
			listed.CinemaName != early.CinemaName || listed.CinemaTimezone != early.CinemaTimezone ||
			listed.RoomID != early.RoomID ||
			listed.FilmID != early.FilmID || listed.PriceCents != early.PriceCents ||
			!listed.StartsAt.Equal(early.StartsAt) {
			t.Fatalf("the list entry %+v is not the single read %+v", listed, early)
		}
	})

	t.Run("the film list answers the stored films in title order", func(t *testing.T) {
		films, err := store.ListFilms(ctx)
		if err != nil {
			t.Fatalf("ListFilms() = unexpected error %v", err)
		}
		// The database is shared with the other gated suites, so the fixture's own films
		// are what this test decides about; another suite's film is not its business.
		order := make([]string, 0, 2)
		stored := make(map[string]venue.Film, 2)
		for _, film := range films {
			if film.ID == fixtureFilm || film.ID == fixtureFilmAlt {
				order = append(order, film.ID)
				stored[film.ID] = film
			}
		}
		if len(order) != 2 || order[0] != fixtureFilmAlt || order[1] != fixtureFilm {
			t.Fatalf("the film list answered the fixture's films in the order %v, want [%s %s] by title",
				order, fixtureFilmAlt, fixtureFilm)
		}
		if film := stored[fixtureFilm]; film.Title != "Admin Fixture" || film.RuntimeMinutes != 90 || film.Rating != "PG" {
			t.Fatalf("film = %+v, want the stored title, runtime and rating", film)
		}
		if film := stored[fixtureFilmAlt]; film.RuntimeMinutes != 77 || film.Rating != "12" {
			t.Fatalf("film = %+v, want the stored title, runtime and rating", film)
		}
	})

	t.Run("the price write still answers the stored columns without a name", func(t *testing.T) {
		stored, err := store.UpdateEvent(ctx, fixtureEarly, venue.EventUpdate{PriceCents: ptrInt64(1250)})
		if err != nil {
			t.Fatalf("UpdateEvent(price only) = unexpected error %v", err)
		}
		if stored.PriceCents != 1250 {
			t.Fatalf("returned price = %d, want 1250", stored.PriceCents)
		}
		// RETURNING cannot join, so the write answers the row and the read names it. A
		// name here would be a value nobody read.
		if stored.FilmTitle != "" || stored.RoomName != "" || stored.CinemaName != "" {
			t.Fatalf("names = %q / %q / %q, want the write to answer only the stored columns",
				stored.FilmTitle, stored.RoomName, stored.CinemaName)
		}
	})
}

// TestEventCreateStoresThePriceAndTheStatusInOneRow proves what only PostgreSQL can about
// the create the administration now sends in one request: the insert carries the price and
// the status it was given, the create that names neither still stores the draft at no price,
// and the administrative read names the venue zone the screening's own hour is rendered from.
func TestEventCreateStoresThePriceAndTheStatusInOneRow(t *testing.T) {
	ctx := context.Background()
	pool, url := venueTestPool(t, ctx)

	seedEventPriceFixture(t, ctx, pool)
	t.Cleanup(func() { cleanupEventPriceFixture(pool) })

	store, err := venue.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the venue store: %v", err)
	}
	defer store.Close()

	// The screening the create names with a price and a status: one row, one request.
	if err := store.CreateEvent(ctx, venue.Event{
		ID: fixtureCreated, RoomID: fixtureRoom, FilmID: fixtureFilm, LayoutVersionID: fixtureLayout,
		StartsAt: fixtureEarlyAt, Status: venue.EventOnSale, PriceCents: 1400,
	}); err != nil {
		t.Fatalf("CreateEvent(on sale at 1400) = unexpected error %v", err)
	}
	read, err := store.GetEvent(ctx, fixtureCreated)
	if err != nil {
		t.Fatalf("GetEvent() = unexpected error %v", err)
	}
	if read.Status != venue.EventOnSale || read.PriceCents != 1400 {
		t.Fatalf("stored event = status %q price %d, want on_sale at 1400", read.Status, read.PriceCents)
	}
	if !read.StartsAt.Equal(fixtureEarlyAt) || read.RoomID != fixtureRoom || read.FilmID != fixtureFilm ||
		read.LayoutVersionID != fixtureLayout {
		t.Fatalf("the create wrote more than the price and the status: %+v", read)
	}
	// The read names the venue's own zone, and the hour rendered from it is the venue's.
	if read.CinemaTimezone != "UTC" {
		t.Fatalf("timezone = %q, want the venue's stored UTC", read.CinemaTimezone)
	}
	local, err := read.LocalStartsAt()
	if err != nil {
		t.Fatalf("LocalStartsAt() = unexpected error %v", err)
	}
	if local != "2040-03-01T18:00:00+00:00" {
		t.Fatalf("LocalStartsAt() = %q, want the venue's own hour", local)
	}

	// The create that names neither field stores what it always stored: a draft at no price.
	if err := store.CreateEvent(ctx, venue.Event{
		ID: fixtureCreatedDefault, RoomID: fixtureRoom, FilmID: fixtureFilm, LayoutVersionID: fixtureLayout,
		StartsAt: fixtureLateAt, Status: venue.EventDraft,
	}); err != nil {
		t.Fatalf("CreateEvent(no price, no status) = unexpected error %v", err)
	}
	readDefault, err := store.GetEvent(ctx, fixtureCreatedDefault)
	if err != nil {
		t.Fatalf("GetEvent() = unexpected error %v", err)
	}
	if readDefault.Status != venue.EventDraft || readDefault.PriceCents != 0 {
		t.Fatalf("stored event = status %q price %d, want the default draft at no price",
			readDefault.Status, readDefault.PriceCents)
	}
}
