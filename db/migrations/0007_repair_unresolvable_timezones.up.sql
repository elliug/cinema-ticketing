-- M4.1 backfill: repair the cinemas whose stored timezone does not resolve.
--
-- #55 made the API refuse a cinema whose timezone `time.LoadLocation` cannot resolve,
-- because the ticket detail renders the venue's own clock from it (`LocalStartsAt` in
-- apps/api/internal/tickets/detail.go, over the tzdata cmd/api embeds). A row stored
-- before that guard still carries whatever string was accepted, and the M1 and M2 gate
-- fixtures themselves stored `America/Madrid`, which does not exist. Every ticket of
-- such a venue answers no `startsAtLocal` at all, so it cannot be printed. This
-- migration repairs those rows, so tickets issued before #55 render again.
--
-- What the check can see, and what it cannot. The only zone database SQL can ask is the
-- one this PostgreSQL was built against: `pg_timezone_names` is the server's own
-- catalogue, filled from its tzdata. "Does not resolve" therefore means "this server
-- does not know the name". The residual gap is stated rather than implied: a name
-- PostgreSQL knows and Go's embedded tzdata does not *survives this repair* and still
-- fails to render, and no migration can close that, because the answer belongs to a
-- different program with its own copy of the zone database. What closes it for new rows
-- is the API's creation guard, which validates against the database the API actually
-- renders with; a surviving row is therefore one written before #55 that also falls
-- inside that gap.
--
-- No zone is inferred. An unresolvable name is replaced with `UTC`: the one name every
-- zone database agrees on, and a value that cannot be wrong about an instant, only
-- unhelpful. Reading the venue's name or address to guess the zone it meant would write
-- a plausible-looking lie into the field the printed hour is computed from. The report
-- below exists instead, so an operator can put the real zone back from a record of what
-- was there.
--
-- The report is part of the migration, not a courtesy: a data repair that changes rows
-- silently is a repair nobody can audit or undo, and NOTICE is what the migration runner
-- prints for the operator who runs it.
DO $$
DECLARE
  repaired integer := 0;
  unresolvable record;
BEGIN
  -- The catalogue is checked before it is trusted. If this server does not even know
  -- `UTC`, `pg_timezone_names` is empty or broken and every comparison below is true,
  -- which would rewrite every stored venue to UTC on the strength of a catalogue that
  -- answered nothing. Refusing is the only safe answer to that.
  IF NOT EXISTS (SELECT 1 FROM pg_timezone_names WHERE name = 'UTC') THEN
    RAISE EXCEPTION 'pg_timezone_names does not know UTC, so this server cannot be trusted to decide which timezones resolve'
      USING ERRCODE = '23514';
  END IF;

  -- Each offending row is read with the value it carries, reported, and then repaired by
  -- id, so the rows reported and the rows written are the same set and the NOTICE can
  -- name the value that is about to be lost. A zone that resolves is never touched.
  FOR unresolvable IN
    SELECT id, timezone
    FROM cinema.cinemas
    WHERE timezone NOT IN (SELECT name FROM pg_timezone_names)
    ORDER BY id
  LOOP
    RAISE NOTICE 'timezone backfill: cinema % had unresolvable timezone %, replaced with UTC',
      unresolvable.id, unresolvable.timezone;
    UPDATE cinema.cinemas SET timezone = 'UTC' WHERE id = unresolvable.id;
    repaired := repaired + 1;
  END LOOP;

  -- Always printed, zero included, so a second application says it repaired nothing
  -- instead of looking like it did something.
  RAISE NOTICE 'timezone backfill: repaired % cinema(s) to UTC', repaired;
END $$;

INSERT INTO platform.schema_migrations (version) VALUES ('0007_repair_unresolvable_timezones');
