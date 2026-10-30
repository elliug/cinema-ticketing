// PostgreSQL-only tests for the two reads the public web needs. They need a migrated
// isolated database and are gated on M3_CHECKOUT_DATABASE_URL, so the default unit
// suite needs no database at all.
//
// What only PostgreSQL can prove is here: that the catalogue answers the screenings a
// buyer can actually buy — only the ones on sale and still ahead of the database
// clock, ordered by the screening time — with the seats available counted in the same
// statement and decided the way the seat map decides them (sold, live-held and
// unavailable seats all excluded, an expired hold counted as free); and that the
// reservation read answers the money a real checkout captured, the seats the order
// holds or sold in seat order, and the tickets the checkout issued.
package catalog_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
)

// catalogueDatabaseURLEnv names the isolated database these tests use: the same
// throwaway M3 database the checkout and ticket suites run against, because both
// reads answer what that sale wrote. The default unit suite skips them; the gated run
// points it at a fresh database migrated through 0006.
const catalogueDatabaseURLEnv = "M3_CHECKOUT_DATABASE_URL"

// Fixture identifiers are fixed so a failed run can be cleaned up by the next one. The
// venue (cinema, room, published layout) is inserted once and reused: M1 makes a
// published layout immutable, so it can never be deleted and re-inserted, and M1
// requires the layout to be published before the event.
const (
	fixtureCinema = "00000000-0000-0000-0000-00000000f3a1"
	fixtureRoom   = "00000000-0000-0000-0000-00000000f3a2"
	fixtureLayout = "00000000-0000-0000-0000-00000000f3a3"
	fixtureRowA   = "00000000-0000-0000-0000-00000000f3a4"
	fixtureRowB   = "00000000-0000-0000-0000-00000000f3a5"
	fixtureSeat1  = "00000000-0000-0000-0000-00000000f3a6"
	fixtureSeat2  = "00000000-0000-0000-0000-00000000f3a7"
	fixtureSeat3  = "00000000-0000-0000-0000-00000000f3a8"
	fixtureSeat4  = "00000000-0000-0000-0000-00000000f3a9"
	fixtureFilm   = "00000000-0000-0000-0000-00000000f3b1"

	// The five screenings the catalogue has to tell apart: the two a buyer can buy,
	// and the three reasons a screening is not in the catalogue.
	eventOnSale    = "00000000-0000-0000-0000-00000000f3c1"
	eventLater     = "00000000-0000-0000-0000-00000000f3c2"
	eventPast      = "00000000-0000-0000-0000-00000000f3c3"
	eventDraft     = "00000000-0000-0000-0000-00000000f3c4"
	eventCancelled = "00000000-0000-0000-0000-00000000f3c5"

	paidReservation    = "00000000-0000-0000-0000-00000000f3d1"
	heldReservation    = "00000000-0000-0000-0000-00000000f3d2"
	expiredReservation = "00000000-0000-0000-0000-00000000f3d3"
)

// fixtureReservations is every reservation the fixture writes, in cleanup order.
var fixtureReservations = []string{paidReservation, heldReservation, expiredReservation}

// fixtureEvents is every event the fixture writes, for the same reason.
var fixtureEvents = []string{eventOnSale, eventLater, eventPast, eventDraft, eventCancelled}

// fixturePriceCents is the price at the time of the sale: two seats sell for 25,00
// EUR.
const fixturePriceCents = 1250

// fixtureCinemaTimezone is the venue's zone. May is summer time in Madrid, so the
// catalogue's local presentation of a May screening has a +02:00 offset.
const fixtureCinemaTimezone = "Europe/Madrid"

// The fixture instants are fixed and far from whenever the test runs: the two
// catalogue entries are in the future (2036) and ordered, and the excluded screening
// is in the past (2020), so the database clock decides their side of the catalogue
// without the test pretending to be a clock. 2036-05-04 is summer time in Madrid.
var (
	fixtureOnSaleAt    = time.Date(2036, time.May, 4, 18, 30, 0, 0, time.UTC)
	fixtureLaterAt     = time.Date(2036, time.May, 6, 9, 0, 0, 0, time.UTC)
	fixturePastAt      = time.Date(2020, time.May, 4, 18, 30, 0, 0, time.UTC)
	fixtureDraftAt     = time.Date(2036, time.May, 10, 18, 30, 0, 0, time.UTC)
	fixtureCancelledAt = time.Date(2036, time.May, 8, 18, 30, 0, 0, time.UTC)
)

