// PostgreSQL-only tests of the identity store: the two statements of issuing and the one
// transaction of redeeming. They need a migrated isolated database and are gated on
// M3_CHECKOUT_DATABASE_URL, so the default unit suite needs no database at all.
//
// What only PostgreSQL can prove is here, and it is the reason the SQL is written the way it
// is (S2, S3, D5): a saved link redeems once and the redemption writes the account, its first
// `verified_at` and the session together; the second presentation of the same nonce is
// refused as spent and writes nothing extra, which is what makes single use a fact instead of
// a signature check; an expired row and a nonce nobody issued answer the same refusal; the
// older orders whose address matches are claimed while the ones that do not stay unowned; and
// a second sign-in for an existing account leaves the first verification instant where it is.
package identity_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/identity"
)

// The fixture of the redemption cases: a venue, a film and an on-sale event, so a reservation
// can exist. The identifiers are fixed so a failed run is cleaned up by the next one, and they
// are their own namespace so this file never collides with another suite's fixture.
const (
	redemptionCinema = "00000000-0000-0000-0000-00000000e1a1"
	redemptionRoom   = "00000000-0000-0000-0000-00000000e1a2"
	redemptionLayout = "00000000-0000-0000-0000-00000000e1a3"
	redemptionRow    = "00000000-0000-0000-0000-00000000e1a4"
	redemptionSeat   = "00000000-0000-0000-0000-00000000e1a5"
	redemptionFilm   = "00000000-0000-0000-0000-00000000e1a9"
	redemptionEvent  = "00000000-0000-0000-0000-00000000e1b1"
)

// redemptionLinkTTL is the lifetime these cases save their links with: the documented fifteen
// minutes, so "this link is still open" is not an accident of a fast test.
const redemptionLinkTTL = 15 * time.Minute

// identityDB is the fixture: one gated pool, one store over it, and the rows this test wrote
// so teardown removes exactly what it created.
type identityDB struct {
	t            *testing.T
	ctx          context.Context
	pool         *pgxpool.Pool
	store        *identity.PostgresStore
	accessNonces []string
	emails       []string
	reservations []string
}

// TestASavedLinkRedeemsOnceAndWritesTheAccountAndTheSession is the whole happy path in one
// transaction (S2, S3): the link is spent, the account exists and carries the instant it was
// verified, and the session is written for that account, keyed by the hash of the token the
// caller holds.
func TestASavedLinkRedeemsOnceAndWritesTheAccountAndTheSession(t *testing.T) {
	db := newIdentityDB(t)
	address := db.newAddress("first")
	nonce, expiresAt := db.saveLink(address)
	sessionToken, session := db.newSession(address, time.Now().UTC())

	claim := identity.AccessClaims{Email: address, Nonce: nonce, ExpiresAt: expiresAt}
	if _, err := db.store.Redeem(db.ctx, claim, session, identity.HashSessionToken(sessionToken)); err != nil {
		t.Fatalf("Redeem() = unexpected error %v", err)
	}

	consumed := db.consumedAt(nonce)
	if consumed.IsZero() {
		t.Fatal("the link is still unspent after a redemption, so it opens a second session")
	}
	customer := db.customerOf(address)
	if customer == "" {
		t.Fatal("no account was created for the address the link verified")
	}
	if verified := db.verifiedAt(customer); verified.IsZero() {
		t.Fatal("the account has no verified_at: verifying the mailbox is the reason it exists (S3)")
	}
	stored := db.sessionRow(session.ID)
	if stored.customer != customer {
		t.Fatalf("the session belongs to %q, want the account the link verified (%q)", stored.customer, customer)
	}
	if string(stored.tokenHash) != string(identity.HashSessionToken(sessionToken)) {
		t.Fatal("the stored token_hash is not the SHA-256 of the token the caller holds")
	}
	if !stored.expiresAt.Equal(session.ExpiresAt.Truncate(time.Microsecond)) {
		t.Fatalf("the stored session expires at %v, want %v", stored.expiresAt, session.ExpiresAt)
	}
}

