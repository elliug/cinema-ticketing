// PostgreSQL-only tests of the session store. They need a migrated isolated database and
// are gated on M3_CHECKOUT_DATABASE_URL, so the default unit suite needs no database at all.
//
// What only PostgreSQL can prove is here, and it is the reason the store's SQL is written
// the way it is (S1): that a created row is found by the hash of its token and that the table
// holds the hash and not the token; that the row's own predicate — and not the service —
// refuses an expired session and a revoked one; that revoking twice keeps the first instant
// and reports that nothing matched; that refreshing a session moves last_seen_at and leaves
// expires_at exactly where it was; and that deleting the account takes its sessions with it,
// which is the cascade 0010 declares and the reason a credential whose owner no longer exists
// is not a row anybody keeps.
package identity_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cinema.local/ticketing/apps/api/internal/identity"
)

// sessionDatabaseURLEnv names the isolated database these tests use: the same throwaway M3
// database the other PostgreSQL suites run against, because the sessions table references
// cinema.guest_customers and is created by the same migration chain.
const sessionDatabaseURLEnv = "M3_CHECKOUT_DATABASE_URL"

// sessionStoreTTL is the lifetime these cases open their sessions with: the documented
// thirty days, so "not expired" is not an accident of a test running fast.
const sessionStoreTTL = 720 * time.Hour

// sessionDB is the fixture: one gated pool, one store, one service and the accounts this
// test created, so teardown removes exactly what it wrote.
type sessionDB struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	store     *identity.PostgresSessionStore
	service   *identity.SessionService
	customers []string
}

// storedSession is one row read back as the store's own columns, so an assertion names the
// column it is about instead of inferring it from a returned struct.
type storedSession struct {
	tokenHash []byte
	createdAt time.Time
	lastSeen  *time.Time
	expiresAt time.Time
	revokedAt *time.Time
}

// TestACreatedSessionIsFoundByItsHash is S1's round trip: the row is written with the hash of
// the token, no column anywhere holds the token, and looking the hash up answers the account
// the session belongs to with the expiry it was created with.
func TestACreatedSessionIsFoundByItsHash(t *testing.T) {
	db := newSessionDB(t)
	customer := db.newCustomer()
	token, opened := db.mintSession(customer, sessionStoreTTL)

	row := db.stored(opened.ID)
	if !bytes.Equal(row.tokenHash, identity.HashSessionToken(token)) {
		t.Fatal("the stored token_hash is not the SHA-256 of the token")
	}
	if bytes.Equal(row.tokenHash, []byte(token)) {
		t.Fatal("the table holds the token itself: a database dump would be a set of live credentials")
	}
	// The token is not stored under any other name either: the only byte column here is the
	// hash, and a lookup by the token must find nothing at all.
	var byToken int
	if err := db.pool.QueryRow(db.ctx,
		`SELECT count(*) FROM cinema.sessions WHERE token_hash = $1`, []byte(token)).Scan(&byToken); err != nil {
		t.Fatalf("count the rows matching the token: %v", err)
	}
	if byToken != 0 {
		t.Fatalf("%d rows hold the token itself, want none", byToken)
	}
	if row.lastSeen != nil {
		t.Fatalf("a session that was just created reports last_seen_at = %v, want none", row.lastSeen)
	}

	found, err := db.store.Find(db.ctx, identity.HashSessionToken(token), time.Now().UTC())
	if err != nil {
		t.Fatalf("Find() = unexpected error %v", err)
	}
	if found.ID != opened.ID {
		t.Fatalf("Find() answered session %q, want %q", found.ID, opened.ID)
	}
	if found.CustomerID != customer {
		t.Fatalf("Find() answered customer %q, want %q", found.CustomerID, customer)
	}
	if !found.ExpiresAt.Equal(opened.ExpiresAt.Truncate(time.Microsecond)) {
		t.Fatalf("Find() answered expiry %v, want %v", found.ExpiresAt, opened.ExpiresAt)
	}
	if found.LastSeenAt != nil {
		t.Fatalf("Find() answered last_seen_at = %v, want none", found.LastSeenAt)
	}
}

