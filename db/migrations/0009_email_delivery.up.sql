-- Email delivery: where the address lives, and how a second outbox consumer keeps
-- its own progress (issue to be opened; study in odd/tasks/email-delivery.md).
--
-- Two objects, for two different reasons.
--
-- 1. `cinema.reservations.contact_email` — the address the buyer gave at checkout for
--    the tickets to be mailed to. It is nullable because the field is optional: forcing
--    it would store personal data for every purchase somebody abandoned, and the product
--    has never required an address to buy. It is unverified by decision: nothing in this
--    schema claims the address belongs to whoever typed it, which is why there is no
--    `verified_at` column here. When identity arrives, this column becomes a reference to
--    a `guest_customers` row that *does* carry verification, and that is the moment a
--    verification claim becomes true. ARCHITECTURE.md's target state says
--    `guest_customers`; this is the honest intermediate, recorded rather than pretended.
--
--    The check is deliberately weak. PostgreSQL is not an email parser and a strict
--    pattern here would reject valid addresses (quoted local parts, internationalised
--    domains) while accepting nonsense. It refuses only what cannot be an address at
--    all: a non-empty string with no `@` and no dot, or one longer than the RFC's 320.
--
-- 2. `cinema.outbox_deliveries` — the delivery record the outbox comment already asked
--    for. `apps/api/internal/pdf/outbox.go` states that `published_at` means "every
--    consumer of this topic has acted", that the PDF worker is the only consumer today,
--    and that a later consumer "must not assume `published_at IS NULL` on its own, and
--    needs its own cursor or a separate delivery record". This table is that record. The
--    reason it must exist is concrete: the PDF worker sets `published_at` when it stores
--    the document, so an email consumer filtering on `published_at IS NULL` would find
--    nothing and send nothing — a green build with no mail, which is the failure mode
--    this project keeps finding.
--
--    The primary key is `(consumer, outbox_id)`, so "this consumer has handled this row"
--    is a fact stated once. A retried tick inserts nothing twice and therefore cannot
--    send twice; a second consumer names itself and keeps its own progress without
--    touching anyone else's.
--
--    `ON DELETE CASCADE` because the delivery record is only meaningful while the row it
--    describes exists: pruning the outbox must not leave orphans behind.
ALTER TABLE cinema.reservations
  ADD COLUMN contact_email text
    CONSTRAINT reservations_contact_email_check
    CHECK (
      contact_email IS NULL
      OR (
        length(trim(contact_email)) BETWEEN 3 AND 320
        AND position('@' IN contact_email) > 1
        AND position('.' IN split_part(contact_email, '@', 2)) > 1
      )
    );

CREATE TABLE cinema.outbox_deliveries (
  consumer text NOT NULL
    CONSTRAINT outbox_deliveries_consumer_check
    CHECK (length(trim(consumer)) > 0),
  outbox_id uuid NOT NULL REFERENCES cinema.outbox (id) ON DELETE CASCADE,
  delivered_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, outbox_id)
);

-- The worker's query: rows of one topic this consumer has not handled yet, oldest first.
--
-- The primary key above is what answers "has this consumer handled this row": that is the
-- join the `NOT EXISTS` performs, once per candidate, by key. What the primary key does
-- **not** serve is finding the candidates, because the worker orders by `created_at, id`
-- over the whole topic — a row's `published_at` says nothing about this consumer any more.
-- Hence this index, on the columns the worker actually sorts by. The outbox's existing
-- partial index is on `published_at IS NULL`, which an email consumer cannot use without
-- falling into exactly the trap this table exists to avoid.
CREATE INDEX outbox_pending_by_topic ON cinema.outbox (topic, created_at, id);

INSERT INTO platform.schema_migrations (version) VALUES ('0009_email_delivery');
