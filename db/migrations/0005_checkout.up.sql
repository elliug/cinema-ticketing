-- M3 checkout: the durable side of turning a hold into a paid ticket.
--
-- Three invariants are enforced here rather than in the service, because only the
-- database can decide them between concurrent checkouts:
--
--   1. A reservation is paid at most once. The reservation IS the order, so the
--      guard is a partial unique index on its captured payment attempt.
--   2. One provider payment reference maps to one attempt, so a retried callback
--      cannot capture a second time from the same provider object.
--   3. One seat carries at most one ticket, and a ticket always belongs to its
--      reservation's event, so the seats of a hold can be sold to exactly one
--      buyer even if two checkouts race.
--
-- Everything the checkout writes lands in one transaction: the captured attempt,
-- the paid reservation, the seat state, the tickets and the outbox row. A durable
-- outbox row is used instead of publishing inside the transaction, so a crash
-- after commit cannot lose the notification and a failed publish cannot lose the
-- sale.

-- V1 pricing: one price per event, in minor units, so the order amount is
-- price_cents x seats. A price is not nullable and cannot be negative; zero is a
-- legal stored value because whether a free event may be sold is a checkout
-- decision (the simulated provider needs a positive amount), not a schema one.
ALTER TABLE cinema.events
  ADD COLUMN price_cents integer NOT NULL DEFAULT 0 CHECK (price_cents >= 0);

-- The capture transaction marks the reservation paid. The 0003 constraint only
-- describes the hold lifecycle, so it is replaced under the same name with one
-- extra terminal state. 'paid' is deliberately outside the M2 sweep's candidate set
-- (status IN ('held','payment_pending') in apps/api/internal/reservation/expiry.go),
-- so a reservation holding a captured payment can never be expired and have its
-- seats released after the money was taken.
ALTER TABLE cinema.reservations DROP CONSTRAINT reservations_status_check;
ALTER TABLE cinema.reservations
  ADD CONSTRAINT reservations_status_check
  CHECK (status IN ('held','payment_pending','paid','expired','cancelled'));

-- The durable replay record of a payment attempt. It is written before the
-- provider is called, so a crash or a retried request can find what was already
-- attempted instead of charging again. That pre-call row is why provider_reference
-- and outcome are nullable: before the provider answers there is neither. The
-- decided-state constraints below require both as soon as the attempt is decided.
--
--   * the client Idempotency-Key names the attempt inside its reservation (unique
--     with reservation_id), and request_fingerprint records the payload that key was
--     first used with, so a replay answers from the stored row, a reused key with a
--     different payload is detectable, and a key another buyer used can never answer
--     for this reservation;
--   * provider and provider_reference identify the provider's own object, so the
--     same reference can never be captured twice;
--   * outcome is the provider's verdict and status is where the attempt stands.
--     Only a captured attempt marks the reservation paid, which is why the
--     double-payment guard is a partial unique index on the reservation over
--     status = 'captured': declined and failed attempts are history, not payments.
CREATE TABLE cinema.payment_attempts (
  id uuid PRIMARY KEY,
  reservation_id uuid NOT NULL REFERENCES cinema.reservations(id),
  provider text NOT NULL CHECK (length(trim(provider)) > 0),
  -- Nullable: the attempt is stored before the provider is called.
  provider_reference text CHECK (provider_reference IS NULL OR length(trim(provider_reference)) > 0),
  amount_cents integer NOT NULL CHECK (amount_cents > 0),
  -- Nullable for the same reason: the verdict arrives with the provider's answer.
  outcome text CHECK (outcome IS NULL OR outcome IN ('approved','declined')),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','captured','declined','failed')),
  idempotency_key text NOT NULL CHECK (length(trim(idempotency_key)) > 0),
  request_fingerprint text NOT NULL CHECK (length(request_fingerprint) > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- A verdict always comes with the provider object that produced it, and a decided
  -- attempt (captured or declined) must carry both. Pending and failed rows may
  -- carry nothing, which is the state of a call that has not answered yet.
  CONSTRAINT payment_attempts_decided_check CHECK (
    (outcome IS NULL OR provider_reference IS NOT NULL)
    AND (status NOT IN ('captured', 'declined')
         OR (outcome IS NOT NULL AND provider_reference IS NOT NULL))
  ),
  -- The stored state and the provider's verdict cannot disagree.
  CONSTRAINT payment_attempts_status_outcome_check CHECK (
    status NOT IN ('captured', 'declined')
    OR (status = 'captured' AND outcome = 'approved')
    OR (status = 'declined' AND outcome = 'declined')
  ),
  -- The replay lookup: a request that reuses its key inside this reservation finds
  -- this row, and the same key from another reservation is a different attempt.
  UNIQUE (reservation_id, idempotency_key)
);

-- The double-payment guard: at most one captured attempt per reservation.
CREATE UNIQUE INDEX payment_attempts_captured_per_reservation
  ON cinema.payment_attempts (reservation_id) WHERE status = 'captured';

-- The provider's object is charged once, whatever reservation claims it. Partial,
-- because a pre-call attempt has no reference yet and two of them must be storable.
CREATE UNIQUE INDEX payment_attempts_provider_reference
  ON cinema.payment_attempts (provider, provider_reference) WHERE provider_reference IS NOT NULL;

-- Every attempt of a reservation, for the replay read and for the ticket detail.
CREATE INDEX payment_attempts_reservation ON cinema.payment_attempts (reservation_id);

-- One row per sold seat. The reservation records the order, the ticket records the
-- seat, and event_seat_id is unique so a seat can never be sold twice, not even
-- through a second reservation that somehow survived the hold. status stays
-- minimal for v1: a ticket is 'issued' when it is sold and 'void' if it has to be
-- withdrawn. Redeeming ("used") belongs to the M4 cashier workflows.
--
-- Both references carry event_id, exactly like cinema.reservation_seats in 0003, so
-- a ticket can never join a reservation to a seat of another event. The composite
-- keys it needs are cinema.reservations (id, event_id) from 0003 and the
-- event_seats_id_event_key constraint 0003 adds for the same reason.
CREATE TABLE cinema.tickets (
  id uuid PRIMARY KEY,
  reservation_id uuid NOT NULL,
  event_id uuid NOT NULL,
  event_seat_id bigint NOT NULL,
  status text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued','void')),
  issued_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (event_seat_id),
  FOREIGN KEY (reservation_id, event_id) REFERENCES cinema.reservations (id, event_id),
  FOREIGN KEY (event_seat_id, event_id) REFERENCES cinema.event_seats (id, event_id)
);

-- The confirmation surface and the PDF reads load the tickets of one reservation.
CREATE INDEX tickets_reservation ON cinema.tickets (reservation_id);

-- The transactional outbox. A row is written in the checkout transaction and
-- published afterwards by a relay, so the sale commits even when the notification
-- cannot be delivered, and a retried delivery can never duplicate the sale.
CREATE TABLE cinema.outbox (
  id uuid PRIMARY KEY,
  topic text NOT NULL CHECK (length(trim(topic)) > 0),
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);

-- The relay selects only unpublished rows, oldest first.
CREATE INDEX outbox_unpublished ON cinema.outbox (created_at) WHERE published_at IS NULL;

INSERT INTO platform.schema_migrations (version) VALUES ('0005_checkout');
