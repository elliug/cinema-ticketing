// PostgreSQL-only ticket detail tests. They need a migrated isolated database and
// are gated on M3_CHECKOUT_DATABASE_URL, so the default unit suite needs no
// database at all.
//
// What only PostgreSQL can prove is here: that a ticket issued by a real checkout
// answers with its own seat, the money that checkout captured for the whole order,
// and the screening time rendered from the venue's own timezone; that a two-seat
// order gives each of its two tickets only its own seat while both carry the same
// reservation and the same charge; that the amount stays the recorded charge when
// the event price moves afterwards; that a voided ticket and a cancelled screening
// still answer with their statuses; and that an identifier naming no ticket is
// simply not found.
package tickets_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// ticketDatabaseURLEnv names the isolated database these tests use: the same
// throwaway M3 database the checkout suite runs against, because the ticket
// projection reads the sale that checkout writes. The default unit suite skips
// them; `make verify-m3` points it at a database migrated through 0006.
const ticketDatabaseURLEnv = "M3_CHECKOUT_DATABASE_URL"

// Fixture identifiers are fixed so a failed run can be cleaned up by the next one.
// The venue (cinema, room, published layout) and the event are inserted once and
// reused: M1 makes a published layout immutable, so it can never be deleted and
// re-inserted, and M1 requires the layout to be published before the event.
const (
	fixtureCinema = "00000000-0000-0000-0000-00000000e0a1"
	fixtureRoom   = "00000000-0000-0000-0000-00000000e0a2"
	fixtureLayout = "00000000-0000-0000-0000-00000000e0a3"
	fixtureRow    = "00000000-0000-0000-0000-00000000e0a4"
	fixtureSeat1  = "00000000-0000-0000-0000-00000000e0a5"
	fixtureSeat2  = "00000000-0000-0000-0000-00000000e0a6"
	fixtureSeat3  = "00000000-0000-0000-0000-00000000e0a7"
	fixtureSeat4  = "00000000-0000-0000-0000-00000000e0a8"
	fixtureFilm   = "00000000-0000-0000-0000-00000000e0a9"
	fixtureEvent  = "00000000-0000-0000-0000-00000000e0b1"

	orderReservation     = "00000000-0000-0000-0000-00000000e0c1"
	voidReservation      = "00000000-0000-0000-0000-00000000e0c2"
	cancelledReservation = "00000000-0000-0000-0000-00000000e0c3"
)

// fixtureReservations is every reservation the fixture sells, in checkout order.
var fixtureReservations = []string{orderReservation, voidReservation, cancelledReservation}

// fixturePriceCents is the event price at the time of the sale: two seats sell for
// 19,98 EUR. A later price change must not move that number.
const fixturePriceCents = 999

// fixtureCinemaTimezone is the venue's zone. May is summer time in Madrid, so the
// local presentation of the fixture screening has a +02:00 offset.
const fixtureCinemaTimezone = "Europe/Madrid"

// fixtureStartsAt is the fixed screening instant. It is stored as an absolute
// instant, so the local presentation is a fact about the venue and not about when
// the test runs.
var fixtureStartsAt = time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC)

// noRelease satisfies the checkout's coordination release; the gated test is about
// the ticket read, and there is no Redis hold behind the fixture reservations.
type noRelease struct{}

func (noRelease) Release(context.Context, string, []int64) error { return nil }

