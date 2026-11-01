// PostgreSQL-only tests of the email consumer's delivery record. They need a migrated
// isolated database and are gated on M3_CHECKOUT_DATABASE_URL, so the default unit suite
// needs no database at all.
//
// What only PostgreSQL can prove is here, and it is the reason the delivery table exists
// (S4): that a row the PDF worker has already marked `published_at` is still returned to
// this consumer, that a delivery this consumer recorded hides a row while another
// consumer's record does not, that recording a delivery twice changes nothing, that the
// recipient is read from the reservation (an absent address is not an absent reservation),
// and that recording a delivery never touches `published_at`, which belongs to the PDF
// worker's meaning of "every consumer acted".
package email_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/email"
)

// emailDatabaseURLEnv names the isolated database these tests use: the same throwaway M3
// database the checkout and ticket suites run against, because the email consumer reads
// the outbox row the checkout writes. The default unit suite skips them; `make verify-m3`
// points it at a database migrated through 0009.
const emailDatabaseURLEnv = "M3_CHECKOUT_DATABASE_URL"

// fixture identifiers are fixed so a failed run can be cleaned up by the next one, and are
// their own namespace so this file never collides with the ticket suite's fixture.
const (
	fixtureCinema = "00000000-0000-0000-0000-00000000f0a1"
	fixtureRoom   = "00000000-0000-0000-0000-00000000f0a2"
	fixtureLayout = "00000000-0000-0000-0000-00000000f0a3"
	fixtureRow    = "00000000-0000-0000-0000-00000000f0a4"
	fixtureSeat1  = "00000000-0000-0000-0000-00000000f0a5"
	fixtureSeat2  = "00000000-0000-0000-0000-00000000f0a6"
	fixtureFilm   = "00000000-0000-0000-0000-00000000f0a9"
	fixtureEvent  = "00000000-0000-0000-0000-00000000f0b1"

	// addressedReservation is the sale whose buyer gave an address; the second is the sale
	// whose buyer declined, which is a deliverable state and not a failure.
	addressedReservation   = "00000000-0000-0000-0000-00000000f0c1"
	unaddressedReservation = "00000000-0000-0000-0000-00000000f0c2"

	// buyerAddress is the address every test here reads back. It is documentation-shaped
	// on purpose: a real mailbox must never receive a test email.
	buyerAddress = "buyer@cinema.test"
)

// outboxTopic is the producer's topic, spelled out rather than imported: if either side
// renames it, this test is the one that says the worker and the checkout stopped agreeing.
const outboxTopic = "ticket.issued"

// fixtureReservations is every reservation the fixture creates.
var fixtureReservations = []string{addressedReservation, unaddressedReservation}

// TestPendingReturnsARowThePDFWorkerAlreadyPublished is the assertion the whole delivery
// record exists for (S4): `published_at` means "every consumer has acted", so the PDF
// worker sets it as soon as the document is stored. A consumer filtering on it would find
// nothing at all and mail nothing.
func TestPendingReturnsARowThePDFWorkerAlreadyPublished(t *testing.T) {
	ctx := context.Background()
	pool, store := emailFixture(t, ctx)

	outboxID := insertOutboxRow(t, ctx, pool, addressedReservation, true)

	pending, err := store.Pending(ctx, email.Consumer, 1000)
	if err != nil {
		t.Fatalf("Pending() = unexpected error %v", err)
	}
	if !containsRow(pending, outboxID) {
		t.Fatalf("Pending() omitted %s, a row the PDF worker already published: the email consumer "+
			"must keep its own progress instead of reading published_at", outboxID)
	}
}

