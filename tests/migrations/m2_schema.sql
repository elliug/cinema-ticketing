\set ON_ERROR_STOP on
-- M2 reservation core: durable seat holds with per-seat exclusivity and expiry.
-- Runs after 0001_init and 0002_venue_layout; every fixture is rolled back.
BEGIN;

-- M1 fixture: one room, one published layout with an irregular row A (3 seats)
-- and row B (1 enabled + 1 disabled seat), plus two events on that layout.
INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('00000000-0000-0000-0000-0000000000c1','Cine M2','UTC');
INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('00000000-0000-0000-0000-0000000000c2','00000000-0000-0000-0000-0000000000c1','Sala M2');
INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('00000000-0000-0000-0000-0000000000c3','00000000-0000-0000-0000-0000000000c2',1);
INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES
 ('00000000-0000-0000-0000-0000000000c4','00000000-0000-0000-0000-0000000000c3','A',1),
 ('00000000-0000-0000-0000-0000000000c5','00000000-0000-0000-0000-0000000000c3','B',2);
INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
 ('00000000-0000-0000-0000-0000000000c6','00000000-0000-0000-0000-0000000000c4','1',1,'standard',true),
 ('00000000-0000-0000-0000-0000000000c7','00000000-0000-0000-0000-0000000000c4','2',2,'standard',true),
 ('00000000-0000-0000-0000-0000000000c8','00000000-0000-0000-0000-0000000000c4','3',3,'accessible',true),
 ('00000000-0000-0000-0000-0000000000b1','00000000-0000-0000-0000-0000000000c5','1',1,'standard',true),
 ('00000000-0000-0000-0000-0000000000b2','00000000-0000-0000-0000-0000000000c5','2',2,'companion',false);
UPDATE cinema.layout_versions SET status='published' WHERE id='00000000-0000-0000-0000-0000000000c3';
INSERT INTO cinema.films (id,title,runtime_minutes,rating) VALUES ('00000000-0000-0000-0000-0000000000c9','M2 Film',100,'PG');
INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES
 ('00000000-0000-0000-0000-0000000000d1','00000000-0000-0000-0000-0000000000c2','00000000-0000-0000-0000-0000000000c9','00000000-0000-0000-0000-0000000000c3',now(),'on_sale'),
 ('00000000-0000-0000-0000-0000000000d2','00000000-0000-0000-0000-0000000000c2','00000000-0000-0000-0000-0000000000c9','00000000-0000-0000-0000-0000000000c3',now() + interval '1 day','on_sale');

CREATE TEMP TABLE m2_seats ON COMMIT DROP AS
SELECT r.label || ls.number AS label, es.id, es.event_id
FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
JOIN cinema.layout_rows r ON r.id = ls.layout_row_id;

-- The hold schema exists, is durable, and carries a real per-seat exclusivity guard.
DO $$
BEGIN
  IF to_regclass('cinema.reservations') IS NULL THEN RAISE EXCEPTION 'missing table cinema.reservations'; END IF;
  IF to_regclass('cinema.reservation_seats') IS NULL THEN RAISE EXCEPTION 'missing table cinema.reservation_seats'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='reservations' AND column_name='expires_at'
      AND data_type='timestamp with time zone' AND is_nullable='NO'
  ) THEN RAISE EXCEPTION 'reservations.expires_at must be NOT NULL timestamptz'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='cinema' AND table_name='reservation_seats' AND column_name='released_at'
      AND data_type='timestamp with time zone' AND is_nullable='YES'
  ) THEN RAISE EXCEPTION 'reservation_seats.released_at must be nullable timestamptz'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname='cinema' AND c.relname='reservation_seats_active_hold' AND i.indisunique
      AND pg_get_indexdef(i.indexrelid) LIKE '%(event_seat_id)%'
      AND coalesce(pg_get_expr(i.indpred, i.indrelid),'') LIKE '%released_at IS NULL%'
  ) THEN RAISE EXCEPTION 'missing partial unique index reservation_seats_active_hold'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='f'
      AND conrelid='cinema.reservations'::regclass AND confrelid='cinema.events'::regclass
  ) THEN RAISE EXCEPTION 'reservations must reference cinema.events'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE contype='f'
      AND conrelid='cinema.reservation_seats'::regclass AND confrelid='cinema.event_seats'::regclass
  ) THEN RAISE EXCEPTION 'reservation_seats must reference cinema.event_seats'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_trigger
    WHERE tgrelid='cinema.reservation_seats'::regclass AND NOT tgisinternal AND tgname='release_expired_seat_hold'
  ) THEN RAISE EXCEPTION 'missing expired-hold release trigger on reservation_seats'; END IF;
END $$;

