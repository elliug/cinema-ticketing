// The door: admitting a ticket exactly once.
//
// # One statement decides, and it is a write
//
// Redemption is a single guarded UPDATE — `SET redeemed_at = now() … WHERE id = $1 AND
// redeemed_at IS NULL AND <still admissible> RETURNING …`. It is never a read followed
// by a write, and that is the whole reason this file exists: a read-then-write lets two
// doors presenting one code at the same instant both read "not redeemed yet" and both
// write, admitting two people with one ticket. A read is stale the moment it answers,
// so a service that decides on one decides on a past state.
//
// PostgreSQL decides instead. A concurrent UPDATE of one row waits for the writer
// holding it and then re-evaluates its own predicate against the committed row, so the
// second statement matches nothing: exactly one redemption writes, whether the two
// doors arrive a microsecond or a minute apart, and no application code has to be right
// about the interleaving. `cinema.tickets` also refuses to replace a recorded instant
// (db/migrations/0008_ticket_redemption.up.sql), so the guarantee survives a future
// writer that forgets this predicate.
//
// # The loser is told when, not that something failed
//
// A second presentation of an admitted ticket is the normal case at a door, not an
// error: it is answered [RedemptionAlreadyRedeemed] with the instant the first
// redemption wrote, so a cashier can say "this was used at 20:14" rather than "invalid".
// The read behind that answer runs only after the guarded write matched nothing, in a
// statement of its own, so it never decides anything — it explains a decision the
// database already made, and it sees the instant a winning door committed even when the
// loser was blocked behind it. Every other answer is its own outcome as well, because a
// cashier acts differently on each: [ErrNotFound], [RedemptionRefusedVoided],
// [RedemptionRefusedEventCancelled]. None of them writes anything.
//
// # Nothing is invented about identity
//
// There are no accounts yet, so whoever holds the local administrative token is the
// door. It may name itself with an opaque label (a till, a gate, a shift) and may name
// nothing at all; the label is stored as given — trimmed, and as no label when blank —
// and never as a user, because this package cannot know who a person is.

package tickets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RedemptionOutcome is what the door decided about one presented ticket. It is a
// stored-state question answered as a value, so a caller passes it to a screen instead
// of inventing a message from an error string.
type RedemptionOutcome string

const (
	// RedemptionRedeemed is the one outcome that writes: this call admitted the ticket.
	RedemptionRedeemed RedemptionOutcome = "redeemed"
	// RedemptionAlreadyRedeemed carries an earlier redemption's instant, so the holder is
	// told when it was used rather than that something failed.
	RedemptionAlreadyRedeemed RedemptionOutcome = "already_redeemed"
	// RedemptionRefusedVoided is a withdrawn ticket: the ticket no longer admits anyone.
	RedemptionRefusedVoided RedemptionOutcome = "voided"
	// RedemptionRefusedEventCancelled is an issued ticket whose screening was cancelled.
	RedemptionRefusedEventCancelled RedemptionOutcome = "event_cancelled"
)

// Redemption is the door's answer about one presented ticket.
type Redemption struct {
	// TicketID is the ticket the answer is about, as the caller named it.
	TicketID string
	// Outcome is what happened. [RedemptionRedeemed] and [RedemptionAlreadyRedeemed]
	// carry an instant; the refusals carry none.
	Outcome RedemptionOutcome
	// RedeemedAt is when the door admitted the ticket: the instant this call wrote, or
	// the first redemption's instant when this call lost. Zero on a refusal.
	RedeemedAt time.Time
	// RedeemedBy is the opaque label the redemption that wrote supplied, and empty when
	// it supplied none. A losing call is told the winner's label, not its own.
	RedeemedBy string
}

