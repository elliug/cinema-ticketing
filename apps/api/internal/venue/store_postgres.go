package venue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore persists venue aggregates in PostgreSQL, which remains the
// authoritative source for layout immutability and event seat materialization.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore opens a pooled connection to the given database URL.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases pooled connections.
func (s *PostgresStore) Close() { s.pool.Close() }

// Ping verifies database reachability.
func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func classifyError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23503", "23514", "23502":
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.Message)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// CreateCinema inserts one cinema.
func (s *PostgresStore) CreateCinema(ctx context.Context, cinema Cinema) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cinema.cinemas (id, name, timezone, address) VALUES ($1, $2, $3, NULLIF($4, ''))`,
		cinema.ID, cinema.Name, cinema.Timezone, cinema.Address)
	return classifyError(err)
}

// rowExists reports whether one row satisfies the given lookup, mapping a
// missing row through the same classification the rest of the store uses.
func (s *PostgresStore) rowExists(ctx context.Context, query, id string) error {
	var found int
	return classifyError(s.pool.QueryRow(ctx, query, id).Scan(&found))
}

// ListCinemas reads every cinema, ordered by name.
func (s *PostgresStore) ListCinemas(ctx context.Context) ([]Cinema, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, timezone, COALESCE(address, '') FROM cinema.cinemas ORDER BY name, id`)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()
	cinemas := []Cinema{}
	for rows.Next() {
		var cinema Cinema
		if err := rows.Scan(&cinema.ID, &cinema.Name, &cinema.Timezone, &cinema.Address); err != nil {
			return nil, classifyError(err)
		}
		cinemas = append(cinemas, cinema)
	}
	return cinemas, classifyError(rows.Err())
}

// CreateRoom inserts one room for an existing cinema.
func (s *PostgresStore) CreateRoom(ctx context.Context, room Room) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ($1, $2, $3)`,
		room.ID, room.CinemaID, room.Name)
	return classifyError(err)
}

// ListRooms reads the rooms of one cinema, ordered by name. A missing cinema is
// reported as not found instead of as an empty list.
func (s *PostgresStore) ListRooms(ctx context.Context, cinemaID string) ([]Room, error) {
	if err := s.rowExists(ctx, `SELECT 1 FROM cinema.cinemas WHERE id = $1`, cinemaID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, cinema_id, name FROM cinema.rooms WHERE cinema_id = $1 ORDER BY name, id`, cinemaID)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()
	rooms := []Room{}
	for rows.Next() {
		var room Room
		if err := rows.Scan(&room.ID, &room.CinemaID, &room.Name); err != nil {
			return nil, classifyError(err)
		}
		rooms = append(rooms, room)
	}
	return rooms, classifyError(rows.Err())
}

// CreateLayout inserts the next draft version for a room.
func (s *PostgresStore) CreateLayout(ctx context.Context, layoutID, roomID string) (Layout, error) {
	var version int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO cinema.layout_versions (id, room_id, version)
		 SELECT $1, $2, COALESCE(MAX(version), 0) + 1 FROM cinema.layout_versions WHERE room_id = $2
		 RETURNING version`, layoutID, roomID).Scan(&version)
	if err != nil {
		return Layout{}, classifyError(err)
	}
	return Layout{ID: layoutID, RoomID: roomID, Version: version, Status: LayoutDraft, Rows: []Row{}}, nil
}

// ListLayouts reads every version of one room, ordered by version. A missing
// room is reported as not found instead of as an empty list.
func (s *PostgresStore) ListLayouts(ctx context.Context, roomID string) ([]Layout, error) {
	if err := s.rowExists(ctx, `SELECT 1 FROM cinema.rooms WHERE id = $1`, roomID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, room_id, version, status FROM cinema.layout_versions WHERE room_id = $1 ORDER BY version`,
		roomID)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()
	layouts := []Layout{}
	for rows.Next() {
		layout := Layout{Rows: []Row{}}
		if err := rows.Scan(&layout.ID, &layout.RoomID, &layout.Version, &layout.Status); err != nil {
			return nil, classifyError(err)
		}
		layouts = append(layouts, layout)
	}
	return layouts, classifyError(rows.Err())
}

