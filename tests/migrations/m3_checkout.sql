\set ON_ERROR_STOP on
-- M3 checkout: one price per event, the durable payment attempt, the sold ticket
-- and the transactional outbox, plus the guards that make a double payment and a
-- duplicate ticket impossible.
-- Runs after 0001_init, 0002_venue_layout, 0003_reservation, 0004_idempotency and
-- 0005_checkout; every fixture is rolled back.
--
-- The rule "one open payment attempt per reservation" is 0006, not 0005, and is
-- pinned by tests/migrations/m3_one_open_attempt.sql. So the fixtures here keep at
-- most one *pending* attempt per reservation, which is what makes this file pass on
-- the 0005 schema and on the full chain alike.
BEGIN;

-- M1 fixture: one room with a published three-seat layout, one film, and two
-- events whose seats are materialized by the M1 trigger.
INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('00000000-0000-0000-0000-0000000003c1','Cine M3 Checkout','UTC');
INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('00000000-0000-0000-0000-0000000003c2','00000000-0000-0000-0000-0000000003c1','Sala Checkout');
INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('00000000-0000-0000-0000-0000000003c3','00000000-0000-0000-0000-0000000003c2',1);
INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('00000000-0000-0000-0000-0000000003c4','00000000-0000-0000-0000-0000000003c3','A',1);
INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
 ('00000000-0000-0000-0000-0000000003c6','00000000-0000-0000-0000-0000000003c4','1',1,'standard',true),
 ('00000000-0000-0000-0000-0000000003c7','00000000-0000-0000-0000-0000000003c4','2',2,'standard',true),
 ('00000000-0000-0000-0000-0000000003c8','00000000-0000-0000-0000-0000000003c4','3',3,'standard',true);
UPDATE cinema.layout_versions SET status='published' WHERE id='00000000-0000-0000-0000-0000000003c3';
INSERT INTO cinema.films (id,title,runtime_minutes,rating) VALUES ('00000000-0000-0000-0000-0000000003c9','M3 Checkout Film',100,'PG');
INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES
 ('00000000-0000-0000-0000-0000000003d1','00000000-0000-0000-0000-0000000003c2','00000000-0000-0000-0000-0000000003c9','00000000-0000-0000-0000-0000000003c3',now(),'on_sale'),
 ('00000000-0000-0000-0000-0000000003d2','00000000-0000-0000-0000-0000000003c2','00000000-0000-0000-0000-0000000003c9','00000000-0000-0000-0000-0000000003c3',now() + interval '1 day','on_sale');

CREATE TEMP TABLE m3_seats ON COMMIT DROP AS
SELECT r.label || ls.number AS label, es.id, es.event_id
FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
JOIN cinema.layout_rows r ON r.id = ls.layout_row_id;