// TestTicketDetailAnswersARealSale walks the projection over a ticket a real
// checkout issued, then over the same ticket once it is voided, and over the ticket
// of a cancelled screening.
func TestTicketDetailAnswersARealSale(t *testing.T) {
	ctx := context.Background()
	pool, url := ticketPool(t, ctx)
	seats := seedTicketFixture(t, ctx, pool)
	t.Cleanup(func() {
		cleanupTicketFixture(pool)
		pool.Close()
	})

	store, err := tickets.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the ticket store: %v", err)
	}
	defer store.Close()

	present, err := store.TicketDetailSchemaPresent(ctx)
	if err != nil {
		t.Fatalf("check the ticket schema: %v", err)
	}
	if !present {
		t.Fatalf("%s is not migrated with 0005_checkout and 0006_one_open_payment_attempt", ticketDatabaseURLEnv)
	}

	// The tickets below are issued by the real checkout, not inserted by hand: the
	// projection is proven against the sale the system actually produces.
	checkoutStore, err := checkout.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the checkout store: %v", err)
	}
	defer checkoutStore.Close()
	checkoutService := checkout.NewService(payment.NewSimulated(), checkoutStore, noRelease{}, payment.ProviderName)

	sold := map[string]checkout.Result{}
	for _, reservationID := range fixtureReservations {
		result, err := checkoutService.Charge(ctx, reservationID, "key-"+reservationID)
		if err != nil {
			t.Fatalf("checkout of %s = unexpected error %v", reservationID, err)
		}
		if result.PaymentStatus != checkout.PaymentApproved {
			t.Fatalf("checkout of %s = %+v, want an approved payment", reservationID, result)
		}
		sold[reservationID] = result
	}

	t.Run("an issued ticket answers with its own seat, the order amount and its local time", func(t *testing.T) {
		detail, err := store.Detail(ctx, sold[orderReservation].TicketIDs[0])
		if err != nil {
			t.Fatalf("Detail() = unexpected error %v", err)
		}

		if detail.TicketID != sold[orderReservation].TicketIDs[0] {
			t.Fatalf("ticket id = %q, want %q", detail.TicketID, sold[orderReservation].TicketIDs[0])
		}
		if detail.Status != tickets.TicketStatusIssued || !detail.Valid() {
			t.Fatalf("status = %q valid = %t, want an issued, valid ticket", detail.Status, detail.Valid())
		}
		if detail.IssuedAt.IsZero() {
			t.Fatal("issuedAt is zero, want the stored issue instant")
		}
		if detail.ReservationID != orderReservation {
			t.Fatalf("reservation = %q, want %q", detail.ReservationID, orderReservation)
		}

		// The screening: the films, room and cinema rows behind the event, plus the
		// venue timezone the local presentation is computed from.
		event := detail.Event
		if event.ID != fixtureEvent {
			t.Fatalf("event = %q, want %q", event.ID, fixtureEvent)
		}
		if event.FilmTitle != "Ticket Fixture" {
			t.Fatalf("film = %q, want the film title the event was scheduled with", event.FilmTitle)
		}
		if event.RoomName != "Sala Tickets" {
			t.Fatalf("room = %q, want Sala Tickets", event.RoomName)
		}
		if event.CinemaName != "Cine Tickets" {
			t.Fatalf("cinema = %q, want Cine Tickets", event.CinemaName)
		}
		if event.Timezone != fixtureCinemaTimezone {
			t.Fatalf("timezone = %q, want %q", event.Timezone, fixtureCinemaTimezone)
		}
		if event.Status != "on_sale" {
			t.Fatalf("event status = %q, want on_sale", event.Status)
		}
		if !event.StartsAt.Equal(fixtureStartsAt) {
			t.Fatalf("startsAt = %v, want the stored instant %v", event.StartsAt, fixtureStartsAt)
		}
		local, err := event.LocalStartsAt()
		if err != nil {
			t.Fatalf("LocalStartsAt() = unexpected error %v", err)
		}
		if local != "2026-05-04T20:30:00+02:00" {
			t.Fatalf("local start = %q, want the venue's own 20:30 at +02:00", local)
		}

		// One ticket is one seat: the detail carries this ticket's own seat and not
		// the two seats of the order, because T6 prints one PDF per ticket and a PDF
		// listing two seats admits two people at the door.
		wantSeat := tickets.Seat{EventSeatID: seats[0], RowLabel: "A", SeatNumber: "1"}
		if len(detail.Seats) != 1 || detail.Seats[0] != wantSeat {
			t.Fatalf("seats = %+v, want only this ticket's seat %+v", detail.Seats, wantSeat)
		}

		// The amount is the money the checkout captured for the whole order: two seats
		// at 9,99 EUR. This ticket's own seat is one of them.
		if detail.OrderAmountCents != fixturePriceCents*2 {
			t.Fatalf("order amount = %d, want %d (two seats at 9,99 EUR)", detail.OrderAmountCents, fixturePriceCents*2)
		}
		if text := detail.OrderAmountText(); text != "19.98" {
			t.Fatalf("order amount text = %q, want 19.98", text)
		}
	})

	// The order the buyer paid for twice: two tickets, one reservation, one charge,
	// two different seats. Each ticket has to answer its own seat, because the PDF
	// (T6) prints one document per ticket and the door admits one holder per seat.
	t.Run("each ticket of one order answers its own seat and the order amount", func(t *testing.T) {
		orderSeats := []tickets.Seat{
			{EventSeatID: seats[0], RowLabel: "A", SeatNumber: "1"},
			{EventSeatID: seats[1], RowLabel: "A", SeatNumber: "2"},
		}
		ticketIDs := sold[orderReservation].TicketIDs
		if len(ticketIDs) != len(orderSeats) {
			t.Fatalf("the checkout issued %d tickets for %d seats", len(ticketIDs), len(orderSeats))
		}

		servedSeats := 0
		for position, ticketID := range ticketIDs {
			detail, err := store.Detail(ctx, ticketID)
			if err != nil {
				t.Fatalf("Detail() of ticket %d = unexpected error %v", position, err)
			}
			// The ticket answers its own seat, exactly as the seats were sold in seat
			// order: ticket 1 is seat A1 and ticket 2 is seat A2, never both.
			if len(detail.Seats) != 1 {
				t.Fatalf("ticket %d (%s) answered %d seats %+v, want only its own seat %+v",
					position, ticketID, len(detail.Seats), detail.Seats, orderSeats[position])
			}
			if detail.Seats[0] != orderSeats[position] {
				t.Fatalf("ticket %d answered seat %+v, want %+v", position, detail.Seats[0], orderSeats[position])
			}
			// Two tickets of one purchase are the same order and the same charge.
			if detail.ReservationID != orderReservation {
				t.Fatalf("ticket %d reservation = %q, want %q", position, detail.ReservationID, orderReservation)
			}
			if detail.OrderAmountCents != fixturePriceCents*2 {
				t.Fatalf("ticket %d order amount = %d, want the captured %d for the whole order",
					position, detail.OrderAmountCents, fixturePriceCents*2)
			}
			servedSeats += len(detail.Seats)
		}
		// The tickets partition the order: every seat sold is answered once, by the
		// ticket that owns it, and no seat is served twice.
		if servedSeats != len(orderSeats) {
			t.Fatalf("the tickets of the order serve %d seats in total, want %d", servedSeats, len(orderSeats))
		}
		if ticketIDs[0] == ticketIDs[1] {
			t.Fatalf("the checkout answered the same ticket id twice for two seats: %q", ticketIDs[0])
		}
	})

	t.Run("the amount is the recorded charge, not the current price", func(t *testing.T) {
		// The event price moves after the sale, which is exactly the situation where
		// price x seats would answer a number nobody paid.
		if _, err := pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, fixtureEvent, 2500); err != nil {
			t.Fatalf("move the event price: %v", err)
		}
		restored := false
		t.Cleanup(func() {
			if restored {
				return
			}
			if _, err := pool.Exec(context.Background(),
				`UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, fixtureEvent, fixturePriceCents); err != nil {
				t.Errorf("restore the event price: %v", err)
			}
		})

		detail, err := store.Detail(ctx, sold[orderReservation].TicketIDs[0])
		if err != nil {
			t.Fatalf("Detail() = unexpected error %v", err)
		}
		if detail.OrderAmountCents != fixturePriceCents*2 {
			t.Fatalf("order amount = %d after the price moved to 2500, want the captured %d",
				detail.OrderAmountCents, fixturePriceCents*2)
		}

		if _, err := pool.Exec(ctx, `UPDATE cinema.events SET price_cents = $2 WHERE id = $1`, fixtureEvent, fixturePriceCents); err != nil {
			t.Fatalf("restore the event price: %v", err)
		}
		restored = true
	})

	t.Run("a voided ticket still answers and says so", func(t *testing.T) {
		ticketID := sold[voidReservation].TicketIDs[0]
		if _, err := pool.Exec(ctx, `UPDATE cinema.tickets SET status = 'void' WHERE id = $1`, ticketID); err != nil {
			t.Fatalf("void the ticket: %v", err)
		}

		detail, err := store.Detail(ctx, ticketID)
		if err != nil {
			t.Fatalf("Detail() of a voided ticket = unexpected error %v", err)
		}
		if detail.Status != tickets.TicketStatusVoid {
			t.Fatalf("status = %q, want void", detail.Status)
		}
		if detail.Valid() {
			t.Fatal("a voided ticket is reported valid")
		}
		// "Still answers" means with a complete detail: the seats, the amount and the
		// screening are served exactly as for a good ticket.
		if len(detail.Seats) != 1 || detail.OrderAmountCents != fixturePriceCents {
			t.Fatalf("the voided ticket lost its detail: seats %+v order amount %d", detail.Seats, detail.OrderAmountCents)
		}
		if detail.Event.FilmTitle == "" || detail.Event.Status == "" {
			t.Fatalf("the voided ticket lost its screening: %+v", detail.Event)
		}
	})

	t.Run("a ticket of a cancelled screening still answers and says so", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE cinema.events SET status = 'cancelled' WHERE id = $1`, fixtureEvent); err != nil {
			t.Fatalf("cancel the event: %v", err)
		}
		restored := false
		t.Cleanup(func() {
			if restored {
				return
			}
			if _, err := pool.Exec(context.Background(),
				`UPDATE cinema.events SET status = 'on_sale' WHERE id = $1`, fixtureEvent); err != nil {
				t.Errorf("restore the event status: %v", err)
			}
		})

		detail, err := store.Detail(ctx, sold[cancelledReservation].TicketIDs[0])
		if err != nil {
			t.Fatalf("Detail() of a cancelled screening = unexpected error %v", err)
		}
		if detail.Status != tickets.TicketStatusIssued {
			t.Fatalf("ticket status = %q, want issued", detail.Status)
		}
		if detail.Event.Status != tickets.EventStatusCancelled {
			t.Fatalf("event status = %q, want cancelled", detail.Event.Status)
		}
		if detail.Valid() {
			t.Fatal("a ticket of a cancelled screening is reported valid")
		}
		if detail.Event.FilmTitle == "" || len(detail.Seats) != 1 {
			t.Fatalf("the cancelled ticket lost its detail: %+v", detail)
		}

		if _, err := pool.Exec(ctx, `UPDATE cinema.events SET status = 'on_sale' WHERE id = $1`, fixtureEvent); err != nil {
			t.Fatalf("restore the event status: %v", err)
		}
		restored = true
	})

	t.Run("an identifier that names no ticket is not found", func(t *testing.T) {
		if _, err := store.Detail(ctx, "00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, tickets.ErrNotFound) {
			t.Fatalf("Detail() of an unknown ticket = %v, want ErrNotFound", err)
		}
	})

	t.Run("an identifier that cannot be a ticket id is not found", func(t *testing.T) {
		for _, garbage := range []string{"", "not-a-ticket-id", "1", "' OR 1=1 --"} {
			if _, err := store.Detail(ctx, garbage); !errors.Is(err, tickets.ErrNotFound) {
				t.Fatalf("Detail(%q) = %v, want ErrNotFound without a query", garbage, err)
			}
		}
	})

	// Preserve: the checkout's own response is untouched by the ticket read, so the
	// tickets the buyer was told about are still the ones the projection serves.
	if len(sold[orderReservation].TicketIDs) != 2 {
		t.Fatalf("the checkout reported %d tickets, want 2", len(sold[orderReservation].TicketIDs))
	}
}

