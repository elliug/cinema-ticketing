-- Identity: the account, the session that proves it, the link that opens it, and the
-- thread from an order back to the account that bought it (study and decisions in
-- odd/tasks/identity-login.md, D1-D5, specs S1, S3, S8).
--
-- Signing in stays optional (D4). Nothing below is required to buy a ticket or to open
-- one already bought, which is why every object here is additive and every reference
-- into it is nullable: the product's promise that a guest can buy is not narrowed by
-- this migration. What identity buys is the list (D3) — from now on the server can
-- answer "these are your tickets" instead of trusting a note the browser wrote.
--
-- Four objects, in the order they depend on each other.
--
-- 1. `cinema.guest_customers` — the account, which is an address that was proved to
--    belong to whoever asked for the link. `verified_at` is NULL until the moment that
--    link is consumed, and it is the reason the table exists at all: 0009_email_delivery
--    deliberately stored `reservations.contact_email` unverified and said the day a
--    `guest_customers` row carried verification was the day the claim became true. This
--    is that day, and nothing here verifies anything by itself (S3 is written by the
--    consuming transaction). `phone` is nullable because no flow collects it yet:
--    ARCHITECTURE.md's target state names the column, and a column that exists and stays
--    empty is honest, while inventing a value would not be. A blank phone is stored as
--    no phone, so "nobody gave one" and "somebody gave an empty one" cannot both be
--    stored and no reader has to trim before deciding.
--
--    The address is stored normalized — trimmed and lower-cased by the caller, before
--    the link is signed and again after it is verified — so `Ana@x.com` and `ana@x.com`
--    are one account and not two. That is why uniqueness is a plain UNIQUE constraint on
--    the stored column and not a functional index on `lower(email)`: a functional index
--    would let the table hold both spellings and rely on the index to collapse them, and
--    every read would then have to remember how to normalize to find its own row.
--    Normalizing once, at the boundary, and letting the stored value be the key means the
--    stored address *is* the identity, and a lookup and a uniqueness decision are the
--    same comparison. The check is the one 0009 carries, and deliberately as weak, for
--    the reason stated there: PostgreSQL is not an email parser, so it refuses only what
--    cannot be an address at all.
CREATE TABLE cinema.guest_customers (
  id uuid PRIMARY KEY,
  email text NOT NULL
    CONSTRAINT guest_customers_email_check
    CHECK (
      length(trim(email)) BETWEEN 3 AND 320
      AND position('@' IN email) > 1
      AND position('.' IN split_part(email, '@', 2)) > 1
    ),
  phone text
    CONSTRAINT guest_customers_phone_check
    CHECK (phone IS NULL OR length(trim(phone)) > 0),
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (email)
);

