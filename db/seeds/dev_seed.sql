-- Development seed for the M3 checkout demo.
--
-- It creates exactly one cinema with one room, one published irregular layout
-- (row A holds three seats, row B holds two and one of them is disabled), two
-- films and two sessions on that layout. A session cannot exist before its layout
-- is published, because the M1 trigger materializes the event seats from the
-- enabled seats of a published layout.
--
-- Two rules make it safe to run on every bootstrap:
--
--   * Every row has a fixed identifier and ON CONFLICT DO NOTHING, so a second run
--     inserts nothing and changes no row count.
--   * The layout is inserted as a draft, its geometry is filled in and only then is
--     it published. The M1 guard refuses a layout inserted with
--     status = 'published' and freezes the geometry of a published one, so the
--     publish step must be the last write and must match status = 'draft'.
--
-- The file is development-only, and two guards say so. `make seed-dev` refuses to run
-- unless API_ENV=development is explicitly set, and the guard below repeats that rule so
-- applying the file with psql directly cannot seed a database by accident. That rule alone
-- is a string the caller supplies, which is why a second guard requires the connection to
-- be local: see the block below for exactly what the pair does and does not prevent.
--
-- Prices arrive with the checkout schema (T2) and are repaired on every run: see
-- the price statements at the end of the file.
--
-- The whole file runs in one transaction, so a seed that fails part-way (for
-- example against a database whose migrations have not reached 0005_checkout)
-- leaves nothing behind instead of half of the demo data.

\if :{?api_env}
\else
  \set api_env ''
\endif

BEGIN;

SET cinema.seed_env = :'api_env';

-- Two guards, because the first one is a string the caller supplies.
--
--   * `API_ENV=development`, which `make seed-dev` also requires before it connects.
--   * A local connection. `inet_server_addr()` is the address the server accepted this
--     connection on: NULL for a unix socket (which is how `make seed-dev` applies this
--     file, with psql inside the postgres container), the loopback address for
--     `psql -h 127.0.0.1`, and the server's own network address for anything else. A
--     loopback address cannot belong to a shared server, so "non-loopback" is the case
--     worth refusing.
--
-- What the pair prevents: "export API_ENV=development and seed the shared database". That
-- database answers the connection on its own address rather than on loopback, so the seed
-- stops before writing a single row.
--
-- What neither prevents: somebody deliberately seeding a database that is local to them,
-- whether over the socket or over loopback. That is a development database by definition,
-- and nothing inside this file can tell an intentional seed from an accidental one once
-- both point at the same local server.
DO $$
DECLARE
  server_address inet := inet_server_addr();
BEGIN
  IF current_setting('cinema.seed_env', true) IS DISTINCT FROM 'development' THEN
    RAISE EXCEPTION 'refusing to seed: this file is development-only and must be applied through make seed-dev with API_ENV=development'
      USING ERRCODE = '42501';
  END IF;
  -- A unix socket answers nothing here and a loopback connection answers 127.0.0.0/8 or
  -- ::1; both are this machine. Anything else was applied across a network.
  IF server_address IS NOT NULL
     AND NOT (server_address << '127.0.0.0/8'::inet OR host(server_address) = '::1') THEN
    RAISE EXCEPTION 'refusing to seed: this server answered the connection on %, which is not loopback, so it is not a local database; the development seed runs only against a database on this machine', host(server_address)
      USING ERRCODE = '42501';
  END IF;
END $$;

-- One cinema and one room.
INSERT INTO cinema.cinemas (id, name, timezone, address)
VALUES ('c0000000-0000-4000-8000-000000000001', 'Cine Prueba', 'Europe/Madrid', 'Calle de la Prueba 1')
ON CONFLICT DO NOTHING;

INSERT INTO cinema.rooms (id, cinema_id, name)
VALUES ('c0000000-0000-4000-8000-000000000002', 'c0000000-0000-4000-8000-000000000001', 'Sala 1')
ON CONFLICT DO NOTHING;

-- The irregular layout starts as a draft; rows and seats are added below.
INSERT INTO cinema.layout_versions (id, room_id, version, status)
VALUES ('c0000000-0000-4000-8000-000000000003', 'c0000000-0000-4000-8000-000000000002', 1, 'draft')
ON CONFLICT DO NOTHING;

