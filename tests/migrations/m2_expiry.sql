\set ON_ERROR_STOP on
-- M2 durable expiry sweep: pins the four statements that
-- apps/api/internal/reservation/expiry.go runs, because their semantics are SQL
-- and a Go fake cannot prove them. Everything is rolled back at the end, so the
-- file is safe to run repeatedly on the same database.
--
-- Covered here: the elapsed hold expires and releases exactly its still-active
-- seat, an already released seat keeps its timestamp, a live hold, a cancelled
-- hold and a `payment_pending` hold behave as designed, retention decides pruning
-- together with the hold's own deadline, availability keeps the L5 predicate, a
-- second sweep is a full no-op, and the batch bound is honoured one row at a time.
BEGIN;

-- M1 fixture: one cinema, one room, a published layout with eight seats, a film
-- and an event, so cinema.event_seats exists for the holds.
INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('00000000-0000-0000-0000-00000000e0c1','Cine Sweep','UTC');
INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('00000000-0000-0000-0000-00000000e0c2','00000000-0000-0000-0000-00000000e0c1','Sala Sweep');
INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('00000000-0000-0000-0000-00000000e0c3','00000000-0000-0000-0000-00000000e0c2',1);
INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ('00000000-0000-0000-0000-00000000e0c4','00000000-0000-0000-0000-00000000e0c3','A',1);
INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
 ('00000000-0000-0000-0000-00000000e0c5','00000000-0000-0000-0000-00000000e0c4','1',1,'standard',true),
 ('00000000-0000-0000-0000-00000000e0c6','00000000-0000-0000-0000-00000000e0c4','2',2,'standard',true),
 ('00000000-0000-0000-0000-00000000e0c8','00000000-0000-0000-0000-00000000e0c4','3',3,'standard',true),
 ('00000000-0000-0000-0000-00000000e0c9','00000000-0000-0000-0000-00000000e0c4','4',4,'standard',true),
 ('00000000-0000-0000-0000-00000000e0ca','00000000-0000-0000-0000-00000000e0c4','5',5,'standard',true),
 ('00000000-0000-0000-0000-00000000e0cb','00000000-0000-0000-0000-00000000e0c4','6',6,'standard',true),
 ('00000000-0000-0000-0000-00000000e0cc','00000000-0000-0000-0000-00000000e0c4','7',7,'standard',true),
 ('00000000-0000-0000-0000-00000000e0cd','00000000-0000-0000-0000-00000000e0c4','8',8,'standard',true);
UPDATE cinema.layout_versions SET status='published' WHERE id='00000000-0000-0000-0000-00000000e0c3';
INSERT INTO cinema.films (id,title,runtime_minutes,rating) VALUES ('00000000-0000-0000-0000-00000000e0c7','Sweep Film',100,'PG');
INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES
 ('00000000-0000-0000-0000-00000000e0d1','00000000-0000-0000-0000-00000000e0c2','00000000-0000-0000-0000-00000000e0c7','00000000-0000-0000-0000-00000000e0c3',now(),'on_sale');

CREATE TEMP TABLE sweep_seats ON COMMIT DROP AS
SELECT es.id, es.event_id FROM cinema.event_seats es
JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id ORDER BY es.id;

-- Four holds with disjoint seats. Seats are inserted while the window is still
-- open, because the claim trigger rejects a seat row for an already expired hold;
-- the deadlines move back afterwards, which is exactly the state the sweep exists
-- to clean up. `cancelled` and `payment_pending` must be treated differently.
INSERT INTO cinema.reservations (id, event_id, status, expires_at) VALUES
 ('00000000-0000-0000-0000-00000000e0f1', (SELECT event_id FROM sweep_seats LIMIT 1), 'held', now() + interval '1 hour'),
 ('00000000-0000-0000-0000-00000000e0f2', (SELECT event_id FROM sweep_seats LIMIT 1), 'held', now() + interval '1 hour'),
 ('00000000-0000-0000-0000-00000000e0f3', (SELECT event_id FROM sweep_seats LIMIT 1), 'cancelled', now() + interval '1 hour'),
 ('00000000-0000-0000-0000-00000000e0f4', (SELECT event_id FROM sweep_seats LIMIT 1), 'payment_pending', now() + interval '1 hour');

INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-00000000e0f1', event_id, id FROM (SELECT * FROM sweep_seats ORDER BY id LIMIT 2) s;
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-00000000e0f2', event_id, id FROM (SELECT * FROM sweep_seats ORDER BY id OFFSET 2 LIMIT 1) s;
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-00000000e0f3', event_id, id FROM (SELECT * FROM sweep_seats ORDER BY id OFFSET 3 LIMIT 1) s;
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-00000000e0f4', event_id, id FROM (SELECT * FROM sweep_seats ORDER BY id OFFSET 4 LIMIT 1) s;

