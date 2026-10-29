-- M2 durable idempotency records for hold creation.
--
-- One row per client supplied Idempotency-Key. The record stores the outcome of
-- the request that first used the key, so a replay answers with the same status
-- and the same hold instead of creating a second reservation. The key is the
-- primary key, so the database itself decides the winner between two concurrent
-- requests that carry the same key.
--
-- Only successful holds are recorded: a request that lost a seat conflict is
-- re-evaluated on retry, so storing its failure would keep reporting a conflict
-- after the seats were freed. hold_id therefore references the reservation the
-- key produced and can never be null.
--
-- expires_at is the retention deadline of the record, not of the hold:
-- hold_expires_at keeps the original hold deadline verbatim so a replay can
-- never extend the window. Records past expires_at may be pruned by the durable
-- expiry sweep.
--
-- The hold reference is deferred: a claim transaction writes its record first,
-- so a replay that loses the key fails on the primary key before it touches the
-- reservation tables, while the reference itself is checked at commit, when the
-- reservation exists. Both rows therefore land in one atomic outcome.
CREATE TABLE cinema.idempotency_keys (
  idempotency_key text PRIMARY KEY CHECK (length(trim(idempotency_key)) > 0),
  request_fingerprint text NOT NULL CHECK (length(request_fingerprint) > 0),
  status integer NOT NULL CHECK (status >= 100 AND status <= 599),
  hold_id uuid NOT NULL REFERENCES cinema.reservations(id) DEFERRABLE INITIALLY DEFERRED,
  hold_expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);

-- The retention sweep selects prunable records by deadline.
CREATE INDEX idempotency_keys_expires_at ON cinema.idempotency_keys (expires_at);

INSERT INTO platform.schema_migrations (version) VALUES ('0004_idempotency');
