-- Roll back only the one-open-attempt guard of the checkout: the 0005 checkout
-- tables, the M2 idempotency records, the M2 hold tables and every row they carry
-- remain.
--
-- This rollback needs no guard of its own, unlike 0005_checkout.down.sql: dropping
-- an index loses no data. It removes a guard, never a row, so a database that has
-- sold something keeps its payment attempts, tickets and outbox intact — and only
-- loses the protection against a second open attempt per reservation.
DROP INDEX IF EXISTS cinema.payment_attempts_open_per_reservation;

DELETE FROM platform.schema_migrations WHERE version = '0006_one_open_payment_attempt';
