// The PostgreSQL read of the ticket detail projection.
//
// One statement answers the whole projection, so the seats, the amount and the
// screening can never be read from two different instants of the database: a
// renderer is handed one ticket, not an assembly of partial reads.

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
	// ticketDetailQuery is the single read of the projection. Each column is the
	// authority for one field, and none of them is derived from another:
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
	// The captured attempt is looked up per reservation and limited to one row
	// because `payment_attempts_captured_per_reservation` (0005) allows at most one
	// captured attempt per reservation; the limit states the invariant instead of
	// hoping the read is never ambiguous. That is why the amount is the charge for
	// the whole order and not a per-ticket price: the money was taken once for the
	// reservation. The lateral join is left-associative on purpose: a stored ticket
	// with no captured payment still answers from this query, and
	// [PostgresStore.Detail] then refuses it as unavailable rather than reporting
	// that the ticket does not exist.
	ticketDetailQuery = `
SELECT t.id::text,
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
       COALESCE(seats.items, '[]'::jsonb)
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
) seats ON TRUE
WHERE t.id = $1`

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

	var (
		detail TicketDetail
		amount *int64
		seats  []byte
	)
	err := s.pool.QueryRow(ctx, ticketDetailQuery, id).Scan(
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
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketDetail{}, fmt.Errorf("%w: %s", ErrNotFound, ticketID)
	}
	if err != nil {
		return TicketDetail{}, classifyTicketError(err)
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
