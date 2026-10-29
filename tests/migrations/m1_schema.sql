\set ON_ERROR_STOP on
BEGIN;
INSERT INTO cinema.cinemas (id, name, timezone) VALUES ('00000000-0000-0000-0000-000000000001','Cinema','UTC');
INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ('00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001','Room');
DO $$ BEGIN
 BEGIN
   INSERT INTO cinema.layout_versions (id, room_id, version, status) VALUES ('00000000-0000-0000-0000-000000000011','00000000-0000-0000-0000-000000000002',2,'published');
   RAISE EXCEPTION 'direct published layout was accepted';
 EXCEPTION WHEN check_violation THEN NULL; END;
END $$;
INSERT INTO cinema.layout_versions (id, room_id, version) VALUES ('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002',1);
INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES
 ('00000000-0000-0000-0000-000000000004','00000000-0000-0000-0000-000000000003','A',1),
 ('00000000-0000-0000-0000-000000000005','00000000-0000-0000-0000-000000000003','B',2);
INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES
 ('00000000-0000-0000-0000-000000000006','00000000-0000-0000-0000-000000000004','1',1,'standard',true),
 ('00000000-0000-0000-0000-000000000007','00000000-0000-0000-0000-000000000004','2',2,'accessible',true),
 ('00000000-0000-0000-0000-000000000008','00000000-0000-0000-0000-000000000005','1',1,'standard',false);
DO $$ BEGIN
 BEGIN
   INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind) VALUES ('00000000-0000-0000-0000-000000000013','00000000-0000-0000-0000-000000000004','1',3,'standard');
   RAISE EXCEPTION 'duplicate seat number accepted';
 EXCEPTION WHEN unique_violation THEN NULL; END;
 IF (SELECT count(*) FROM cinema.layout_seats WHERE layout_row_id='00000000-0000-0000-0000-000000000004') <> 2 THEN RAISE EXCEPTION 'rejected seat changed stored data'; END IF;
END $$;
INSERT INTO cinema.films (id,title,runtime_minutes,rating) VALUES ('00000000-0000-0000-0000-000000000009','Film',90,'PG');
DO $$ BEGIN
 BEGIN
   INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES ('00000000-0000-0000-0000-000000000012','00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000009','00000000-0000-0000-0000-000000000003',now(),'draft');
   RAISE EXCEPTION 'draft layout accepted';
 EXCEPTION WHEN check_violation THEN NULL; END;
 IF EXISTS (SELECT 1 FROM cinema.events WHERE id='00000000-0000-0000-0000-000000000012') THEN RAISE EXCEPTION 'rejected event persisted'; END IF;
END $$;
UPDATE cinema.layout_versions SET status='published' WHERE id='00000000-0000-0000-0000-000000000003';
DO $$ BEGIN
  BEGIN
    UPDATE cinema.layout_seats SET number='3' WHERE id='00000000-0000-0000-0000-000000000006';
    RAISE EXCEPTION 'published layout was editable';
  EXCEPTION WHEN check_violation THEN NULL; END;
END $$;
CREATE FUNCTION pg_temp.fail_event_seat() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'simulated materialization failure' USING ERRCODE = 'ZX001'; END $$;
CREATE TRIGGER test_fail_event_seat BEFORE INSERT ON cinema.event_seats FOR EACH ROW EXECUTE FUNCTION pg_temp.fail_event_seat();
DO $$ BEGIN
 BEGIN
   INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES ('00000000-0000-0000-0000-000000000014','00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000009','00000000-0000-0000-0000-000000000003',now(),'draft');
   RAISE EXCEPTION 'expected materialization failure';
 EXCEPTION WHEN SQLSTATE 'ZX001' THEN NULL; END;
 IF EXISTS (SELECT 1 FROM cinema.events WHERE id='00000000-0000-0000-0000-000000000014') OR EXISTS (SELECT 1 FROM cinema.event_seats WHERE event_id='00000000-0000-0000-0000-000000000014') THEN RAISE EXCEPTION 'failed materialization persisted partial data'; END IF;
