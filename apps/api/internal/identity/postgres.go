// The durable half of the identity flow: one row for issuing, and one transaction for
// redeeming.
//
// [PostgresStore] implements both boundaries the domain declares — [AccessLinkStore] for the
// issuer and [RedemptionStore] for the redeemer — over the two tables 0010_identity declares,
// so a deployment wires one store and the two halves of a login cannot be pointed at different
// databases by accident.
//
// # Why redeeming is one transaction and not four calls
//
// Verifying an address is the moment three facts become true at once (S2, S3, D5): the link is
// spent, the account is verified, and the orders previously placed with that address belong to
// it. Written in that order inside one transaction, the whole thing commits or none of it does.
// Split into four calls, every prefix of that list is a state somebody could be left in — a
// spent link that verified nobody, an account verified whose orders stayed unowned, a session
// that was never written because the row before it failed — and no later repair could tell which
// of them happened. A person retrying a login is not supposed to be the recovery mechanism.
//
// # Why the session is written here
//
// The session row belongs to this transaction precisely because it must not exist outside it.
// [SessionService.Open] would write it in its own statement, before or after the account exists,
// and either order leaves a window where somebody is signed in as nobody or where a verified
// address has bought a session that was never written. So this file writes the row itself, with
// the hash it is given, and the account the transaction just resolved — and it never sees the
// token, only its digest (S1).
//
// # What one redemption answers besides "it worked"
//
// It answers which account the link verified and how many orders that address claimed, because
// D5 records the claim in the log and nowhere else: no column says "claimed", and a table of
// past claims would be a second history of the same fact, aging beside the rows it describes.
// The count is the claim statement's own `RowsAffected`, read after the commit so it describes
// work that survived; a number computed separately from the write could describe nothing at
// all.
//
// # What the statements refuse
//
// The guards live in the SQL as well as in the calling code, because this store is not the only
// thing that will ever read these tables — a repair script, a console, a later task with a
// different idea of what "spent" means:
//
//   - spending is `consumed_at IS NULL`, so a second presentation matches no row and cannot
//     move the instant the link was used;
//   - the row's own `expires_at` is checked against the instant of the redemption, so a link
//     whose lifetime ran out is refused even if the signature would still accept it — the row
//     is the record of when the link was issued, and that is what its lifetime is measured
//     from;
//   - the address in the row must be the address in the claim; see [PostgresStore.Redeem];
//   - the account's `verified_at` keeps its first value, so a second sign-in records a new
//     session without rewriting when the mailbox was first proved;
//   - claiming orders matches the address exactly and only touches rows that have no account
//     yet, so a verified address cannot take over an order somebody else already owns, and the
//     redemption reports the rows that statement matched — the claim is recorded in the log,
//     and a number computed apart from the write could be a number about nothing.
package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// saveAccessLinkQuery records one issued link. `created_at` is left to the column default
// because it is the start of the row's lifetime and this instant is the database's to keep;
// `expires_at` is supplied, because it is the instant the signature names and the two must
// agree. `consumed_at` is not written: a row cannot be created spent.
const saveAccessLinkQuery = `
INSERT INTO cinema.access_links (nonce, email, expires_at)
VALUES ($1::uuid, $2, $3)`

// consumeAccessLinkQuery spends one link and answers what the row says about it.
//
// The guard is `consumed_at IS NULL`, so out of two concurrent presentations of one link exactly
// one matches — the row is the lock — and the instant it was spent is written once. Matching
// nothing is not an error the statement can explain: the link was used, was never issued or has
// expired, and all three are [ErrLinkSpent].
const consumeAccessLinkQuery = `
UPDATE cinema.access_links
   SET consumed_at = $2
 WHERE nonce = $1::uuid
   AND consumed_at IS NULL
RETURNING email, expires_at`

