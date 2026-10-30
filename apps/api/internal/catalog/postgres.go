// The PostgreSQL reads of the projections.
//
// One statement answers the whole catalogue, the same statement answers one screening,
// and one statement answers the whole reservation, so a buyer never sees an entry
// assembled from two different instants of the database, the seat page never sees a
// screening the list would disagree with, and the counts are decided by the same clock
// that governs holds.

package catalog

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
	// screeningQuery is the one projection both screening reads answer. The list wraps it
	// in a filter, the single read in an identifier predicate, and neither adds a column:
	// there is one definition of what a screening is on the wire, and one definition of
	// how many of its seats are free.
	//
	// Each column is the authority for one field, and none is derived from another read:
	//
	//   * `on_sale` is the eligibility rule, written once: the screening must be
	//     `on_sale` and still ahead of `now()`. That is the database clock the hold window
	//     is decided by, so a screening the catalogue offers can never be one a hold would
	//     refuse as already started. Because the rule is a column of this projection, the
	//     list can filter on it (`WHERE screening.on_sale`) and the single read can answer
	//     it, without the predicate being written twice or drifting apart.
	//   * the two seat numbers come from the same lateral aggregate, which is why this
	//     read is one query per request and not one per event or one per seat: the join is
	//     per event, and PostgreSQL evaluates the aggregate once for the whole result set.
	//     `seat_count` counts the materialized event seats; `available_seats` counts the
	//     ones a buyer can select now — state `available` and not owned by a live hold. The
	//     `NOT EXISTS` predicate is the seat map's own definition of held (`released_at IS
	//     NULL AND expires_at > now()`), so an expired hold nobody has swept yet is already
	//     free here, exactly as it is there.
	//   * the venue zone travels with the screening because the local presentation is
	//     computed from it; it is never taken from the process or the browser.
	screeningQuery = `
SELECT e.id::text AS id,
       e.status AS status,
       (e.status = 'on_sale' AND e.starts_at > now()) AS on_sale,
       f.title AS film_title,
       c.name AS cinema_name,
       r.name AS room_name,
       e.starts_at AS starts_at,
       c.timezone AS timezone,
       e.price_cents AS price_cents,
       occupancy.seat_count AS seat_count,
       occupancy.available_seats AS available_seats
FROM cinema.events e
JOIN cinema.films f ON f.id = e.film_id
JOIN cinema.rooms r ON r.id = e.room_id
JOIN cinema.cinemas c ON c.id = r.cinema_id
JOIN LATERAL (
  SELECT count(*) AS seat_count,
         count(*) FILTER (
           WHERE es.state = 'available'
             AND NOT EXISTS (
               SELECT 1
               FROM cinema.reservation_seats rs
               JOIN cinema.reservations held ON held.id = rs.reservation_id
               WHERE rs.event_seat_id = es.id
                 AND rs.released_at IS NULL
                 AND held.expires_at > now()
             )
         ) AS available_seats
  FROM cinema.event_seats es
  WHERE es.event_id = e.id
) occupancy ON TRUE`

	// eventsQuery answers the whole catalogue: the shared projection, filtered by the
	// eligibility column it already carries. `screening.*` is the projection's own column
	// order, so the wire shape and the scan order cannot drift apart.
	//
	// The order is the contract the catalogue screen renders: screening time ascending,
	// with the identifier as the tiebreaker so two screenings at the same minute cannot
	// swap places between two reads.
	eventsQuery = `
SELECT screening.*
FROM (` + screeningQuery + `
) screening
WHERE screening.on_sale
ORDER BY screening.starts_at, screening.id`

	// eventQuery answers one screening, whatever its status, from the same projection: a
	// buyer who followed a link to a screening that is no longer offered is told so by the
	// `status` and `on_sale` columns, not by a 404 that would claim it never existed.
	eventQuery = `
SELECT screening.*
FROM (` + screeningQuery + `
) screening
WHERE screening.id = $1`

	// reservationQuery answers the confirmation's read in one statement:
	//
	//   * the order's own row gives its identifier, its status and its deadline;
	//   * the amount comes from the reservation's captured payment attempt — the money
	//     that was actually taken — never from `events.price_cents` times the seats. The
	//     lateral is left-associative and limited to one row because
	//     `payment_attempts_captured_per_reservation` (0005) allows at most one captured
	//     attempt per reservation, so an unpaid order simply answers no amount instead of
	//     failing the read or inventing a zero. Only `amount_cents` is selected: the
	//     attempt's identifier, provider, provider reference, idempotency key, fingerprint,
	//     outcome and status are the checkout's ledger and are not part of this read.
	//   * the seats are the ones the order holds or sold — `released_at IS NULL`, so a
	//     hold that ended releases nothing into a confirmation — read through their layout
	//     row and seat so the labels are the ones painted at the door, ordered by row and
	//     seat position, which is the seat map's order.
	//   * the tickets are the rows the checkout issued, in the checkout's own seat order.
	//     An unpaid order has none, and `COALESCE` makes that an empty list rather than an
	//     absent value.
	reservationQuery = `
SELECT r.id::text,
       r.status,
       r.expires_at,
       captured.amount_cents,
       COALESCE(seats.items, '[]'::jsonb),
       COALESCE(tickets.items, '[]'::jsonb),
       e.id::text,
       f.title,
       c.name,
       room.name,
       e.starts_at,
       c.timezone
FROM cinema.reservations r
JOIN cinema.events e ON e.id = r.event_id
JOIN cinema.films f ON f.id = e.film_id
JOIN cinema.rooms room ON room.id = e.room_id
JOIN cinema.cinemas c ON c.id = room.cinema_id
LEFT JOIN LATERAL (
  SELECT pa.amount_cents
  FROM cinema.payment_attempts pa
  WHERE pa.reservation_id = r.id AND pa.status = 'captured'
  LIMIT 1
) captured ON TRUE
LEFT JOIN LATERAL (
  SELECT jsonb_agg(jsonb_build_object(
           'eventSeatId', rs.event_seat_id,
           'rowLabel', lr.label,
           'seatNumber', ls.number) ORDER BY lr.position, ls.position, rs.event_seat_id) AS items
  FROM cinema.reservation_seats rs
  JOIN cinema.event_seats es ON es.id = rs.event_seat_id
  JOIN cinema.layout_seats ls ON ls.id = es.layout_seat_id
  JOIN cinema.layout_rows lr ON lr.id = ls.layout_row_id
  WHERE rs.reservation_id = r.id AND rs.released_at IS NULL
) seats ON TRUE
LEFT JOIN LATERAL (
  SELECT jsonb_agg(t.id::text ORDER BY t.event_seat_id) AS items
  FROM cinema.tickets t
  WHERE t.reservation_id = r.id
) tickets ON TRUE
WHERE r.id = $1`

	// readSchemaQuery is the closed-surface check: the process advertises the reads only
	// against a database that carries every table they touch, including the event price
	// and the seats both aggregations count. A database migrated to 0004 has the
	// venue and reservation tables but no tickets and no payment attempts, so the
	// confirmation's read could not answer its amount or its tickets there.
	readSchemaQuery = `
SELECT to_regclass('cinema.events') IS NOT NULL
   AND to_regclass('cinema.films') IS NOT NULL
   AND to_regclass('cinema.rooms') IS NOT NULL
   AND to_regclass('cinema.cinemas') IS NOT NULL
   AND to_regclass('cinema.event_seats') IS NOT NULL
   AND to_regclass('cinema.reservations') IS NOT NULL
   AND to_regclass('cinema.reservation_seats') IS NOT NULL
   AND to_regclass('cinema.payment_attempts') IS NOT NULL
   AND to_regclass('cinema.tickets') IS NOT NULL
   AND to_regclass('cinema.layout_seats') IS NOT NULL
   AND to_regclass('cinema.layout_rows') IS NOT NULL
   AND EXISTS (
     SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'cinema' AND table_name = 'events' AND column_name = 'price_cents')`
)