// noSeatRelease satisfies the checkout's coordination release; this test is about the
// reads, and there is no Redis hold behind the fixture reservations.
type noSeatRelease struct{}

func (noSeatRelease) Release(context.Context, string, []int64) error { return nil }

// TestCatalogReadsARealSaleAndItsNeighbourhood walks both reads over a database in
// which one order was really sold, one hold is still live and one hold already
// expired, and where screenings exist that the catalogue must not answer.
func TestCatalogReadsARealSaleAndItsNeighbourhood(t *testing.T) {
	ctx := context.Background()
	pool, url := cataloguePool(t, ctx)
	seats := seedCatalogueFixture(t, ctx, pool)
	t.Cleanup(func() {
		cleanupCatalogueFixture(pool)
		pool.Close()
	})

	store, err := catalog.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the catalogue store: %v", err)
	}
	defer store.Close()

	present, err := store.ReadSchemaPresent(ctx)
	if err != nil {
		t.Fatalf("check the read schema: %v", err)
	}
	if !present {
		t.Fatalf("%s is not migrated with 0005_checkout and 0006_one_open_payment_attempt", catalogueDatabaseURLEnv)
	}

	// The sold order is sold by the real checkout, not inserted by hand: the amount
	// and the ticket ids the reservation read answers are the ones the system produced.
	checkoutStore, err := checkout.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the checkout store: %v", err)
	}
	defer checkoutStore.Close()
	checkoutService := checkout.NewService(payment.NewSimulated(), checkoutStore, noSeatRelease{}, payment.ProviderName)
	sold, err := checkoutService.Charge(ctx, paidReservation, "key-"+paidReservation)
	if err != nil {
		t.Fatalf("checkout of %s = unexpected error %v", paidReservation, err)
	}
	if sold.PaymentStatus != checkout.PaymentApproved {
		t.Fatalf("checkout of %s = %+v, want an approved payment", paidReservation, sold)
	}
	if len(sold.TicketIDs) != 2 {
		t.Fatalf("the checkout issued %d tickets for the two sold seats", len(sold.TicketIDs))
	}

	t.Run("the catalogue answers the screenings a buyer can buy, in screening order", func(t *testing.T) {
		screenings, err := store.Events(ctx)
		if err != nil {
			t.Fatalf("Events() = unexpected error %v", err)
		}
		// The database is shared with the other M3 read-model suites, so the fixture's
		// own entries are what this test decides about; an entry of another suite is
		// not this test's business.
		mine := make(map[string]catalog.Screening, len(fixtureEvents))
		order := make([]string, 0, len(fixtureEvents))
		for _, screening := range screenings {
			for _, eventID := range fixtureEvents {
				if screening.ID == eventID {
					mine[eventID] = screening
					order = append(order, eventID)
				}
			}
		}

		// Exactly the two on-sale, still-ahead screenings, in the order the SQL fixes
		// (screening time ascending). The past one is behind the database clock, and
		// the draft and cancelled ones are not on sale.
		if len(order) != 2 || order[0] != eventOnSale || order[1] != eventLater {
			t.Fatalf("the catalogue answered the fixture's screenings in the order %v, want [%s %s]",
				order, eventOnSale, eventLater)
		}

		onSale := mine[eventOnSale]
		if onSale.FilmTitle != "Catalogue Fixture" {
			t.Fatalf("film = %q, want the film title the event was scheduled with", onSale.FilmTitle)
		}
		if onSale.CinemaName != "Cine Catalogue" || onSale.RoomName != "Sala Catalogue" {
			t.Fatalf("venue = %q / %q, want Cine Catalogue in Sala Catalogue", onSale.CinemaName, onSale.RoomName)
		}
		if !onSale.StartsAt.Equal(fixtureOnSaleAt) {
			t.Fatalf("startsAt = %v, want the stored instant %v", onSale.StartsAt, fixtureOnSaleAt)
		}
		if onSale.Timezone != fixtureCinemaTimezone {
			t.Fatalf("timezone = %q, want %q", onSale.Timezone, fixtureCinemaTimezone)
		}
		local, err := onSale.LocalStartsAt()
		if err != nil {
			t.Fatalf("LocalStartsAt() = unexpected error %v", err)
		}
		if local != "2036-05-04T20:30:00+02:00" {
			t.Fatalf("local start = %q, want the venue's own 20:30 at +02:00", local)
		}
		if onSale.PriceCents != fixturePriceCents {
			t.Fatalf("price = %d, want %d", onSale.PriceCents, fixturePriceCents)
		}
		if text := onSale.PriceText(); text != "12.50" {
			t.Fatalf("price text = %q, want 12.50", text)
		}

		// Four seats were materialized for this screening: two sold by the real
		// checkout, one held by a live hold, one free. The count is decided in the same
		// statement as the entry, so this number is the same answer the seat map gives.
		if onSale.SeatCount != 4 || onSale.AvailableSeats != 1 {
			t.Fatalf("seats = %d of %d available, want 1 of 4 (2 sold, 1 held, 1 free)",
				onSale.AvailableSeats, onSale.SeatCount)
		}

		// The screening whose only hold already expired is fully available: the
		// database clock decides, exactly as the hold window does, and no sweep has to
		// have run for that to be true.
		later := mine[eventLater]
		if later.SeatCount != 4 || later.AvailableSeats != 4 {
			t.Fatalf("seats = %d of %d available on the screening with an expired hold, want 4 of 4",
				later.AvailableSeats, later.SeatCount)
		}
	})

	t.Run("the reservation read answers the money, the seats and the tickets of the sale", func(t *testing.T) {
		detail, err := store.Reservation(ctx, paidReservation)
		if err != nil {
			t.Fatalf("Reservation() = unexpected error %v", err)
		}
		if detail.ID != paidReservation {
			t.Fatalf("reservation = %q, want %q", detail.ID, paidReservation)
		}
		if detail.Status != "paid" {
			t.Fatalf("status = %q, want paid", detail.Status)
		}
		if detail.ExpiresAt.IsZero() {
			t.Fatal("expiresAt is zero, want the stored hold deadline")
		}
		// The screening travels with the order, so a reloaded confirmation says what is
		// being bought and when — the gap T1 left open.
		if detail.EventID == "" || detail.FilmTitle == "" || detail.CinemaName == "" || detail.RoomName == "" {
			t.Fatalf("screening = %q/%q/%q/%q, want event, film, cinema and room",
				detail.EventID, detail.FilmTitle, detail.CinemaName, detail.RoomName)
		}
		if detail.StartsAt.IsZero() || detail.Timezone == "" {
			t.Fatalf("startsAt/timezone = %v/%q, want both", detail.StartsAt, detail.Timezone)
		}
		local, err := detail.LocalStartsAt()
		if err != nil {
			t.Fatalf("LocalStartsAt() = unexpected error %v", err)
		}
		if local == "" || local == detail.StartsAt.UTC().Format(time.RFC3339) {
			t.Fatalf("local start %q, want the venue's own spelling", local)
		}

		// The amount is the charge the checkout captured for the whole order, not the
		// price times the seats this read happened to join.
		if detail.AmountCents == nil {
			t.Fatal("amountCents is nil for a paid order, want the captured charge")
		}
		if *detail.AmountCents != 2*fixturePriceCents {
			t.Fatalf("amount = %d, want the captured %d", *detail.AmountCents, 2*fixturePriceCents)
		}
		if text, ok := detail.AmountText(); !ok || text != "25.00" {
			t.Fatalf("amount text = %q, %t, want 25.00", text, ok)
		}

		// The seats it sold, in the order the door paints them.
		wantSeats := []catalog.Seat{
			{EventSeatID: seats[0], RowLabel: "A", SeatNumber: "1"},
			{EventSeatID: seats[1], RowLabel: "A", SeatNumber: "2"},
		}
		if len(detail.Seats) != len(wantSeats) {
			t.Fatalf("seats = %+v, want the order's two seats %+v", detail.Seats, wantSeats)
		}
		for position := range wantSeats {
			if detail.Seats[position] != wantSeats[position] {
				t.Fatalf("seat %d = %+v, want %+v", position, detail.Seats[position], wantSeats[position])
			}
		}

		// The tickets the checkout issued, in the same order the checkout answered them.
		if len(detail.TicketIDs) != len(sold.TicketIDs) {
			t.Fatalf("ticket ids = %v, want the %d the checkout issued %v",
				detail.TicketIDs, len(sold.TicketIDs), sold.TicketIDs)
		}
		for position, ticketID := range sold.TicketIDs {
			if detail.TicketIDs[position] != ticketID {
				t.Fatalf("ticket %d = %q, want %q", position, detail.TicketIDs[position], ticketID)
			}
		}
	})

	t.Run("the reservation read answers an unpaid order with no amount and no ticket", func(t *testing.T) {
		detail, err := store.Reservation(ctx, heldReservation)
		if err != nil {
			t.Fatalf("Reservation() = unexpected error %v", err)
		}
		if detail.Status != "held" {
			t.Fatalf("status = %q, want held", detail.Status)
		}
		if detail.AmountCents != nil {
			t.Fatalf("amountCents = %d for an unpaid order, want no amount", *detail.AmountCents)
		}
		if text, ok := detail.AmountText(); ok || text != "" {
			t.Fatalf("amount text = %q, %t for an unpaid order, want none", text, ok)
		}
		if len(detail.Seats) != 1 || detail.Seats[0].EventSeatID != seats[2] {
			t.Fatalf("seats = %+v, want the seat the hold owns (%d)", detail.Seats, seats[2])
		}
		if detail.Seats[0].RowLabel != "A" || detail.Seats[0].SeatNumber != "3" {
			t.Fatalf("seat = %+v, want the row label and seat number painted at the door", detail.Seats[0])
		}
		if detail.TicketIDs == nil {
			t.Fatal("ticketIds is nil for an unpaid order, want an empty list")
		}
		if len(detail.TicketIDs) != 0 {
			t.Fatalf("ticketIds = %v for an unpaid order, want none", detail.TicketIDs)
		}
	})

	t.Run("an identifier that names no reservation is not found", func(t *testing.T) {
		if _, err := store.Reservation(ctx, "00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, catalog.ErrNotFound) {
			t.Fatalf("Reservation() of an unknown reservation = %v, want ErrNotFound", err)
		}
	})

	t.Run("an identifier that cannot be a reservation id is not found", func(t *testing.T) {
		for _, garbage := range []string{"", "not-a-reservation-id", "1", "' OR 1=1 --"} {
			if _, err := store.Reservation(ctx, garbage); !errors.Is(err, catalog.ErrNotFound) {
				t.Fatalf("Reservation(%q) = %v, want ErrNotFound without a query", garbage, err)
			}
		}
	})
}