// redeemTicketQuery is the one statement that decides a redemption. Every guard it
// carries is a fact the database already stores, so the write and the decision cannot
// disagree; an UPDATE without them would be the double admission this package exists to
// prevent.
const redeemTicketQuery = `
UPDATE cinema.tickets t
SET redeemed_at = now(), redeemed_by = NULLIF(trim($2), '')
WHERE t.id = $1
  AND t.redeemed_at IS NULL
  AND t.status = 'issued'
  AND EXISTS (
    SELECT 1
    FROM cinema.events e
    WHERE e.id = t.event_id AND e.status <> 'cancelled'
  )
RETURNING t.redeemed_at, COALESCE(t.redeemed_by, '')`

// redeemedStateQuery explains a redemption whose guarded write matched nothing. It is a
// read of the current row in a new statement, never a decision: the decision was the
// write above, and this only names which of its guards held.
const redeemedStateQuery = `
SELECT t.status,
       e.status,
       t.redeemed_at,
       COALESCE(t.redeemed_by, '')
FROM cinema.tickets t
JOIN cinema.events e ON e.id = t.event_id
WHERE t.id = $1`

// Redeem admits a ticket at the door, exactly once.
//
// The identifier is validated before it reaches SQL, so a value that cannot be a ticket
// id answers [ErrNotFound] with the same words a ticket that never existed does, and no
// statement runs. A store that cannot be asked answers [ErrUnavailable]: a database
// failure is not evidence about the code in a cashier's hand. label is the opaque name
// the door gives itself, stored trimmed, and a blank one is stored as no label at all.
func (s *PostgresStore) Redeem(ctx context.Context, ticketID, label string) (Redemption, error) {
	var id pgtype.UUID
	if err := id.Scan(ticketID); err != nil || !id.Valid {
		return Redemption{}, fmt.Errorf("%w: %q is not a ticket identifier", ErrNotFound, ticketID)
	}
	if s == nil || s.pool == nil {
		return Redemption{}, fmt.Errorf("%w: the ticket store is not configured", ErrUnavailable)
	}

	redemption := Redemption{TicketID: ticketID}
	var (
		redeemedAt time.Time
		redeemedBy string
	)
	err := s.pool.QueryRow(ctx, redeemTicketQuery, id, label).Scan(&redeemedAt, &redeemedBy)
	if err == nil {
		redemption.Outcome = RedemptionRedeemed
		redemption.RedeemedAt = redeemedAt
		redemption.RedeemedBy = redeemedBy
		return redemption, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Redemption{}, classifyTicketError(err)
	}

	// Nothing was written, so the row is either absent or was not admissible. Which one is
	// read here, after the decision, from a statement of its own: a value read before
	// deciding would have told this call nothing the guarded write did not already settle.
	var (
		ticketStatus    string
		eventStatus     string
		firstRedeemedAt *time.Time
		firstRedeemedBy string
	)
	err = s.pool.QueryRow(ctx, redeemedStateQuery, id).
		Scan(&ticketStatus, &eventStatus, &firstRedeemedAt, &firstRedeemedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Redemption{}, fmt.Errorf("%w: %s", ErrNotFound, ticketID)
	}
	if err != nil {
		return Redemption{}, classifyTicketError(err)
	}

	switch {
	case firstRedeemedAt != nil:
		// The ticket was already admitted, which is the answer a cashier needs: when.
		redemption.Outcome = RedemptionAlreadyRedeemed
		redemption.RedeemedAt = *firstRedeemedAt
		redemption.RedeemedBy = firstRedeemedBy
	case ticketStatus != TicketStatusIssued:
		redemption.Outcome = RedemptionRefusedVoided
	case eventStatus == EventStatusCancelled:
		redemption.Outcome = RedemptionRefusedEventCancelled
	default:
		// The write matched nothing while the row reads as admissible — a state this schema
		// cannot produce. Refusing to guess is the only honest answer: an "admitted" here
		// would report a ticket nobody admitted.
		return Redemption{}, fmt.Errorf(
			"%w: ticket %s reads as admissible but no redemption was written", ErrUnavailable, ticketID)
	}
	return redemption, nil
}