// ticketPool opens the gated isolated database and refuses to run anywhere the
// fixtures could collide with someone else's data.
func ticketPool(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(ticketDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set: this test needs a migrated isolated database (make verify-m3 sets it)", ticketDatabaseURLEnv)
	}
	requireIsolatedTicketDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open %s pool: %v", ticketDatabaseURLEnv, err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Fatalf("%s is not reachable: %v", ticketDatabaseURLEnv, err)
	}
	return pool, url
}

// requireIsolatedTicketDatabase refuses the development database. The test writes
// real rows, so the name has to say it is disposable.
func requireIsolatedTicketDatabase(t *testing.T, url string) {
	t.Helper()
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if name == "cinema" {
		t.Fatalf("%s must never point at the development database", ticketDatabaseURLEnv)
	}
	if !strings.HasPrefix(name, "cinema_m3") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m3, got %q", ticketDatabaseURLEnv, name)
	}
}

// seedTicketFixture creates the venue, film and event once, then one held
// reservation per case: the order that is sold and read, the ticket that is voided,
// and the ticket whose screening is cancelled. It returns the event seat ids in seat
// order.
func seedTicketFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []int64 {
	t.Helper()
	cleanupTicketFixture(pool)

	var layoutExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, fixtureLayout).
		Scan(&layoutExists); err != nil {
		t.Fatalf("inspect the ticket venue fixture: %v", err)
	}
	if !layoutExists {
		// The layout is published before the event exists, because M1 materializes
		// event seats from a published layout and refuses a draft one.
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + fixtureCinema + `','Cine Tickets','` + fixtureCinemaTimezone + `')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + fixtureRoom + `','` + fixtureCinema + `','Sala Tickets')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + fixtureLayout + `','` + fixtureRoom + `',1)`},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('` + fixtureRow + `','` + fixtureLayout + `','A',1)`},
			{"seats", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
			 ('` + fixtureSeat1 + `','` + fixtureRow + `','1',1,'standard',true),
			 ('` + fixtureSeat2 + `','` + fixtureRow + `','2',2,'standard',true),
			 ('` + fixtureSeat3 + `','` + fixtureRow + `','3',3,'standard',true),
			 ('` + fixtureSeat4 + `','` + fixtureRow + `','4',4,'standard',true)`},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = '` + fixtureLayout + `'`},
		} {
			if _, err := pool.Exec(ctx, statement.sql); err != nil {
				t.Fatalf("seed %s: %v", statement.what, err)
			}
		}
	}

	var eventExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.events WHERE id = $1)`, fixtureEvent).
		Scan(&eventExists); err != nil {
		t.Fatalf("inspect the ticket event fixture: %v", err)
	}
	if !eventExists {
		if _, err := pool.Exec(ctx,
			`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1,'Ticket Fixture',90,'PG')`,
			fixtureFilm); err != nil {
			t.Fatalf("seed film: %v", err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
VALUES ($1, $2, $3, $4, $5, 'on_sale')`,
			fixtureEvent, fixtureRoom, fixtureFilm, fixtureLayout, fixtureStartsAt); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
	// The screening instant and the price are reset on every run, so a run that a
	// previous case changed cannot decide what this one asserts.
	if _, err := pool.Exec(ctx, `
UPDATE cinema.events SET price_cents = $2, starts_at = $3, status = 'on_sale' WHERE id = $1`,
		fixtureEvent, fixturePriceCents, fixtureStartsAt); err != nil {
		t.Fatalf("seed the event price and screening time: %v", err)
	}

	rows, err := pool.Query(ctx, `
SELECT es.id
FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
JOIN cinema.layout_rows lr ON lr.id = ls.layout_row_id
WHERE es.event_id = $1
ORDER BY lr.position, ls.position`, fixtureEvent)
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

	// One reservation per case, each holding seats of its own: a seat can only be
	// held once, so the cases cannot share them.
	holds := []struct {
		reservation string
		seats       []int64
	}{
		{orderReservation, seats[0:2]},
		{voidReservation, seats[2:3]},
		{cancelledReservation, seats[3:4]},
	}
	for _, hold := range holds {
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservations (id, event_id, status, expires_at)
VALUES ($1, $2, 'held', now() + interval '10 minutes')`, hold.reservation, fixtureEvent); err != nil {
			t.Fatalf("seed reservation %s: %v", hold.reservation, err)
		}
		for _, seat := range hold.seats {
			if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id) VALUES ($1, $2, $3)`,
				hold.reservation, fixtureEvent, seat); err != nil {
				t.Fatalf("seed reservation seat %d: %v", seat, err)
			}
		}
	}
	return seats
}

// cleanupTicketFixture removes the rows the test creates. The venue and the event
// stay: M1 makes a published layout immutable and the event is reusable.
func cleanupTicketFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	reservations := fixtureReservations
	for _, sql := range []string{
		`DELETE FROM cinema.outbox WHERE payload->>'reservationId' = ANY($1)`,
		`DELETE FROM cinema.tickets WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.payment_attempts WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservation_seats WHERE reservation_id::text = ANY($1)`,
		`DELETE FROM cinema.reservations WHERE id::text = ANY($1)`,
	} {
		if _, err := pool.Exec(ctx, sql, reservations); err != nil && !strings.Contains(err.Error(), "does not exist") {
			// Cleanup runs before seeding too, where a failure is only diagnostic.
			continue
		}
	}
	// The seats of a previous run are sold again on every run, so their state and the
	// layout labels the assertions read are restored here.
	if _, err := pool.Exec(ctx, `
UPDATE cinema.event_seats SET state = 'available' WHERE event_id = $1`, fixtureEvent); err != nil {
		return
	}
}
