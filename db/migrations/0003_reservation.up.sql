-- M2 reservation core: durable seat holds with per-seat exclusivity and expiry.
--
-- A hold is one row in cinema.reservation_seats. The row is active while
-- released_at IS NULL, and a partial unique index allows at most one active row
-- per event_seat_id. Expiry is durable in cinema.reservations.expires_at; the
-- partial index cannot depend on the clock, so the claim trigger releases the
-- expired rows of the claimed seat inside the acquiring transaction. That makes
-- expiry free the seat before any background sweep runs, while the unique index
-- still guarantees that two concurrent claimers cannot both end up active.
CREATE TABLE cinema.reservations (
  id uuid PRIMARY KEY,
  event_id uuid NOT NULL REFERENCES cinema.events(id),
  status text NOT NULL DEFAULT 'held' CHECK (status IN ('held','payment_pending','expired','cancelled')),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  -- Referenced by reservation_seats so a hold cannot point at another event.
  UNIQUE (id, event_id)
);

-- Every event seat already belongs to exactly one event; this key lets the
-- hold association enforce the same event on both of its foreign keys.
ALTER TABLE cinema.event_seats ADD CONSTRAINT event_seats_id_event_key UNIQUE (id, event_id);

CREATE TABLE cinema.reservation_seats (
  reservation_id uuid NOT NULL,
  event_id uuid NOT NULL,
  event_seat_id bigint NOT NULL,
  released_at timestamptz,
  PRIMARY KEY (reservation_id, event_seat_id),
  FOREIGN KEY (reservation_id, event_id) REFERENCES cinema.reservations (id, event_id),
  FOREIGN KEY (event_seat_id, event_id) REFERENCES cinema.event_seats (id, event_id)
);

-- The concurrency guard: at most one active hold per event seat.
CREATE UNIQUE INDEX reservation_seats_active_hold
  ON cinema.reservation_seats (event_seat_id) WHERE released_at IS NULL;

-- Foreign-key and history lookups by seat, independent of the active predicate.
CREATE INDEX reservation_seats_event_seat ON cinema.reservation_seats (event_seat_id);

-- The expiry worker selects live holds by deadline.
CREATE INDEX reservations_expires_at ON cinema.reservations (expires_at)
  WHERE status IN ('held','payment_pending');

-- Free the claimed seat from holds whose window already closed, then let the
-- partial unique index decide the winner between concurrent claimers.
CREATE FUNCTION cinema.release_expired_seat_hold() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE hold_expires timestamptz;
BEGIN
  SELECT r.expires_at INTO hold_expires FROM cinema.reservations r WHERE r.id = NEW.reservation_id;
  IF hold_expires IS NOT NULL AND hold_expires <= now() THEN
    RAISE EXCEPTION 'reservation % cannot hold event seat %: its hold window already expired',
      NEW.reservation_id, NEW.event_seat_id USING ERRCODE = '23514';
  END IF;
  UPDATE cinema.reservation_seats rs
     SET released_at = now()
    FROM cinema.reservations r
   WHERE r.id = rs.reservation_id
     AND rs.event_seat_id = NEW.event_seat_id
     AND rs.released_at IS NULL
     AND r.expires_at <= now();
  RETURN NEW;
END $$;
CREATE TRIGGER release_expired_seat_hold BEFORE INSERT ON cinema.reservation_seats
  FOR EACH ROW EXECUTE FUNCTION cinema.release_expired_seat_hold();

INSERT INTO platform.schema_migrations (version) VALUES ('0003_reservation');