-- The checkout schema exists with the shape the checkout transaction relies on.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='events' AND column_name='price_cents'
      AND data_type='integer' AND is_nullable='NO' AND column_default = '0'
  ) THEN RAISE EXCEPTION 'events.price_cents must be a NOT NULL integer defaulting to 0'; END IF;

  -- The checkout has to be able to mark a reservation paid, and the M2 sweep
  -- selects only live holds, so 'paid' has to be part of the same constraint.
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='reservations_status_check' AND conrelid='cinema.reservations'::regclass
      AND pg_get_constraintdef(oid) LIKE '%paid%'
  ) THEN RAISE EXCEPTION 'reservations_status_check must accept the paid status'; END IF;

  IF to_regclass('cinema.payment_attempts') IS NULL THEN
    RAISE EXCEPTION 'missing table cinema.payment_attempts';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='reservation_id'
      AND data_type='uuid' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'payment_attempts.reservation_id must be a NOT NULL uuid'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='provider'
      AND data_type='text' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'payment_attempts.provider must be NOT NULL text'; END IF;
  -- The attempt is written before the provider is called, so the reference and the
  -- verdict cannot be required columns: they are required by the decided-state
  -- constraint below instead.
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='provider_reference'
      AND data_type='text' AND is_nullable='YES'
  ) THEN RAISE EXCEPTION 'payment_attempts.provider_reference must be nullable text'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='amount_cents'
      AND data_type='integer' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'payment_attempts.amount_cents must be a NOT NULL integer'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='outcome'
      AND data_type='text' AND is_nullable='YES'
  ) THEN RAISE EXCEPTION 'payment_attempts.outcome must be nullable text'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='status'
      AND data_type='text' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'payment_attempts.status must be NOT NULL text'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='idempotency_key'
      AND data_type='text' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'payment_attempts.idempotency_key must be NOT NULL text'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='request_fingerprint'
      AND data_type='text' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'payment_attempts.request_fingerprint must be NOT NULL text'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='payment_attempts' AND column_name='created_at'
      AND data_type='timestamp with time zone' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'payment_attempts.created_at must be NOT NULL timestamptz'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='f'
      AND conrelid='cinema.payment_attempts'::regclass AND confrelid='cinema.reservations'::regclass
  ) THEN RAISE EXCEPTION 'payment attempts must reference cinema.reservations'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='cinema' AND c.relname='payment_attempts_captured_per_reservation' AND i.indisunique
      AND pg_get_indexdef(i.indexrelid) LIKE '%(reservation_id)%'
      AND coalesce(pg_get_expr(i.indpred, i.indrelid),'') LIKE '%captured%'
  ) THEN RAISE EXCEPTION 'missing partial unique index payment_attempts_captured_per_reservation'; END IF;
  -- The provider reference is unique only among the rows that have one: a pending
  -- attempt that never reached the provider stores no reference at all.
  IF NOT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='cinema' AND c.relname='payment_attempts_provider_reference' AND i.indisunique
      AND pg_get_indexdef(i.indexrelid) LIKE '%(provider, provider_reference)%'
      AND coalesce(pg_get_expr(i.indpred, i.indrelid),'') LIKE '%provider_reference IS NOT NULL%'
  ) THEN RAISE EXCEPTION 'missing partial unique index payment_attempts_provider_reference'; END IF;
  -- The client key is scoped to its reservation, so a key another buyer used can
  -- never answer for this reservation.
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint con
    WHERE con.contype='u' AND con.conrelid='cinema.payment_attempts'::regclass
      AND (SELECT string_agg(a.attname, ',' ORDER BY k.ord)
           FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
           JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum) = 'reservation_id,idempotency_key'
  ) THEN RAISE EXCEPTION 'payment attempts must be unique per (reservation_id, idempotency_key)'; END IF;

  IF to_regclass('cinema.tickets') IS NULL THEN
    RAISE EXCEPTION 'missing table cinema.tickets';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='tickets' AND column_name='event_id'
      AND data_type='uuid' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'tickets.event_id must be a NOT NULL uuid'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='tickets' AND column_name='event_seat_id'
      AND data_type='bigint' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'tickets.event_seat_id must be a NOT NULL bigint'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='tickets' AND column_name='status'
      AND data_type='text' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'tickets.status must be NOT NULL text'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='tickets' AND column_name='issued_at'
      AND data_type='timestamp with time zone' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'tickets.issued_at must be NOT NULL timestamptz'; END IF;
  -- The seat and the reservation are tied to the same event, exactly like
  -- cinema.reservation_seats: a composite foreign key, never a bare id.
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='f'
      AND conrelid='cinema.tickets'::regclass AND confrelid='cinema.event_seats'::regclass
      AND array_length(conkey, 1) = 2
  ) THEN RAISE EXCEPTION 'tickets must reference cinema.event_seats by (event_seat_id, event_id)'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='f'
      AND conrelid='cinema.tickets'::regclass AND confrelid='cinema.reservations'::regclass
      AND array_length(conkey, 1) = 2
  ) THEN RAISE EXCEPTION 'tickets must reference cinema.reservations by (reservation_id, event_id)'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint con
    WHERE con.contype='u' AND con.conrelid='cinema.tickets'::regclass
      AND (SELECT string_agg(a.attname, ',' ORDER BY k.ord)
           FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
           JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum) = 'event_seat_id'
  ) THEN RAISE EXCEPTION 'tickets must be unique per event seat'; END IF;

  IF to_regclass('cinema.outbox') IS NULL THEN
    RAISE EXCEPTION 'missing table cinema.outbox';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='outbox' AND column_name='payload' AND data_type='jsonb' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'outbox.payload must be NOT NULL jsonb'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='outbox' AND column_name='published_at'
      AND data_type='timestamp with time zone' AND is_nullable='YES'
  ) THEN RAISE EXCEPTION 'outbox.published_at must be a nullable timestamptz'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='cinema' AND c.relname='outbox_unpublished'
      AND coalesce(pg_get_expr(i.indpred, i.indrelid),'') LIKE '%published_at IS NULL%'
  ) THEN RAISE EXCEPTION 'missing index outbox_unpublished on the unpublished rows'; END IF;
