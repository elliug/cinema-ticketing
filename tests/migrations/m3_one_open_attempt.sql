\set ON_ERROR_STOP on
-- M3 checkout, part two: one open payment attempt per reservation.
--
-- Runs after 0001_init, 0002_venue_layout, 0003_reservation, 0004_idempotency,
-- 0005_checkout and 0006_one_open_payment_attempt; every fixture is rolled back.
--
-- What is pinned here is the invariant 0006 adds, not a timed approximation of it:
-- a reservation can carry at most one *open* (pending) payment attempt, whatever
-- the age of that attempt, because an open attempt means the provider may already
-- hold the money for those seats.
--
-- The 0006 rollback itself (the index disappears, the checkout tables stay) is
-- verified by scripts/migrations/verify_migrations.sh: a test file cannot drop and
-- reapply a migration inside its own transaction.
BEGIN;

-- M1 fixture: one room with a published three-seat layout, one film, and the event
-- the reservations below are paid for.
INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('00000000-0000-0000-0000-00000000e0a1','Cine M3 Open Attempt','UTC');
INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('00000000-0000-0000-0000-00000000e0a2','00000000-0000-0000-0000-00000000e0a1','Sala Abierta');
INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('00000000-0000-0000-0000-00000000e0a3','00000000-0000-0000-0000-00000000e0a2',1);
INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('00000000-0000-0000-0000-00000000e0a4','00000000-0000-0000-0000-00000000e0a3','A',1);
INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
 ('00000000-0000-0000-0000-00000000e0a6','00000000-0000-0000-0000-00000000e0a4','1',1,'standard',true),
 ('00000000-0000-0000-0000-00000000e0a7','00000000-0000-0000-0000-00000000e0a4','2',2,'standard',true),
 ('00000000-0000-0000-0000-00000000e0a8','00000000-0000-0000-0000-00000000e0a4','3',3,'standard',true);
UPDATE cinema.layout_versions SET status='published' WHERE id='00000000-0000-0000-0000-00000000e0a3';
INSERT INTO cinema.films (id,title,runtime_minutes,rating) VALUES ('00000000-0000-0000-0000-00000000e0a9','M3 Open Attempt Film',95,'PG');
INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES
 ('00000000-0000-0000-0000-00000000e0b1','00000000-0000-0000-0000-00000000e0a2','00000000-0000-0000-0000-00000000e0a9','00000000-0000-0000-0000-00000000e0a3',now() + interval '1 day','on_sale');

-- Four reservations of that event: the one whose payment is open, a bystander that
-- may open its own attempt, one that already captured a payment, and one whose
-- payment was declined.
INSERT INTO cinema.reservations (id, event_id, expires_at) VALUES
 ('00000000-0000-0000-0000-00000000e1a1','00000000-0000-0000-0000-00000000e0b1', now() + interval '10 minutes'),
 ('00000000-0000-0000-0000-00000000e1a2','00000000-0000-0000-0000-00000000e0b1', now() + interval '10 minutes'),
 ('00000000-0000-0000-0000-00000000e1a3','00000000-0000-0000-0000-00000000e0b1', now() + interval '10 minutes'),
 ('00000000-0000-0000-0000-00000000e1a4','00000000-0000-0000-0000-00000000e0b1', now() + interval '10 minutes');

-- The guard exists, has the shape the invariant needs, and says what it means.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='cinema' AND c.relname='payment_attempts_open_per_reservation' AND i.indisunique
      AND pg_get_indexdef(i.indexrelid) LIKE '%(reservation_id)%'
      AND coalesce(pg_get_expr(i.indpred, i.indrelid),'') LIKE '%pending%'
  ) THEN RAISE EXCEPTION 'missing partial unique index payment_attempts_open_per_reservation on (reservation_id) WHERE status = pending'; END IF;
  IF coalesce(obj_description('cinema.payment_attempts_open_per_reservation'::regclass, 'pg_class'), '') NOT LIKE '%may already hold the money%' THEN
    RAISE EXCEPTION 'the open-attempt index must carry a comment stating the invariant in domain terms';
  END IF;
  -- The 0005 guards survive: this version narrows the open case, it does not
  -- replace the captured one.
  IF NOT EXISTS (
    SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='cinema' AND c.relname='payment_attempts_captured_per_reservation'
  ) THEN RAISE EXCEPTION '0006 must not remove payment_attempts_captured_per_reservation'; END IF;
END $$;

