-- M1 venue and immutable layout foundation.
CREATE SCHEMA cinema;
CREATE TABLE cinema.cinemas (
 id uuid PRIMARY KEY, name text NOT NULL CHECK (length(trim(name)) > 0),
 timezone text NOT NULL CHECK (length(trim(timezone)) > 0), address text
);
CREATE TABLE cinema.rooms (
 id uuid PRIMARY KEY, cinema_id uuid NOT NULL REFERENCES cinema.cinemas(id),
 name text NOT NULL CHECK (length(trim(name)) > 0), UNIQUE (cinema_id, name)
);
CREATE TABLE cinema.layout_versions (
 id uuid PRIMARY KEY, room_id uuid NOT NULL REFERENCES cinema.rooms(id),
 version integer NOT NULL CHECK (version > 0),
 status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published')),
 UNIQUE (room_id, version), UNIQUE (id, room_id)
);
CREATE TABLE cinema.layout_rows (
 id uuid PRIMARY KEY, layout_version_id uuid NOT NULL REFERENCES cinema.layout_versions(id),
 label text NOT NULL CHECK (length(trim(label)) > 0), position integer NOT NULL CHECK (position > 0),
 UNIQUE (layout_version_id, label), UNIQUE (layout_version_id, position)
);
CREATE TABLE cinema.layout_seats (
 id uuid PRIMARY KEY, layout_row_id uuid NOT NULL REFERENCES cinema.layout_rows(id),
 number text NOT NULL CHECK (length(trim(number)) > 0), position integer NOT NULL CHECK (position > 0),
 kind text NOT NULL CHECK (kind IN ('standard','accessible','companion')),
 enabled boolean NOT NULL DEFAULT true,
 UNIQUE (layout_row_id, number), UNIQUE (layout_row_id, position)
);
CREATE TABLE cinema.films (
 id uuid PRIMARY KEY, title text NOT NULL CHECK (length(trim(title)) > 0),
 runtime_minutes integer NOT NULL CHECK (runtime_minutes > 0), rating text NOT NULL
);
CREATE TABLE cinema.events (
 id uuid PRIMARY KEY, room_id uuid NOT NULL REFERENCES cinema.rooms(id),
 film_id uuid NOT NULL REFERENCES cinema.films(id), layout_version_id uuid NOT NULL,
 starts_at timestamptz NOT NULL, status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','on_sale','started','completed','cancelled')),
 FOREIGN KEY (layout_version_id, room_id) REFERENCES cinema.layout_versions(id, room_id)
);
CREATE TABLE cinema.event_seats (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, event_id uuid NOT NULL REFERENCES cinema.events(id),
 layout_seat_id uuid NOT NULL REFERENCES cinema.layout_seats(id),
 state text NOT NULL DEFAULT 'available' CHECK (state IN ('available','held','sold','unavailable')),
 UNIQUE (event_id, layout_seat_id)
);

CREATE FUNCTION cinema.guard_layout_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' THEN
   IF NEW.status <> 'draft' THEN
     RAISE EXCEPTION 'new layout must be a draft' USING ERRCODE = '23514';
   END IF;
   RETURN NEW;
 END IF;
 IF OLD.status = 'published' THEN
   RAISE EXCEPTION 'published layout is immutable' USING ERRCODE = '23514';
 END IF;
 IF TG_OP = 'UPDATE' AND NEW.status = 'published' AND NOT EXISTS (
   SELECT 1 FROM cinema.layout_rows r JOIN cinema.layout_seats s ON s.layout_row_id=r.id
   WHERE r.layout_version_id=OLD.id AND s.enabled
 ) THEN
   RAISE EXCEPTION 'published layout must have enabled seats' USING ERRCODE = '23514';
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_layout_version BEFORE INSERT OR UPDATE OR DELETE ON cinema.layout_versions FOR EACH ROW EXECUTE FUNCTION cinema.guard_layout_version();