END $$;

-- One price per event: the default is zero and a real price is stored as minor units.
DO $$
BEGIN
  IF (SELECT price_cents FROM cinema.events WHERE id='00000000-0000-0000-0000-0000000003d1') <> 0 THEN
    RAISE EXCEPTION 'an event must default to price 0';
  END IF;
  UPDATE cinema.events SET price_cents = 1250 WHERE id='00000000-0000-0000-0000-0000000003d1';
  IF (SELECT price_cents FROM cinema.events WHERE id='00000000-0000-0000-0000-0000000003d1') <> 1250 THEN
    RAISE EXCEPTION 'an event price must be storable in minor units';
  END IF;
  UPDATE cinema.events SET price_cents = 900 WHERE id='00000000-0000-0000-0000-0000000003d2';
END $$;

-- A negative price is refused and leaves the stored price untouched. A zero price
-- is accepted: whether it can be sold is a checkout decision, not a schema one.
DO $$
BEGIN
  BEGIN
    UPDATE cinema.events SET price_cents = -1 WHERE id='00000000-0000-0000-0000-0000000003d1';
    RAISE EXCEPTION 'a negative event price was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF (SELECT price_cents FROM cinema.events WHERE id='00000000-0000-0000-0000-0000000003d1') <> 1250 THEN
    RAISE EXCEPTION 'the rejected negative price overwrote the stored price';
  END IF;
  BEGIN
    INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status,price_cents) VALUES
      ('00000000-0000-0000-0000-0000000003d3','00000000-0000-0000-0000-0000000003c2',
       '00000000-0000-0000-0000-0000000003c9','00000000-0000-0000-0000-0000000003c3',now() + interval '2 days','on_sale',-1250);
    RAISE EXCEPTION 'an event inserted with a negative price was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.events WHERE id='00000000-0000-0000-0000-0000000003d3') THEN
    RAISE EXCEPTION 'rejected negative-price event persisted';
  END IF;
END $$;

-- Holds: the first one pays, the second one is the bystander of every duplicate,
-- and the third one is the bystander of the duplicate that needs a reservation of
-- its own because a reservation may only hold one open attempt (0006).
INSERT INTO cinema.reservations (id, event_id, expires_at) VALUES
 ('00000000-0000-0000-0000-0000000003e1','00000000-0000-0000-0000-0000000003d1', now() + interval '10 minutes'),
 ('00000000-0000-0000-0000-0000000003e2','00000000-0000-0000-0000-0000000003d1', now() + interval '10 minutes'),
 ('00000000-0000-0000-0000-0000000003e5','00000000-0000-0000-0000-0000000003d1', now() + interval '10 minutes');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000003e1','00000000-0000-0000-0000-0000000003d1', id
FROM m3_seats WHERE event_id='00000000-0000-0000-0000-0000000003d1' AND label='A1';
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000003e2','00000000-0000-0000-0000-0000000003d1', id
FROM m3_seats WHERE event_id='00000000-0000-0000-0000-0000000003d1' AND label='A2';

-- The capture transaction marks the reservation paid, and the M2 sweep must leave
-- it alone: a reservation with a captured payment cannot be expired (money would be
-- taken and the seats released). The sweep's candidate query is the one in
-- apps/api/internal/reservation/expiry.go, mirrored here with a past deadline.
INSERT INTO cinema.reservations (id, event_id, expires_at, status) VALUES
 ('00000000-0000-0000-0000-0000000003e3','00000000-0000-0000-0000-0000000003d1', now() - interval '1 minute', 'held'),
 ('00000000-0000-0000-0000-0000000003e4','00000000-0000-0000-0000-0000000003d1', now() - interval '1 minute', 'held');
