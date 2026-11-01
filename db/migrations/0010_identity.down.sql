-- Roll back identity, in the reverse of the order it was built: the thread from an order
-- to an account, then the link, then the session, then the account they all point at.
--
-- This rollback needs no guard of its own, unlike 0005_checkout.down.sql,
-- 0008_ticket_redemption.down.sql and 0009_email_delivery.down.sql, and that is a
-- decision rather than an omission. Those refusals protect the only record of something
-- that already happened: money taken, a ticket admitted, mail delivered. Nothing here is
-- that. A session and an access link are credentials with a short life, and dropping them
-- logs people out and kills links that were going to be useless within the hour anyway —
-- and it fails closed, because a link whose nonce is gone cannot be consumed: the row is
-- what "one use" was made of. Refusing there would hold a schema hostage to a password
-- reset.
--
-- `reservations.customer_id` is the one object here that touches real purchases, and the
-- cost is stated rather than hidden: dropping the column loses the association between an
-- order and an account, not the order. Every reservation, ticket and payment stays, and so
-- does the contact address 0009 added — but nothing in the schema says any more which
-- purchases belonged together, and the claim of D5 is over. It is dropped unconditionally
-- because the alternative is worse: a rollback that refuses for as long as one nullable
-- column holds a value is not a rollback, it is a schema nobody can undo, and the
-- denormalisation it protects is one application run away from being claimed again.
--
-- Dropping `guest_customers` takes the verified addresses with it, which is the rollback
-- saying the verification never happened. With the reservation column gone at the same
-- moment, nothing in the schema is left claiming otherwise, so no invariant is silently
-- reversed — what is gone is gone. The index on `sessions.customer_id` goes with its
-- table and is therefore not dropped separately; the reservations index is its own object
-- and is.
DROP INDEX cinema.reservations_by_customer;

ALTER TABLE cinema.reservations
  DROP COLUMN customer_id;

DROP TABLE cinema.access_links;

DROP TABLE cinema.sessions;

DROP TABLE cinema.guest_customers;

DELETE FROM platform.schema_migrations WHERE version = '0010_identity';