-- A pending attempt is stored, and it is deliberately *aged*: the invariant is not
-- a window. A request that crashed leaves this row behind, and the money may already
-- be gone with it, so age must not weaken the guard.
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint, created_at)
VALUES
  ('00000000-0000-0000-0000-00000000e2a1','00000000-0000-0000-0000-00000000e1a1','simulated',1250,'pending','key-open','fingerprint-open', now() - interval '3 minutes');

-- A second open attempt for the same reservation is refused by the index itself,
-- and the refused row persists nothing: the first attempt stays the only one.
DO $$
DECLARE violated_constraint text;
BEGIN
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-00000000e2a2','00000000-0000-0000-0000-00000000e1a1','simulated',1250,'pending','key-open-second','fingerprint-open-second');
    RAISE EXCEPTION 'a second open payment attempt for one reservation was accepted';
  EXCEPTION WHEN unique_violation THEN
    GET STACKED DIAGNOSTICS violated_constraint = CONSTRAINT_NAME;
    IF violated_constraint <> 'payment_attempts_open_per_reservation' THEN
      RAISE EXCEPTION 'the second open attempt was refused by %, want payment_attempts_open_per_reservation', violated_constraint;
    END IF;
  END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-00000000e2a2') THEN
    RAISE EXCEPTION 'rejected second open attempt persisted';
  END IF;
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-00000000e1a1') <> 1 THEN
    RAISE EXCEPTION 'one reservation must keep exactly one open attempt';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM cinema.payment_attempts
    WHERE id='00000000-0000-0000-0000-00000000e2a1' AND status='pending' AND idempotency_key='key-open'
  ) THEN RAISE EXCEPTION 'the refused second attempt disturbed the first open attempt'; END IF;
END $$;

-- The guard is per reservation: another reservation may open its own attempt at the
-- same time, which is what keeps two buyers paying independently.
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-00000000e2a3','00000000-0000-0000-0000-00000000e1a2','simulated',1250,'pending','key-bystander','fingerprint-bystander');
DO $$
BEGIN
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-00000000e1a2' AND status='pending') <> 1 THEN
    RAISE EXCEPTION 'a second reservation must be able to open its own payment attempt';
  END IF;
END $$;

-- A decided attempt leaves the index, so paying again is still allowed after a
-- capture: the new attempt is a pending row next to the captured one, and the
-- 0005 double-payment guard is untouched (a second *captured* attempt stays
-- impossible, which is pinned by tests/migrations/m3_checkout.sql).
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-00000000e2a4','00000000-0000-0000-0000-00000000e1a3','simulated','ref-captured','1250','approved','captured','key-captured','fingerprint-captured');
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-00000000e2a5','00000000-0000-0000-0000-00000000e1a3','simulated',1250,'pending','key-after-capture','fingerprint-after-capture');
DO $$
BEGIN
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-00000000e1a3') <> 2 THEN
    RAISE EXCEPTION 'a captured attempt must not block a later attempt: the index covers open attempts only';
  END IF;
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-00000000e1a3' AND status='pending') <> 1 THEN
    RAISE EXCEPTION 'a captured reservation must be able to hold one open attempt';
  END IF;
END $$;

-- The same after a decline: the buyer may pay again inside the hold window.
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-00000000e2a6','00000000-0000-0000-0000-00000000e1a4','simulated','ref-declined',1250,'declined','declined','key-declined','fingerprint-declined');
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-00000000e2a7','00000000-0000-0000-0000-00000000e1a4','simulated',1250,'pending','key-after-decline','fingerprint-after-decline');
DO $$
BEGIN
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-00000000e1a4') <> 2 THEN
    RAISE EXCEPTION 'a declined attempt must not block a new payment: the index covers open attempts only';
  END IF;
  -- ...and now that the new attempt is open, a third one is refused again, so the
  -- guard keeps holding after a decline instead of being spent once.
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-00000000e2a8','00000000-0000-0000-0000-00000000e1a4','simulated',1250,'pending','key-third','fingerprint-third');
    RAISE EXCEPTION 'a second open attempt after a decline was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-00000000e2a8') THEN
    RAISE EXCEPTION 'rejected post-decline second open attempt persisted';
  END IF;
  -- Two decided attempts of one reservation are history and stay storable.
  INSERT INTO cinema.payment_attempts
    (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
  VALUES
    ('00000000-0000-0000-0000-00000000e2a9','00000000-0000-0000-0000-00000000e1a4','simulated','ref-declined-2',1250,'declined','declined','key-declined-2','fingerprint-declined-2');
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-00000000e1a4' AND status='declined') <> 2 THEN
    RAISE EXCEPTION 'decided attempts of one reservation must remain storable as history';
  END IF;
END $$;

ROLLBACK;