// seatDocument is the seat list as the query aggregates it. Its JSON keys are the
// database's own document shape and not a wire contract: the route reads [Seat] through
// the projection, which keeps the public field names in one place.
type seatDocument struct {
	EventSeatID int64  `json:"eventSeatId"`
	RowLabel    string `json:"rowLabel"`
	SeatNumber  string `json:"seatNumber"`
}

// PostgresStore reads both projections from PostgreSQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore opens a pooled connection to the given database URL.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open catalogue database: %w", err)
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

// ReadSchemaPresent reports whether this database carries everything the reads touch.
// The process uses it to decide whether the catalogue, screening and confirmation
// routes belong in this deployment: a database without the checkout schema has no
// tickets and no captured payment, so the confirmation could answer neither its amount
// nor its tickets, and advertising the routes would answer 503 for every request.
func (s *PostgresStore) ReadSchemaPresent(ctx context.Context) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("catalogue database is not configured")
	}
	var present bool
	if err := s.pool.QueryRow(ctx, readSchemaQuery).Scan(&present); err != nil {
		return false, fmt.Errorf("read the catalogue schema state: %w", err)
	}
	return present, nil
}

// Events answers the screenings a buyer can buy right now, in screening order.
//
// A catalogue with nothing to sell answers an empty list and no error: "nothing on sale"
// is a fact about the programme, not a failed read, and a transport must be able to
// answer it with 200.
func (s *PostgresStore) Events(ctx context.Context) ([]Screening, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("%w: the catalogue store is not configured", ErrUnavailable)
	}
	rows, err := s.pool.Query(ctx, eventsQuery)
	if err != nil {
		return nil, classifyReadError(err)
	}
	defer rows.Close()

	screenings := []Screening{}
	for rows.Next() {
		screening, err := scanScreening(rows)
		if err != nil {
			return nil, classifyReadError(err)
		}
		screenings = append(screenings, screening)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyReadError(err)
	}
	return screenings, nil
}

