// The PostgreSQL reads of the ticket detail projection: one ticket by its identifier, and the
// account's own list by the orders it owns.
//
// One statement answers the whole projection, so the seats, the amount and the
// screening can never be read from two different instants of the database: a
// renderer is handed one ticket, not an assembly of partial reads. The list answers the
// same projection from the same column list and joins, so a ticket cannot look like one
// thing on its own page and another in the list that leads there.

package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ticketProjectionColumns is the column list of the one ticket projection. Each column is
	// the authority for one field, and none of them is derived from another:
	//
	//   * the ticket row is the right of entry, its status, its issue instant and, once
	//     the door has admitted it, when and under which label — the redemption is a
	//     guard written by [PostgresStore.Redeem] and read here, never recomputed;
	//   * the event, its film, its room and its cinema give the screening, and the
	//     cinema row carries the timezone the local presentation is computed from;
	//   * the amount comes from the reservation's captured payment attempt — the
	//     money that was actually taken for the whole order — never from
	//     `events.price_cents` times the seat count, because a price that moved after
	//     the sale would silently rewrite what the buyer paid;
	//   * the seat is the ticket's own: `t.event_seat_id` is the seat this ticket
	//     was sold for, and joining it to its layout seat and row gives the labels
	//     printed at the door. This milestone issues one ticket per seat, so the
	//     projection always answers exactly one seat — a ticket that answered every
	//     seat of its order would print a PDF that admits two people at the door, and
	//     ticket A1's document would be indistinguishable from ticket A2's. A later
	//     milestone that issues one ticket for several seats extends this lateral
	//     (the aggregate and its order are already that shape) rather than a renderer
	//     inventing a seat list of its own.
	//
	// The list is shared with [ticketsByCustomerQuery] instead of being written a second time,
	// because two spellings of one projection is how a surface ends up serving a thinner ticket
	// than the page it is sharing a screen with.
	ticketProjectionColumns = `
       t.id::text,
       t.status,
       t.issued_at,
       t.redeemed_at,
       COALESCE(t.redeemed_by, ''),
       t.reservation_id::text,
       e.id::text,
       f.title,
       e.starts_at,
       e.status,
       r.name,
       c.name,
       c.timezone,
       captured.amount_cents,
       COALESCE(seats.items, '[]'::jsonb)`

	// ticketProjectionFrom is the joins the projection is assembled from, shared by the
	// ticket's own read and the account's list for the reason the column list is.
	//
	// The captured attempt is looked up per reservation and limited to one row
	// because `payment_attempts_captured_per_reservation` (0005) allows at most one
	// captured attempt per reservation; the limit states the invariant instead of
	// hoping the read is never ambiguous. That is why the amount is the charge for
	// the whole order and not a per-ticket price: the money was taken once for the
	// reservation. The lateral join is left-associative on purpose: a stored ticket
	// with no captured payment still answers from this query, and
	// [PostgresStore.Detail] then refuses it as unavailable rather than reporting
	// that the ticket does not exist.
	ticketProjectionFrom = `
FROM cinema.tickets t
JOIN cinema.reservations res ON res.id = t.reservation_id
JOIN cinema.events e ON e.id = t.event_id
JOIN cinema.films f ON f.id = e.film_id
JOIN cinema.rooms r ON r.id = e.room_id
JOIN cinema.cinemas c ON c.id = r.cinema_id
LEFT JOIN LATERAL (
  SELECT pa.amount_cents
  FROM cinema.payment_attempts pa
  WHERE pa.reservation_id = t.reservation_id AND pa.status = 'captured'
  LIMIT 1
) captured ON TRUE
LEFT JOIN LATERAL (
  SELECT jsonb_agg(jsonb_build_object(
           'eventSeatId', s.event_seat_id,
           'rowLabel', lr.label,
           'seatNumber', ls.number) ORDER BY lr.position, ls.position, s.event_seat_id) AS items
  FROM cinema.tickets s
  JOIN cinema.event_seats es ON es.id = s.event_seat_id
  JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
  JOIN cinema.layout_rows lr ON lr.id = ls.layout_row_id
  WHERE s.id = t.id
) seats ON TRUE`

	// ticketDetailQuery is the single read of the projection, addressed by the ticket the
	// route was asked about.
	ticketDetailQuery = "SELECT" + ticketProjectionColumns + ticketProjectionFrom + "\nWHERE t.id = $1"

	// ticketsByCustomerQuery is the account's own list: the same projection, scoped to the
	// orders the account owns.
	//
	// The scope is `reservations.customer_id` and nothing else. It is deliberately not
	// `contact_email`, which 0009 stored unverified and which is the address a buyer typed
	// rather than one they proved: a list built from it would show an account the orders
	// somebody else bought with a shared or mistyped address, and every typo would be a small
	// leak. It is not the ticket identifier either — that is a capability, not an owner.
	//
	// A null `customer_id` belongs to nobody. It is a guest purchase, or one written before
	// the column existed, and those tickets stay reachable by the link that was mailed (D4)
	// and appear in no account's list. That is why the predicate is equality: nothing here
	// may tolerate a null into somebody's list.
	//
	// Voided tickets are included on purpose. The list is the record of what the account
	// bought, not a claim about what still admits someone: a ticket that was withdrawn is
	// still one this account paid for, and hiding it would erase an order from the record of
	// the person who paid. The ticket page answers validity; this read answers ownership.
	//
	// Newest first by issue instant, and the ticket identifier breaks ties. Two tickets of one
	// order are written by one statement and therefore carry the same instant, so without the
	// second column their order would be whatever the planner produced, and two loads of the
	// same list could disagree. The identifier says nothing about the ticket, but it is stable
	// and unique, which is exactly what a tie-break has to be.
	ticketsByCustomerQuery = "SELECT" + ticketProjectionColumns + ticketProjectionFrom + `
WHERE res.customer_id = $1
ORDER BY t.issued_at DESC, t.id DESC`

	// ticketDetailSchemaQuery is the closed-surface check: the process advertises
	// the ticket route only against a database that carries every table this read
	// touches. A database migrated to 0004 has the venue and reservation tables but
	// no tickets and no payment attempts, so it has nothing to serve here; a database
	// stopped at 0007 has the tickets but not the redemption columns, and would answer
	// every ticket read as an error instead of a ticket. Every version this read
	// depends on is named here, never assumed.
	ticketDetailSchemaQuery = `
SELECT to_regclass('cinema.tickets') IS NOT NULL
   AND to_regclass('cinema.payment_attempts') IS NOT NULL
   AND to_regclass('cinema.reservations') IS NOT NULL
   AND to_regclass('cinema.events') IS NOT NULL
   AND to_regclass('cinema.films') IS NOT NULL
   AND to_regclass('cinema.rooms') IS NOT NULL
   AND to_regclass('cinema.cinemas') IS NOT NULL
   AND to_regclass('cinema.event_seats') IS NOT NULL
   AND to_regclass('cinema.layout_seats') IS NOT NULL
   AND to_regclass('cinema.layout_rows') IS NOT NULL
   AND EXISTS (
     SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'tickets' AND column_name = 'redeemed_at'
   )`
)