-- The candidate query is a temp view so it is evaluated at read time, like the
-- sweep's own SELECT, and disappears with the session that ran this test.
CREATE TEMP VIEW m3_expiry_candidates AS
SELECT r.id
FROM cinema.reservations r
WHERE r.status IN ('held', 'payment_pending')
  AND r.expires_at <= now()
ORDER BY r.expires_at, r.id;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM m3_expiry_candidates WHERE id='00000000-0000-0000-0000-0000000003e3') THEN
    RAISE EXCEPTION 'a live hold past its deadline must be an expiry candidate';
  END IF;
  BEGIN
    UPDATE cinema.reservations SET status='paid' WHERE id='00000000-0000-0000-0000-0000000003e4';
  EXCEPTION WHEN check_violation THEN
    RAISE EXCEPTION 'a reservation must be markable as paid by the capture transaction: %', SQLERRM;
  END;
  IF (SELECT status FROM cinema.reservations WHERE id='00000000-0000-0000-0000-0000000003e4') <> 'paid' THEN
    RAISE EXCEPTION 'the paid status was not stored';
  END IF;
  IF EXISTS (SELECT 1 FROM m3_expiry_candidates WHERE id='00000000-0000-0000-0000-0000000003e4') THEN
    RAISE EXCEPTION 'a paid reservation must never be an expiry candidate';
  END IF;
  IF EXISTS (SELECT 1 FROM m3_expiry_candidates WHERE id='00000000-0000-0000-0000-0000000003e1') THEN
    RAISE EXCEPTION 'a hold whose deadline has not elapsed must not be an expiry candidate';
  END IF;
END $$;

-- A captured attempt is stored and is the one payment of its reservation.
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-0000000003f1','00000000-0000-0000-0000-0000000003e1','simulated','ref-captured-1',1250,'approved','captured','key-captured-1','fingerprint-captured-1');
DO $$
BEGIN
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-0000000003e1') <> 1 THEN
    RAISE EXCEPTION 'a captured payment attempt was not stored';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM cinema.payment_attempts
    WHERE reservation_id='00000000-0000-0000-0000-0000000003e1'
      AND status='captured' AND outcome='approved' AND amount_cents=1250
  ) THEN RAISE EXCEPTION 'the captured attempt must keep its outcome and amount'; END IF;
END $$;

-- The attempt is written before the provider is called, so that row has neither a
-- reference nor a verdict yet. Without this shape the crash window the table exists
-- to close cannot be recorded at all.
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-0000000003fb','00000000-0000-0000-0000-0000000003e2','simulated',1250,'pending','key-precall-1','fingerprint-precall-1');
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM cinema.payment_attempts
    WHERE id='00000000-0000-0000-0000-0000000003fb'
      AND provider_reference IS NULL AND outcome IS NULL AND status='pending'
  ) THEN RAISE EXCEPTION 'a pending attempt before the provider call must be storable without a reference or an outcome'; END IF;
  -- Two pending attempts without a reference do not collide: the provider reference
  -- is only unique among the rows that have one. They belong to different
  -- reservations because 0006 allows one open attempt per reservation, which is a
  -- rule about the reservation's money and not about this partial index.
  INSERT INTO cinema.payment_attempts
    (id, reservation_id, provider, amount_cents, status, idempotency_key, request_fingerprint)
  VALUES
    ('00000000-0000-0000-0000-0000000003fc','00000000-0000-0000-0000-0000000003e5','simulated',1250,'pending','key-precall-2','fingerprint-precall-2');
  IF (SELECT count(*) FROM cinema.payment_attempts
      WHERE id IN ('00000000-0000-0000-0000-0000000003fb','00000000-0000-0000-0000-0000000003fc')
        AND provider_reference IS NULL) <> 2 THEN
    RAISE EXCEPTION 'two pending attempts without a provider reference must be storable';
  END IF;
END $$;