-- The geometry is only filled while the layout is a draft: the M1 guard rejects any
-- write to rows or seats of a published layout, even one that would conflict, so a
-- second run has to skip this block entirely instead of relying on ON CONFLICT.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM cinema.layout_versions
             WHERE id = 'c0000000-0000-4000-8000-000000000003' AND status = 'draft') THEN
    -- Row A has three seats, row B has two: the rows do not share a seat count.
    INSERT INTO cinema.layout_rows (id, layout_version_id, label, position)
    VALUES
      ('c0000000-0000-4000-8000-000000000011', 'c0000000-0000-4000-8000-000000000003', 'A', 1),
      ('c0000000-0000-4000-8000-000000000012', 'c0000000-0000-4000-8000-000000000003', 'B', 2)
    ON CONFLICT DO NOTHING;

    -- B2 is disabled, so it is never sold and never materialized as an event seat;
    -- B1 is the accessible seat of the room.
    INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled)
    VALUES
      ('c0000000-0000-4000-8000-000000000021', 'c0000000-0000-4000-8000-000000000011', '1', 1, 'standard', true),
      ('c0000000-0000-4000-8000-000000000022', 'c0000000-0000-4000-8000-000000000011', '2', 2, 'standard', true),
      ('c0000000-0000-4000-8000-000000000023', 'c0000000-0000-4000-8000-000000000011', '3', 3, 'standard', true),
      ('c0000000-0000-4000-8000-000000000024', 'c0000000-0000-4000-8000-000000000012', '1', 1, 'accessible', true),
      ('c0000000-0000-4000-8000-000000000025', 'c0000000-0000-4000-8000-000000000012', '2', 2, 'standard', false)
    ON CONFLICT DO NOTHING;
  END IF;
END $$;

-- Publishing must come after the geometry: the guard refuses any update of a
-- layout that is already published, hence the status = 'draft' predicate, which
-- also makes a second run a no-op.
UPDATE cinema.layout_versions
SET status = 'published'
WHERE id = 'c0000000-0000-4000-8000-000000000003' AND status = 'draft';

-- Two films and two sessions in the room, on the published layout.
INSERT INTO cinema.films (id, title, runtime_minutes, rating)
VALUES
  ('c0000000-0000-4000-8000-000000000031', 'El viaje de la luz', 112, '12'),
  ('c0000000-0000-4000-8000-000000000032', 'Cartas desde el puerto', 98, '7')
ON CONFLICT DO NOTHING;

-- The insert materializes the event seats from the four enabled seats, so each
-- session offers A1, A2, A3 and B1.
INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
VALUES
  ('c0000000-0000-4000-8000-000000000041', 'c0000000-0000-4000-8000-000000000002',
   'c0000000-0000-4000-8000-000000000031', 'c0000000-0000-4000-8000-000000000003',
   timestamptz '2030-01-15 19:00:00+01', 'on_sale'),
  ('c0000000-0000-4000-8000-000000000042', 'c0000000-0000-4000-8000-000000000002',
   'c0000000-0000-4000-8000-000000000032', 'c0000000-0000-4000-8000-000000000003',
   timestamptz '2030-01-15 21:30:00+01', 'on_sale')
ON CONFLICT DO NOTHING;

-- The simulated provider declines an amount whose two last minor units are 13, so
-- the prices are chosen to keep every order payable. Each session has four seats,
-- and the amount is price x seats, so the reachable amounts are:
--
--   12.50 EUR: 1250, 2500, 3750, 5000  (ends in 50 or 00)
--   11.50 EUR: 1150, 2300, 3450, 4600  (ends in 50 or 00)
--
-- No seat count of these two events can produce a ...13 amount, so the demo can
-- always complete a checkout.
UPDATE cinema.events SET price_cents = prices.price_cents
FROM (VALUES
  ('c0000000-0000-4000-8000-000000000041'::uuid, 1250),
  ('c0000000-0000-4000-8000-000000000042'::uuid, 1150)
) AS prices(id, price_cents)
WHERE cinema.events.id = prices.id
  -- ON CONFLICT DO NOTHING above never rewrites an existing row, so the price of an
  -- event seeded before this column existed (or before a price change) has to be
  -- repaired here. Skipping rows that already match keeps a second run a true
  -- no-op instead of a no-change update.
  AND cinema.events.price_cents IS DISTINCT FROM prices.price_cents;

COMMIT;