// seatDocument is the seat list as the query aggregates it. Its JSON keys are the
// database's own document shape and not a wire contract: the route and the PDF read
// [Seat] through the projection, which keeps the public field names in one place.
// The array holds the ticket's own seat; it is an array rather than one object
// because a later milestone may sell several seats on one ticket.
type seatDocument struct {
	EventSeatID int64  `json:"eventSeatId"`
	RowLabel    string `json:"rowLabel"`
	SeatNumber  string `json:"seatNumber"`
}

// PostgresStore reads the ticket detail projection from PostgreSQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore opens a pooled connection to the given database URL.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open ticket database: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases pooled connections.
func (s *PostgresStore) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

// TicketDetailSchemaPresent reports whether this database carries everything the
// ticket detail read touches. The process uses it to decide whether the ticket
// route belongs in this deployment: a database without the checkout schema has no
// tickets to show, so advertising the route would answer 503 for every request.
func (s *PostgresStore) TicketDetailSchemaPresent(ctx context.Context) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("ticket database is not configured")
	}
	var present bool
	if err := s.pool.QueryRow(ctx, ticketDetailSchemaQuery).Scan(&present); err != nil {
		return false, fmt.Errorf("read the ticket schema state: %w", err)
	}
	return present, nil
}

// rowScanner is the one row [scanTicketDetail] decodes a projection from: a single row for
// [PostgresStore.Detail], a cursor for [PostgresStore.TicketsByCustomer]. Both reads decode
// through it so a list entry cannot answer a ticket the ticket page would answer differently.
type rowScanner interface {
	Scan(dest ...any) error
}

