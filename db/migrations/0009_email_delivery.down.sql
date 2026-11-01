-- Roll back email delivery: the delivery record and the address column.
--
-- This rollback refuses while any delivery is recorded, and the reason is not
-- sentimentality about history. Dropping `outbox_deliveries` would make every email
-- already sent look unsent, so the next tick of the worker would send all of them again,
-- to real inboxes. The record is not a log of something that happened; it is the thing
-- that prevents it happening twice. (The same reasoning as 0005_checkout.down.sql
-- refusing while money was captured, and 0008_ticket_redemption.down.sql while a ticket
-- was admitted.)
--
-- To roll back anyway, knowing the consequence:
--
--   DELETE FROM cinema.outbox_deliveries WHERE consumer = 'email';
--
-- Dropping `contact_email` is *not* refused, and the asymmetry is deliberate: an address
-- is contact data, not a guarantee. Nothing in the product replays it, no invariant
-- depends on it, and dropping it is in fact the desired outcome under a data-retention
-- request. The cost is stated rather than hidden: after this column is gone, no further
-- ticket email can be sent for a purchase until the buyer provides an address again.
DO $$
DECLARE
  delivered integer;
BEGIN
  SELECT count(*) INTO delivered FROM cinema.outbox_deliveries WHERE consumer = 'email';
  IF delivered > 0 THEN
    RAISE EXCEPTION 'refusing to roll back 0009_email_delivery: % ticket email(s) were already delivered', delivered
      USING HINT = 'Dropping the delivery record would make the worker send every one of them again. Delete those records explicitly if that is what you mean.';
  END IF;
END $$;

DROP INDEX cinema.outbox_pending_by_topic;
DROP TABLE cinema.outbox_deliveries;

ALTER TABLE cinema.reservations
  DROP COLUMN contact_email;

DELETE FROM platform.schema_migrations WHERE version = '0009_email_delivery';
