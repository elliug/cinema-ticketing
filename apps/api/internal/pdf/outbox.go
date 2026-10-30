// The eager PDF worker: the consumer of the checkout's ticket.issued outbox rows.
//
// # Why a worker at all
//
// The checkout commits the sale, the tickets and one outbox row in a single
// transaction, and it never waits for the bucket: a captured payment is not a
// successful payment because an object store was up. The PDF is therefore produced
// after the sale, from the durable row the sale wrote. This worker is that producer:
// it reads the unpublished ticket.issued rows, renders and stores one document per
// ticket the payload names, and only then records the row as published.
//
// # Why the buyer is still served when the worker fails
//
// The download route generates on demand (see [Archive.DownloadURL]), so a crashed
// worker, a bucket that was down or an artefact that was deleted never leaves the
// buyer without their ticket. The two paths cannot disagree: both render the same
// [tickets.TicketDetail] through [Render], and both derive the key with [ObjectKey],
// so the worker is an optimisation of latency, never a dependency of correctness.
//
// # What "published" means
//
// The outbox row is marked with `published_at` only after every artefact it names is
// stored, so a crash between the put and the mark is retried, and a retry is cheap:
// an existing object is the answer (the immutability rule), so the worker does not
// read, render or write it again. `published_at` therefore means "every consumer of
// this topic has acted". This worker is the only consumer today; a later relay that
// publishes the same rows to a message bus must not assume `published_at IS NULL` on
// its own, and needs its own cursor or a separate delivery record.
//
// # Rows it abandons
//
// A payload that cannot be decoded, or that names no event and no ticket, can never
// be acted on, so retrying it forever would stall the queue behind it. It is logged
// and marked, and the buyer is unaffected because the download route reads the
// ticket from the database rather than from the outbox.

package pdf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/checkout"
)

// maxBatchesPerTick bounds how many passes one tick runs, so a backlog is drained
// across passes instead of holding the worker in one long loop.
const maxBatchesPerTick = 8

// OutboxRow is one unpublished outbox row as the worker consumes it: the row's
// identifier, which is how it is marked, and the JSON document the checkout wrote.
type OutboxRow struct {
	// ID is the outbox row identifier (`cinema.outbox.id`).
	ID string
	// Payload is the stored JSON document, decoded by the worker into the producer's
	// own payload type.
	Payload []byte
}

// OutboxSource is the durable side of the worker. It is an interface so the worker's
// rules can be tested without a database, and so a different queue can replace it
// without touching how a document is produced.
type OutboxSource interface {
	// PendingTicketIssued answers up to limit unpublished ticket.issued rows, oldest
	// first. A read that cannot be completed is an error, never an empty queue.
	PendingTicketIssued(ctx context.Context, limit int) ([]OutboxRow, error)
	// MarkPublished records that every artefact the row names has been stored. It is
	// called only after that is true, so a row is retried until it is.
	MarkPublished(ctx context.Context, rowID string) error
}

// WorkerConfig is the worker's cadence and bounds. All three durations and the batch
// are required: a worker with no cadence would spin, one with no deadline could hold
// a connection forever, and one with no batch could read an unbounded row set.
type WorkerConfig struct {
	// Interval is how long the worker waits between passes.
	Interval time.Duration
	// Batch is how many outbox rows one pass reads.
	Batch int
	// Timeout bounds one pass and, within it, one row.
	Timeout time.Duration
	// Logger receives the start, stop and per-row failures. A nil logger uses
	// slog.Default(), so a caller cannot lose the worker's diagnostics by omission.
	Logger *slog.Logger
}

// Worker renders and stores the PDF of every ticket an issued order names.
type Worker struct {
	source  OutboxSource
	archive *Archive
	cfg     WorkerConfig
}