// ReplaceRows replaces the geometry of a draft layout in one transaction.
func (s *PostgresStore) ReplaceRows(ctx context.Context, layoutID string, rows []Row) (Layout, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Layout{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var roomID string
	var version int
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT room_id, version, status FROM cinema.layout_versions WHERE id = $1 FOR UPDATE`,
		layoutID).Scan(&roomID, &version, &status); err != nil {
		return Layout{}, classifyError(err)
	}
	if status != LayoutDraft {
		return Layout{}, fmt.Errorf("%w: layout %s is published", ErrConflict, layoutID)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM cinema.layout_seats WHERE layout_row_id IN (SELECT id FROM cinema.layout_rows WHERE layout_version_id = $1)`,
		layoutID); err != nil {
		return Layout{}, classifyError(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cinema.layout_rows WHERE layout_version_id = $1`, layoutID); err != nil {
		return Layout{}, classifyError(err)
	}
	for _, row := range rows {
		rowID := row.ID
		if rowID == "" {
			rowID = NewID()
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO cinema.layout_rows (id, layout_version_id, label, position) VALUES ($1, $2, $3, $4)`,
			rowID, layoutID, row.Label, row.Position); err != nil {
			return Layout{}, classifyError(err)
		}
		for _, seat := range row.Seats {
			seatID := seat.ID
			if seatID == "" {
				seatID = NewID()
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO cinema.layout_seats (id, layout_row_id, number, position, kind, enabled) VALUES ($1, $2, $3, $4, $5, $6)`,
				seatID, rowID, seat.Number, seat.Position, string(seat.Kind), seat.Enabled); err != nil {
				return Layout{}, classifyError(err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Layout{}, classifyError(err)
	}
	return s.GetLayout(ctx, layoutID)
}

// PublishLayout freezes a draft layout; PostgreSQL rejects later geometry edits.
func (s *PostgresStore) PublishLayout(ctx context.Context, layoutID string) (Layout, error) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE cinema.layout_versions SET status = 'published' WHERE id = $1`, layoutID); err != nil {
		return Layout{}, classifyError(err)
	}
	return s.GetLayout(ctx, layoutID)
}

// CloneLayout copies a published geometry into a new draft version.
func (s *PostgresStore) CloneLayout(ctx context.Context, sourceID, targetID string) (Layout, error) {
	var version int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM cinema.layout_versions
		 WHERE room_id = (SELECT room_id FROM cinema.layout_versions WHERE id = $1)`, sourceID).Scan(&version)
	if err != nil {
		return Layout{}, classifyError(err)
	}
	if _, err := s.pool.Exec(ctx, `SELECT cinema.copy_layout_version($1, $2, $3)`, sourceID, targetID, version); err != nil {
		return Layout{}, classifyError(err)
	}
	return s.GetLayout(ctx, targetID)
}

// GetLayout reads one layout version with its rows and seats.
func (s *PostgresStore) GetLayout(ctx context.Context, layoutID string) (Layout, error) {
	var layout Layout
	if err := s.pool.QueryRow(ctx,
		`SELECT id, room_id, version, status FROM cinema.layout_versions WHERE id = $1`,
		layoutID).Scan(&layout.ID, &layout.RoomID, &layout.Version, &layout.Status); err != nil {
		return Layout{}, classifyError(err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, label, position FROM cinema.layout_rows WHERE layout_version_id = $1 ORDER BY position`,
		layoutID)
	if err != nil {
		return Layout{}, err
	}
	defer rows.Close()
	layout.Rows = []Row{}
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.ID, &row.Label, &row.Position); err != nil {
			return Layout{}, err
		}
		row.Seats = []Seat{}
		layout.Rows = append(layout.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return Layout{}, err
	}
	for i := range layout.Rows {
		seats, err := s.pool.Query(ctx,
			`SELECT id, number, position, kind, enabled FROM cinema.layout_seats WHERE layout_row_id = $1 ORDER BY position`,
			layout.Rows[i].ID)
		if err != nil {
			return Layout{}, err
		}
		for seats.Next() {
			var seat Seat
			var kind string
			if err := seats.Scan(&seat.ID, &seat.Number, &seat.Position, &kind, &seat.Enabled); err != nil {
				seats.Close()
				return Layout{}, err
			}
			seat.Kind = SeatKind(kind)
			layout.Rows[i].Seats = append(layout.Rows[i].Seats, seat)
		}
		if err := seats.Err(); err != nil {
			seats.Close()
			return Layout{}, err
		}
		seats.Close()
	}
	return layout, nil
}