END $$;
DROP TRIGGER test_fail_event_seat ON cinema.event_seats;
INSERT INTO cinema.events (id,room_id,film_id,layout_version_id,starts_at,status) VALUES ('00000000-0000-0000-0000-000000000010','00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000009','00000000-0000-0000-0000-000000000003',now(),'draft');
DO $$ BEGIN
 IF (SELECT count(*) FROM cinema.event_seats WHERE event_id='00000000-0000-0000-0000-000000000010') <> 2 THEN RAISE EXCEPTION 'expected two enabled event seats'; END IF;
END $$;
SELECT cinema.copy_layout_version('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000015',2);
DO $$ BEGIN
 IF (SELECT status FROM cinema.layout_versions WHERE id='00000000-0000-0000-0000-000000000015') <> 'draft' THEN RAISE EXCEPTION 'copy is not draft'; END IF;
 IF (SELECT count(*) FROM cinema.layout_rows r JOIN cinema.layout_seats s ON s.layout_row_id=r.id WHERE r.layout_version_id='00000000-0000-0000-0000-000000000015') <> 3 THEN RAISE EXCEPTION 'copy did not preserve all seats'; END IF;
 UPDATE cinema.layout_seats SET number='10' WHERE layout_row_id=(SELECT id FROM cinema.layout_rows WHERE layout_version_id='00000000-0000-0000-0000-000000000015' AND label='A') AND number='1';
 IF (SELECT number FROM cinema.layout_seats WHERE id='00000000-0000-0000-0000-000000000006') <> '1' THEN RAISE EXCEPTION 'copy changed source'; END IF;
 IF (SELECT count(*) FROM cinema.event_seats WHERE event_id='00000000-0000-0000-0000-000000000010') <> 2 THEN RAISE EXCEPTION 'event changed after copying layout'; END IF;
END $$;
DO $$ BEGIN
 IF (SELECT count(*) FROM cinema.layout_seats s JOIN cinema.layout_rows r ON r.id=s.layout_row_id WHERE r.label='A' AND r.layout_version_id='00000000-0000-0000-0000-000000000003') <> 2 THEN RAISE EXCEPTION 'row A must hold two seats'; END IF;
 IF (SELECT count(*) FROM cinema.layout_seats s JOIN cinema.layout_rows r ON r.id=s.layout_row_id WHERE r.label='B' AND r.layout_version_id='00000000-0000-0000-0000-000000000003') <> 1 THEN RAISE EXCEPTION 'row B must hold one seat'; END IF;
END $$;
DO $$ BEGIN
 BEGIN
   DELETE FROM cinema.layout_versions WHERE id='00000000-0000-0000-0000-000000000003';
   RAISE EXCEPTION 'published layout was deletable';
 EXCEPTION WHEN foreign_key_violation THEN NULL; WHEN check_violation THEN NULL; END;
 IF NOT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id='00000000-0000-0000-0000-000000000003') THEN RAISE EXCEPTION 'rejected delete removed stored data'; END IF;
 IF (SELECT count(*) FROM cinema.layout_rows WHERE layout_version_id='00000000-0000-0000-0000-000000000003') <> 2 THEN RAISE EXCEPTION 'rejected delete removed rows'; END IF;
END $$;
DO $$ BEGIN
 BEGIN
   PERFORM cinema.copy_layout_version('00000000-0000-0000-0000-000000000015','00000000-0000-0000-0000-000000000016',3);
   RAISE EXCEPTION 'copy from draft source was accepted';
 EXCEPTION WHEN check_violation THEN NULL; END;
 IF EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id='00000000-0000-0000-0000-000000000016') THEN RAISE EXCEPTION 'rejected copy persisted a version'; END IF;
END $$;
ROLLBACK;