-- 2. `cinema.sessions` — one row per signed-in browser, and the reason a session here is
--    revocable at all (D2, S1). Two things are not visible from the column list:
--
--    The table stores a *hash* of the token, never the token. The token itself exists in
--    the HttpOnly cookie the browser holds and nowhere else, so a database dump — the
--    commonest way a project like this leaks — cannot be replayed as anybody's session.
--    `id` names the row, for logs, joins and revocation; the credential is the token, and
--    only its hash is written here. `token_hash` is unique because two rows holding one
--    hash would be two live sessions for one token, which is the same credential twice.
--
--    `revoked_at` exists so that closing a session is a fact rather than the absence of a
--    row. A deleted row says the same thing at the moment it happens and nothing
--    afterwards, and it makes "signed out" indistinguishable from "never existed" in the
--    one place somebody will look while investigating a complaint. A timestamp keeps
--    `expires_at` meaning what it says and records that the end was chosen, not reached.
--    Either way the promise holds: a row that is gone, revoked or past `expires_at` is
--    not a session any more, and that is a decision the reading side takes from this row.
--
--    `last_seen_at` is nullable because a session that has not been presented once since
--    the act that created it has no later sighting to report, and NULL says that without
--    a sentinel timestamp. `customer_id` cascades because a session is not a record of
--    something that happened: deleting the account deletes the sessions, and a credential
--    whose owner no longer exists is a row nobody could justify keeping.
CREATE TABLE cinema.sessions (
  id uuid PRIMARY KEY,
  customer_id uuid NOT NULL REFERENCES cinema.guest_customers(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  UNIQUE (token_hash)
);

-- The account's sessions: "sign out everywhere" and any listing of them starts here, and
-- the foreign key alone does not answer it — PostgreSQL indexes the referenced side, not
-- the referencing one.
CREATE INDEX sessions_customer ON cinema.sessions (customer_id);

-- 3. `cinema.access_links` — the nonce of the link mailed to an address (S2), and the
--    record that makes "one use" true instead of hoped for.
--
--    An access link is a step, not a key: it expires in minutes rather than days, and it
--    opens one session. The signature proves we issued it; the row proves it has not been
--    spent, because a second presentation of the same link has to find something that
--    says "taken". A token checked only against its signature would be a key that works
--    until it expires, however many times it is presented, and the whole difference
--    between the two is `consumed_at` written once under this row's own lock.
--
--    `email` is the normalized address the link was addressed to — same value, same
--    check, same reasoning as the account above — so consuming a link cannot be turned
--    into verifying a different address by editing what was mailed. `created_at` is here
--    because "minutes" is only meaningful against a stated start: the lifetime of a link
--    is a fact about when it was issued, not about what its own token claims.
--
--    The nonce is the primary key and is chosen by the caller rather than defaulted,
--    because the nonce is part of the payload that gets signed: the row is written after
--    the token is built, so a value generated here would be a value the signature does
--    not cover. Being the key is also what makes two links issued in the same second two
--    different links. There is no hash column, and the asymmetry with `cinema.sessions`
--    is deliberate: the nonce grants nothing on its own. It is the identifier a signed
--    token names, and the signature is what carries the proof.
CREATE TABLE cinema.access_links (
  nonce uuid PRIMARY KEY,
  email text NOT NULL
    CONSTRAINT access_links_email_check
    CHECK (
      length(trim(email)) BETWEEN 3 AND 320
      AND position('@' IN email) > 1
      AND position('.' IN split_part(email, '@', 2)) > 1
    ),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);

-- 4. `cinema.reservations.customer_id` — the order's account, when there was one. Two
--    consequences are worth stating rather than leaving to be discovered:
--
--    Existing rows keep NULL and are *not* backfilled. A purchase made before identity
--    existed recorded an address nobody had proved, which is exactly what 0009 said by
--    not carrying a `verified_at` for it. Claiming those orders is application work that
--    runs once, in the transaction that verifies an address, against the value that was
--    just proved (D5, S3) — never a migration deciding by a string that may belong to
--    anyone. So the NULL here is the same honest intermediate 0009 recorded, not a gap a
--    later script is expected to fill.
--
--    ON DELETE SET NULL rather than CASCADE, because an order is a sale that happened,
--    not a row that belongs to an account. Deleting the account must not delete money,
--    and a ticket already issued must not vanish from the door because somebody asked to
--    be forgotten. What a deletion takes with it is the association, which is precisely
--    what such a request asks for. The reservation keeps the address 0009 added, so a
--    paid order is never left with no contact at all.
ALTER TABLE cinema.reservations
  ADD COLUMN customer_id uuid REFERENCES cinema.guest_customers(id) ON DELETE SET NULL;

-- The only read this column serves: one account's orders, newest first. The ordering is
-- in the index so the list does not have to sort, and the column order matches the query
-- — a filter on the account, then a sort by when it was placed.
CREATE INDEX reservations_by_customer ON cinema.reservations (customer_id, created_at DESC);

INSERT INTO platform.schema_migrations (version) VALUES ('0010_identity');