-- The double-payment guard: a second captured attempt for one reservation is
-- refused by the database, and the refused row persists nothing.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003f2','00000000-0000-0000-0000-0000000003e1','simulated','ref-captured-2',1250,'approved','captured','key-captured-2','fingerprint-captured-2');
    RAISE EXCEPTION 'a second captured attempt for one reservation was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-0000000003f2') THEN
    RAISE EXCEPTION 'rejected second captured attempt persisted';
  END IF;
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-0000000003e1' AND status='captured') <> 1 THEN
    RAISE EXCEPTION 'one reservation must keep exactly one captured attempt';
  END IF;
END $$;

-- The guard is about captured attempts only: a declined attempt of the same
-- reservation is a different, allowed history row.
INSERT INTO cinema.payment_attempts
  (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
VALUES
  ('00000000-0000-0000-0000-0000000003f3','00000000-0000-0000-0000-0000000003e1','simulated','ref-declined-1',1250,'declined','declined','key-declined-1','fingerprint-declined-1');
DO $$
BEGIN
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-0000000003e1') <> 2 THEN
    RAISE EXCEPTION 'a declined attempt must be storable next to the captured one';
  END IF;
END $$;

-- One provider reference belongs to at most one attempt, whatever the reservation.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003f4','00000000-0000-0000-0000-0000000003e2','simulated','ref-captured-1',1250,'approved','captured','key-captured-4','fingerprint-captured-4');
    RAISE EXCEPTION 'a duplicated (provider, provider reference) was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-0000000003f4') THEN
    RAISE EXCEPTION 'rejected duplicated provider reference persisted';
  END IF;
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE provider='simulated' AND provider_reference='ref-captured-1') <> 1 THEN
    RAISE EXCEPTION 'a provider reference must stay unique';
  END IF;
  -- The same reference under another provider is a different reference. The row is
  -- 'failed' rather than 'pending' so it is not a second open attempt of a
  -- reservation that already holds one (0006); the reference index it exercises does
  -- not look at status.
  INSERT INTO cinema.payment_attempts
    (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
  VALUES
    ('00000000-0000-0000-0000-0000000003f5','00000000-0000-0000-0000-0000000003e2','other-provider','ref-captured-1',1250,NULL,'failed','key-captured-5','fingerprint-captured-5');
END $$;

-- The client idempotency key is scoped to its reservation: a replay of that
-- reservation finds the stored row instead of charging again, and a key another
-- buyer used can never answer for this reservation.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003f6','00000000-0000-0000-0000-0000000003e2','simulated','ref-reused-key',1250,'approved','pending','key-captured-5','fingerprint-captured-6');
    RAISE EXCEPTION 'a reused payment idempotency key for one reservation was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-0000000003f6') THEN
    RAISE EXCEPTION 'rejected reused idempotency key persisted';
  END IF;
  IF (SELECT request_fingerprint FROM cinema.payment_attempts
      WHERE reservation_id='00000000-0000-0000-0000-0000000003e2' AND idempotency_key='key-captured-5') <> 'fingerprint-captured-5' THEN
    RAISE EXCEPTION 'the rejected reuse overwrote the stored fingerprint';
  END IF;
  -- The same key on another reservation is another attempt, because the key only
  -- identifies a request inside the reservation it was sent for.
  INSERT INTO cinema.payment_attempts
    (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
  VALUES
    ('00000000-0000-0000-0000-0000000003fd','00000000-0000-0000-0000-0000000003e3','simulated','ref-other-reservation',1250,'approved','pending','key-captured-5','fingerprint-other-reservation');
  IF (SELECT count(*) FROM cinema.payment_attempts WHERE idempotency_key='key-captured-5') <> 2 THEN
    RAISE EXCEPTION 'the same idempotency key must be storable for two different reservations';
  END IF;
END $$;

-- A decided attempt always names the provider's object and its verdict: the
-- columns are nullable only for the rows that have not reached that point yet.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003fe','00000000-0000-0000-0000-0000000003e2','simulated',NULL,1250,'approved','captured','key-null-ref','fingerprint-null-ref');
    RAISE EXCEPTION 'a captured attempt without a provider reference was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-0000000003fe') THEN
    RAISE EXCEPTION 'rejected captured attempt without a reference persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003ff','00000000-0000-0000-0000-0000000003e2','simulated','ref-null-outcome',1250,NULL,'declined','key-null-outcome','fingerprint-null-outcome');
    RAISE EXCEPTION 'a declined attempt without an outcome was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-0000000003ff') THEN
    RAISE EXCEPTION 'rejected declined attempt without an outcome persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-00000000040a','00000000-0000-0000-0000-0000000003e2','simulated',NULL,1250,'approved','pending','key-verdict','fingerprint-verdict');
    RAISE EXCEPTION 'a verdict without a provider reference was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-00000000040a') THEN
    RAISE EXCEPTION 'rejected verdict without a reference persisted';
  END IF;
