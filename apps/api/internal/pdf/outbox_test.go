// Behaviour tests for the eager PDF worker: the consumer of the checkout's
// ticket.issued outbox rows.
//
// They need no database and no bucket. The outbox is a fake that returns rows and
// records which ones were marked, and the object store is the same fake the archive
// tests use, so the interesting rules are observable: one document per ticket the
// payload names, an existing artefact is never rewritten, and a row is only marked
// once every artefact it names is stored — which is what makes a crash or a failed
// put retry instead of losing the ticket.
package pdf_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/pdf"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// fakeOutbox is the durable outbox as the worker sees it: unpublished rows in
// publication order, and the marks it made.
type fakeOutbox struct {
	rows     []pdf.OutboxRow
	readErr  error
	markErr  error
	limits   []int
	pending  []string
	markCall []string
}

func (f *fakeOutbox) PendingTicketIssued(_ context.Context, limit int) ([]pdf.OutboxRow, error) {
	f.limits = append(f.limits, limit)
	if f.readErr != nil {
		return nil, f.readErr
	}
	// Only the rows that are still unpublished: marking one removes it from the next
	// read, exactly as the durable query does.
	rows := make([]pdf.OutboxRow, 0, len(f.rows))
	for _, row := range f.rows {
		if contains(f.pending, row.ID) {
			continue
		}
		rows = append(rows, row)
		if len(rows) == limit {
			break
		}
	}
	return rows, nil
}