// TestAnExpiredSessionIsRefusedByTheRow pins the store's own guard: the expiry is in the
// WHERE clause, so an expired session cannot authenticate even if the service asking for it
// forgot to check. The row is still there, which is the point — it is the predicate refusing,
// not a missing row that would hide an expired session as an unknown one.
func TestAnExpiredSessionIsRefusedByTheRow(t *testing.T) {
	db := newSessionDB(t)
	customer := db.newCustomer()
	// A negative lifetime is what mintSession refuses, so the row is written directly with an
	// expiry one minute in the past.
	token, session := db.writeSession(customer, time.Now().UTC().Add(-time.Minute))

	var rows int
	if err := db.pool.QueryRow(db.ctx,
		`SELECT count(*) FROM cinema.sessions WHERE id = $1::uuid`, session.ID).Scan(&rows); err != nil {
		t.Fatalf("count the expired row: %v", err)
	}
	if rows != 1 {
		t.Fatalf("the expired session's row is not in the table, so this case proves nothing")
	}

	if found, err := db.store.Find(db.ctx, identity.HashSessionToken(token), time.Now().UTC()); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("Find() of an expired session = (%+v, %v), want ErrNoSession", found, err)
	}
}

// TestARevokedSessionIsRefusedByTheRow is the other half of the predicate: a row that is
// still in the table and still in date stops being a session the instant it is revoked,
// because `revoked_at IS NULL` is part of the read.
func TestARevokedSessionIsRefusedByTheRow(t *testing.T) {
	db := newSessionDB(t)
	customer := db.newCustomer()
	token, session := db.mintSession(customer, sessionStoreTTL)
	hash := identity.HashSessionToken(token)

	if err := db.store.Revoke(db.ctx, hash, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatalf("Revoke() = unexpected error %v", err)
	}
	if row := db.stored(session.ID); row.revokedAt == nil {
		t.Fatal("Revoke() left the row's revoked_at empty: closing a session is a fact, not an absence")
	}
	if found, err := db.store.Find(db.ctx, hash, time.Now().UTC()); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("Find() of a revoked session = (%+v, %v), want ErrNoSession", found, err)
	}
}

// TestRevokingTwiceKeepsTheFirstInstant pins idempotence at the SQL: the UPDATE is guarded by
// `revoked_at IS NULL`, so a second revocation moves nothing and reports that it matched no
// row — the same answer an unknown token gets, because the store is not allowed to say why.
// The service on top of it answers nil either way, which is the decision that keeps sign-out
// from telling a client whether its token ever existed.
func TestRevokingTwiceKeepsTheFirstInstant(t *testing.T) {
	db := newSessionDB(t)
	customer := db.newCustomer()
	token, session := db.mintSession(customer, sessionStoreTTL)
	hash := identity.HashSessionToken(token)
	first := time.Now().UTC().Truncate(time.Microsecond)

	if err := db.store.Revoke(db.ctx, hash, first); err != nil {
		t.Fatalf("first Revoke() = unexpected error %v", err)
	}
	recorded := db.stored(session.ID)
	if recorded.revokedAt == nil || !recorded.revokedAt.Equal(first) {
		t.Fatalf("revoked_at = %v, want %v", recorded.revokedAt, first)
	}

	if err := db.store.Revoke(db.ctx, hash, first.Add(time.Hour)); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("second Revoke() = %v, want ErrNoSession: the row is already closed", err)
	}
	if again := db.stored(session.ID); again.revokedAt == nil || !again.revokedAt.Equal(first) {
		t.Fatalf("revoked_at moved to %v on the second call, want %v", again.revokedAt, first)
	}

	// The service's answer is quiet, twice, so a client learns nothing about the row.
	if err := db.service.Close(db.ctx, token); err != nil {
		t.Fatalf("Close() of an already closed session = %v, want nil", err)
	}
	if err := db.service.Close(db.ctx, token); err != nil {
		t.Fatalf("second Close() = %v, want nil", err)
	}
}