// TestPendingSkipsThisConsumerAndOnlyThisConsumer pins both halves of the record (S4): a
// row this consumer has delivered stops being pending, and a record another consumer left
// does not hide a row from this one.
func TestPendingSkipsThisConsumerAndOnlyThisConsumer(t *testing.T) {
	ctx := context.Background()
	pool, store := emailFixture(t, ctx)

	delivered := insertOutboxRow(t, ctx, pool, addressedReservation, true)
	foreign := insertOutboxRow(t, ctx, pool, unaddressedReservation, true)

	if err := store.Delivered(ctx, email.Consumer, delivered); err != nil {
		t.Fatalf("Delivered() = unexpected error %v", err)
	}
	// The PDF archive is the other consumer of this topic; its record is about its own work.
	if err := store.Delivered(ctx, "pdf", foreign); err != nil {
		t.Fatalf("Delivered() for another consumer = unexpected error %v", err)
	}

	pending, err := store.Pending(ctx, email.Consumer, 1000)
	if err != nil {
		t.Fatalf("Pending() = unexpected error %v", err)
	}
	if containsRow(pending, delivered) {
		t.Fatalf("Pending() returned %s, a row this consumer already delivered", delivered)
	}
	if !containsRow(pending, foreign) {
		t.Fatalf("Pending() omitted %s, a row another consumer delivered: one consumer's record "+
			"must never hide a row from another", foreign)
	}
}

// TestDeliveredIsIdempotent pins that a retried pass cannot record twice or fail: the
// primary key is the fact, and the second insert changes nothing (S4).
func TestDeliveredIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, store := emailFixture(t, ctx)

	outboxID := insertOutboxRow(t, ctx, pool, addressedReservation, true)
	if err := store.Delivered(ctx, email.Consumer, outboxID); err != nil {
		t.Fatalf("first Delivered() = unexpected error %v", err)
	}
	first := deliveredAt(t, ctx, pool, outboxID)

	time.Sleep(10 * time.Millisecond)
	if err := store.Delivered(ctx, email.Consumer, outboxID); err != nil {
		t.Fatalf("second Delivered() = unexpected error %v", err)
	}

	var recorded int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cinema.outbox_deliveries WHERE consumer = $1 AND outbox_id = $2::uuid`,
		email.Consumer, outboxID).Scan(&recorded); err != nil {
		t.Fatalf("count the delivery records: %v", err)
	}
	if recorded != 1 {
		t.Fatalf("the row has %d delivery records, want exactly 1", recorded)
	}
	if second := deliveredAt(t, ctx, pool, outboxID); !second.Equal(first) {
		t.Fatalf("delivered_at moved from %s to %s on the second call, want it unchanged", first, second)
	}
}

// TestRecipientAnswersTheStoredAddress pins S1's read side: the address comes from the
// reservation, an absent address is the empty string (the worker records it and moves on,
// because no later pass can invent one), and a reservation the database does not have is an
// error rather than the same empty answer. Collapsing "I cannot answer" into "there is no
// address" would silently drop an email that should exist.
func TestRecipientAnswersTheStoredAddress(t *testing.T) {
	ctx := context.Background()
	_, store := emailFixture(t, ctx)

	to, err := store.Recipient(ctx, addressedReservation)
	if err != nil {
		t.Fatalf("Recipient() of the addressed sale = unexpected error %v", err)
	}
	if to != buyerAddress {
		t.Fatalf("Recipient() = %q, want %q", to, buyerAddress)
	}

	to, err = store.Recipient(ctx, unaddressedReservation)
	if err != nil {
		t.Fatalf("Recipient() of the sale without an address = unexpected error %v", err)
	}
	if to != "" {
		t.Fatalf("Recipient() = %q, want the empty string for a sale whose buyer gave none", to)
	}

	if to, err = store.Recipient(ctx, "00000000-0000-0000-0000-00000000f0ff"); err == nil {
		t.Fatalf("Recipient() of a reservation that does not exist = %q, nil; want an error: "+
			"an unknown reservation is not a sale without an address", to)
	}
}

// TestRecipientRefusesAnIdentifierThatCannotBeOne pins the other half of the same
// distinction: an identifier the database cannot even resolve is reported, never answered
// as an empty address.
func TestRecipientRefusesAnIdentifierThatCannotBeOne(t *testing.T) {
	ctx := context.Background()
	_, store := emailFixture(t, ctx)

	if to, err := store.Recipient(ctx, "not-a-reservation"); err == nil {
		t.Fatalf("Recipient() of a malformed identifier = %q, nil; want an error", to)
	}
}

// TestDeliveredLeavesPublishedAtUntouched pins the invariant the PDF worker relies on
// (S4): the delivery record is this consumer's, and the outbox mark keeps meaning "every
// consumer has acted" for the consumer that owns it.
func TestDeliveredLeavesPublishedAtUntouched(t *testing.T) {
	ctx := context.Background()
	pool, store := emailFixture(t, ctx)

	published := insertOutboxRow(t, ctx, pool, addressedReservation, true)
	unpublished := insertOutboxRow(t, ctx, pool, unaddressedReservation, false)

	if err := store.Delivered(ctx, email.Consumer, published); err != nil {
		t.Fatalf("Delivered() = unexpected error %v", err)
	}
	if err := store.Delivered(ctx, email.Consumer, unpublished); err != nil {
		t.Fatalf("Delivered() = unexpected error %v", err)
	}

	if at := publishedAt(t, ctx, pool, published); at.IsZero() {
		t.Fatal("recording a delivery cleared published_at, which the PDF worker had set")
	}
	if at := publishedAt(t, ctx, pool, unpublished); !at.IsZero() {
		t.Fatalf("recording a delivery set published_at to %s: the email consumer must not claim "+
			"that every consumer has acted", at)
	}
}

// emailFixture opens the gated database, seeds the fixture and returns a store over it.
// Cleanup removes the rows the fixture wrote and closes both pools.
func emailFixture(t *testing.T, ctx context.Context) (*pgxpool.Pool, *email.PostgresStore) {
	t.Helper()
	pool, url := emailPool(t, ctx)
	seedEmailFixture(t, ctx, pool)
	t.Cleanup(func() {
		cleanupEmailFixture(pool)
		pool.Close()
	})

	store, err := email.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the email store: %v", err)
	}
	var present bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('cinema.outbox_deliveries') IS NOT NULL AND EXISTS (
		   SELECT 1 FROM information_schema.columns
		    WHERE table_schema = 'cinema' AND table_name = 'reservations'
		      AND column_name = 'contact_email')`).Scan(&present); err != nil {
		t.Fatalf("inspect the email schema: %v", err)
	}
	if !present {
		t.Fatalf("%s is not migrated with 0009_email_delivery", emailDatabaseURLEnv)
	}
	t.Cleanup(store.Close)
	return pool, store
}