// CreateFilm inserts one catalog entry.
func (s *PostgresStore) CreateFilm(ctx context.Context, film Film) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cinema.films (id, title, runtime_minutes, rating) VALUES ($1, $2, $3, $4)`,
		film.ID, film.Title, film.RuntimeMinutes, film.Rating)
	return classifyError(err)
}

// ListFilms reads every catalog entry, ordered by title with the identifier as the
// tiebreaker so two films with one title cannot swap places between two reads, the same
// rule the cinema, room and screening lists follow.
func (s *PostgresStore) ListFilms(ctx context.Context) ([]Film, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, title, runtime_minutes, rating FROM cinema.films ORDER BY title, id`)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()
	films := []Film{}
	for rows.Next() {
		var film Film
		if err := rows.Scan(&film.ID, &film.Title, &film.RuntimeMinutes, &film.Rating); err != nil {
			return nil, classifyError(err)
		}
		films = append(films, film)
	}
	return films, classifyError(rows.Err())
}

// CreateEvent inserts the event; the database materializes its seats in the same
// transaction, so a failure leaves no event and no partial seats.
//
// The price column is written **only when the request named a price**. That is not a
// micro-optimisation: `price_cents` arrives with the checkout schema (0005), so an M1
// database has no such column at all, and a create that mentions no price — the request the
// M1 gate sends — must keep working there. Writing the column unconditionally turned that
// request into a 500 ("column price_cents does not exist", SQLSTATE 42703) the moment the
// create learned about prices.
const createEventStatement = `INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
		 VALUES ($1, $2, $3, $4, $5, $6)`

// createEventWithPriceStatement is the same row plus the price, for a request that named
// one: a zero here is the free screening the column documents, not an omission.
const createEventWithPriceStatement = `INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status, price_cents)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`

func (s *PostgresStore) CreateEvent(ctx context.Context, event Event, priceCents *int64) error {
	var err error
	if priceCents == nil {
		_, err = s.pool.Exec(ctx, createEventStatement,
			event.ID, event.RoomID, event.FilmID, event.LayoutVersionID, event.StartsAt, event.Status)
	} else {
		_, err = s.pool.Exec(ctx, createEventWithPriceStatement,
			event.ID, event.RoomID, event.FilmID, event.LayoutVersionID, event.StartsAt, event.Status, event.PriceCents)
	}
	return classifyError(err)
}

// eventColumns is the stored-column shape of a screening. The update write selects it so
// its RETURNING and the reads below cannot drift apart; RETURNING cannot join, so the
// administrative reads project the names separately in eventReadColumns.
const eventColumns = `id, room_id, film_id, layout_version_id, starts_at, status, price_cents`

// eventReadColumns is the administrative read's projection: the stored columns of a
// screening plus the names of what it is scheduled against and the venue's own zone. The
// names and the zone come from the same statement, so a list of screenings is one query and
// never an n+1 of films, rooms and cinemas, and the venue hour a read renders needs no
// second read.
const eventReadColumns = `e.id, e.room_id, e.film_id, e.layout_version_id, e.starts_at, e.status,
       e.price_cents, f.title, r.name, c.name, c.timezone`