// S2: the second presentation of one nonce is refused, and it writes nothing — no second
// account, no second session, and the instant the link was spent does not move.
func TestRedeemingTheSameNonceTwiceAnswersSpentAndWritesNothing(t *testing.T) {
	db := newIdentityDB(t)
	address := db.newAddress("twice")
	nonce, expiresAt := db.saveLink(address)
	sessionToken, session := db.newSession(address, time.Now().UTC())
	claim := identity.AccessClaims{Email: address, Nonce: nonce, ExpiresAt: expiresAt}

	if _, err := db.store.Redeem(db.ctx, claim, session, identity.HashSessionToken(sessionToken)); err != nil {
		t.Fatalf("first Redeem() = unexpected error %v", err)
	}
	firstConsumed := db.consumedAt(nonce)
	firstCustomer := db.customerOf(address)

	secondToken, second := db.newSession(address, time.Now().UTC().Add(time.Minute))
	if _, err := db.store.Redeem(db.ctx, claim, second, identity.HashSessionToken(secondToken)); !errors.Is(err, identity.ErrLinkSpent) {
		t.Fatalf("second Redeem() = %v, want ErrLinkSpent", err)
	}

	if again := db.consumedAt(nonce); !again.Equal(firstConsumed) {
		t.Fatalf("consumed_at moved from %v to %v on the second redemption", firstConsumed, again)
	}
	if customer := db.customerOf(address); customer != firstCustomer {
		t.Fatalf("the second redemption changed the account from %q to %q", firstCustomer, customer)
	}
	if sessions := db.countSessions(address); sessions != 1 {
		t.Fatalf("the account has %d sessions after one link was redeemed twice, want exactly 1", sessions)
	}
	if rows := db.countSessionRow(second.ID); rows != 0 {
		t.Fatalf("the refused redemption wrote session %s anyway", second.ID)
	}
}

// S2: an expired row and a nonce nobody issued answer the one opaque refusal, and neither
// leaves an account or a session behind.
func TestAnExpiredOrUnknownLinkWritesNothing(t *testing.T) {
	db := newIdentityDB(t)
	now := time.Now().UTC()

	expired := db.newAddress("expired")
	expiredNonce, expiredExpiry := db.saveLinkAt(expired, now.Add(-time.Minute))
	sessionToken, session := db.newSession(expired, now)
	if _, err := db.store.Redeem(db.ctx,
		identity.AccessClaims{Email: expired, Nonce: expiredNonce, ExpiresAt: expiredExpiry},
		session, identity.HashSessionToken(sessionToken)); !errors.Is(err, identity.ErrLinkSpent) {
		t.Fatalf("Redeem() of an expired link = %v, want ErrLinkSpent", err)
	}

	unknown := db.newAddress("unknown")
	unknownToken, unknownSession := db.newSession(unknown, now)
	if _, err := db.store.Redeem(db.ctx,
		identity.AccessClaims{Email: unknown, Nonce: newFixtureUUID(t), ExpiresAt: now.Add(redemptionLinkTTL)},
		unknownSession, identity.HashSessionToken(unknownToken)); !errors.Is(err, identity.ErrLinkSpent) {
		t.Fatalf("Redeem() of a nonce nobody issued = %v, want ErrLinkSpent", err)
	}

	for _, address := range []string{expired, unknown} {
		if customer := db.customerOf(address); customer != "" {
			t.Fatalf("a refused link created account %s for %q", customer, address)
		}
		if sessions := db.countSessions(address); sessions != 0 {
			t.Fatalf("a refused link wrote %d sessions for %q", sessions, address)
		}
	}
}

