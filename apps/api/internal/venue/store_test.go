// PostgreSQL-only tests for the administrative screening read and price write. They
// need a migrated isolated database and are gated on M3_CHECKOUT_DATABASE_URL, so the
// default unit suite needs no database at all.
//
// What only PostgreSQL can prove is here: that the price write is one statement whose
// guard is in WHERE (an unknown event is not found instead of an invented success), that
// the stored event comes back as the database holds it, that the column refuses a
// negative value even when the application validation is bypassed, and that changing a
// price leaves the event's room, film, layout, start and status untouched.
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
	fixtureEarly  = "00000000-0000-0000-0000-0000000008c1"
	fixtureLate   = "00000000-0000-0000-0000-0000000008c2"
	fixtureAbsent = "00000000-0000-0000-0000-0000000008cf"
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
	url := strings.TrimSpace(os.Getenv(venueDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set: this test needs a migrated isolated database", venueDatabaseURLEnv)
	}
	requireIsolatedVenueDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open %s pool: %v", venueDatabaseURLEnv, err)
	}
	// Register the close first so it runs last: the fixture cleanup below needs the
	// pool, and a closed pool would silently leave the fixture rows behind.
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("%s is not reachable: %v", venueDatabaseURLEnv, err)
	}

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
		stored, err := store.SetEventPrice(ctx, fixtureEarly, 1250)
		if err != nil {
			t.Fatalf("SetEventPrice() = unexpected error %v", err)
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
		stored, err := store.SetEventPrice(ctx, fixtureEarly, 0)
		if err != nil {
			t.Fatalf("SetEventPrice(0) = unexpected error %v", err)
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
		if _, err := store.SetEventPrice(ctx, fixtureEarly, -1); !errors.Is(err, venue.ErrConflict) {
			t.Fatalf("SetEventPrice(-1) = %v, want the check violation as ErrConflict", err)
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
		if _, err := store.SetEventPrice(ctx, fixtureEarly, venue.MaxPriceCents); err != nil {
			t.Fatalf("SetEventPrice(%d) = unexpected error %v", venue.MaxPriceCents, err)
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
		if _, err := store.SetEventPrice(ctx, fixtureAbsent, 500); !errors.Is(err, venue.ErrNotFound) {
			t.Fatalf("SetEventPrice(unknown) = %v, want ErrNotFound", err)
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

	if _, err := pool.Exec(ctx,
		`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1,'Admin Fixture',90,'PG') ON CONFLICT DO NOTHING`,
		fixtureFilm); err != nil {
		t.Fatalf("seed film: %v", err)
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

func fixtureEvents() []string { return []string{fixtureEarly, fixtureLate, fixtureAbsent} }
