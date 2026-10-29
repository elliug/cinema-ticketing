\set ON_ERROR_STOP on
-- M2 idempotency: durable hold request records, plus the availability predicate
-- that decides whether a seat is currently held.
-- Runs after 0001_init, 0002_venue_layout, 0003_reservation and 0004_idempotency;
-- every fixture is rolled back.
BEGIN;

-- M1 fixture: one room with a published two-seat layout, one film, and two
-- events whose seats are materialized by the M1 trigger.
INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('00000000-0000-0000-0000-0000000001c1','Cine M2 Idempotency','UTC');
INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('00000000-0000-0000-0000-0000000001c2','00000000-0000-0000-0000-0000000001c1','Sala Idempotency');
INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('00000000-0000-0000-0000-0000000001c3','00000000-0000-0000-0000-0000000001c2',1);
INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('00000000-0000-0000-0000-0000000001c4','00000000-0000-0000-0000-0000000001c3','A',1);
INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
 ('00000000-0000-0000-0000-0000000001c6','00000000-0000-0000-0000-0000000001c4','1',1,'standard',true),
 ('00000000-0000-0000-0000-0000000001c7','00000000-0000-0000-0000-0000000001c4','2',2,'standard',true);
UPDATE cinema.layout_versions SET status='published' WHERE id='00000000-0000-0000-0000-0000000001c3';
INSERT INTO cinema.films (id,title,runtime_minutes,rating) VALUES ('00000000-0000-0000-0000-0000000001c9','M2 Idempotency Film',100,'PG');
INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES
 ('00000000-0000-0000-0000-0000000001d1','00000000-0000-0000-0000-0000000001c2','00000000-0000-0000-0000-0000000001c9','00000000-0000-0000-0000-0000000001c3',now(),'on_sale'),
 ('00000000-0000-0000-0000-0000000001d2','00000000-0000-0000-0000-0000000001c2','00000000-0000-0000-0000-0000000001c9','00000000-0000-0000-0000-0000000001c3',now() + interval '1 day','on_sale');

CREATE TEMP TABLE m2i_seats ON COMMIT DROP AS
SELECT r.label || ls.number AS label, es.id, es.event_id
FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
JOIN cinema.layout_rows r ON r.id = ls.layout_row_id;

-- The availability predicate of apps/api/internal/reservation/service.go
-- (listSeatAvailabilityQuery). A seat counts as held only while its hold row is
-- active and its reservation has not expired; an expired hold that nobody has
-- re-claimed is still stored as active, so the deadline has to be checked too.
CREATE TEMP VIEW m2i_availability AS
SELECT es.id, es.event_id,
       EXISTS (
         SELECT 1
         FROM cinema.reservation_seats rs
         JOIN cinema.reservations r ON r.id = rs.reservation_id
         WHERE rs.event_seat_id = es.id
           AND rs.released_at IS NULL
           AND r.expires_at > now()
       ) AS held
FROM cinema.event_seats es;