// cataloguePool opens the gated isolated database and refuses to run anywhere the
// fixtures could collide with someone else's data.
func cataloguePool(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(catalogueDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set: this test needs a migrated isolated database", catalogueDatabaseURLEnv)
	}
	requireIsolatedCatalogueDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open %s pool: %v", catalogueDatabaseURLEnv, err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Fatalf("%s is not reachable: %v", catalogueDatabaseURLEnv, err)
	}
	return pool, url
}

// requireIsolatedCatalogueDatabase refuses the development database. The test writes
// real rows, so the name has to say it is disposable.
func requireIsolatedCatalogueDatabase(t *testing.T, url string) {
	t.Helper()
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if name == "cinema" {
		t.Fatalf("%s must never point at the development database", catalogueDatabaseURLEnv)
	}
	if !strings.HasPrefix(name, "cinema_m3") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m3, got %q", catalogueDatabaseURLEnv, name)
	}
}

// seedCatalogueFixture creates the venue, the film and the five screenings once, then
// one order per case: the one that is sold and read, the live hold, and the hold whose
// window already closed. It returns the event seat ids of the on-sale screening in seat
// order.
func seedCatalogueFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []int64 {
	t.Helper()
	cleanupCatalogueFixture(pool)

	var layoutExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, fixtureLayout).
		Scan(&layoutExists); err != nil {
		t.Fatalf("inspect the catalogue venue fixture: %v", err)
	}
	if !layoutExists {
		// The layout is published before the event exists, because M1 materializes event
		// seats from a published layout and refuses a draft one. Row B has one seat and
		// row A three, so a count that assumed a rectangle would be wrong.
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + fixtureCinema + `','Cine Catalogue','` + fixtureCinemaTimezone + `')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + fixtureRoom + `','` + fixtureCinema + `','Sala Catalogue')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + fixtureLayout + `','` + fixtureRoom + `',1)`},
			{"rows", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES
			 ('` + fixtureRowA + `','` + fixtureLayout + `','A',1),
			 ('` + fixtureRowB + `','` + fixtureLayout + `','B',2)`},
			{"seats", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
			 ('` + fixtureSeat1 + `','` + fixtureRowA + `','1',1,'standard',true),
			 ('` + fixtureSeat2 + `','` + fixtureRowA + `','2',2,'standard',true),
			 ('` + fixtureSeat3 + `','` + fixtureRowA + `','3',3,'accessible',true),
			 ('` + fixtureSeat4 + `','` + fixtureRowB + `','1',1,'standard',true)`},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = '` + fixtureLayout + `'`},
		} {
			if _, err := pool.Exec(ctx, statement.sql); err != nil {
				t.Fatalf("seed %s: %v", statement.what, err)
			}
		}
	}

	var filmExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.films WHERE id = $1)`, fixtureFilm).
		Scan(&filmExists); err != nil {
		t.Fatalf("inspect the catalogue film fixture: %v", err)
	}
	if !filmExists {
		if _, err := pool.Exec(ctx,
			`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1,'Catalogue Fixture',90,'PG')`,
			fixtureFilm); err != nil {
			t.Fatalf("seed film: %v", err)
		}
	}

	for _, screening := range []struct {
		id       string
		startsAt time.Time
		status   string
	}{
		{eventOnSale, fixtureOnSaleAt, "on_sale"},
		{eventLater, fixtureLaterAt, "on_sale"},
		{eventPast, fixturePastAt, "on_sale"},
		{eventDraft, fixtureDraftAt, "draft"},
		{eventCancelled, fixtureCancelledAt, "cancelled"},
	} {
		// The event is reset on every run, so a run that a previous case changed cannot
		// decide what this one asserts.
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status, price_cents)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET starts_at = EXCLUDED.starts_at, status = EXCLUDED.status,
                               price_cents = EXCLUDED.price_cents`,
			screening.id, fixtureRoom, fixtureFilm, fixtureLayout, screening.startsAt, screening.status,
			fixturePriceCents); err != nil {
			t.Fatalf("seed event %s: %v", screening.id, err)
		}
	}

	rows, err := pool.Query(ctx, `
SELECT es.id
FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
JOIN cinema.layout_rows lr ON lr.id = ls.layout_row_id
WHERE es.event_id = $1
ORDER BY lr.position, ls.position`, eventOnSale)
	if err != nil {
		t.Fatalf("read the event seats: %v", err)
	}
	defer rows.Close()
	var seats []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan an event seat: %v", err)
		}
		seats = append(seats, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the event seats: %v", err)
	}
	if len(seats) != 4 {
		t.Fatalf("the event materialized %d seats, want 4", len(seats))
	}
	laterSeat, err := eventSeatIn(ctx, pool, eventLater)
	if err != nil {
		t.Fatalf("read the second screening's seat: %v", err)
	}

	// One reservation per case, each holding seats of its own: a seat can only be held
	// once, so the cases cannot share them. The expired one is written live and then
	// moved behind the clock, because the schema refuses to insert a hold whose window
	// already closed.
	holds := []struct {
		reservation string
		event       string
		seats       []int64
	}{
		{paidReservation, eventOnSale, seats[0:2]},
		{heldReservation, eventOnSale, seats[2:3]},
		{expiredReservation, eventLater, []int64{laterSeat}},
	}
	for _, hold := range holds {
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservations (id, event_id, status, expires_at)
VALUES ($1, $2, 'held', now() + interval '10 minutes')`, hold.reservation, hold.event); err != nil {
			t.Fatalf("seed reservation %s: %v", hold.reservation, err)
		}
		for _, seat := range hold.seats {
			if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id) VALUES ($1, $2, $3)`,
				hold.reservation, hold.event, seat); err != nil {
				t.Fatalf("seed reservation seat %d: %v", seat, err)
			}
		}
	}
	if _, err := pool.Exec(ctx,
		`UPDATE cinema.reservations SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		expiredReservation); err != nil {
		t.Fatalf("expire the third reservation: %v", err)
	}
	return seats
}

// eventSeatIn reads the event seat ids of one screening in seat order.
func eventSeatIn(ctx context.Context, pool *pgxpool.Pool, eventID string) (int64, error) {
	var seat int64
	err := pool.QueryRow(ctx, `
SELECT es.id
FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
JOIN cinema.layout_rows lr ON lr.id = ls.layout_row_id
WHERE es.event_id = $1
ORDER BY lr.position, ls.position
LIMIT 1`, eventID).Scan(&seat)
	return seat, err
}

// cleanupCatalogueFixture removes the rows the test creates. The venue stays: M1 makes
// a published layout immutable, so it can never be deleted and re-inserted.
func cleanupCatalogueFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	for _, statement := range []string{
		`DELETE FROM cinema.outbox WHERE payload->>'reservationId' = ANY($1)`,
		`DELETE FROM cinema.tickets WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.payment_attempts WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservation_seats WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservations WHERE id::text = ANY($1)`,
	} {
		// Cleanup runs before seeding too, where a failure is only diagnostic.
		_, _ = pool.Exec(ctx, statement, fixtureReservations)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.event_seats WHERE event_id::text = ANY($1)`, fixtureEvents)
	_, _ = pool.Exec(ctx, `DELETE FROM cinema.events WHERE id::text = ANY($1)`, fixtureEvents)
}