// Event answers one screening, whatever its status, from the same statement that fills
// the catalogue: a screening that exists but is no longer offered answers its status and
// `onSale: false` instead of disappearing.
//
// The identifier is validated before it reaches SQL, for the reason the reservation read
// validates its own: a value that cannot be an event id cannot name a stored screening,
// so it answers exactly like an identifier that never existed instead of turning a wrong
// guess into a cast error. A read that could not be completed is [ErrUnavailable], never
// a screening with missing fields.
func (s *PostgresStore) Event(ctx context.Context, eventID string) (Screening, error) {
	var id pgtype.UUID
	if err := id.Scan(eventID); err != nil || !id.Valid {
		return Screening{}, fmt.Errorf("%w: %q is not an event identifier", ErrNotFound, eventID)
	}
	if s == nil || s.pool == nil {
		return Screening{}, fmt.Errorf("%w: the catalogue store is not configured", ErrUnavailable)
	}

	screening, err := scanScreening(s.pool.QueryRow(ctx, eventQuery, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Screening{}, fmt.Errorf("%w: %s", ErrNotFound, eventID)
	}
	if err != nil {
		return Screening{}, classifyReadError(err)
	}
	return screening, nil
}

// rowScanner is what both reads scan through: a pgx row and pgx rows implement it, so the
// two screening reads share one column order and one decoding.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanScreening decodes one row of the shared screening projection. The order is the
// projection's own: id, status, on_sale, film, cinema, room, starts_at, timezone,
// price_cents, seat_count, available_seats.
func scanScreening(row rowScanner) (Screening, error) {
	var (
		screening      Screening
		seatCount      int64
		availableSeats int64
	)
	if err := row.Scan(
		&screening.ID,
		&screening.Status,
		&screening.OnSale,
		&screening.FilmTitle,
		&screening.CinemaName,
		&screening.RoomName,
		&screening.StartsAt,
		&screening.Timezone,
		&screening.PriceCents,
		&seatCount,
		&availableSeats,
	); err != nil {
		return Screening{}, err
	}
	screening.SeatCount = int(seatCount)
	screening.AvailableSeats = int(availableSeats)
	return screening, nil
}

// Reservation answers the confirmation's read of one order in one query.
//
// The identifier is validated before it reaches SQL: it is the capability this read is
// addressed by, and a value that cannot be a reservation id cannot name a stored order,
// so it answers exactly like an identifier that never existed instead of turning a wrong
// guess into a cast error. An unpaid order answers with no amount and no tickets; a read
// that could not be completed is [ErrUnavailable] and never an empty reservation.
func (s *PostgresStore) Reservation(ctx context.Context, reservationID string) (ReservationDetail, error) {
	var id pgtype.UUID
	if err := id.Scan(reservationID); err != nil || !id.Valid {
		return ReservationDetail{}, fmt.Errorf("%w: %q is not a reservation identifier", ErrNotFound, reservationID)
	}
	if s == nil || s.pool == nil {
		return ReservationDetail{}, fmt.Errorf("%w: the catalogue store is not configured", ErrUnavailable)
	}

	var (
		detail    ReservationDetail
		amount    *int64
		seats     []byte
		ticketIDs []byte
	)
	err := s.pool.QueryRow(ctx, reservationQuery, id).Scan(
		&detail.ID,
		&detail.Status,
		&detail.ExpiresAt,
		&amount,
		&seats,
		&ticketIDs,
		&detail.EventID,
		&detail.FilmTitle,
		&detail.CinemaName,
		&detail.RoomName,
		&detail.StartsAt,
		&detail.Timezone,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReservationDetail{}, fmt.Errorf("%w: %s", ErrNotFound, reservationID)
	}
	if err != nil {
		return ReservationDetail{}, classifyReadError(err)
	}

	// No captured attempt means no money was taken. The amount stays absent rather than
	// being recomputed from the event price, which would answer a charge nobody made.
	detail.AmountCents = amount

	var document []seatDocument
	if err := json.Unmarshal(seats, &document); err != nil {
		return ReservationDetail{}, fmt.Errorf("%w: decode the seats of reservation %s: %v",
			ErrUnavailable, detail.ID, err)
	}
	detail.Seats = make([]Seat, 0, len(document))
	for _, seat := range document {
		detail.Seats = append(detail.Seats, Seat{
			EventSeatID: seat.EventSeatID,
			RowLabel:    seat.RowLabel,
			SeatNumber:  seat.SeatNumber,
		})
	}

	if err := json.Unmarshal(ticketIDs, &detail.TicketIDs); err != nil {
		return ReservationDetail{}, fmt.Errorf("%w: decode the tickets of reservation %s: %v",
			ErrUnavailable, detail.ID, err)
	}
	if detail.TicketIDs == nil {
		// An unpaid order answers an empty list, never a missing one, so a confirmation
		// page can iterate it without a nil check.
		detail.TicketIDs = []string{}
	}
	return detail, nil
}

// classifyReadError keeps an unreadable projection unavailable, whatever the driver
// said, except for a cast failure: a value that cannot be an identifier is the same
// answer a wrong guess gets.
func classifyReadError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return fmt.Errorf("%w: %s", ErrNotFound, pgErr.Message)
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// compile-time proof that the store satisfies the read contract.
var _ Reader = (*PostgresStore)(nil)