// eventReadFrom is the FROM clause and the joins every administrative screening read
// walks. All three joins are inner ones because cinema.events references a film and a room
// and a room belongs to exactly one cinema, so a stored screening always has the three
// names.
const eventReadFrom = `
FROM cinema.events e
JOIN cinema.films f ON f.id = e.film_id
JOIN cinema.rooms r ON r.id = e.room_id
JOIN cinema.cinemas c ON c.id = r.cinema_id`

// eventTargets is the scan destinations of eventColumns, shared by the update write.
func eventTargets(event *Event) []any {
	return []any{&event.ID, &event.RoomID, &event.FilmID, &event.LayoutVersionID,
		&event.StartsAt, &event.Status, &event.PriceCents}
}

// eventReadTargets is eventTargets plus the three names and the venue zone
// eventReadColumns projects, in the order that projection selects them.
func eventReadTargets(event *Event) []any {
	return append(eventTargets(event), &event.FilmTitle, &event.RoomName, &event.CinemaName, &event.CinemaTimezone)
}

// ListEvents reads every screening in start order, with the identifier as the
// tiebreaker so two screenings at the same minute cannot swap places between two reads,
// and names the film, the room and the cinema each one is against — plus the venue's own
// zone, from which the caller renders the screening's local hour.
func (s *PostgresStore) ListEvents(ctx context.Context) ([]Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+eventReadColumns+eventReadFrom+`
ORDER BY e.starts_at, e.id`)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(eventReadTargets(&event)...); err != nil {
			return nil, classifyError(err)
		}
		events = append(events, event)
	}
	return events, classifyError(rows.Err())
}

// GetEvent reads one screening with its stored price and the names of what it is against.
// A missing row is not found.
func (s *PostgresStore) GetEvent(ctx context.Context, eventID string) (Event, error) {
	var event Event
	err := s.pool.QueryRow(ctx,
		`SELECT `+eventReadColumns+eventReadFrom+`
WHERE e.id = $1`, eventID).Scan(eventReadTargets(&event)...)
	if err != nil {
		return Event{}, classifyError(err)
	}
	return event, nil
}

// UpdateEvent updates the columns one screening's administration request named, in a single
// statement. The SET list is built from the fields that were provided, so a field the
// request did not carry is not written at all — neither to its stored value nor to zero.
// The event is the guard in WHERE, so an unknown identifier updates no row and answers not
// found instead of a success the database never produced. RETURNING answers the event as it
// is now stored, not a value assembled from the arguments.
func (s *PostgresStore) UpdateEvent(ctx context.Context, eventID string, update EventUpdate) (Event, error) {
	assignments := make([]string, 0, 2)
	args := []any{eventID}
	if update.PriceCents != nil {
		args = append(args, *update.PriceCents)
		assignments = append(assignments, fmt.Sprintf("price_cents = $%d", len(args)))
	}
	if update.Status != nil {
		args = append(args, *update.Status)
		assignments = append(assignments, fmt.Sprintf("status = $%d", len(args)))
	}
	if len(assignments) == 0 {
		// The service refuses this before SQL; the store refuses it too rather than send an
		// UPDATE with no SET list, which is not a statement at all.
		return Event{}, NewInvalidError("", "nothing to change: provide priceCents or status")
	}
	var event Event
	err := s.pool.QueryRow(ctx,
		`UPDATE cinema.events SET `+strings.Join(assignments, ", ")+` WHERE id = $1 RETURNING `+eventColumns,
		args...).
		Scan(eventTargets(&event)...)
	if err != nil {
		return Event{}, classifyError(err)
	}
	return event, nil
}

// ListEventSeats reads the materialized seats of one event.
func (s *PostgresStore) ListEventSeats(ctx context.Context, eventID string) ([]EventSeat, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, event_id, layout_seat_id, state FROM cinema.event_seats WHERE event_id = $1 ORDER BY id`,
		eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seats := []EventSeat{}
	for rows.Next() {
		var seat EventSeat
		if err := rows.Scan(&seat.ID, &seat.EventID, &seat.LayoutSeatID, &seat.State); err != nil {
			return nil, err
		}
		seats = append(seats, seat)
	}
	return seats, rows.Err()
}
