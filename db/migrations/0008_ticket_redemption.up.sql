-- M4.1 the door: a ticket is admitted exactly once.
--
-- 0005 stored a ticket as a right of entry and left `status` as its whole
-- lifecycle ('issued', 'void'). Admitting the holder is not a change of status: the
-- sale, the QR token, the seat and the PDF stay exactly as they were issued, and the
-- PDF is a record of the sale that never changes afterwards (S5). So the admission is
-- recorded as its own fact — the instant the door admitted the ticket — and the
-- nullable column *is* the "not yet redeemed" state. Nothing else about the ticket is
-- touched, and no row is backfilled: every ticket already sold was issued before the
-- door existed, and it is unredeemed, which is what NULL says.
--
-- The second column is who did it, and it is deliberately not a person. There are no
-- accounts yet (S4: the door uses the local administrative token), so the door may
-- supply an opaque label — a till, a gate, a shift — and may supply nothing. A blank
-- label is stored as no label, so "nobody named one" and "somebody named an empty
-- one" cannot both be stored and a reader never has to trim before deciding.
--
-- There is no unique index behind "once", and that is not an omission: a ticket is
-- one row, so a second redemption is a second write to that same row and no index can
-- forbid it. What forbids it is two things in this schema. First, the guard the writing
-- statement carries — `WHERE id = $1 AND redeemed_at IS NULL` — evaluated by
-- PostgreSQL under that row's own lock and re-evaluated after a concurrent writer
-- commits, so of two doors presenting one code exactly one matches and exactly one
-- writes (S1, S2). That is what the primitive in
-- apps/api/internal/tickets/redeem.go relies on, and it is why it is a guarded write
-- and never a read followed by a write.
--
-- Second, the trigger below, so the guarantee does not depend on every future writer
-- remembering the predicate: once an instant is recorded, no UPDATE can replace it. A
-- second admission would be a *changed* instant, and that is refused here with a
-- check_violation whether it arrives through a mistake, a new surface or a hand-run
-- query. Clearing the record stays possible, because a rollback or a data repair has to
-- be able to say "this redemption was wrong" — the trigger refuses a second *admission*,
-- not the withdrawal of a mistaken one.
ALTER TABLE cinema.tickets
  ADD COLUMN redeemed_at timestamptz,
  ADD COLUMN redeemed_by text
    CONSTRAINT tickets_redeemed_by_check
    CHECK (redeemed_by IS NULL OR length(trim(redeemed_by)) > 0);

CREATE FUNCTION cinema.guard_ticket_redemption() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.redeemed_at IS NOT NULL
     AND NEW.redeemed_at IS NOT NULL
     AND NEW.redeemed_at IS DISTINCT FROM OLD.redeemed_at THEN
    RAISE EXCEPTION 'ticket % was already admitted at %, and a redemption is never replaced',
      OLD.id, OLD.redeemed_at
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER guard_ticket_redemption
  BEFORE UPDATE ON cinema.tickets
  FOR EACH ROW EXECUTE FUNCTION cinema.guard_ticket_redemption();

INSERT INTO platform.schema_migrations (version) VALUES ('0008_ticket_redemption');