-- A hold is durable and referenceable, and one reservation may hold several seats.
INSERT INTO cinema.reservations (id, event_id, expires_at)
VALUES ('00000000-0000-0000-0000-0000000000e1','00000000-0000-0000-0000-0000000000d1', now() + interval '10 minutes');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000000e1','00000000-0000-0000-0000-0000000000d1', id
FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label IN ('A1','B1') ORDER BY label;
DO $$ BEGIN
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e1') <> 2 THEN
    RAISE EXCEPTION 'a multi-seat hold did not store every seat';
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e1' AND released_at IS NULL) <> 2 THEN
    RAISE EXCEPTION 'a fresh hold must start active';
  END IF;
  IF (SELECT expires_at FROM cinema.reservations WHERE id='00000000-0000-0000-0000-0000000000e1') <= now() THEN
    RAISE EXCEPTION 'a fresh hold must expire in the future';
  END IF;
END $$;

-- S1: a second hold of an actively held seat is rejected and persists nothing.
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label='A1';
  BEGIN
    INSERT INTO cinema.reservations (id, event_id, expires_at)
    VALUES ('00000000-0000-0000-0000-0000000000e2','00000000-0000-0000-0000-0000000000d1', now() + interval '10 minutes');
    INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000000e2','00000000-0000-0000-0000-0000000000d1', target);
    RAISE EXCEPTION 'a second active hold for the same event seat was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.reservations WHERE id='00000000-0000-0000-0000-0000000000e2')
     OR EXISTS (SELECT 1 FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e2') THEN
    RAISE EXCEPTION 'rejected duplicate hold persisted data';
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE event_seat_id=target AND released_at IS NULL) <> 1 THEN
    RAISE EXCEPTION 'a contested seat must keep exactly one active hold';
  END IF;
END $$;

-- S5: a multi-seat hold is all-or-nothing; a conflicting seat leaves no partial hold.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.reservations (id, event_id, expires_at)
    VALUES ('00000000-0000-0000-0000-0000000000e3','00000000-0000-0000-0000-0000000000d1', now() + interval '10 minutes');
    INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
    SELECT '00000000-0000-0000-0000-0000000000e3','00000000-0000-0000-0000-0000000000d1', id
    FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label IN ('A2','A1') ORDER BY label;
    RAISE EXCEPTION 'a partially conflicting multi-seat hold was accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.reservations WHERE id='00000000-0000-0000-0000-0000000000e3')
     OR EXISTS (SELECT 1 FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e3') THEN
    RAISE EXCEPTION 'failed multi-seat hold persisted partial data';
  END IF;
  IF EXISTS (
    SELECT 1 FROM cinema.reservation_seats rs JOIN m2_seats s ON s.id = rs.event_seat_id
    WHERE s.event_id='00000000-0000-0000-0000-0000000000d1' AND s.label='A2' AND rs.released_at IS NULL
  ) THEN RAISE EXCEPTION 'the free seat of a failed multi-seat hold stayed held'; END IF;
END $$;

-- An unrelated free multi-seat selection is still accepted as one statement.
INSERT INTO cinema.reservations (id, event_id, expires_at)
VALUES ('00000000-0000-0000-0000-0000000000e4','00000000-0000-0000-0000-0000000000d1', now() + interval '10 minutes');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000000e4','00000000-0000-0000-0000-0000000000d1', id
FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label IN ('A2','A3') ORDER BY label;
DO $$ BEGIN
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e4' AND released_at IS NULL) <> 2 THEN
    RAISE EXCEPTION 'a free multi-seat selection must be fully held';
  END IF;
END $$;

-- S6: expiry frees a seat at claim time, before any background sweep runs.
UPDATE cinema.reservations SET expires_at = now() - interval '1 minute' WHERE id='00000000-0000-0000-0000-0000000000e1';
INSERT INTO cinema.reservations (id, event_id, expires_at)
VALUES ('00000000-0000-0000-0000-0000000000e5','00000000-0000-0000-0000-0000000000d1', now() + interval '10 minutes');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000000e5','00000000-0000-0000-0000-0000000000d1', id
FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label='A1';
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label='A1';
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE event_seat_id=target AND released_at IS NULL) <> 1 THEN
    RAISE EXCEPTION 'an expired hold must free the seat for exactly one new hold';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM cinema.reservation_seats rs JOIN cinema.reservations r ON r.id = rs.reservation_id
    WHERE rs.reservation_id='00000000-0000-0000-0000-0000000000e1' AND rs.event_seat_id=target
      AND rs.released_at IS NOT NULL AND rs.released_at >= r.expires_at
  ) THEN RAISE EXCEPTION 'the expired hold row was not released at claim time'; END IF;
  IF EXISTS (
    SELECT 1 FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e5' AND event_seat_id=target AND released_at IS NOT NULL
  ) THEN RAISE EXCEPTION 'the new hold must stay active'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM cinema.reservation_seats rs JOIN m2_seats s ON s.id = rs.event_seat_id
    WHERE rs.reservation_id='00000000-0000-0000-0000-0000000000e1' AND s.label='B1' AND rs.released_at IS NULL
  ) THEN RAISE EXCEPTION 'unclaimed seats of an expired hold must stay untouched until claimed'; END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e1') <> 2 THEN
    RAISE EXCEPTION 'expiry takeover must preserve hold history';
  END IF;