func (f *fakeOutbox) MarkPublished(_ context.Context, rowID string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.markCall = append(f.markCall, rowID)
	f.pending = append(f.pending, rowID)
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// issuedRow builds the outbox row the checkout writes for one captured sale, from the
// producer's own payload type so the test cannot drift from it.
func issuedRow(t *testing.T, rowID, eventID string, ticketIDs ...string) pdf.OutboxRow {
	t.Helper()
	payload, err := json.Marshal(checkout.TicketIssued{
		ReservationID: "00000000-0000-0000-0000-00000000d0a2",
		EventID:       eventID,
		AttemptID:     "00000000-0000-0000-0000-00000000d0a5",
		AmountCents:   1998,
		TicketIDs:     ticketIDs,
		SeatIDs:       []int64{11, 12},
	})
	if err != nil {
		t.Fatalf("marshal the outbox payload: %v", err)
	}
	return pdf.OutboxRow{ID: rowID, Payload: payload}
}

func discardPDFLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// workerFixture builds the worker over the archive fixture and a fake outbox.
func workerFixture(t *testing.T, rows ...pdf.OutboxRow) (*pdf.Archive, *pdf.Worker, *fakeObjects, *fakeDetails, *fakeOutbox) {
	t.Helper()
	archive, objects, details := archiveFixture(t, time.Minute)
	outbox := &fakeOutbox{rows: rows}
	worker, err := pdf.NewWorker(outbox, archive, pdf.WorkerConfig{
		Interval: time.Minute,
		Batch:    10,
		Timeout:  time.Second,
		Logger:   discardPDFLogger(),
	})
	if err != nil {
		t.Fatalf("NewWorker returned error: %v", err)
	}
	return archive, worker, objects, details, outbox
}

// TestWorkerStoresThePDFOfEveryTicketOfAnIssuedOrder pins the eager path: the
// payload of one captured sale names its tickets, and each of them gets its own
// document, stored under its own key, after which the row is marked published.
func TestWorkerStoresThePDFOfEveryTicketOfAnIssuedOrder(t *testing.T) {
	eventID := "00000000-0000-0000-0000-00000000d0a3"
	first := "00000000-0000-0000-0000-00000000d0a1"
	second := "00000000-0000-0000-0000-00000000d0a9"
	_, worker, objects, details, outbox := workerFixture(t, issuedRow(t, "row-1", eventID, first, second))

	completed, err := worker.Once(t.Context())
	if err != nil {
		t.Fatalf("Once returned error: %v", err)
	}
	if completed != 1 {
		t.Fatalf("Once completed %d rows, want 1", completed)
	}

	if len(objects.puts) != 2 {
		t.Fatalf("the worker stored %d documents, want one per ticket", len(objects.puts))
	}
	for index, ticketID := range []string{first, second} {
		want, err := pdf.ObjectKey(eventID, ticketID)
		if err != nil {
			t.Fatalf("ObjectKey returned error: %v", err)
		}
		if objects.puts[index].key != want {
			t.Fatalf("document %d was stored under %q, want %q", index, objects.puts[index].key, want)
		}
		if !strings.Contains(visibleText(t, objects.puts[index].data), ticketID) {
			t.Fatalf("document %d does not carry the ticket id %q", index, ticketID)
		}
	}
	if len(details.asked) != 2 {
		t.Fatalf("the worker read %v, want both issued tickets", details.asked)
	}
	if len(outbox.markCall) != 1 || outbox.markCall[0] != "row-1" {
		t.Fatalf("marked rows = %v, want row-1 published", outbox.markCall)
	}
}

// TestWorkerNeverRewritesAnExistingArtefact pins immutability on the eager path: a
// redelivered or replayed row finds the objects already stored and writes nothing.
func TestWorkerNeverRewritesAnExistingArtefact(t *testing.T) {
	eventID := "00000000-0000-0000-0000-00000000d0a3"
	ticketID := "00000000-0000-0000-0000-00000000d0a1"
	_, worker, objects, details, outbox := workerFixture(t, issuedRow(t, "row-1", eventID, ticketID))

	key, err := pdf.ObjectKey(eventID, ticketID)
	if err != nil {
		t.Fatalf("ObjectKey returned error: %v", err)
	}
	objects.stored[key] = []byte("%PDF-1.4 as sold")

	if _, err := worker.Once(t.Context()); err != nil {
		t.Fatalf("Once returned error: %v", err)
	}
	if len(objects.puts) != 0 {
		t.Fatalf("the worker rewrote an existing artefact (%d puts)", len(objects.puts))
	}
	if len(details.asked) != 0 {
		t.Fatalf("the worker read %v although the artefact exists", details.asked)
	}
	if stored := string(objects.stored[key]); stored != "%PDF-1.4 as sold" {
		t.Fatalf("the stored artefact changed to %q", stored)
	}
	if len(outbox.markCall) != 1 {
		t.Fatalf("marked rows = %v, want the row published: every artefact it names already exists", outbox.markCall)
	}
}

// TestWorkerRetriesARowWhoseArtefactCouldNotBeStored pins the retry contract: a row is
// marked published only after every artefact it names is stored, so a failed put (or a
// crash between the put and the mark) is retried instead of losing the ticket.
func TestWorkerRetriesARowWhoseArtefactCouldNotBeStored(t *testing.T) {
	eventID := "00000000-0000-0000-0000-00000000d0a3"
	ticketID := "00000000-0000-0000-0000-00000000d0a1"
	_, worker, objects, _, outbox := workerFixture(t, issuedRow(t, "row-1", eventID, ticketID))

	objects.putErr = errors.New("bucket unavailable")
	completed, err := worker.Once(t.Context())
	if err == nil {
		t.Fatal("Once reported no error although the artefact could not be stored")
	}
	if completed != 0 {
		t.Fatalf("Once completed %d rows, want 0", completed)
	}
	if len(outbox.markCall) != 0 {
		t.Fatalf("marked rows = %v although nothing was stored", outbox.markCall)
	}

	// The bucket comes back: the next pass stores the artefact and publishes the row.
	objects.putErr = nil
	if completed, err = worker.Once(t.Context()); err != nil || completed != 1 {
		t.Fatalf("the retry = (%d, %v), want (1, nil)", completed, err)
	}
	if len(objects.puts) != 1 {
		t.Fatalf("the retry stored %d documents, want one", len(objects.puts))
	}
	if len(outbox.markCall) != 1 || outbox.markCall[0] != "row-1" {
		t.Fatalf("marked rows = %v, want row-1 published after the retry", outbox.markCall)
	}
}

// TestWorkerPublishesARowThatNamesNoTicket pins the one row it abandons: a payload that
// cannot be decoded (or names no ticket and no event) can never be acted on, so
// retrying it forever would stall the queue behind it. It is marked published and
// logged, and the buyer is still served by the on-demand download path.
func TestWorkerPublishesARowThatNamesNoTicket(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"not JSON at all", "not json"},
		{"an empty object", `{}`},
		{"an event with no tickets", `{"eventId":"00000000-0000-0000-0000-00000000d0a3","ticketIds":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, worker, objects, details, outbox := workerFixture(t,
				pdf.OutboxRow{ID: "row-1", Payload: []byte(tc.payload)})

			completed, err := worker.Once(t.Context())
			if err != nil {
				t.Fatalf("Once returned error: %v", err)
			}
			if completed != 1 {
				t.Fatalf("Once completed %d rows, want the unusable row settled", completed)
			}
			if len(objects.puts) != 0 || len(details.asked) != 0 {
				t.Fatalf("an unusable payload stored %d documents and read %v, want none", len(objects.puts), details.asked)
			}
			if len(outbox.markCall) != 1 {
				t.Fatalf("marked rows = %v, want the unusable row published", outbox.markCall)
			}
		})
	}
}

// TestWorkerKeepsGoingPastARowItCannotProcess pins that one failing sale cannot starve
// the ones behind it: the batch continues, and the failed row is the one retried next
// time.
func TestWorkerKeepsGoingPastARowItCannotProcess(t *testing.T) {
	eventID := "00000000-0000-0000-0000-00000000d0a3"
	brokenTicket := "00000000-0000-0000-0000-00000000d0a1"
	goodTicket := "00000000-0000-0000-0000-00000000d0a9"
	_, worker, objects, details, outbox := workerFixture(t,
		issuedRow(t, "row-broken", eventID, brokenTicket),
		issuedRow(t, "row-good", eventID, goodTicket))
	details.err = tickets.ErrNotFound

	if _, err := worker.Once(t.Context()); err == nil {
		t.Fatal("Once reported no error although a ticket could not be read")
	}
	if len(outbox.markCall) != 0 {
		t.Fatalf("marked rows = %v, want none: both rows name an unreadable ticket", outbox.markCall)
	}

	// The database answers again: both rows are stored and published.
	details.err = nil
	if _, err := worker.Once(t.Context()); err != nil {
		t.Fatalf("Once returned error: %v", err)
	}
	if len(objects.puts) != 2 {
		t.Fatalf("the retry stored %d documents, want both", len(objects.puts))
	}
	if len(outbox.markCall) != 2 {
		t.Fatalf("marked rows = %v, want both published", outbox.markCall)
	}
}

// TestWorkerReportsAnUnreadableOutbox pins that a database that cannot be read is an
// error, not an empty queue: the worker has to say it could not look.
func TestWorkerReportsAnUnreadableOutbox(t *testing.T) {
	_, worker, _, _, outbox := workerFixture(t)
	outbox.readErr = errors.New("database unreachable")

	if completed, err := worker.Once(t.Context()); err == nil || completed != 0 {
		t.Fatalf("Once = (%d, %v), want (0, an error)", completed, err)
	}
}

// TestWorkerBoundsOnePassToItsBatch pins that one pass reads a bounded number of rows:
// a backlog is drained across passes instead of holding the worker in one long loop.
func TestWorkerBoundsOnePassToItsBatch(t *testing.T) {
	eventID := "00000000-0000-0000-0000-00000000d0a3"
	_, worker, _, _, outbox := workerFixture(t,
		issuedRow(t, "row-1", eventID, "00000000-0000-0000-0000-00000000d0a1"),
		issuedRow(t, "row-2", eventID, "00000000-0000-0000-0000-00000000d0a9"))

	if _, err := worker.Once(t.Context()); err != nil {
		t.Fatalf("Once returned error: %v", err)
	}
	if len(outbox.limits) == 0 {
		t.Fatal("the worker never read the outbox")
	}
	for _, limit := range outbox.limits {
		if limit <= 0 || limit > 10 {
			t.Fatalf("the worker asked for %d rows, want the configured batch of 10", limit)
		}
	}
}

// TestNewWorkerFailsClosed pins the construction rules: without a source or without an
// archive there is nothing to consume, and a worker with no cadence, batch or deadline
// would either spin or hold a database connection forever.
func TestNewWorkerFailsClosed(t *testing.T) {
	archive, _, _ := archiveFixture(t, time.Minute)
	outbox := &fakeOutbox{}

	valid := pdf.WorkerConfig{Interval: time.Minute, Batch: 10, Timeout: time.Second, Logger: discardPDFLogger()}

	if _, err := pdf.NewWorker(nil, archive, valid); !errors.Is(err, pdf.ErrNotConfigured) {
		t.Errorf("NewWorker without a source = %v, want ErrNotConfigured", err)
	}
	if _, err := pdf.NewWorker(outbox, nil, valid); !errors.Is(err, pdf.ErrNotConfigured) {
		t.Errorf("NewWorker without an archive = %v, want ErrNotConfigured", err)
	}
	for _, broken := range []pdf.WorkerConfig{
		{Interval: 0, Batch: 10, Timeout: time.Second},
		{Interval: time.Minute, Batch: 0, Timeout: time.Second},
		{Interval: time.Minute, Batch: 10, Timeout: 0},
	} {
		if _, err := pdf.NewWorker(outbox, archive, broken); !errors.Is(err, pdf.ErrNotConfigured) {
			t.Errorf("NewWorker(%+v) = %v, want ErrNotConfigured", broken, err)
		}
	}
}

// TestWorkerStopsWithTheProcess pins that Run is bound to the process context: a
// shutting-down API does not leave a goroutine polling the database.
func TestWorkerStopsWithTheProcess(t *testing.T) {
	archive, _, _ := archiveFixture(t, time.Minute)
	outbox := &fakeOutbox{}
	worker, err := pdf.NewWorker(outbox, archive, pdf.WorkerConfig{
		Interval: time.Hour, Batch: 10, Timeout: time.Second, Logger: discardPDFLogger(),
	})
	if err != nil {
		t.Fatalf("NewWorker returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestWorkerRunDrainsABacklogAndThenWaits pins the loop: one Run pass stores every
// pending artefact it can, and the loop then waits for its next tick instead of
// re-reading the outbox in a spin.
func TestWorkerRunDrainsABacklogAndThenWaits(t *testing.T) {
	eventID := "00000000-0000-0000-0000-00000000d0a3"
	archive, objects, _ := archiveFixture(t, time.Minute)
	outbox := &fakeOutbox{rows: []pdf.OutboxRow{
		issuedRow(t, "row-1", eventID, "00000000-0000-0000-0000-00000000d0a1"),
	}}
	worker, err := pdf.NewWorker(outbox, archive, pdf.WorkerConfig{
		Interval: time.Hour, Batch: 10, Timeout: time.Second, Logger: discardPDFLogger(),
	})
	if err != nil {
		t.Fatalf("NewWorker returned error: %v", err)
	}

	// The worker runs in its own goroutine, so the test waits for the fake store to
	// report the write instead of reading its records concurrently.
	objects.wrote = make(chan struct{}, 4)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()

	select {
	case <-objects.wrote:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("the worker did not store the pending artefact within five seconds")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	// A second pass with nothing pending writes nothing.
	reads := len(outbox.limits)
	if _, err := worker.Once(t.Context()); err != nil {
		t.Fatalf("Once returned error: %v", err)
	}
	if len(objects.puts) != 1 {
		t.Fatalf("the worker stored %d documents, want one", len(objects.puts))
	}
	if len(outbox.limits) <= reads {
		t.Fatal("Once did not ask the outbox for work")
	}
}

// Gated PostgreSQL evidence. Set M3_PDF_DATABASE_URL to a throwaway database with
// migrations 0001-0006 applied and the test consumes a real outbox row: the row is
// inserted as the checkout inserts it, the worker reads it, stores the artefact through
// the archive and marks it published, and a second read no longer returns it.
//
// The default unit suite skips it, so no database is needed to test this package. It
// inserts and deletes only its own rows.
func TestOutboxWorkerRoundTripAgainstPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("M3_PDF_DATABASE_URL"))
	if dsn == "" {
		t.Skipf("M3_PDF_DATABASE_URL is not set: this test needs a database with migrations 0001-0006 applied")
	}

	ctx := t.Context()
	store, err := pdf.NewOutboxStore(ctx, dsn)
	if err != nil {
		t.Fatalf("NewOutboxStore returned error: %v", err)
	}
	defer store.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open the test pool: %v", err)
	}
	// Registered before the row cleanup, so the row is removed with an open pool (the
	// cleanups run last in, first out).
	t.Cleanup(pool.Close)

	eventID := "00000000-0000-0000-0000-00000000d0a3"
	ticketID := "00000000-0000-0000-0000-00000000d0a1"
	rowID := testUUID(t)

	// The row is written exactly as the checkout writes it: a topic, a JSON payload and
	// no publication mark.
	payload := issuedRow(t, rowID, eventID, ticketID).Payload
	if _, err := pool.Exec(ctx,
		`INSERT INTO cinema.outbox (id, topic, payload) VALUES ($1::uuid, $2, $3::jsonb)`,
		rowID, checkout.OutboxTopicTicketIssued, string(payload)); err != nil {
		t.Fatalf("insert the outbox row: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM cinema.outbox WHERE id = $1::uuid`, rowID); err != nil {
			t.Errorf("remove the outbox row: %v", err)
		}
	})

	rows, err := store.PendingTicketIssued(ctx, 50)
	if err != nil {
		t.Fatalf("PendingTicketIssued returned error: %v", err)
	}
	if !outboxRowsContain(rows, rowID) {
		t.Fatalf("the unpublished row %s was not read: %d rows", rowID, len(rows))
	}

	archive, objects, _ := archiveFixture(t, time.Minute)
	worker, err := pdf.NewWorker(store, archive, pdf.WorkerConfig{
		Interval: time.Minute, Batch: 50, Timeout: 5 * time.Second, Logger: discardPDFLogger(),
	})
	if err != nil {
		t.Fatalf("NewWorker returned error: %v", err)
	}
	if _, err := worker.Once(ctx); err != nil {
		t.Fatalf("Once returned error: %v", err)
	}
	if len(objects.puts) != 1 {
		t.Fatalf("the worker stored %d documents, want one", len(objects.puts))
	}

	var published bool
	if err := pool.QueryRow(ctx,
		`SELECT published_at IS NOT NULL FROM cinema.outbox WHERE id = $1::uuid`, rowID).Scan(&published); err != nil {
		t.Fatalf("read the publication mark: %v", err)
	}
	if !published {
		t.Fatal("the outbox row is still unpublished after the worker stored its artefact")
	}

	rows, err = store.PendingTicketIssued(ctx, 50)
	if err != nil {
		t.Fatalf("PendingTicketIssued returned error: %v", err)
	}
	if outboxRowsContain(rows, rowID) {
		t.Fatal("a published row is still answered as pending work")
	}

	// The immutability rule survives the real store too: the row is gone from the queue,
	// so nothing re-renders the artefact.
	if _, err := worker.Once(ctx); err != nil {
		t.Fatalf("Once returned error: %v", err)
	}
	if len(objects.puts) != 1 {
		t.Fatalf("the worker stored %d documents after the row was published, want one", len(objects.puts))
	}
}

// outboxRowsContain reports whether the read returned one row by identifier.
func outboxRowsContain(rows []pdf.OutboxRow, rowID string) bool {
	for _, row := range rows {
		if row.ID == rowID {
			return true
		}
	}
	return false
}

// testUUID builds a random version 4 UUID, so every run inserts its own row.
func testUUID(t *testing.T) string {
	t.Helper()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatalf("generate a uuid: %v", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