-- One seat of the elapsed hold was already released by a takeover: the sweep must
-- leave that timestamp alone while still expiring the hold.
UPDATE cinema.reservation_seats
   SET released_at = now() - interval '2 hours'
 WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f1'
   AND event_seat_id = (SELECT id FROM sweep_seats ORDER BY id LIMIT 1 OFFSET 1);

UPDATE cinema.reservations SET expires_at = now() - interval '1 hour' WHERE id IN
 ('00000000-0000-0000-0000-00000000e0f1','00000000-0000-0000-0000-00000000e0f3','00000000-0000-0000-0000-00000000e0f4');

-- Retention decides pruning together with the hold deadline: only a record whose
-- retention AND whose hold are both past may be pruned.
INSERT INTO cinema.idempotency_keys (idempotency_key, request_fingerprint, status, hold_id, hold_expires_at, expires_at) VALUES
 ('exp-prune', 'fp', 201, '00000000-0000-0000-0000-00000000e0f1', now() - interval '3 hours', now() - interval '1 hour'),
 ('exp-keep-live-hold', 'fp', 201, '00000000-0000-0000-0000-00000000e0f2', now() + interval '1 hour', now() - interval '1 hour'),
 ('exp-keep-retained', 'fp', 201, '00000000-0000-0000-0000-00000000e0f2', now() + interval '1 hour', now() + interval '1 day'),
 ('exp-keep-future-retention', 'fp', 201, '00000000-0000-0000-0000-00000000e0f3', now() - interval '3 hours', now() + interval '1 day');

-- The deadline is captured once and passed to every statement, mirroring the
-- caller-supplied deadline of ExpireHolds.
CREATE TEMP TABLE sweep_run ON COMMIT DROP AS SELECT now() AS at;

-- Statement 1: prune retained records whose hold is also past.
DELETE FROM cinema.idempotency_keys k
 USING (SELECT idempotency_key FROM cinema.idempotency_keys
         WHERE expires_at <= (SELECT at FROM sweep_run) AND hold_expires_at <= (SELECT at FROM sweep_run)
         ORDER BY expires_at, idempotency_key LIMIT 100 FOR UPDATE SKIP LOCKED) p
 WHERE k.idempotency_key = p.idempotency_key;

-- Statement 2: lock the elapsed, non-cancelled holds.
CREATE TEMP TABLE sweep_batch ON COMMIT DROP AS
SELECT r.id FROM cinema.reservations r
 WHERE r.status IN ('held','payment_pending') AND r.expires_at <= (SELECT at FROM sweep_run)
 ORDER BY r.expires_at, r.id
 LIMIT 100 FOR NO KEY UPDATE SKIP LOCKED;

-- Statement 3: release only the seats that are still active, bound as an array.
UPDATE cinema.reservation_seats SET released_at = (SELECT at FROM sweep_run)
 WHERE released_at IS NULL AND reservation_id = ANY(ARRAY(SELECT id FROM sweep_batch));

-- Statement 4: expire the batch.
UPDATE cinema.reservations SET status = 'expired'
 WHERE id = ANY(ARRAY(SELECT id FROM sweep_batch)) AND status IN ('held','payment_pending');

