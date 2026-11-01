// Package email delivers the ticket email as a consumer of the sale's outbox.
//
// It is a consumer and not a delivery mechanism: the checkout already writes a
// `ticket.issued` row inside the transaction that captures the payment, and this worker
// reads those rows. Nothing here is called from the payment path, so a mail provider that
// is down cannot fail a sale — it only means the message is sent on a later pass.
//
// # Why this worker keeps its own progress
//
// `cinema.outbox.published_at` means "every consumer of this topic has acted". The PDF
// worker sets it when it has stored the document, so a second consumer filtering on
// `published_at IS NULL` would find nothing at all and mail nothing — a green build with no
// email, which is the failure this project keeps finding. The outbox comment asks for a
// separate delivery record instead, and `cinema.outbox_deliveries` is it: one row per
// (consumer, outbox row), which makes a retried pass unable to send twice.
//
// # What it deliberately does not do
//
// No bounce handling, no retries with backoff beyond "the next pass", no unsubscribe. The
// address is unverified by product decision, so nothing here may claim the mailbox belongs
// to whoever typed it; the message says as much in its own words.
package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
)

// Errors this package reports to its callers.
var (
	// ErrNotConfigured reports a worker or a store built without what it needs to run.
	ErrNotConfigured = errors.New("email: delivery worker is not configured")
	// ErrUnavailable reports a durable layer or a provider that could not be reached.
	// A pass that returns it is logged and retried on the next tick, never fatal.
	ErrUnavailable = errors.New("email: delivery storage is unavailable")
)

// Consumer names this consumer in `cinema.outbox_deliveries`. It is a constant because the
// name is durable: changing it would make every delivered row look undelivered and mail
// the whole history again.
const Consumer = "email"

// Subject is the one line every ticket email carries. It names the product rather than the
// film: the body is what names the screening.
const Subject = "Your CinemaTicketing tickets"

// Message is one email this worker asks a provider to send.
type Message struct {
	// To is the address the buyer gave. Unverified: see the package comment.
	To string
	// Subject is a single line, never empty.
	Subject string
	// Body is plain text. No HTML: a ticket link is a URL, and plain text avoids a whole
	// class of rendering differences for a message whose only job is to carry it.
	Body string
}

// Sender is the provider boundary: SMTP today, an API tomorrow. It must be safe for
// concurrent use, because one pass may deliver several messages.
type Sender interface {
	Send(ctx context.Context, message Message) error
}

// Row is one outbox row this consumer still has to handle.
type Row struct {
	// ID is `cinema.outbox.id`.
	ID string
	// Payload is the sale the row describes, as the producer wrote it.
	Payload []byte
}

// Store is the durable half of the worker: what to deliver, to whom, and what has already
// been delivered.
type Store interface {
	// Pending returns rows of the ticket topic that this consumer has not delivered yet,
	// oldest first, at most limit of them.
	Pending(ctx context.Context, consumer string, limit int) ([]Row, error)
	// Recipient answers the address stored for a reservation, or "" when the buyer gave
	// none. An error means the answer is unknown, which is not the same as "none".
	Recipient(ctx context.Context, reservationID string) (string, error)
	// Delivered records that this consumer is done with the row. It is idempotent: the
	// second call for the same pair changes nothing.
	Delivered(ctx context.Context, consumer, outboxID string) error
}

// LinkBuilder turns a reservation into the URL the email carries. It is injected rather
// than imported so the signing key stays the caller's business and the worker never holds
// one.
type LinkBuilder func(reservationID string) (string, error)

// WorkerConfig is the process wiring of the email consumer.
type WorkerConfig struct {
	// Interval is the cadence between passes. It must be positive.
	Interval time.Duration
	// Batch is how many rows one pass reads.
	Batch int
	// Timeout bounds one pass, so a provider that stops answering cannot wedge the worker.
	Timeout time.Duration
	// From addresses the message envelope. It must name a mailbox: a provider that
	// rejects the sender would make every pass fail identically.
	From string
	// Logger receives the start, stop and per-row outcomes.
	Logger *slog.Logger
}

// Worker delivers the ticket email for sales the checkout has already recorded.
type Worker struct {
	store  Store
	sender Sender
	link   LinkBuilder
	cfg    WorkerConfig
}