END $$;

-- The state and the verdict cannot disagree: a captured attempt is approved and a
-- declined attempt is declined.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-00000000040b','00000000-0000-0000-0000-0000000003e2','simulated','ref-captured-declined',1250,'declined','captured','key-captured-declined','fingerprint-captured-declined');
    RAISE EXCEPTION 'a captured attempt with a declined outcome was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-00000000040c','00000000-0000-0000-0000-0000000003e2','simulated','ref-declined-approved',1250,'approved','declined','key-declined-approved','fingerprint-declined-approved');
    RAISE EXCEPTION 'a declined attempt with an approved outcome was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id IN ('00000000-0000-0000-0000-00000000040b','00000000-0000-0000-0000-00000000040c')) THEN
    RAISE EXCEPTION 'rejected incoherent attempt persisted';
  END IF;
END $$;

-- A payment amount is always positive minor units, and the enumerated states are
-- the only ones the checkout can write.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003f7','00000000-0000-0000-0000-0000000003e2','simulated','ref-zero',0,'approved','pending','key-zero','fingerprint-zero');
    RAISE EXCEPTION 'a zero amount was accepted as a payment';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE id='00000000-0000-0000-0000-0000000003f7') THEN
    RAISE EXCEPTION 'rejected zero-amount attempt persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003f8','00000000-0000-0000-0000-0000000003e2','simulated','ref-outcome',1250,'refunded','pending','key-outcome','fingerprint-outcome');
    RAISE EXCEPTION 'an outcome outside (approved, declined) was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003f9','00000000-0000-0000-0000-0000000003e2','simulated','ref-status',1250,'approved','refunded','key-status','fingerprint-status');
    RAISE EXCEPTION 'a status outside (pending, captured, declined, failed) was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE reservation_id='00000000-0000-0000-0000-0000000003e2' AND idempotency_key IN ('key-outcome','key-status')) THEN
    RAISE EXCEPTION 'rejected attempt with an unknown state persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.payment_attempts
      (id, reservation_id, provider, provider_reference, amount_cents, outcome, status, idempotency_key, request_fingerprint)
    VALUES
      ('00000000-0000-0000-0000-0000000003fa','00000000-0000-0000-0000-0000000003ef','simulated','ref-unknown-hold',1250,'approved','pending','key-unknown-hold','fingerprint-unknown-hold');
    RAISE EXCEPTION 'an attempt for an unknown reservation was accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.payment_attempts WHERE idempotency_key='key-unknown-hold') THEN
    RAISE EXCEPTION 'rejected attempt for an unknown reservation persisted';
  END IF;
END $$;

