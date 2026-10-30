-- Roll back only the M3 checkout objects; the 0004 idempotency records, the 0003
-- hold tables, every M1 row and the M0 platform metadata remain.
--
-- The first statement is a guard, so a rollback that must not happen is refused
-- before anything is dropped. Dropping the checkout rows cannot undo a payment, so
-- a database that sold tickets is not silently turned back into one holding unpaid
-- reservations; the failure leaves the 0005 schema exactly as it was.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM cinema.reservations WHERE status = 'paid') THEN
    RAISE EXCEPTION 'cannot roll back 0005_checkout while a reservation is paid: dropping the checkout rows would not undo the payment'
      USING ERRCODE = '23514';
  END IF;
  -- A captured payment whose reservation was moved off `paid` still must not lose
  -- its record: the money left the customer, so the evidence stays.
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE status = 'captured') THEN
    RAISE EXCEPTION 'cannot roll back 0005_checkout while a payment is captured: dropping the checkout rows would delete the record of money taken'
      USING ERRCODE = '23514';
  END IF;
END $$;

-- Restore the 0003 hold-lifecycle constraint verbatim, so a rolled back database is
-- exactly the pre-0005 schema. The guard above is what makes this safe.
ALTER TABLE cinema.reservations DROP CONSTRAINT reservations_status_check;
ALTER TABLE cinema.reservations
  ADD CONSTRAINT reservations_status_check
  CHECK (status IN ('held','payment_pending','expired','cancelled'));

-- The tables are dropped before the price column, because a ticket or an attempt
-- written by the checkout references a reservation whose event carries that price.
-- Dropping the tables removes their indexes (including the partial guards) with
-- them, so no index is dropped separately.
DROP TABLE cinema.outbox;
DROP TABLE cinema.tickets;
DROP TABLE cinema.payment_attempts;
ALTER TABLE cinema.events DROP COLUMN price_cents;

DELETE FROM platform.schema_migrations WHERE version = '0005_checkout';