// NewWorker builds the worker over an outbox source and the archive. Both are
// required: without a source there is nothing to consume, and without an archive there
// is nowhere to store what is consumed.
func NewWorker(source OutboxSource, archive *Archive, cfg WorkerConfig) (*Worker, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: the outbox source is missing", ErrNotConfigured)
	}
	if archive == nil {
		return nil, fmt.Errorf("%w: the artefact archive is missing", ErrNotConfigured)
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("%w: the worker cadence must be positive, got %s", ErrNotConfigured, cfg.Interval)
	}
	if cfg.Batch <= 0 {
		return nil, fmt.Errorf("%w: the worker batch must be positive, got %d", ErrNotConfigured, cfg.Batch)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("%w: the worker deadline must be positive, got %s", ErrNotConfigured, cfg.Timeout)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{source: source, archive: archive, cfg: cfg}, nil
}

// Run consumes the outbox until ctx is cancelled, one bounded pass per tick.
//
// It belongs to the process: it starts with the API, keeps its cadence, and stops with
// the process context, so shutdown never leaves a goroutine polling a closed pool.
func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.source == nil || w.archive == nil {
		return
	}
	w.cfg.Logger.Info("ticket PDF worker started", "interval", w.cfg.Interval, "batch", w.cfg.Batch, "timeout", w.cfg.Timeout)
	defer w.cfg.Logger.Info("ticket PDF worker stopped")

	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := w.drain(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.cfg.Logger.Error("ticket PDF worker pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// drain runs passes until the outbox stops answering a full batch, bounded by
// maxBatchesPerTick so one tick cannot run longer than its share of the process.
func (w *Worker) drain(ctx context.Context) error {
	for pass := 1; pass <= maxBatchesPerTick; pass++ {
		completed, err := w.Once(ctx)
		if err != nil {
			return err
		}
		if completed < w.cfg.Batch {
			return nil
		}
	}
	return nil
}

// Once runs one bounded pass and answers how many rows it completed.
//
// A row that cannot be processed does not stop the rows behind it: it stays
// unpublished and is retried on the next pass, while the rest of the batch is
// processed. The failures are reported together, so Run logs one line per pass.
func (w *Worker) Once(ctx context.Context) (int, error) {
	if w == nil || w.source == nil || w.archive == nil {
		return 0, fmt.Errorf("%w: the worker was not built by NewWorker", ErrNotConfigured)
	}

	readCtx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()
	rows, err := w.source.PendingTicketIssued(readCtx, w.cfg.Batch)
	if err != nil {
		return 0, fmt.Errorf("pdf: read the ticket.issued outbox rows: %w", err)
	}

	completed := 0
	var failures []error
	for _, row := range rows {
		if err := w.process(ctx, row); err != nil {
			failures = append(failures, err)
			continue
		}
		completed++
	}
	return completed, errors.Join(failures...)
}

// process stores the PDF of every ticket one outbox row names and then marks the row.
//
// The order is the retry contract: nothing is marked before every artefact exists, so
// a failure at any point leaves the row pending.
func (w *Worker) process(ctx context.Context, row OutboxRow) error {
	rowCtx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()

	var issued checkout.TicketIssued
	if err := json.Unmarshal(row.Payload, &issued); err != nil {
		return w.abandon(rowCtx, row, "the payload is not a ticket.issued document: "+err.Error())
	}
	if strings.TrimSpace(issued.EventID) == "" {
		return w.abandon(rowCtx, row, "the payload names no event, so no artefact can be addressed")
	}
	if len(issued.TicketIDs) == 0 {
		return w.abandon(rowCtx, row, "the payload names no ticket, so there is no artefact to store")
	}

	for _, ticketID := range issued.TicketIDs {
		if _, err := w.archive.EnsureIssued(rowCtx, issued.EventID, ticketID); err != nil {
			// The ticket id is not a secret and is the only thing that makes this
			// diagnosable; the artefact and its URL never appear in a log.
			return fmt.Errorf("pdf: store the ticket PDF of ticket %s of outbox row %s: %w", ticketID, row.ID, err)
		}
	}

	if err := w.source.MarkPublished(rowCtx, row.ID); err != nil {
		return fmt.Errorf("pdf: mark outbox row %s published: %w", row.ID, err)
	}
	return nil
}

// abandon settles a row that can never be acted on: it is logged and marked, so it
// cannot stall the queue behind it. The buyer is unaffected, because the download
// route reads the ticket from the database instead of from the outbox.
func (w *Worker) abandon(ctx context.Context, row OutboxRow, reason string) error {
	w.cfg.Logger.Error("ticket PDF worker abandoned an outbox row", "outbox_row", row.ID, "reason", reason)
	if err := w.source.MarkPublished(ctx, row.ID); err != nil {
		return fmt.Errorf("pdf: mark outbox row %s published: %w", row.ID, err)
	}
	return nil
}

// The durable reads and the one write of the worker. The topic is a parameter of the
// read, so the producer's own constant is the single source of its name.
var (
	// pendingTicketIssuedQuery reads unpublished rows of one topic, oldest first, which
	// is the order the partial index on (created_at) WHERE published_at IS NULL serves.
	pendingTicketIssuedQuery = `
SELECT id::text, payload
  FROM cinema.outbox
 WHERE topic = $1 AND published_at IS NULL
 ORDER BY created_at, id
 LIMIT $2`

	// markOutboxPublishedQuery records that every consumer has acted on the row. The
	// predicate keeps a concurrent worker from rewriting the first mark.
	markOutboxPublishedQuery = `
UPDATE cinema.outbox SET published_at = now() WHERE id = $1::uuid AND published_at IS NULL`
)

// OutboxStore is the PostgreSQL [OutboxSource]: the table the checkout already writes.
// It opens its own pool because the worker has its own lifetime, and it is never used
// to write anything but the publication mark.
type OutboxStore struct {
	pool *pgxpool.Pool
}

// NewOutboxStore opens a pooled connection for the worker.
func NewOutboxStore(ctx context.Context, databaseURL string) (*OutboxStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open the outbox database: %w", err)
	}
	return &OutboxStore{pool: pool}, nil
}

// Close releases pooled connections. It is nil-safe so the process wiring can defer it
// unconditionally.
func (s *OutboxStore) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

// PendingTicketIssued answers up to limit unpublished rows of the ticket.issued topic,
// oldest first.
func (s *OutboxStore) PendingTicketIssued(ctx context.Context, limit int) ([]OutboxRow, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("%w: the outbox store was not opened by NewOutboxStore", ErrNotConfigured)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a batch of %d rows would never make progress", ErrNotConfigured, limit)
	}

	rows, err := s.pool.Query(ctx, pendingTicketIssuedQuery, checkout.OutboxTopicTicketIssued, limit)
	if err != nil {
		return nil, fmt.Errorf("pdf: read the ticket.issued outbox rows: %w", err)
	}
	defer rows.Close()

	var pending []OutboxRow
	for rows.Next() {
		var row OutboxRow
		if err := rows.Scan(&row.ID, &row.Payload); err != nil {
			return nil, fmt.Errorf("pdf: read an outbox row: %w", err)
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pdf: read the ticket.issued outbox rows: %w", err)
	}
	return pending, nil
}

// MarkPublished records that every artefact the row names is stored.
func (s *OutboxStore) MarkPublished(ctx context.Context, rowID string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("%w: the outbox store was not opened by NewOutboxStore", ErrNotConfigured)
	}
	if strings.TrimSpace(rowID) == "" {
		return fmt.Errorf("%w: an outbox row with no identifier cannot be marked", ErrNotConfigured)
	}
	if _, err := s.pool.Exec(ctx, markOutboxPublishedQuery, rowID); err != nil {
		return fmt.Errorf("pdf: mark outbox row %s published: %w", rowID, err)
	}
	return nil
}

// compile-time proof that the PostgreSQL store is the source the worker consumes.
var _ OutboxSource = (*OutboxStore)(nil)
