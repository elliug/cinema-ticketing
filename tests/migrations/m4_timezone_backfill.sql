\set ON_ERROR_STOP on
-- M4.1 timezone backfill: what 0007_repair_unresolvable_timezones did to the cinemas that
-- already existed when it ran.
--
-- Runs after 0001_init through 0007_repair_unresolvable_timezones. The four fixtures are
-- stored by scripts/migrations/verify_migrations.sh *before* the version is applied,
-- because that is the only order in which the repair is what is under test: a cinema
-- stored afterwards would prove the insert path and nothing else.
--
--   00000000-0000-0000-0000-00000000f1a1  America/Madrid    invented — the M1/M2 fixture value
--   00000000-0000-0000-0000-00000000f1a2  Europe/Madrid     a real zone, must not be touched
--   00000000-0000-0000-0000-00000000f1a3  UTC               already the value the repair writes
--   00000000-0000-0000-0000-00000000f1a4  "Europe/Madrid "  a real zone with a trailing space
--
-- The fourth fixture pins "no guessing and no normalising": the repair does not trim,
-- case-fold or reinterpret a name. It asks the server's catalogue whether the stored
-- string resolves, that one does not, and the row is replaced with UTC — the trailing
-- space is data the report shows an operator, who can then set the zone they meant.
--
-- This file is run again after the 0007 rollback, and again after the reapplication, so
-- every assertion below has to hold for a repair that has already happened and re-ran
-- against the data it left behind. That is the idempotence the migration claims: the
-- second application changes nothing.
BEGIN;

DO $$
DECLARE
  unresolved integer;
  matching integer;
  stored text;
BEGIN
  -- 1. Nothing anywhere in the table is still a zone that does not resolve. Asked of the
  -- whole table rather than of the fixtures, so a row nobody planted cannot hide.
  SELECT count(*) INTO unresolved
  FROM cinema.cinemas
  WHERE timezone NOT IN (SELECT name FROM pg_timezone_names);
  IF unresolved <> 0 THEN
    RAISE EXCEPTION '% cinema(s) still carry a timezone that does not resolve', unresolved;
  END IF;

  -- 2. Both cinemas whose zone did not resolve are UTC now, and both rows still exist.
  SELECT count(*) INTO matching
  FROM cinema.cinemas
  WHERE id IN ('00000000-0000-0000-0000-00000000f1a1', '00000000-0000-0000-0000-00000000f1a4')
    AND timezone = 'UTC';
  IF matching <> 2 THEN
    RAISE EXCEPTION 'the cinemas whose timezone did not resolve were not replaced with UTC, found % of 2', matching;
  END IF;

  -- 3. A zone that resolved is exactly what it was: the repair replaces the unresolvable
  -- rows, it never rewrites every venue to UTC.
  SELECT timezone INTO stored FROM cinema.cinemas WHERE id = '00000000-0000-0000-0000-00000000f1a2';
  IF stored IS DISTINCT FROM 'Europe/Madrid' THEN
    RAISE EXCEPTION 'the repair touched a timezone that resolved: found %', coalesce(stored, '<missing row>');
  END IF;
  SELECT timezone INTO stored FROM cinema.cinemas WHERE id = '00000000-0000-0000-0000-00000000f1a3';
  IF stored IS DISTINCT FROM 'UTC' THEN
    RAISE EXCEPTION 'the repair touched a cinema already stored as UTC: found %', coalesce(stored, '<missing row>');
  END IF;

  -- 4. The repair is an update, not a delete: all four fixtures are still four rows. A
  -- migration that removed the offending venues would have taken their rooms, screenings
  -- and tickets with them, which is a lost sale and not a repair.
  SELECT count(*) INTO matching
  FROM cinema.cinemas
  WHERE id IN ('00000000-0000-0000-0000-00000000f1a1', '00000000-0000-0000-0000-00000000f1a2',
               '00000000-0000-0000-0000-00000000f1a3', '00000000-0000-0000-0000-00000000f1a4');
  IF matching <> 4 THEN
    RAISE EXCEPTION 'the repair changed how many cinemas are stored: found % of the 4 fixtures', matching;
  END IF;

  -- 5. The column keeps the shape 0002 gave it, so the repair narrowed nothing and left
  -- no row that could not have been stored before it.
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'cinema' AND table_name = 'cinemas' AND column_name = 'timezone'
      AND is_nullable = 'NO' AND data_type = 'text'
  ) THEN
    RAISE EXCEPTION 'cinema.cinemas.timezone must stay a NOT NULL text column';
  END IF;
  -- The 0002 non-blank check is proved by using it, not by reading its definition: a blank
  -- zone is still refused, and the refusal stores nothing.
  BEGIN
    INSERT INTO cinema.cinemas (id, name, timezone)
    VALUES ('00000000-0000-0000-0000-00000000f1a5', 'Cine Zona En Blanco', '  ');
    RAISE EXCEPTION 'a blank timezone was accepted after the repair';
  EXCEPTION WHEN check_violation THEN NULL; END;
  IF EXISTS (SELECT 1 FROM cinema.cinemas WHERE id = '00000000-0000-0000-0000-00000000f1a5') THEN
    RAISE EXCEPTION 'the rejected blank timezone stored a row';
  END IF;
END $$;

ROLLBACK;