// TestTouchMovesLastSeenAndOnlyLastSeen pins the two columns the refresh may touch: a session
// that is being used records the sighting, its expiry and creation instant stay exactly where
// they were (S1: no silent renewal), and a dead row cannot acquire a sighting at all.
func TestTouchMovesLastSeenAndOnlyLastSeen(t *testing.T) {
	db := newSessionDB(t)
	customer := db.newCustomer()
	token, session := db.mintSession(customer, sessionStoreTTL)
	hash := identity.HashSessionToken(token)
	before := db.stored(session.ID)

	seen := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.store.Touch(db.ctx, hash, seen); err != nil {
		t.Fatalf("Touch() = unexpected error %v", err)
	}
	after := db.stored(session.ID)

	if after.lastSeen == nil || !after.lastSeen.Equal(seen) {
		t.Fatalf("last_seen_at = %v, want %v", after.lastSeen, seen)
	}
	if !after.expiresAt.Equal(before.expiresAt) {
		t.Fatalf("touching moved expires_at from %v to %v: a session is not renewed silently",
			before.expiresAt, after.expiresAt)
	}
	if !after.createdAt.Equal(before.createdAt) {
		t.Fatalf("touching moved created_at from %v to %v", before.createdAt, after.createdAt)
	}

	// The refresh is guarded by the same liveness predicate as the read, so an expired row is
	// not stamped: a sighting of a session that is already over would be a lie in the audit.
	expiredToken, expired := db.writeSession(customer, time.Now().UTC().Add(-time.Minute))
	if err := db.store.Touch(db.ctx, identity.HashSessionToken(expiredToken), seen); err != nil {
		t.Fatalf("Touch() of an expired session = unexpected error %v", err)
	}
	if row := db.stored(expired.ID); row.lastSeen != nil {
		t.Fatalf("an expired session recorded last_seen_at = %v, want none", row.lastSeen)
	}
}

// TestDeletingTheAccountTakesItsSessions is the cascade 0010 declares: a session is not a
// record of something that happened, it is a credential, and a credential whose owner no
// longer exists is a row nobody could justify keeping. Deleting the account is therefore also
// the way "sign me out everywhere" becomes true.
func TestDeletingTheAccountTakesItsSessions(t *testing.T) {
	db := newSessionDB(t)
	customer := db.newCustomer()
	firstToken, first := db.mintSession(customer, sessionStoreTTL)
	_, second := db.mintSession(customer, sessionStoreTTL)

	if _, err := db.pool.Exec(db.ctx,
		`DELETE FROM cinema.guest_customers WHERE id = $1::uuid`, customer); err != nil {
		t.Fatalf("delete the account: %v", err)
	}

	var remaining int
	if err := db.pool.QueryRow(db.ctx,
		`SELECT count(*) FROM cinema.sessions WHERE customer_id = $1::uuid`, customer).Scan(&remaining); err != nil {
		t.Fatalf("count the account's sessions: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("%d sessions survived the account they belonged to, want none", remaining)
	}
	for _, id := range []string{first.ID, second.ID} {
		var rows int
		if err := db.pool.QueryRow(db.ctx,
			`SELECT count(*) FROM cinema.sessions WHERE id = $1::uuid`, id).Scan(&rows); err != nil {
			t.Fatalf("count session %s: %v", id, err)
		}
		if rows != 0 {
			t.Fatalf("session %s survived the deletion of its account", id)
		}
	}
	if _, err := db.store.Find(db.ctx, identity.HashSessionToken(firstToken), time.Now().UTC()); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("Find() of a deleted account's session = %v, want ErrNoSession", err)
	}
}