-- The durable idempotency record exists, is durable, and carries one row per key.
DO $$
BEGIN
  IF to_regclass('cinema.idempotency_keys') IS NULL THEN
    RAISE EXCEPTION 'missing table cinema.idempotency_keys';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='idempotency_keys' AND column_name='idempotency_key'
      AND data_type='text' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'idempotency_key must be a NOT NULL text key'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='idempotency_keys' AND column_name='request_fingerprint'
      AND data_type='text' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'request_fingerprint must be NOT NULL text'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='idempotency_keys' AND column_name='status'
      AND data_type='integer' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'status must be a NOT NULL integer'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='idempotency_keys' AND column_name='hold_id'
      AND data_type='uuid' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'hold_id must be a NOT NULL uuid'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='idempotency_keys' AND column_name='hold_expires_at'
      AND data_type='timestamp with time zone' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'hold_expires_at must be NOT NULL timestamptz'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='idempotency_keys' AND column_name='expires_at'
      AND data_type='timestamp with time zone' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'expires_at must be NOT NULL timestamptz'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='p' AND conrelid='cinema.idempotency_keys'::regclass
      AND conkey = ARRAY[(SELECT attnum FROM pg_attribute
                          WHERE attrelid='cinema.idempotency_keys'::regclass AND attname='idempotency_key')]
  ) THEN RAISE EXCEPTION 'idempotency_keys must be keyed by idempotency_key'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='cinema' AND c.relname='idempotency_keys_expires_at'
      AND pg_get_indexdef(i.indexrelid) LIKE '%(expires_at)%'
  ) THEN RAISE EXCEPTION 'missing index idempotency_keys_expires_at'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='f'
      AND conrelid='cinema.idempotency_keys'::regclass AND confrelid='cinema.reservations'::regclass
  ) THEN RAISE EXCEPTION 'idempotency records must reference cinema.reservations'; END IF;
  -- The claim transaction writes its record before its hold, so the reference
  -- has to be deferred; otherwise every hold would violate it at insert time.
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='f'
      AND conrelid='cinema.idempotency_keys'::regclass AND confrelid='cinema.reservations'::regclass
      AND condeferrable AND condeferred
  ) THEN RAISE EXCEPTION 'the hold reference must be DEFERRABLE INITIALLY DEFERRED'; END IF;
END $$;

-- A hold can be recorded, and a replay finds it with its stored result.
INSERT INTO cinema.reservations (id, event_id, expires_at)
VALUES ('00000000-0000-0000-0000-0000000001e1','00000000-0000-0000-0000-0000000001d1', now() + interval '10 minutes');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000001e1','00000000-0000-0000-0000-0000000001d1', id
FROM m2i_seats WHERE event_id='00000000-0000-0000-0000-0000000001d1' AND label='A1';

INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
SELECT 'key-recorded', 'fingerprint-a1', 201, r.id, r.expires_at, r.expires_at + interval '24 hours'
FROM cinema.reservations r WHERE r.id='00000000-0000-0000-0000-0000000001e1';

DO $$
DECLARE stored record;
BEGIN
  SELECT status, hold_id::text AS hold_id, request_fingerprint INTO stored
  FROM cinema.idempotency_keys WHERE idempotency_key='key-recorded';
  IF stored IS NULL THEN RAISE EXCEPTION 'the idempotency record was not stored'; END IF;
  IF stored.status <> 201 OR stored.hold_id <> '00000000-0000-0000-0000-0000000001e1' THEN
    RAISE EXCEPTION 'the stored result must keep its status and hold id, got % / %', stored.status, stored.hold_id;
  END IF;
END $$;

-- Reusing a key for a different payload cannot even be stored: the key is unique.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
    SELECT 'key-recorded', 'fingerprint-a2', 201, r.id, r.expires_at, r.expires_at + interval '24 hours'
    FROM cinema.reservations r WHERE r.id='00000000-0000-0000-0000-0000000001e1';
    RAISE EXCEPTION 'a second record for one idempotency key was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF (SELECT request_fingerprint FROM cinema.idempotency_keys WHERE idempotency_key='key-recorded') <> 'fingerprint-a1' THEN
    RAISE EXCEPTION 'the rejected reuse overwrote the stored fingerprint';
  END IF;
END $$;

-- A record cannot point at a hold that does not exist, or carry a nonsense status.
DO $$
BEGIN
  -- The reference is deferred, so an unknown hold is still rejected, just at the
  -- point where the constraint is checked.
  SET CONSTRAINTS ALL IMMEDIATE;
  BEGIN
    INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
    VALUES ('key-unknown-hold', 'fingerprint-x', 201, '00000000-0000-0000-0000-0000000001ff', now(), now());
    RAISE EXCEPTION 'a record for an unknown hold was accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  SET CONSTRAINTS ALL DEFERRED;
  IF EXISTS (SELECT 1 FROM cinema.idempotency_keys WHERE idempotency_key='key-unknown-hold') THEN
    RAISE EXCEPTION 'rejected record without a hold persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
    SELECT 'key-bad-status', 'fingerprint-x', 42, r.id, r.expires_at, r.expires_at
    FROM cinema.reservations r WHERE r.id='00000000-0000-0000-0000-0000000001e1';
    RAISE EXCEPTION 'a status that is not an HTTP status was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
  BEGIN
    INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
    SELECT '   ', 'fingerprint-x', 201, r.id, r.expires_at, r.expires_at
    FROM cinema.reservations r WHERE r.id='00000000-0000-0000-0000-0000000001e1';
    RAISE EXCEPTION 'a blank idempotency key was accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