// emailPool reads the gated database URL, refuses anything that is not a throwaway
// database, and proves the pool answers.
func emailPool(t *testing.T, ctx context.Context) (*pgxpool.Pool, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(emailDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set; the email delivery record is proven against PostgreSQL by `make verify-m3`", emailDatabaseURLEnv)
	}
	requireIsolatedEmailDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open the fixture pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("%s does not answer: %v", emailDatabaseURLEnv, err)
	}
	return pool, url
}

// requireIsolatedEmailDatabase refuses the development database. The test writes real
// rows, so the name has to say it is disposable.
func requireIsolatedEmailDatabase(t *testing.T, url string) {
	t.Helper()
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if name == "cinema" {
		t.Fatalf("%s must never point at the development database", emailDatabaseURLEnv)
	}
	if !strings.HasPrefix(name, "cinema_m3") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m3, got %q", emailDatabaseURLEnv, name)
	}
}

// seedEmailFixture creates the venue, film and event once and one held reservation per
// case. The layout is published before the event exists, because M1 materializes event
// seats from a published layout and refuses a draft one.
func seedEmailFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	cleanupEmailFixture(pool)

	var layoutExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, fixtureLayout).
		Scan(&layoutExists); err != nil {
		t.Fatalf("inspect the email venue fixture: %v", err)
	}
	if !layoutExists {
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + fixtureCinema + `','Cine Emails','Europe/Madrid')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + fixtureRoom + `','` + fixtureCinema + `','Sala Emails')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + fixtureLayout + `','` + fixtureRoom + `',1)`},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('` + fixtureRow + `','` + fixtureLayout + `','A',1)`},
			{"seats", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
			 ('` + fixtureSeat1 + `','` + fixtureRow + `','1',1,'standard',true),
			 ('` + fixtureSeat2 + `','` + fixtureRow + `','2',2,'standard',true)`},
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
		t.Fatalf("inspect the email event fixture: %v", err)
	}
	if !eventExists {
		if _, err := pool.Exec(ctx,
			`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1,'Email Fixture',90,'PG')`,
			fixtureFilm); err != nil {
			t.Fatalf("seed film: %v", err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
VALUES ($1, $2, $3, $4, now() + interval '1 day', 'on_sale')`,
			fixtureEvent, fixtureRoom, fixtureFilm, fixtureLayout); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}

	// The addresses are reset on every run, so a case that changed one cannot decide what
	// the next one asserts. The unaddressed sale is the state of a buyer who declined.
	for _, reservation := range []struct {
		id      string
		address *string
	}{
		{addressedReservation, pointerTo(buyerAddress)},
		{unaddressedReservation, nil},
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.reservations (id, event_id, status, expires_at, contact_email)
VALUES ($1, $2, 'held', now() + interval '10 minutes', $3)`,
			reservation.id, fixtureEvent, reservation.address); err != nil {
			t.Fatalf("seed reservation %s: %v", reservation.id, err)
		}
	}
}

// insertOutboxRow writes the outbox row the checkout would have written, with the
// published_at marker the PDF worker would have set when published is true.
func insertOutboxRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reservationID string, published bool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
INSERT INTO cinema.outbox (id, topic, payload, published_at)
VALUES (gen_random_uuid(), $1, jsonb_build_object('reservationId', $2::text),
        CASE WHEN $3 THEN now() END)
RETURNING id::text`, outboxTopic, reservationID, published).Scan(&id)
	if err != nil {
		t.Fatalf("insert the outbox row of %s: %v", reservationID, err)
	}
	return id
}