// Detail answers the projection of one ticket in one query.
//
// The identifier is validated before it reaches SQL: it is the capability the route
// is addressed by, and a value that cannot be a ticket id cannot name a stored
// ticket, so it answers exactly like an identifier that never existed instead of
// turning a wrong guess into a cast error. A stored ticket whose amount was never
// captured, or a read that could not be completed, is [ErrUnavailable]: the
// projection is either complete or absent, never a ticket with holes in it.
func (s *PostgresStore) Detail(ctx context.Context, ticketID string) (TicketDetail, error) {
	var id pgtype.UUID
	if err := id.Scan(ticketID); err != nil || !id.Valid {
		return TicketDetail{}, fmt.Errorf("%w: %q is not a ticket identifier", ErrNotFound, ticketID)
	}
	if s == nil || s.pool == nil {
		return TicketDetail{}, fmt.Errorf("%w: the ticket store is not configured", ErrUnavailable)
	}

	detail, err := scanTicketDetail(s.pool.QueryRow(ctx, ticketDetailQuery, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketDetail{}, fmt.Errorf("%w: %s", ErrNotFound, ticketID)
	}
	if errors.Is(err, ErrUnavailable) {
		// The row was read and the projection is incomplete. It already says why, and no
		// transport detail should be wrapped around an answer the buyer's page can explain.
		return TicketDetail{}, err
	}
	if err != nil {
		return TicketDetail{}, classifyTicketError(err)
	}
	return detail, nil
}

// TicketsByCustomer answers every ticket the account owns, newest first.
//
// The scope is the order's `customer_id` — see [ticketsByCustomerQuery] for why that column
// and no other, and for why a null owner is nobody. An account that bought nothing answers an
// empty slice and no error: the account exists and simply has no orders, which is a different
// statement from "there is no such account", and the caller is expected to answer it with an
// empty list rather than a refusal.
//
// The identifier is validated before it reaches SQL, exactly as [PostgresStore.Detail]
// validates a ticket id: a value that cannot be a customer id cannot name an account, so it
// answers like an account that never existed instead of turning a wrong type into a cast
// error. A stored ticket whose amount was never captured makes the whole list [ErrUnavailable]
// rather than an entry with a hole in it: the projection is either complete for every ticket
// or absent, and a list is a claim about the account that a half-read answer cannot support.
func (s *PostgresStore) TicketsByCustomer(ctx context.Context, customerID string) ([]TicketDetail, error) {
	var id pgtype.UUID
	if err := id.Scan(customerID); err != nil || !id.Valid {
		return nil, fmt.Errorf("%w: %q is not a customer identifier", ErrNotFound, customerID)
	}
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("%w: the ticket store is not configured", ErrUnavailable)
	}

	rows, err := s.pool.Query(ctx, ticketsByCustomerQuery, id)
	if err != nil {
		return nil, classifyTicketError(err)
	}
	defer rows.Close()

	tickets := make([]TicketDetail, 0, 8)
	for rows.Next() {
		detail, err := scanTicketDetail(rows)
		if errors.Is(err, ErrUnavailable) {
			return nil, err
		}
		if err != nil {
			return nil, classifyTicketError(err)
		}
		tickets = append(tickets, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyTicketError(err)
	}
	return tickets, nil
}

// scanTicketDetail decodes one projection row. The amount and the seats are the two fields
// the database can hand back empty, and both are refused rather than invented: a ticket whose
// money was never captured, and a seat list that cannot be decoded, are [ErrUnavailable] —
// never a ticket a renderer would print with a hole in it.
func scanTicketDetail(row rowScanner) (TicketDetail, error) {
	var (
		detail TicketDetail
		amount *int64
		seats  []byte
	)
	if err := row.Scan(
		&detail.TicketID,
		&detail.Status,
		&detail.IssuedAt,
		&detail.RedeemedAt,
		&detail.RedeemedBy,
		&detail.ReservationID,
		&detail.Event.ID,
		&detail.Event.FilmTitle,
		&detail.Event.StartsAt,
		&detail.Event.Status,
		&detail.Event.RoomName,
		&detail.Event.CinemaName,
		&detail.Event.Timezone,
		&amount,
		&seats,
	); err != nil {
		return TicketDetail{}, err
	}

	// A stored ticket without a captured payment has no amount that was actually
	// taken. Recomputing it from the event price would invent money, so the
	// projection refuses instead.
	if amount == nil {
		return TicketDetail{}, fmt.Errorf(
			"%w: ticket %s has no captured payment, so the amount that was taken is unknown",
			ErrUnavailable, detail.TicketID)
	}
	detail.OrderAmountCents = *amount

	var document []seatDocument
	if err := json.Unmarshal(seats, &document); err != nil {
		return TicketDetail{}, fmt.Errorf("%w: decode the seats of ticket %s: %v",
			ErrUnavailable, detail.TicketID, err)
	}
	detail.Seats = make([]Seat, 0, len(document))
	for _, seat := range document {
		detail.Seats = append(detail.Seats, Seat{
			EventSeatID: seat.EventSeatID,
			RowLabel:    seat.RowLabel,
			SeatNumber:  seat.SeatNumber,
		})
	}
	return detail, nil
}

// classifyTicketError keeps an unreadable ticket unavailable, whatever the driver
// said, so no transport detail can be mistaken for a ticket that does not exist.
func classifyTicketError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		// A cast failure means the identifier cannot name a stored ticket, which is
		// the same answer a wrong guess gets.
		return fmt.Errorf("%w: %s", ErrNotFound, pgErr.Message)
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// compile-time proof that the store satisfies the projection contract.
var _ Reader = (*PostgresStore)(nil)