DO $$
DECLARE released timestamp with time zone; kept timestamp with time zone;
BEGIN
  -- The elapsed `held` hold expired and released exactly its still-active seat.
  IF (SELECT status FROM cinema.reservations WHERE id = '00000000-0000-0000-0000-00000000e0f1') <> 'expired' THEN
    RAISE EXCEPTION 'the elapsed hold must become expired';
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats
       WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f1' AND released_at = (SELECT at FROM sweep_run)) <> 1 THEN
    RAISE EXCEPTION 'the sweep must release exactly the still-active seat of the elapsed hold';
  END IF;
  -- The seat released earlier by a takeover keeps its original timestamp.
  SELECT released_at INTO kept FROM cinema.reservation_seats
   WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f1' AND released_at < (SELECT at FROM sweep_run);
  IF kept IS NULL THEN
    RAISE EXCEPTION 'an already released seat must keep its original released_at';
  END IF;
  -- `payment_pending` is an elapsed hold too: it expires and releases its seat.
  IF (SELECT status FROM cinema.reservations WHERE id = '00000000-0000-0000-0000-00000000e0f4') <> 'expired' THEN
    RAISE EXCEPTION 'an elapsed payment_pending hold must expire as well';
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats
       WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f4' AND released_at = (SELECT at FROM sweep_run)) <> 1 THEN
    RAISE EXCEPTION 'an elapsed payment_pending hold must release its seat';
  END IF;
  -- Live and cancelled holds are untouched.
  IF (SELECT status FROM cinema.reservations WHERE id = '00000000-0000-0000-0000-00000000e0f2') <> 'held' THEN
    RAISE EXCEPTION 'a live hold must not be expired';
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f2' AND released_at IS NOT NULL) <> 0 THEN
    RAISE EXCEPTION 'a live hold must keep its seats active';
  END IF;
  IF (SELECT status FROM cinema.reservations WHERE id = '00000000-0000-0000-0000-00000000e0f3') <> 'cancelled' THEN
    RAISE EXCEPTION 'a cancelled hold must never be rewritten by the sweep';
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f3' AND released_at IS NOT NULL) <> 0 THEN
    RAISE EXCEPTION 'a cancelled hold must keep its seats untouched';
  END IF;
  -- Retention: pruned only when the record and its hold are both past.
  IF EXISTS (SELECT 1 FROM cinema.idempotency_keys WHERE idempotency_key = 'exp-prune') THEN
    RAISE EXCEPTION 'a retained record whose hold expired must be pruned';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM cinema.idempotency_keys WHERE idempotency_key = 'exp-keep-live-hold') THEN
    RAISE EXCEPTION 'a retained record whose hold is live must survive';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM cinema.idempotency_keys WHERE idempotency_key = 'exp-keep-retained') THEN
    RAISE EXCEPTION 'a record with future retention must survive';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM cinema.idempotency_keys WHERE idempotency_key = 'exp-keep-future-retention') THEN
    RAISE EXCEPTION 'a record with future retention must survive even when its hold has elapsed';
  END IF;
  -- Availability keeps the L5 rule: only the live hold's seat reads as held.
  IF (SELECT count(*) FROM cinema.reservation_seats rs JOIN cinema.reservations r ON r.id = rs.reservation_id
       WHERE rs.released_at IS NULL AND r.expires_at > (SELECT at FROM sweep_run)) <> 1 THEN
    RAISE EXCEPTION 'availability must report exactly the live hold seat as held';
  END IF;
  SELECT max(released_at) INTO released FROM cinema.reservation_seats
   WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f1';
  IF released <> (SELECT at FROM sweep_run) THEN
    RAISE EXCEPTION 'the released timestamp must be the sweep deadline, got %', released;
  END IF;
END $$;

-- Full no-op: the same four statements run again change nothing at all.
DELETE FROM cinema.idempotency_keys k
 USING (SELECT idempotency_key FROM cinema.idempotency_keys
         WHERE expires_at <= (SELECT at FROM sweep_run) AND hold_expires_at <= (SELECT at FROM sweep_run)
         ORDER BY expires_at, idempotency_key LIMIT 100 FOR UPDATE SKIP LOCKED) p
 WHERE k.idempotency_key = p.idempotency_key;

CREATE TEMP TABLE sweep_batch_again ON COMMIT DROP AS
SELECT r.id FROM cinema.reservations r
 WHERE r.status IN ('held','payment_pending') AND r.expires_at <= (SELECT at FROM sweep_run)
 ORDER BY r.expires_at, r.id
 LIMIT 100 FOR NO KEY UPDATE SKIP LOCKED;

UPDATE cinema.reservation_seats SET released_at = (SELECT at FROM sweep_run)
 WHERE released_at IS NULL AND reservation_id = ANY(ARRAY(SELECT id FROM sweep_batch_again));

UPDATE cinema.reservations SET status = 'expired'
 WHERE id = ANY(ARRAY(SELECT id FROM sweep_batch_again)) AND status IN ('held','payment_pending');

DO $$
DECLARE candidates integer; released timestamp with time zone;
BEGIN
  SELECT count(*) INTO candidates FROM sweep_batch_again;
  IF candidates <> 0 THEN
    RAISE EXCEPTION 'a second sweep must lock no hold, locked %', candidates;
  END IF;
  SELECT count(*) INTO candidates FROM cinema.idempotency_keys
   WHERE expires_at <= (SELECT at FROM sweep_run) AND hold_expires_at <= (SELECT at FROM sweep_run);
  IF candidates <> 0 THEN
    RAISE EXCEPTION 'a second sweep must find no prunable record, found %', candidates;
  END IF;
  SELECT count(*) INTO candidates FROM cinema.reservation_seats
   WHERE released_at IS NULL AND reservation_id IN ('00000000-0000-0000-0000-00000000e0f1','00000000-0000-0000-0000-00000000e0f4');
  IF candidates <> 0 THEN
    RAISE EXCEPTION 'expired holds must not keep active seats, found %', candidates;
  END IF;
  SELECT max(released_at) INTO released FROM cinema.reservation_seats
   WHERE reservation_id = '00000000-0000-0000-0000-00000000e0f1';
  IF released <> (SELECT at FROM sweep_run) THEN
    RAISE EXCEPTION 'a second sweep must not move a released timestamp, got %', released;
  END IF;