// upsertCustomerQuery finds or creates the account of a verified address.
//
// `COALESCE(cinema.guest_customers.verified_at, EXCLUDED.verified_at)` is the whole of "the first
// verification is the one that counts" (S3): the second sign-in updates the row, finds a value
// already there, and keeps it. The conflict target is the stored address because the table
// stores the address normalized and treats it as the identity, so one mailbox is one row.
//
// The identifier is generated here and written explicitly: the column has no default, and a
// conflict on the address keeps the existing row's identifier, which is what RETURNING answers.
const upsertCustomerQuery = `
INSERT INTO cinema.guest_customers (id, email, verified_at)
VALUES ($1::uuid, $2, $3)
ON CONFLICT (email) DO UPDATE
   SET verified_at = COALESCE(cinema.guest_customers.verified_at, EXCLUDED.verified_at)
RETURNING id::text`

// claimReservationsQuery is D5: the orders placed with the address that was just proved become
// that account's. It is the one claim this design makes, and it is made here rather than in a
// migration: verifying the mailbox is the proof 0009 said the column was missing, and the proof
// exists only at this instant.
//
// Two limits are part of the statement. `contact_email = $2` is an exact comparison on the
// normalized value, because a near match is no evidence at all, and `customer_id IS NULL` leaves
// an order that already belongs to an account alone — a verified address claims what was
// unowned, never what somebody else owns.
const claimReservationsQuery = `
UPDATE cinema.reservations
   SET customer_id = $1::uuid
 WHERE contact_email = $2
   AND customer_id IS NULL`

// insertRedeemedSessionQuery writes the session inside the redemption's transaction, with the
// account the transaction resolved. `last_seen_at` is NULL for a session nobody has presented
// yet and `revoked_at` is not written, because a row cannot be created closed.
const insertRedeemedSessionQuery = `
INSERT INTO cinema.sessions (id, customer_id, token_hash, created_at, last_seen_at, expires_at)
VALUES ($1::uuid, $2::uuid, $3, $4, NULL, $5)`

// PostgresStore is the durable implementation of [AccessLinkStore] and [RedemptionStore].
type PostgresStore struct {
	pool *pgxpool.Pool
}

// compile-time proof that the one store is both halves of the domain's contract.
var (
	_ AccessLinkStore = (*PostgresStore)(nil)
	_ RedemptionStore = (*PostgresStore)(nil)
)

// NewPostgresStore opens a pool and proves it answers before returning, so a deployment whose
// database is unreachable fails at startup rather than on somebody's first sign-in.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: open the pool: %v", ErrLinkUnavailable, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("%w: the database did not answer: %v", ErrLinkUnavailable, err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases the pooled connections.
func (s *PostgresStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Save records one issued link.
//
// The address is the normalized one the issuer signed, and the expiry is the one the signature
// names, so the row describes the mail that is about to leave instead of a second computation
// of the same instant. The row holds no token: the signature is the proof, and this table only
// has to remember which nonces were issued and which have been spent.
func (s *PostgresStore) Save(ctx context.Context, link AccessLink) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("%w: the identity store is not configured", ErrLinkUnavailable)
	}
	if _, err := s.pool.Exec(ctx, saveAccessLinkQuery, link.Nonce, link.Email, link.ExpiresAt.UTC()); err != nil {
		return fmt.Errorf("%w: record the access link: %v", ErrLinkUnavailable, err)
	}
	return nil
}

