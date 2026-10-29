package venue

import (
	"context"
	"errors"
	"fmt"

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

// CreateRoom inserts one room for an existing cinema.
func (s *PostgresStore) CreateRoom(ctx context.Context, room Room) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cinema.rooms (id, cinema_id, name) VALUES ($1, $2, $3)`,
		room.ID, room.CinemaID, room.Name)
	return classifyError(err)
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

// CreateEvent inserts the event; the database materializes its seats in the
// same transaction, so a failure leaves no event and no partial seats.
func (s *PostgresStore) CreateEvent(ctx context.Context, event Event) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cinema.events (id, room_id, film_id, layout_version_id, starts_at, status)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		event.ID, event.RoomID, event.FilmID, event.LayoutVersionID, event.StartsAt, event.Status)
	return classifyError(err)
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