// containsRow reports whether Pending returned the row. The worker asks for a batch of the
// whole topic, so this test never assumes its own rows are the only ones in the database.
func containsRow(rows []email.Row, id string) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// deliveredAt reads the recorded instant of one delivery.
func deliveredAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, outboxID string) time.Time {
	t.Helper()
	var at time.Time
	if err := pool.QueryRow(ctx,
		`SELECT delivered_at FROM cinema.outbox_deliveries WHERE consumer = $1 AND outbox_id = $2::uuid`,
		email.Consumer, outboxID).Scan(&at); err != nil {
		t.Fatalf("read the delivery instant: %v", err)
	}
	return at
}

// publishedAt reads the outbox mark, reported as the zero instant when it is NULL.
func publishedAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, outboxID string) time.Time {
	t.Helper()
	var at *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT published_at FROM cinema.outbox WHERE id = $1::uuid`, outboxID).Scan(&at); err != nil {
		t.Fatalf("read published_at: %v", err)
	}
	if at == nil {
		return time.Time{}
	}
	return *at
}

// cleanupEmailFixture removes the rows the test creates. The venue and the event stay: M1
// makes a published layout immutable and the event is reusable.
func cleanupEmailFixture(pool *pgxpool.Pool) {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM cinema.outbox WHERE payload->>'reservationId' = ANY($1)`,
		`DELETE FROM cinema.reservations WHERE id::text = ANY($1)`,
	} {
		_, _ = pool.Exec(ctx, sql, fixtureReservations)
	}
}

func pointerTo(value string) *string { return &value }