END $$;

-- The batch bound: two more elapsed holds appear, and LIMIT 1 must expire exactly
-- one per sweep while the array binding keeps working.
INSERT INTO cinema.reservations (id, event_id, status, expires_at) VALUES
 ('00000000-0000-0000-0000-00000000e0f5', (SELECT event_id FROM sweep_seats LIMIT 1), 'held', now() + interval '1 hour'),
 ('00000000-0000-0000-0000-00000000e0f6', (SELECT event_id FROM sweep_seats LIMIT 1), 'held', now() + interval '1 hour');
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-00000000e0f5', event_id, id FROM (SELECT * FROM sweep_seats ORDER BY id OFFSET 5 LIMIT 1) s;
INSERT INTO cinema.reservation_seats (reservation_id, event_id, event_seat_id)
SELECT '00000000-0000-0000-0000-00000000e0f6', event_id, id FROM (SELECT * FROM sweep_seats ORDER BY id OFFSET 6 LIMIT 1) s;
UPDATE cinema.reservations SET expires_at = now() - interval '30 minutes' WHERE id IN
 ('00000000-0000-0000-0000-00000000e0f5','00000000-0000-0000-0000-00000000e0f6');

CREATE TEMP TABLE sweep_bound ON COMMIT DROP AS
SELECT r.id FROM cinema.reservations r
 WHERE r.status IN ('held','payment_pending') AND r.expires_at <= (SELECT at FROM sweep_run)
 ORDER BY r.expires_at, r.id
 LIMIT 1 FOR NO KEY UPDATE SKIP LOCKED;

UPDATE cinema.reservation_seats SET released_at = (SELECT at FROM sweep_run)
 WHERE released_at IS NULL AND reservation_id = ANY(ARRAY(SELECT id FROM sweep_bound));
UPDATE cinema.reservations SET status = 'expired'
 WHERE id = ANY(ARRAY(SELECT id FROM sweep_bound)) AND status IN ('held','payment_pending');

DO $$
DECLARE expired integer;
BEGIN
  SELECT count(*) INTO expired FROM cinema.reservations
   WHERE id IN ('00000000-0000-0000-0000-00000000e0f5','00000000-0000-0000-0000-00000000e0f6') AND status = 'expired';
  IF expired <> 1 THEN
    RAISE EXCEPTION 'a LIMIT 1 sweep must expire exactly one of the two holds, got %', expired;
  END IF;
  IF (SELECT count(*) FROM cinema.reservation_seats
       WHERE reservation_id IN ('00000000-0000-0000-0000-00000000e0f5','00000000-0000-0000-0000-00000000e0f6')
         AND released_at = (SELECT at FROM sweep_run)) <> 1 THEN
    RAISE EXCEPTION 'a LIMIT 1 sweep must release exactly one seat';
  END IF;
END $$;

-- The next sweep picks up the remaining one.
CREATE TEMP TABLE sweep_bound_again ON COMMIT DROP AS
SELECT r.id FROM cinema.reservations r
 WHERE r.status IN ('held','payment_pending') AND r.expires_at <= (SELECT at FROM sweep_run)
 ORDER BY r.expires_at, r.id
 LIMIT 1 FOR NO KEY UPDATE SKIP LOCKED;

UPDATE cinema.reservation_seats SET released_at = (SELECT at FROM sweep_run)
 WHERE released_at IS NULL AND reservation_id = ANY(ARRAY(SELECT id FROM sweep_bound_again));
UPDATE cinema.reservations SET status = 'expired'
 WHERE id = ANY(ARRAY(SELECT id FROM sweep_bound_again)) AND status IN ('held','payment_pending');

DO $$
BEGIN
  IF (SELECT count(*) FROM cinema.reservations
       WHERE id IN ('00000000-0000-0000-0000-00000000e0f5','00000000-0000-0000-0000-00000000e0f6') AND status = 'expired') <> 2 THEN
    RAISE EXCEPTION 'the following sweep must finish the remaining hold';
  END IF;
END $$;

ROLLBACK;