// D5 (S3): verifying an address claims the orders placed with that address, and only those —
// a reservation whose contact_email differs keeps its null account, because a matching string
// is the only evidence there is and a near match is none.
func TestRedeemingClaimsTheOrdersOfTheVerifiedAddressAndOnlyThose(t *testing.T) {
	db := newIdentityDB(t)
	address := db.newAddress("claim")
	other := db.newAddress("other")
	matching := db.newReservation(address)
	unrelated := db.newReservation(other)
	nonce, expiresAt := db.saveLink(address)
	sessionToken, session := db.newSession(address, time.Now().UTC())

	if _, err := db.store.Redeem(db.ctx,
		identity.AccessClaims{Email: address, Nonce: nonce, ExpiresAt: expiresAt},
		session, identity.HashSessionToken(sessionToken)); err != nil {
		t.Fatalf("Redeem() = unexpected error %v", err)
	}

	customer := db.customerOf(address)
	if claimed := db.customerOfReservation(matching); claimed != customer {
		t.Fatalf("the order placed with the verified address belongs to %q, want %q: verifying "+
			"the mailbox is the proof the order was missing (D5)", claimed, customer)
	}
	if claimed := db.customerOfReservation(unrelated); claimed != "" {
		t.Fatalf("an order placed with another address was claimed by %q", claimed)
	}
}

// D5 (S3): the redemption reports how many orders it claimed, and it reports the rows the
// statement actually touched. Three orders carry the verified address and one of them already
// belongs to another account: two are claimed, the third is left where it is, and the count
// says two — a number taken from `RowsAffected` and not from a count this code guessed.
func TestRedeemingReportsTheOrdersItClaimed(t *testing.T) {
	db := newIdentityDB(t)
	address := db.newAddress("reported")
	owner, _ := db.newAccount("owner")
	first := db.newReservation(address)
	second := db.newReservation(address)
	taken := db.newReservationOwnedBy(address, owner)
	nonce, expiresAt := db.saveLink(address)
	sessionToken, session := db.newSession(address, time.Now().UTC())

	redemption, err := db.store.Redeem(db.ctx,
		identity.AccessClaims{Email: address, Nonce: nonce, ExpiresAt: expiresAt},
		session, identity.HashSessionToken(sessionToken))
	if err != nil {
		t.Fatalf("Redeem() = unexpected error %v", err)
	}

	customer := db.customerOf(address)
	if redemption.CustomerID != customer {
		t.Fatalf("the redemption named account %q, want the one it resolved (%q)", redemption.CustomerID, customer)
	}
	if redemption.Claimed != 2 {
		t.Fatalf("the redemption claimed %d order(s), want 2: the count is the rows the claim statement touched",
			redemption.Claimed)
	}
	for name, id := range map[string]string{"first": first, "second": second} {
		if got := db.customerOfReservation(id); got != customer {
			t.Fatalf("the %s order belongs to %q, want %q: it was unowned and its address was proved",
				name, got, customer)
		}
	}
	if got := db.customerOfReservation(taken); got != owner {
		t.Fatalf("an order that already belonged to %q now belongs to %q: it was counted as claimed",
			owner, got)
	}
}

// S3: the first verification is the one that counts. A second link for an account that
// already exists does not move `verified_at`, so the column records when the mailbox was
// first proved rather than when it was last used.
func TestASecondLinkDoesNotMoveTheFirstVerifiedAt(t *testing.T) {
	db := newIdentityDB(t)
	address := db.newAddress("again")
	first := time.Now().UTC().Truncate(time.Microsecond)

	firstNonce, firstExpiry := db.saveLinkAt(address, first.Add(redemptionLinkTTL))
	firstToken, firstSession := db.newSession(address, first)
	if _, err := db.store.Redeem(db.ctx,
		identity.AccessClaims{Email: address, Nonce: firstNonce, ExpiresAt: firstExpiry},
		firstSession, identity.HashSessionToken(firstToken)); err != nil {
		t.Fatalf("first Redeem() = unexpected error %v", err)
	}
	customer := db.customerOf(address)
	verified := db.verifiedAt(customer)
	if verified.IsZero() {
		t.Fatal("the first redemption left verified_at empty")
	}

	later := first.Add(48 * time.Hour)
	secondNonce, secondExpiry := db.saveLinkAt(address, later.Add(redemptionLinkTTL))
	secondToken, secondSession := db.newSession(address, later)
	if _, err := db.store.Redeem(db.ctx,
		identity.AccessClaims{Email: address, Nonce: secondNonce, ExpiresAt: secondExpiry},
		secondSession, identity.HashSessionToken(secondToken)); err != nil {
		t.Fatalf("second Redeem() = unexpected error %v", err)
	}

	if again := db.verifiedAt(customer); !again.Equal(verified) {
		t.Fatalf("verified_at moved from %v to %v on a second sign-in, want the first instant", verified, again)
	}
	if again := db.customerOf(address); again != customer {
		t.Fatalf("a second link for one address created account %q, want the existing %q", again, customer)
	}
	if sessions := db.countSessions(address); sessions != 2 {
		t.Fatalf("the account has %d sessions after two redemptions, want 2", sessions)
	}
}

