// The durable half of the email consumer.
//
// Three questions, three statements: which rows are still mine to deliver, who do I send
// to, and record that I am done. The middle one is the reason the address lives on the
// reservation and not in the outbox payload — a queue row is a record of a sale, and
// copying personal data into it would keep that data alive in two places.
package email

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the durable implementation of [Store].
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore opens a pool and proves it answers before returning, so a deployment
// whose database is unreachable fails at startup rather than on the first pass.
func NewPostgresStore(ctx context.Context, url string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("%w: open the pool: %v", ErrUnavailable, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("%w: the database did not answer: %v", ErrUnavailable, err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases the pooled connections.
func (s *PostgresStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// pendingTicketIssuedQuery reads the rows of one topic this consumer has not delivered.
//
// The predicate is a `NOT EXISTS` against the delivery record and **not**
// `published_at IS NULL`. That distinction is the whole reason the delivery table exists:
// `published_at` means "every consumer has acted", so it is set by the PDF worker as soon
// as the document is stored, and a consumer filtering on it would see nothing.
//
// The order is the one the partial index `outbox_pending_by_topic (topic, created_at, id)`
// serves, so a backlog is delivered oldest first rather than in whatever order the planner
// prefers.
const pendingTicketIssuedQuery = `
SELECT o.id::text, o.payload
  FROM cinema.outbox o
 WHERE o.topic = $1
   AND NOT EXISTS (
     SELECT 1
       FROM cinema.outbox_deliveries d
      WHERE d.outbox_id = o.id
        AND d.consumer = $2)
 ORDER BY o.created_at, o.id
 LIMIT $3`

// recipientQuery answers the address stored for a reservation. `COALESCE` turns "the buyer
// gave none" into the empty string, which is the worker's signal to skip and record, so a
// NULL never reaches Go as an error that would be retried for ever.
const recipientQuery = `
SELECT COALESCE(contact_email, '')
  FROM cinema.reservations
 WHERE id = $1::uuid`

// markDeliveredQuery records the delivery. `ON CONFLICT DO NOTHING` makes it idempotent, so
// a pass that crashed between the send and the mark cannot send twice on its retry — it can
// only fail to notice it already sent, which the record prevents on the next attempt.
const markDeliveredQuery = `
INSERT INTO cinema.outbox_deliveries (consumer, outbox_id)
VALUES ($1, $2::uuid)
ON CONFLICT (consumer, outbox_id) DO NOTHING`

// Pending reads at most limit rows of the ticket topic that this consumer has not handled.
func (s *PostgresStore) Pending(ctx context.Context, consumer string, limit int) ([]Row, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: the batch must be positive, got %d", ErrNotConfigured, limit)
	}
	rows, err := s.pool.Query(ctx, pendingTicketIssuedQuery, Topic(), consumer, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: read pending rows: %v", ErrUnavailable, err)
	}
	defer rows.Close()

	pending := make([]Row, 0, limit)
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.ID, &row.Payload); err != nil {
			return nil, fmt.Errorf("%w: read a pending row: %v", ErrUnavailable, err)
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read pending rows: %v", ErrUnavailable, err)
	}
	return pending, nil
}

// Recipient answers the address stored for a reservation, or "" when there is none.
//
// A reservation that no longer exists (a database repaired by hand, say) is reported as no
// address rather than as a failure: there is nothing to send and nothing to retry.
func (s *PostgresStore) Recipient(ctx context.Context, reservationID string) (string, error) {
	var email string
	err := s.pool.QueryRow(ctx, recipientQuery, reservationID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: read the recipient: %v", ErrUnavailable, err)
	}
	return email, nil
}

// Delivered records that this consumer is done with the row.
func (s *PostgresStore) Delivered(ctx context.Context, consumer, outboxID string) error {
	if _, err := s.pool.Exec(ctx, markDeliveredQuery, consumer, outboxID); err != nil {
		return fmt.Errorf("%w: record the delivery: %v", ErrUnavailable, err)
	}
	return nil
}