// NewWorker builds the worker and refuses a configuration that could not work, naming the
// missing piece instead of failing on the first pass.
func NewWorker(store Store, sender Sender, link LinkBuilder, cfg WorkerConfig) (*Worker, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: the outbox store is missing", ErrNotConfigured)
	}
	if sender == nil {
		return nil, fmt.Errorf("%w: no mail provider is wired", ErrNotConfigured)
	}
	if link == nil {
		return nil, fmt.Errorf("%w: no ticket link builder is wired", ErrNotConfigured)
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("%w: the cadence must be positive, got %s", ErrNotConfigured, cfg.Interval)
	}
	if cfg.Batch <= 0 {
		return nil, fmt.Errorf("%w: the batch must be positive, got %d", ErrNotConfigured, cfg.Batch)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("%w: the deadline must be positive, got %s", ErrNotConfigured, cfg.Timeout)
	}
	if strings.TrimSpace(cfg.From) == "" {
		return nil, fmt.Errorf("%w: the sender address is missing", ErrNotConfigured)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{store: store, sender: sender, link: link, cfg: cfg}, nil
}

// Run delivers until ctx is cancelled, one bounded pass per tick. It belongs to the
// process, like the PDF worker: it starts with the API and stops with it, so shutdown never
// leaves a goroutine polling a closed pool.
func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.store == nil || w.sender == nil {
		return
	}
	w.cfg.Logger.Info("ticket email worker started",
		"interval", w.cfg.Interval, "batch", w.cfg.Batch, "timeout", w.cfg.Timeout, "from", w.cfg.From)
	defer w.cfg.Logger.Info("ticket email worker stopped")

	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := w.Pass(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.cfg.Logger.Error("ticket email pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pass runs one bounded pass: read a batch, deliver each row, record what was delivered.
//
// A row that cannot be delivered is left unrecorded so the next pass retries it. A row
// whose reservation has no address *is* recorded, with a log line: the buyer declined to
// give one, no later pass can invent one, and retrying for ever would grow the outbox
// without ever sending anything. "Delivered" here means "this consumer is done with the
// row", which is the honest reading for both cases.
func (w *Worker) Pass(ctx context.Context) error {
	passCtx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()

	rows, err := w.store.Pending(passCtx, Consumer, w.cfg.Batch)
	if err != nil {
		return err
	}

	for _, row := range rows {
		if err := w.deliver(passCtx, row); err != nil {
			// One row's provider failure must not stop the others, and must not be
			// recorded: the next pass tries it again.
			w.cfg.Logger.Error("ticket email not delivered", "outbox", row.ID, "error", err)
		}
	}
	return nil
}

// deliver handles one row: resolve the address, build the link, send, record.
func (w *Worker) deliver(ctx context.Context, row Row) error {
	var payload struct {
		ReservationID string `json:"reservationId"`
	}
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		// A payload this worker cannot read is never going to become readable: record it
		// so it stops being retried, and say so loudly. The producer validates what it
		// writes, so this is a defect, not a routine case.
		w.cfg.Logger.Error("ticket email skipped: the outbox payload cannot be decoded",
			"outbox", row.ID, "error", err)
		return w.store.Delivered(ctx, Consumer, row.ID)
	}
	if strings.TrimSpace(payload.ReservationID) == "" {
		w.cfg.Logger.Error("ticket email skipped: the outbox row names no reservation", "outbox", row.ID)
		return w.store.Delivered(ctx, Consumer, row.ID)
	}

	to, err := w.store.Recipient(ctx, payload.ReservationID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(to) == "" {
		w.cfg.Logger.Info("ticket email skipped: the buyer gave no address",
			"outbox", row.ID, "reservation", payload.ReservationID)
		return w.store.Delivered(ctx, Consumer, row.ID)
	}

	link, err := w.link(payload.ReservationID)
	if err != nil {
		return err
	}

	if err := w.sender.Send(ctx, Message{
		To:      to,
		Subject: Subject,
		Body:    body(link),
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return w.store.Delivered(ctx, Consumer, row.ID)
}

// body is the whole message. It states what the link is and what it is not, because the
// email is the one place a buyer reads about the link before clicking it: it expires, and
// whoever holds it can open the tickets.
func body(link string) string {
	return strings.Join([]string{
		"Thanks for your purchase. Your tickets are here:",
		"",
		link,
		"",
		"This link expires, so open it while you still have the email. Anyone with the link",
		"can open the tickets, so treat it like the tickets themselves.",
		"",
		"The PDF of each ticket is downloadable from that page.",
		"",
		"CinemaTicketing",
	}, "\n")
}

// Topic is the outbox topic this consumer reads. It is the producer's own constant, so a
// rename cannot leave the worker reading a topic nobody writes.
func Topic() string { return checkout.OutboxTopicTicketIssued }