// newIdentityDB opens the gated database through the store under test and registers the
// teardown that removes everything this test wrote.
func newIdentityDB(t *testing.T) *identityDB {
	t.Helper()
	ctx := context.Background()
	url := strings.TrimSpace(os.Getenv(sessionDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set; the identity store is proven against PostgreSQL by `make verify-m3`", sessionDatabaseURLEnv)
	}
	// The same isolated-database rule the session suite applies: these tests write real rows,
	// so the name has to say it is disposable.
	requireIsolatedSessionDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open the fixture pool: %v", err)
	}
	store, err := identity.NewPostgresStore(ctx, url)
	if err != nil {
		pool.Close()
		t.Fatalf("open the identity store: %v", err)
	}

	db := &identityDB{t: t, ctx: ctx, pool: pool, store: store}
	t.Cleanup(func() {
		// The order is the schema's: orders point at accounts with SET NULL, sessions cascade,
		// and a link is a row of its own.
		for _, reservation := range db.reservations {
			_, _ = pool.Exec(ctx, `DELETE FROM cinema.reservations WHERE id = $1::uuid`, reservation)
		}
		for _, nonce := range db.accessNonces {
			_, _ = pool.Exec(ctx, `DELETE FROM cinema.access_links WHERE nonce = $1::uuid`, nonce)
		}
		for _, address := range db.emails {
			// Deleting the account is enough: 0010 cascades its sessions.
			_, _ = pool.Exec(ctx, `DELETE FROM cinema.guest_customers WHERE email = $1`, address)
		}
		store.Close()
		pool.Close()
	})

	var migrated bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('cinema.access_links') IS NOT NULL
		    AND to_regclass('cinema.guest_customers') IS NOT NULL
		    AND EXISTS (
		      SELECT 1 FROM information_schema.columns
		       WHERE table_schema = 'cinema' AND table_name = 'reservations'
		         AND column_name = 'customer_id')`).Scan(&migrated); err != nil {
		t.Fatalf("inspect the identity schema: %v", err)
	}
	if !migrated {
		t.Fatalf("%s is not migrated with 0010_identity", sessionDatabaseURLEnv)
	}
	seedRedemptionFixture(t, ctx, pool)
	return db
}

// storedRedemptionSession is one session row read back as the store's own columns.
type storedRedemptionSession struct {
	customer  string
	tokenHash []byte
	expiresAt time.Time
}

// newAddress mints an address this run owns, because the table treats the stored address as
// the identity and two tests must never share one.
func (db *identityDB) newAddress(label string) string {
	db.t.Helper()
	address := fmt.Sprintf("identity-%s-%s@cinema.test", label, newFixtureUUID(db.t))
	db.emails = append(db.emails, address)
	return address
}

// saveLink records a link the way the issuer does, with the documented lifetime.
func (db *identityDB) saveLink(address string) (string, time.Time) {
	db.t.Helper()
	return db.saveLinkAt(address, time.Now().UTC().Add(redemptionLinkTTL))
}

// saveLinkAt records a link that ends at a chosen instant, so a case can expire one.
func (db *identityDB) saveLinkAt(address string, expiresAt time.Time) (string, time.Time) {
	db.t.Helper()
	nonce := newFixtureUUID(db.t)
	db.accessNonces = append(db.accessNonces, nonce)
	if err := db.store.Save(db.ctx, identity.AccessLink{
		Nonce: nonce, Email: address, ExpiresAt: expiresAt,
	}); err != nil {
		db.t.Fatalf("Save() = unexpected error %v", err)
	}
	return nonce, expiresAt
}

// newSession mints the session token and the row the redeemer would hand the store: the
// redeemer's own job is proven by the unit suite, so these cases hand the store a token they
// can read back.
func (db *identityDB) newSession(customer string, createdAt time.Time) (string, identity.Session) {
	db.t.Helper()
	token, err := identity.NewSessionToken()
	if err != nil {
		db.t.Fatalf("mint a session token: %v", err)
	}
	return token, identity.Session{
		ID:         newFixtureUUID(db.t),
		CustomerID: customer,
		CreatedAt:  createdAt,
		ExpiresAt:  createdAt.Add(sessionStoreTTL),
	}
}

// newAccount writes an account this run owns, as a verified sign-in would have, so an order
// can already belong to somebody else. It answers the account and its address.
func (db *identityDB) newAccount(label string) (string, string) {
	db.t.Helper()
	address := db.newAddress(label)
	customer := newFixtureUUID(db.t)
	if _, err := db.pool.Exec(db.ctx, `
INSERT INTO cinema.guest_customers (id, email, verified_at) VALUES ($1::uuid, $2, now())`,
		customer, address); err != nil {
		db.t.Fatalf("insert an account for %q: %v", address, err)
	}
	return customer, address
}

// newReservation writes a held reservation whose contact address is address, as a sale from
// before identity existed did.
func (db *identityDB) newReservation(address string) string {
	db.t.Helper()
	return db.newReservationOwnedBy(address, "")
}

// newReservationOwnedBy writes a held reservation for address that already belongs to owner,
// which is how an order placed by an account that had signed in looks. An empty owner leaves
// it unowned, which is what 0009 alone could produce.
func (db *identityDB) newReservationOwnedBy(address, owner string) string {
	db.t.Helper()
	id := newFixtureUUID(db.t)
	db.reservations = append(db.reservations, id)
	var ownerID any
	if owner != "" {
		ownerID = owner
	}
	if _, err := db.pool.Exec(db.ctx, `
INSERT INTO cinema.reservations (id, event_id, status, expires_at, contact_email, customer_id)
VALUES ($1::uuid, $2::uuid, 'held', now() + interval '10 minutes', $3, $4::uuid)`,
		id, redemptionEvent, address, ownerID); err != nil {
		db.t.Fatalf("insert a reservation for %q: %v", address, err)
	}
	return id
}

// consumedAt answers when a link was spent, or the zero instant while it is unspent.
func (db *identityDB) consumedAt(nonce string) time.Time {
	db.t.Helper()
	var at *time.Time
	if err := db.pool.QueryRow(db.ctx,
		`SELECT consumed_at FROM cinema.access_links WHERE nonce = $1::uuid`, nonce).Scan(&at); err != nil {
		db.t.Fatalf("read the link %s: %v", nonce, err)
	}
	if at == nil {
		return time.Time{}
	}
	return *at
}

// customerOf answers the account of one address, or "" when there is none.
func (db *identityDB) customerOf(address string) string {
	db.t.Helper()
	var id string
	err := db.pool.QueryRow(db.ctx,
		`SELECT id::text FROM cinema.guest_customers WHERE email = $1`, address).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		db.t.Fatalf("read the account of %q: %v", address, err)
	}
	return id
}

// verifiedAt answers when an account's address was first proved, or the zero instant.
func (db *identityDB) verifiedAt(customer string) time.Time {
	db.t.Helper()
	var at *time.Time
	if err := db.pool.QueryRow(db.ctx,
		`SELECT verified_at FROM cinema.guest_customers WHERE id = $1::uuid`, customer).Scan(&at); err != nil {
		db.t.Fatalf("read verified_at: %v", err)
	}
	if at == nil {
		return time.Time{}
	}
	return *at
}

// sessionRow reads one session back by identifier.
func (db *identityDB) sessionRow(id string) storedRedemptionSession {
	db.t.Helper()
	var row storedRedemptionSession
	if err := db.pool.QueryRow(db.ctx, `
SELECT customer_id::text, token_hash, expires_at FROM cinema.sessions WHERE id = $1::uuid`, id).
		Scan(&row.customer, &row.tokenHash, &row.expiresAt); err != nil {
		db.t.Fatalf("read session %s: %v", id, err)
	}
	return row
}

// countSessionRow counts the rows of one session identifier, which a refused redemption must
// leave at zero.
func (db *identityDB) countSessionRow(id string) int {
	db.t.Helper()
	var rows int
	if err := db.pool.QueryRow(db.ctx,
		`SELECT count(*) FROM cinema.sessions WHERE id = $1::uuid`, id).Scan(&rows); err != nil {
		db.t.Fatalf("count session %s: %v", id, err)
	}
	return rows
}

// countSessions counts the sessions written for one address.
func (db *identityDB) countSessions(address string) int {
	db.t.Helper()
	var rows int
	if err := db.pool.QueryRow(db.ctx, `
SELECT count(*)
  FROM cinema.sessions s
  JOIN cinema.guest_customers c ON c.id = s.customer_id
 WHERE c.email = $1`, address).Scan(&rows); err != nil {
		db.t.Fatalf("count the sessions of %q: %v", address, err)
	}
	return rows
}

// customerOfReservation answers the account of one order, or "" while it has none.
func (db *identityDB) customerOfReservation(id string) string {
	db.t.Helper()
	var customer *string
	if err := db.pool.QueryRow(db.ctx,
		`SELECT customer_id::text FROM cinema.reservations WHERE id = $1::uuid`, id).Scan(&customer); err != nil {
		db.t.Fatalf("read the account of reservation %s: %v", id, err)
	}
	if customer == nil {
		return ""
	}
	return *customer
}

// seedRedemptionFixture creates the venue, the film and the event once, so a reservation can
// exist. The layout is published before the event, because M1 materializes event seats from a
// published layout and refuses a draft one.
func seedRedemptionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	var layoutExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id = $1)`, redemptionLayout).
		Scan(&layoutExists); err != nil {
		t.Fatalf("inspect the identity venue fixture: %v", err)
	}
	if !layoutExists {
		for _, statement := range []struct {
			what string
			sql  string
		}{
			{"cinema", `INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('` + redemptionCinema + `','Cine Identidad','Europe/Madrid')`},
			{"room", `INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('` + redemptionRoom + `','` + redemptionCinema + `','Sala Identidad')`},
			{"layout", `INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('` + redemptionLayout + `','` + redemptionRoom + `',1)`},
			{"row", `INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('` + redemptionRow + `','` + redemptionLayout + `','A',1)`},
			{"seat", `INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES ('` + redemptionSeat + `','` + redemptionRow + `','1',1,'standard',true)`},
			{"publish", `UPDATE cinema.layout_versions SET status = 'published' WHERE id = '` + redemptionLayout + `'`},
		} {
			if _, err := pool.Exec(ctx, statement.sql); err != nil {
				t.Fatalf("seed %s: %v", statement.what, err)
			}
		}
	}

	var eventExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cinema.events WHERE id = $1)`, redemptionEvent).
		Scan(&eventExists); err != nil {
		t.Fatalf("inspect the identity event fixture: %v", err)
	}
	if !eventExists {
		if _, err := pool.Exec(ctx,
			`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1,'Identity Fixture',90,'PG')`,
			redemptionFilm); err != nil {
			t.Fatalf("seed film: %v", err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
VALUES ($1, $2, $3, $4, now() + interval '1 day', 'on_sale')`,
			redemptionEvent, redemptionRoom, redemptionFilm, redemptionLayout); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
}