// Redeem spends one link and writes everything that becomes true with it, in one transaction.
//
// The order is the contract, and every step refuses rather than guesses:
//
//  1. Spend the row. No row matched means the link was already used, was never issued or has
//     expired, and all three answer [ErrLinkSpent] — one opaque refusal, because telling them
//     apart would confirm which links this deployment once issued.
//  2. Compare the address in the row with the address in the claim. They cannot disagree unless
//     this code is broken: the signature covers the address, and the row was written by the
//     statement above, from the same signed value. A disagreement therefore means the row was
//     not written by this version of this code — a defect, not an attack — and the only safe
//     answer is still a refusal, because spending a row for one address to verify another is
//     exactly what the row exists to prevent.
//  3. Check the row's own expiry against the instant of the redemption. The signer already
//     refused a token whose window closed; this is the row's independent record of when the
//     link was issued, and it wins if the two ever disagree.
//  4. Find or create the account, and write the first `verified_at` (S3).
//  5. Claim the unowned orders placed with that address (D5), and keep the number of rows the
//     statement matched: D5 asks for the claim to be recorded, and the row count is the only
//     honest record of what it did.
//  6. Write the session, for the account the transaction just resolved. It is written here and
//     not through [SessionService] because it must be part of this transaction: see the file
//     comment.
//
// Any failure rolls the transaction back, so a refusal writes nothing at all — not a spent link,
// not an account, not a session — and the redemption it answers is empty. The instant every
// stamp uses is the one the caller put in the session's `created_at`, so the link, the
// verification and the session agree about when this happened.
func (s *PostgresStore) Redeem(ctx context.Context, claim AccessClaims, session Session, tokenHash []byte) (Redemption, error) {
	if s == nil || s.pool == nil {
		return Redemption{}, fmt.Errorf("%w: the identity store is not configured", ErrLinkUnavailable)
	}

	now := session.CreatedAt.UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Redemption{}, fmt.Errorf("%w: begin the redemption: %v", ErrLinkUnavailable, err)
	}
	// Rollback after a commit is a no-op, so this is the one exit that is always safe.
	defer func() { _ = tx.Rollback(ctx) }()

	var storedEmail string
	var expiresAt time.Time
	err = tx.QueryRow(ctx, consumeAccessLinkQuery, claim.Nonce, now).Scan(&storedEmail, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Redemption{}, ErrLinkSpent
	}
	if err != nil {
		// A nonce PostgreSQL cannot even read as an identifier names no row this store ever
		// wrote, which is the same answer as a row that is gone. Only this code signs nonces, so
		// reaching it is a defect, and it is still refused rather than reported as an outage.
		return Redemption{}, classifyConsumeError(err)
	}
	if storedEmail != claim.Email {
		// The signature binds the address to the nonce, so a row naming another address is a
		// defect in whatever wrote it — and spending that row to verify this claim would verify
		// an address this link never proved.
		return Redemption{}, fmt.Errorf("%w: the recorded link names another address than the one it was signed for", ErrLinkSpent)
	}
	if !expiresAt.After(now) {
		// The row's lifetime is measured from when it was issued, which is what this column
		// records; a signature that still verified does not extend it.
		return Redemption{}, ErrLinkSpent
	}

	customerID := ""
	if err := tx.QueryRow(ctx, upsertCustomerQuery, venue.NewID(), storedEmail, now).Scan(&customerID); err != nil {
		return Redemption{}, fmt.Errorf("%w: record the verified account: %v", ErrLinkUnavailable, err)
	}

	claimed, err := tx.Exec(ctx, claimReservationsQuery, customerID, storedEmail)
	if err != nil {
		return Redemption{}, fmt.Errorf("%w: claim the orders of the verified address: %v", ErrLinkUnavailable, err)
	}

	if _, err := tx.Exec(ctx, insertRedeemedSessionQuery,
		session.ID, customerID, tokenHash, session.CreatedAt.UTC(), session.ExpiresAt.UTC()); err != nil {
		return Redemption{}, fmt.Errorf("%w: write the session: %v", ErrLinkUnavailable, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Redemption{}, fmt.Errorf("%w: commit the redemption: %v", ErrLinkUnavailable, err)
	}
	return Redemption{CustomerID: customerID, Claimed: int(claimed.RowsAffected())}, nil
}

// classifyConsumeError turns a failure of the spending statement into the flow's vocabulary. A
// value PostgreSQL could not read as an identifier (invalid_text_representation) names no row,
// which is the same answer as a row that is gone; everything else is an outage.
func classifyConsumeError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return ErrLinkSpent
	}
	return fmt.Errorf("%w: spend the access link: %v", ErrLinkUnavailable, err)
}