END $$;

-- An already expired hold window cannot acquire a seat.
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label='B1';
  BEGIN
    INSERT INTO cinema.reservations (id, event_id, expires_at)
    VALUES ('00000000-0000-0000-0000-0000000000e6','00000000-0000-0000-0000-0000000000d1', now() - interval '1 second');
    INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000000e6','00000000-0000-0000-0000-0000000000d1', target);
    RAISE EXCEPTION 'an already expired hold window acquired a seat';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.reservations WHERE id='00000000-0000-0000-0000-0000000000e6')
     OR EXISTS (SELECT 1 FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e6') THEN
    RAISE EXCEPTION 'rejected expired hold persisted data';
  END IF;
END $$;

-- A reservation may only hold seats of its own event.
DO $$
DECLARE foreign_seat bigint;
BEGIN
  SELECT id INTO foreign_seat FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d2' AND label='A1';
  BEGIN
    INSERT INTO cinema.reservations (id, event_id, expires_at)
    VALUES ('00000000-0000-0000-0000-0000000000e7','00000000-0000-0000-0000-0000000000d1', now() + interval '10 minutes');
    INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000000e7','00000000-0000-0000-0000-0000000000d1', foreign_seat);
    RAISE EXCEPTION 'a seat of another event was held';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.reservations WHERE id='00000000-0000-0000-0000-0000000000e7') THEN
    RAISE EXCEPTION 'rejected cross-event hold persisted its reservation';
  END IF;
  BEGIN
    INSERT INTO cinema.reservations (id, event_id, expires_at)
    VALUES ('00000000-0000-0000-0000-0000000000e8','00000000-0000-0000-0000-0000000000ff', now() + interval '10 minutes');
    RAISE EXCEPTION 'a reservation for an unknown event was accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.reservations WHERE id='00000000-0000-0000-0000-0000000000e8') THEN
    RAISE EXCEPTION 'rejected unknown-event reservation persisted';
  END IF;
END $$;

-- A hold row cannot exist without its reservation or its event seat.
DO $$
BEGIN
  BEGIN
    INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000000fa','00000000-0000-0000-0000-0000000000d2',
            (SELECT id FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d2' AND label='A1'));
    RAISE EXCEPTION 'a hold for an unknown reservation was accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000fa') THEN
    RAISE EXCEPTION 'rejected hold without a reservation persisted';
  END IF;
  BEGIN
    INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
    VALUES ('00000000-0000-0000-0000-0000000000e5','00000000-0000-0000-0000-0000000000d1',-1);
    RAISE EXCEPTION 'a hold for an unknown event seat was accepted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.reservation_seats WHERE reservation_id='00000000-0000-0000-0000-0000000000e5' AND event_seat_id = -1) THEN
    RAISE EXCEPTION 'rejected hold for an unknown seat persisted';
  END IF;
END $$;

-- A held or released seat cannot disappear from under its hold history.
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label='A2';
  BEGIN
    DELETE FROM cinema.event_seats WHERE id=target;
    RAISE EXCEPTION 'a held event seat was deleted';
  EXCEPTION WHEN foreign_key_violation THEN NULL; END;
  IF NOT EXISTS (SELECT 1 FROM cinema.event_seats WHERE id=target) THEN
    RAISE EXCEPTION 'rejected event-seat delete removed stored data';
  END IF;
END $$;

-- An explicit release frees the seat for a new hold and keeps the audit history.
UPDATE cinema.reservation_seats rs SET released_at = now()
FROM m2_seats s
WHERE s.id = rs.event_seat_id AND s.event_id='00000000-0000-0000-0000-0000000000d1' AND s.label='A1'
  AND rs.reservation_id='00000000-0000-0000-0000-0000000000e5';
INSERT INTO cinema.reservations (id, event_id, expires_at)
VALUES ('00000000-0000-0000-0000-0000000000e9','00000000-0000-0000-0000-0000000000d1', now() + interval '10 minutes');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-0000000000e9','00000000-0000-0000-0000-0000000000d1', id
FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label='A1';
DO $$
DECLARE target bigint;
BEGIN
  SELECT id INTO target FROM m2_seats WHERE event_id='00000000-0000-0000-0000-0000000000d1' AND label='A1';
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE event_seat_id=target) <> 3 THEN
    RAISE EXCEPTION 'released hold rows must be kept as history';
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE event_seat_id=target AND released_at IS NULL) <> 1 THEN
    RAISE EXCEPTION 'exactly one active hold must remain after release and reclaim';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM cinema.reservation_seats
    WHERE event_seat_id=target AND reservation_id='00000000-0000-0000-0000-0000000000e9' AND released_at IS NULL
  ) THEN RAISE EXCEPTION 'the newest reservation must own the active hold'; END IF;
END $$;

ROLLBACK;