END $$;

-- The claim path writes the record before the hold it references. That order must
-- be legal inside one transaction, and the reference must be satisfied once the
-- hold is written (checked here by forcing a deferred check).
DO $$
BEGIN
  INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at)
  VALUES ('key-record-first', 'fingerprint-a2', 201, '00000000-0000-0000-0000-0000000001e5', now() + interval '10 minutes', now() + interval '24 hours');
  INSERT INTO cinema.reservations (id, event_id, expires_at)
  VALUES ('00000000-0000-0000-0000-0000000001e5','00000000-0000-0000-0000-0000000001d1', now() + interval '10 minutes');
  SET CONSTRAINTS ALL IMMEDIATE;
  SET CONSTRAINTS ALL DEFERRED;
EXCEPTION WHEN others THEN
  RAISE EXCEPTION 'a record written before its hold must satisfy the deferred reference: %', SQLERRM;
END $$;

-- Availability: a live hold marks its seat held and leaves the others free.
DO $$
DECLARE target bigint; other bigint;
BEGIN
  SELECT id INTO target FROM m2i_seats WHERE event_id='00000000-0000-0000-0000-0000000001d1' AND label='A1';
  SELECT id INTO other FROM m2i_seats WHERE event_id='00000000-0000-0000-0000-0000000001d1' AND label='A2';
  IF (SELECT held FROM m2i_availability WHERE id=target) IS NOT TRUE THEN
    RAISE EXCEPTION 'a live hold must report its seat as held';
  END IF;
  IF (SELECT held FROM m2i_availability WHERE id=other) IS NOT FALSE THEN
    RAISE EXCEPTION 'an unheld seat must report as free';
  END IF;
END $$;

-- Availability ignores an expired hold: the row is still stored as active until
-- the trigger or the expiry sweep releases it, and the read must not report it.
UPDATE cinema.reservations SET expires_at = now() - interval '1 minute'
WHERE id='00000000-0000-0000-0000-0000000001e1';
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m2i_seats WHERE event_id='00000000-0000-0000-0000-0000000001d1' AND label='A1';
  IF (SELECT released_at FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000001e1' AND event_seat_id=target) IS NOT NULL THEN
    RAISE EXCEPTION 'the lazy-expiry fixture must still be stored as active';
  END IF;
  IF (SELECT held FROM m2i_availability WHERE id=target) IS NOT FALSE THEN
    RAISE EXCEPTION 'an expired hold must not report its seat as held';
  END IF;
END $$;

-- A new claim takes the expired seat over, and availability follows the new hold.
INSERT INTO cinema.reservations (id, event_id, expires_at)
VALUES ('00000000-0000-0000-0000-0000000001e2','00000000-0000-0000-0000-0000000001d1', now() + interval '10 minutes');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000001e2','00000000-0000-0000-0000-0000000001d1', id
FROM m2i_seats WHERE event_id='00000000-0000-0000-0000-0000000001d1' AND label='A1';
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m2i_seats WHERE event_id='00000000-0000-0000-0000-0000000001d1' AND label='A1';
  IF (SELECT held FROM m2i_availability WHERE id=target) IS NOT TRUE THEN
    RAISE EXCEPTION 'the new hold must report the re-claimed seat as held';
  END IF;
  IF (SELECT released_at FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000001e1' AND event_seat_id=target) IS NULL THEN
    RAISE EXCEPTION 'the expired hold row must be released when the seat is re-claimed';
  END IF;
  IF (SELECT count(*) FROM m2i_availability WHERE event_id='00000000-0000-0000-0000-0000000001d2' AND held) <> 0 THEN
    RAISE EXCEPTION 'availability must not leak the holds of another event';
  END IF;
END $$;

ROLLBACK;