-- The first ticket of each sold seat is issued; a second ticket for the same seat
-- is refused, even from another reservation.
INSERT INTO cinema.tickets (id, reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000004a1','00000000-0000-0000-0000-0000000003e1','00000000-0000-0000-0000-0000000003d1', id
FROM m3_seats WHERE event_id='00000000-0000-0000-0000-0000000003d1' AND label='A1';
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m3_seats WHERE event_id='00000000-0000-0000-0000-0000000003d1' AND label='A1';
  IF NOT EXISTS (
    SELECT 1 FROM cinema.tickets
    WHERE reservation_id='00000000-0000-0000-0000-0000000003e1' AND event_id='00000000-0000-0000-0000-0000000003d1'
      AND event_seat_id=target AND status='issued' AND issued_at IS NOT NULL
  ) THEN RAISE EXCEPTION 'an issued ticket must be stored with its event, status and issue time'; END IF;
  BEGIN
    INSERT INTO cinema.tickets (id, reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000004a2','00000000-0000-0000-0000-0000000003e2','00000000-0000-0000-0000-0000000003d1', target);
    RAISE EXCEPTION 'a second ticket for one event seat was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.tickets WHERE id='00000000-0000-0000-0000-0000000004a2') THEN
    RAISE EXCEPTION 'rejected duplicate ticket persisted';
  END IF;
  IF (SELECT count(*) FROM cinema.tickets WHERE event_seat_id=target) <> 1 THEN
    RAISE EXCEPTION 'one event seat must keep exactly one ticket';
  END IF;
END $$;

-- A ticket belongs to its reservation's event: the composite keys make a ticket
-- for a seat of another event impossible, and one for the reservation's own seat
-- still legal.
DO $$
DECLARE foreign_seat bigint;
BEGIN
  SELECT id INTO foreign_seat FROM m3_seats WHERE event_id='00000000-0000-0000-0000-0000000003d2' AND label='A3';
  BEGIN
    INSERT INTO cinema.tickets (id, reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000004a3','00000000-0000-0000-0000-0000000003e2','00000000-0000-0000-0000-0000000003d2', foreign_seat);
    RAISE EXCEPTION 'a ticket for a seat of another event was accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.tickets WHERE id='00000000-0000-0000-0000-0000000004a3') THEN
    RAISE EXCEPTION 'rejected cross-event ticket persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.tickets (id, reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000004a4','00000000-0000-0000-0000-0000000003e2','00000000-0000-0000-0000-0000000003d2',
            (SELECT id FROM m3_seats WHERE event_id='00000000-0000-0000-0000-0000000003d1' AND label='A3'));
    RAISE EXCEPTION 'a ticket whose event disagrees with its seat was accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.tickets WHERE id='00000000-0000-0000-0000-0000000004a4') THEN
    RAISE EXCEPTION 'rejected mismatched-event ticket persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.tickets (id, reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000004a5','00000000-0000-0000-0000-0000000003e2','00000000-0000-0000-0000-0000000003d1',
            (SELECT id FROM m3_seats WHERE event_id='00000000-0000-0000-0000-0000000003d1' AND label='A2'));
  EXCEPTION WHEN others THEN
    RAISE EXCEPTION 'a ticket for the reservation''s own held seat must be accepted: %', SQLERRM;
  END;
  IF NOT EXISTS (SELECT 1 FROM cinema.tickets WHERE id='00000000-0000-0000-0000-0000000004a5') THEN
    RAISE EXCEPTION 'a legal ticket for the reservation''s own seat was not stored';
  END IF;
END $$;

-- The outbox row is written in the same transaction and stays unpublished until a
-- relay picks it up, which is what the partial index is for.
INSERT INTO cinema.outbox (id, topic, payload) VALUES
  ('00000000-0000-0000-0000-0000000004b1','ticket.issued','{"ticketId":"00000000-0000-0000-0000-0000000004a1","reservationId":"00000000-0000-0000-0000-0000000003e1"}');
DO $$
BEGIN
  IF (SELECT published_at FROM cinema.outbox WHERE id='00000000-0000-0000-0000-0000000004b1') IS NOT NULL THEN
    RAISE EXCEPTION 'a new outbox row must start unpublished';
  END IF;
  IF (SELECT payload->>'ticketId' FROM cinema.outbox WHERE id='00000000-0000-0000-0000-0000000004b1')
     <> '00000000-0000-0000-0000-0000000004a1' THEN
    RAISE EXCEPTION 'the outbox payload must be stored as jsonb';
  END IF;
  IF (SELECT count(*) FROM cinema.outbox WHERE id='00000000-0000-0000-0000-0000000004b1' AND published_at IS NULL) <> 1 THEN
    RAISE EXCEPTION 'the unpublished-row index must find the new outbox row';
  END IF;
  BEGIN
    INSERT INTO cinema.outbox (id, topic, payload) VALUES
      ('00000000-0000-0000-0000-0000000004b2','   ','{}');
    RAISE EXCEPTION 'an outbox row without a topic was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.outbox WHERE id='00000000-0000-0000-0000-0000000004b2') THEN
    RAISE EXCEPTION 'rejected outbox row persisted';
  END IF;
END $$;

ROLLBACK;