// newSessionDB opens the gated database through the store under test and registers the
// teardown that removes everything this test wrote.
func newSessionDB(t *testing.T) *sessionDB {
	t.Helper()
	ctx := context.Background()
	url := strings.TrimSpace(os.Getenv(sessionDatabaseURLEnv))
	if url == "" {
		t.Skipf("%s is not set; the session store is proven against PostgreSQL by `make verify-m3`", sessionDatabaseURLEnv)
	}
	requireIsolatedSessionDatabase(t, url)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("open the fixture pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("%s does not answer: %v", sessionDatabaseURLEnv, err)
	}

	store, err := identity.NewSessionStore(ctx, url)
	if err != nil {
		pool.Close()
		t.Fatalf("open the session store: %v", err)
	}
	service, err := identity.NewSessionService(store, sessionStoreTTL)
	if err != nil {
		store.Close()
		pool.Close()
		t.Fatalf("build the session service: %v", err)
	}

	db := &sessionDB{t: t, ctx: ctx, pool: pool, store: store, service: service}
	t.Cleanup(func() {
		for _, customer := range db.customers {
			// Deleting the account is enough: 0010 cascades its sessions, which is the fact
			// the cascade test asserts.
			_, _ = pool.Exec(ctx, `DELETE FROM cinema.guest_customers WHERE id = $1::uuid`, customer)
		}
		store.Close()
		pool.Close()
	})

	var migrated bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('cinema.sessions') IS NOT NULL
		    AND to_regclass('cinema.guest_customers') IS NOT NULL`).Scan(&migrated); err != nil {
		t.Fatalf("inspect the identity schema: %v", err)
	}
	if !migrated {
		t.Fatalf("%s is not migrated with 0010_identity", sessionDatabaseURLEnv)
	}
	return db
}

// requireIsolatedSessionDatabase refuses the development database: these tests write real
// rows, so the name has to say it is disposable.
func requireIsolatedSessionDatabase(t *testing.T, url string) {
	t.Helper()
	name := url[strings.LastIndex(url, "/")+1:]
	if index := strings.IndexAny(name, "?&"); index >= 0 {
		name = name[:index]
	}
	if name == "cinema" {
		t.Fatalf("%s must never point at the development database", sessionDatabaseURLEnv)
	}
	if !strings.HasPrefix(name, "cinema_m3") {
		t.Fatalf("%s must name an isolated database beginning with cinema_m3, got %q", sessionDatabaseURLEnv, name)
	}
}

// newCustomer writes the account a session has to belong to: the foreign key is NOT NULL, so
// no session can exist without one. The address is unique per run, because the table treats
// the stored address as the identity.
func (db *sessionDB) newCustomer() string {
	db.t.Helper()
	var customer string
	address := fmt.Sprintf("session-%s@cinema.test", newFixtureUUID(db.t))
	if err := db.pool.QueryRow(db.ctx, `
INSERT INTO cinema.guest_customers (id, email, verified_at)
VALUES ($1::uuid, $2, now())
RETURNING id::text`, newFixtureUUID(db.t), address).Scan(&customer); err != nil {
		db.t.Fatalf("insert the account fixture: %v", err)
	}
	db.customers = append(db.customers, customer)
	return customer
}

// mintSession opens a session through the service, which is the path a login takes, and skips
// the shape checks a unit test already proves.
func (db *sessionDB) mintSession(customer string, ttl time.Duration) (string, identity.Session) {
	db.t.Helper()
	if ttl <= 0 {
		db.t.Fatalf("a session lifetime must be positive, got %s", ttl)
	}
	token, err := identity.NewSessionToken()
	if err != nil {
		db.t.Fatalf("mint a token: %v", err)
	}
	return db.writeSessionWithToken(customer, token, ttl)
}

// writeSession writes a row with an expiry the caller chooses, so a case can be expired
// without waiting for it. The token is minted here because the row's key is its hash.
func (db *sessionDB) writeSession(customer string, expiresAt time.Time) (string, identity.Session) {
	db.t.Helper()
	token, err := identity.NewSessionToken()
	if err != nil {
		db.t.Fatalf("mint a token: %v", err)
	}
	return db.writeSessionFor(customer, token, expiresAt)
}

// writeSessionWithToken writes a session that outlives the call by ttl.
func (db *sessionDB) writeSessionWithToken(customer, token string, ttl time.Duration) (string, identity.Session) {
	db.t.Helper()
	return db.writeSessionFor(customer, token, time.Now().UTC().Add(ttl))
}

// writeSessionFor writes one row and fails the test when the store refuses it.
func (db *sessionDB) writeSessionFor(customer, token string, expiresAt time.Time) (string, identity.Session) {
	db.t.Helper()
	session := identity.Session{
		ID:         newFixtureUUID(db.t),
		CustomerID: customer,
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  expiresAt,
	}
	if err := db.store.Create(db.ctx, session, identity.HashSessionToken(token)); err != nil {
		db.t.Fatalf("Create() = unexpected error %v", err)
	}
	return token, session
}

// stored reads one row back by identifier.
func (db *sessionDB) stored(id string) storedSession {
	db.t.Helper()
	var row storedSession
	if err := db.pool.QueryRow(db.ctx, `
SELECT token_hash, created_at, last_seen_at, expires_at, revoked_at
  FROM cinema.sessions
 WHERE id = $1::uuid`, id).Scan(
		&row.tokenHash, &row.createdAt, &row.lastSeen, &row.expiresAt, &row.revokedAt); err != nil {
		db.t.Fatalf("read session %s: %v", id, err)
	}
	return row
}

// newFixtureUUID builds a random version 4 UUID, so every run inserts its own rows.
func newFixtureUUID(t *testing.T) string {
	t.Helper()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatalf("generate a uuid: %v", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