CREATE FUNCTION cinema.guard_layout_geometry() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_version uuid; new_version uuid;
BEGIN
 IF TG_TABLE_NAME = 'layout_rows' THEN
   IF TG_OP <> 'INSERT' THEN old_version := OLD.layout_version_id; END IF;
   IF TG_OP <> 'DELETE' THEN new_version := NEW.layout_version_id; END IF;
 ELSE
   IF TG_OP <> 'INSERT' THEN SELECT layout_version_id INTO old_version FROM cinema.layout_rows WHERE id=OLD.layout_row_id; END IF;
   IF TG_OP <> 'DELETE' THEN SELECT layout_version_id INTO new_version FROM cinema.layout_rows WHERE id=NEW.layout_row_id; END IF;
 END IF;
 -- Lock the parent to serialize geometry changes with publication.
 PERFORM 1 FROM cinema.layout_versions WHERE id IN (old_version, new_version) ORDER BY id FOR UPDATE;
 IF EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id IN (old_version,new_version) AND status='published') THEN
   RAISE EXCEPTION 'published layout is immutable' USING ERRCODE = '23514';
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_layout_rows BEFORE INSERT OR UPDATE OR DELETE ON cinema.layout_rows FOR EACH ROW EXECUTE FUNCTION cinema.guard_layout_geometry();
CREATE TRIGGER guard_layout_seats BEFORE INSERT OR UPDATE OR DELETE ON cinema.layout_seats FOR EACH ROW EXECUTE FUNCTION cinema.guard_layout_geometry();

-- Copy a published geometry into a separate editable version atomically.
CREATE FUNCTION cinema.copy_layout_version(source_id uuid, target_id uuid, target_version integer) RETURNS void LANGUAGE plpgsql AS $$
DECLARE source_room uuid; source_row record; target_row uuid;
BEGIN
 SELECT room_id INTO source_room FROM cinema.layout_versions WHERE id=source_id AND status='published';
 IF NOT FOUND THEN
   RAISE EXCEPTION 'source layout must be published' USING ERRCODE = '23514';
 END IF;
 INSERT INTO cinema.layout_versions (id,room_id,version) VALUES (target_id,source_room,target_version);
 FOR source_row IN SELECT id,label,position FROM cinema.layout_rows WHERE layout_version_id=source_id ORDER BY position LOOP
   target_row := gen_random_uuid();
   INSERT INTO cinema.layout_rows (id,layout_version_id,label,position) VALUES (target_row,target_id,source_row.label,source_row.position);
   INSERT INTO cinema.layout_seats (id,layout_row_id,number,position,kind,enabled)
   SELECT gen_random_uuid(),target_row,number,position,kind,enabled FROM cinema.layout_seats WHERE layout_row_id=source_row.id ORDER BY position;
 END LOOP;
END $$;

CREATE FUNCTION cinema.materialize_event_seats() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM cinema.layout_versions WHERE id=NEW.layout_version_id AND room_id=NEW.room_id AND status='published') THEN
   RAISE EXCEPTION 'event requires a published layout for its room' USING ERRCODE = '23514';
 END IF;
 INSERT INTO cinema.event_seats (event_id,layout_seat_id)
 SELECT NEW.id,s.id FROM cinema.layout_rows r JOIN cinema.layout_seats s ON s.layout_row_id=r.id
 WHERE r.layout_version_id=NEW.layout_version_id AND s.enabled;
 RETURN NEW;
END $$;
CREATE TRIGGER materialize_event_seats AFTER INSERT ON cinema.events FOR EACH ROW EXECUTE FUNCTION cinema.materialize_event_seats();
CREATE FUNCTION cinema.guard_event_layout() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.room_id IS DISTINCT FROM OLD.room_id OR NEW.layout_version_id IS DISTINCT FROM OLD.layout_version_id THEN
   RAISE EXCEPTION 'event layout is immutable' USING ERRCODE = '23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_event_layout BEFORE UPDATE ON cinema.events FOR EACH ROW EXECUTE FUNCTION cinema.guard_event_layout();
INSERT INTO platform.schema_migrations (version) VALUES ('0002_venue_layout');
