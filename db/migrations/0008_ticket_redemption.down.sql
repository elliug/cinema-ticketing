-- Roll back the door's record: the two columns 0008_ticket_redemption added, and
-- nothing else. Every other ticket column, every ticket row and every PDF stays.
--
-- This rollback refuses while any ticket has been admitted. Dropping `redeemed_at`
-- would not only erase when each one was used, it would make every admitted ticket
-- look free to be admitted again — a single-use guarantee silently reversed on a
-- database whose holders are still walking in with their codes. That is the same
-- reason 0005_checkout.down.sql refuses while money has been captured: a rollback
-- that deletes the only record of something that already happened is not a rollback.
--
-- To roll back anyway, resolve the redemptions first, knowing the consequence:
--
--   UPDATE cinema.tickets SET redeemed_at = NULL, redeemed_by = NULL
--   WHERE redeemed_at IS NOT NULL;
--
-- The dropping of the columns takes `tickets_redeemed_by_check` with it, because the
-- constraint belongs to the column.
-- The dropping of the columns takes `tickets_redeemed_by_check` with it, because the
-- constraint belongs to the column; the guard trigger and its function are dropped by
-- name, because they are objects of their own and would otherwise outlive the column
-- they guard.
DO $$
DECLARE
  admitted integer;
BEGIN
  SELECT count(*) INTO admitted FROM cinema.tickets WHERE redeemed_at IS NOT NULL;
  IF admitted > 0 THEN
    RAISE EXCEPTION 'refusing to roll back 0008_ticket_redemption: % ticket(s) were admitted at the door', admitted
      USING HINT = 'Dropping redeemed_at would let every one of them be admitted again. Clear those redemptions explicitly if that is what you mean.';
  END IF;
END $$;

ALTER TABLE cinema.tickets
  DROP COLUMN redeemed_by,
  DROP COLUMN redeemed_at;

DROP TRIGGER guard_ticket_redemption ON cinema.tickets;
DROP FUNCTION cinema.guard_ticket_redemption();

DELETE FROM platform.schema_migrations WHERE version = '0008_ticket_redemption';
