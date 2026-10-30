-- M3 checkout, part two: one open payment attempt per reservation.
--
-- The checkout stores a `pending` cinema.payment_attempts row *before* it calls the
-- provider, and that row is the only record of a charge that may already have
-- happened: the provider holds money under a reference nobody else can settle. So
-- while an attempt is open, a second payment attempt for the same reservation is not
-- a retry, it is a second charge.
--
-- 0005 already refuses a second *captured* attempt, but that guard only fires after
-- the money is recorded. This version closes the window before it: the partial
-- unique index below allows at most one open attempt per reservation, so the
-- database itself refuses the second one. The application therefore does not have
-- to decide it, and cannot decide it wrongly under concurrency: the index is the
-- authority, whatever order the checkouts interleave in.
--
-- The index is partial on status = 'pending' on purpose. A decided attempt
-- (captured or declined) is history, not an open charge, so it leaves the index:
-- a buyer whose payment was declined may pay again inside the same hold with a new
-- key, and a sale that is already captured is guarded by
-- payment_attempts_captured_per_reservation instead. That is also why the indexed
-- column is the reservation and not the client key: the invariant is about one
-- reservation's money, and the key only names which attempt owns it.
--
-- Only a request holding the client Idempotency-Key of the open attempt can settle
-- it, which is what makes persisting that key across a page reload a requirement of
-- the client rather than a nicety: a browser that invents a fresh key on refresh is
-- told a payment is already under way, because that is the truth.
CREATE UNIQUE INDEX payment_attempts_open_per_reservation
  ON cinema.payment_attempts (reservation_id) WHERE status = 'pending';

COMMENT ON INDEX cinema.payment_attempts_open_per_reservation IS
  'One open payment attempt per reservation: an open attempt means the provider may already hold the money for that reservation, so a second concurrent payment would charge the buyer twice, and only the attempt that holds the client Idempotency-Key can settle it. A decided attempt (captured or declined) leaves the index, so paying again after a decline is still allowed, which is why the reservation and not the key is the indexed column.';

INSERT INTO platform.schema_migrations (version) VALUES ('0006_one_open_payment_attempt');
