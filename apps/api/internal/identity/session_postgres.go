// The durable half of the session domain: four statements over cinema.sessions.
//
// What the service above cannot enforce, this file makes the database enforce, because the
// service is not the only thing that will ever read this table — a repair script, a console
// an operator has open, a later task with a different idea of what "live" means. So the
// liveness predicate is part of every statement rather than a condition the Go caller is
// trusted to have applied:
//
//   - the read only answers rows where `revoked_at IS NULL AND expires_at > $now`, so an
//     expired session cannot authenticate even if the caller asking forgot to check, and a
//     revoked one stops being a credential the instant the row is stamped;
//   - the refresh carries the same predicate, so a dead row never acquires a sighting;
//   - the revocation is guarded by `revoked_at IS NULL`, so closing twice cannot move the
//     instant at which the session actually ended, and a statement that matched no row is
//     reported without saying which of the possible reasons it was.
//
// The token itself never reaches this file: [PostgresSessionStore] is handed the hash, which
// is what the table holds (S1). There is no column for a token, no query that would accept
// one, and no format string into which one could be pasted.
package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// createSessionQuery writes the row [SessionService.Open] built, including the creation
// instant it decided on: the row and the session handed back to the caller must describe the
// same thing, so `created_at` is supplied here and not left to the column default.
//
// `last_seen_at` is written as NULL for a session nobody has presented yet, and `revoked_at`
// is not written at all, because a row cannot be created closed.
const createSessionQuery = `
INSERT INTO cinema.sessions (id, customer_id, token_hash, created_at, last_seen_at, expires_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)`

// findSessionQuery answers one live session by the hash of its token.
//
// The predicate is the contract S1 makes to a person: a session that is over — by time or by
// having been closed — is not a session, whatever the service believes. It lives in the
// statement so there is exactly one definition of "live" in the system, and this is it.
const findSessionQuery = `
SELECT s.id::text, s.customer_id::text, s.created_at, s.last_seen_at, s.expires_at
  FROM cinema.sessions s
 WHERE s.token_hash = $1
   AND s.revoked_at IS NULL
   AND s.expires_at > $2`

// touchSessionQuery records a sighting of a live session.
//
// Only `last_seen_at` is written: `expires_at` is not mentioned, which is how "a session is
// not renewed silently" stays true even if somebody later calls this in a loop. The predicate
// is the read's, so an expired or revoked row is not stamped — a sighting of a session that is
// already over would be a false entry in the audit this column exists for.
const touchSessionQuery = `
UPDATE cinema.sessions
   SET last_seen_at = $2
 WHERE token_hash = $1
   AND revoked_at IS NULL
   AND expires_at > $2`

// revokeSessionQuery closes a session without deleting its row.
//
// `revoked_at IS NULL` is what makes closing idempotent in the strict sense: the first call
// stamps the instant the session ended, and a second call — a retried request, a browser
// sending sign-out twice — matches nothing and therefore cannot rewrite that instant. The call
// reports that it matched nothing, without saying whether the hash was unknown or the row was
// already closed: the store does not know the difference from a row count, and it should not.
const revokeSessionQuery = `
UPDATE cinema.sessions
   SET revoked_at = $2
 WHERE token_hash = $1
   AND revoked_at IS NULL`

// PostgresSessionStore is the durable implementation of [SessionStore], over the columns
// 0010_identity declares.
type PostgresSessionStore struct {
	pool *pgxpool.Pool
}

// compile-time proof that the store satisfies the domain's contract.
var _ SessionStore = (*PostgresSessionStore)(nil)

// NewSessionStore opens a pool and proves it answers before returning, so a deployment whose
// database is unreachable fails at startup rather than on the first sign-in attempt.
func NewSessionStore(ctx context.Context, dsn string) (*PostgresSessionStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: open the pool: %v", ErrSessionUnavailable, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("%w: the database did not answer: %v", ErrSessionUnavailable, err)
	}
	return &PostgresSessionStore{pool: pool}, nil
}

// Close releases the pooled connections.
func (s *PostgresSessionStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Create writes one session row. The hash is the credential the row is found by; there is no
// statement in this file that would take the token.
func (s *PostgresSessionStore) Create(ctx context.Context, session Session, tokenHash []byte) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("%w: the session store is not configured", ErrSessionUnavailable)
	}
	if _, err := s.pool.Exec(ctx, createSessionQuery,
		session.ID,
		session.CustomerID,
		tokenHash,
		session.CreatedAt,
		session.LastSeenAt,
		session.ExpiresAt,
	); err != nil {
		// The hash is not in the message. A failure names the operation, never the credential.
		return fmt.Errorf("%w: write the session: %v", ErrSessionUnavailable, err)
	}
	return nil
}

// Find answers the live session a token hash belongs to.
//
// A row that is expired, revoked or absent is [ErrNoSession], and the three are not
// distinguished here either: this store cannot tell them apart usefully, and the layer above
// is not allowed to. Anything else — an unreachable database, a value PostgreSQL refused — is
// [ErrSessionUnavailable], because "I cannot answer" is a different answer from "no session"
// and must never be reported as a sign-out.
func (s *PostgresSessionStore) Find(ctx context.Context, tokenHash []byte, now time.Time) (Session, error) {
	if s == nil || s.pool == nil {
		return Session{}, fmt.Errorf("%w: the session store is not configured", ErrSessionUnavailable)
	}
	var session Session
	err := s.pool.QueryRow(ctx, findSessionQuery, tokenHash, now.UTC()).Scan(
		&session.ID,
		&session.CustomerID,
		&session.CreatedAt,
		&session.LastSeenAt,
		&session.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("%w: read the session: %v", ErrSessionUnavailable, err)
	}
	return session, nil
}

// Touch records that the session was presented at now, and changes nothing else.
//
// A hash that matches no live row is not an error: the refresh is bookkeeping, the caller has
// already established that the session is live, and a row that closed between the read and
// this write is exactly the race the predicate is there for. The caller ignores this error
// anyway — see [SessionService.Authenticate] — but the statement is still written to match
// nothing rather than to lie about a dead session.
func (s *PostgresSessionStore) Touch(ctx context.Context, tokenHash []byte, now time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("%w: the session store is not configured", ErrSessionUnavailable)
	}
	if _, err := s.pool.Exec(ctx, touchSessionQuery, tokenHash, now.UTC()); err != nil {
		return fmt.Errorf("%w: record the sighting: %v", ErrSessionUnavailable, err)
	}
	return nil
}

// Revoke closes the session a token hash belongs to, keeping the row.
//
// The row is kept on purpose: a deleted row says "signed out" only at the instant it happens
// and nothing afterwards, while `revoked_at` records that the end was chosen and when. When
// the statement matches no row — the hash was never issued, or the session was already closed
// — the answer is [ErrNoSession], without a reason, because the row count does not carry one.
func (s *PostgresSessionStore) Revoke(ctx context.Context, tokenHash []byte, now time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("%w: the session store is not configured", ErrSessionUnavailable)
	}
	tag, err := s.pool.Exec(ctx, revokeSessionQuery, tokenHash, now.UTC())
	if err != nil {
		return fmt.Errorf("%w: close the session: %v", ErrSessionUnavailable, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoSession
	}
	return nil
}
