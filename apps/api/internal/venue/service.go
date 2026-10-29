package venue

import (
	"context"
	"fmt"
	"time"
)

// Store persists venue aggregates. PostgreSQL is the durable source of truth;
// the interface keeps the service testable without a live database.
type Store interface {
	CreateCinema(ctx context.Context, cinema Cinema) error
	CreateRoom(ctx context.Context, room Room) error
	CreateLayout(ctx context.Context, layoutID, roomID string) (Layout, error)
	ReplaceRows(ctx context.Context, layoutID string, rows []Row) (Layout, error)
	PublishLayout(ctx context.Context, layoutID string) (Layout, error)
	CloneLayout(ctx context.Context, sourceID, targetID string) (Layout, error)
	GetLayout(ctx context.Context, layoutID string) (Layout, error)
	CreateFilm(ctx context.Context, film Film) error
	CreateEvent(ctx context.Context, event Event) error
	ListEventSeats(ctx context.Context, eventID string) ([]EventSeat, error)
}

// Service applies M1 validation before persisting venue aggregates.
type Service struct {
	store Store
}

// NewService builds a Service over the given store.
func NewService(store Store) *Service { return &Service{store: store} }

// CreateCinema validates and stores a venue.
func (s *Service) CreateCinema(ctx context.Context, name, timezone, address string) (Cinema, error) {
	name, err := validateText("name", name)
	if err != nil {
		return Cinema{}, err
	}
	timezone, err = validateText("timezone", timezone)
	if err != nil {
		return Cinema{}, err
	}
	address = trimOptional(address)
	if len(address) > maxNameLength {
		return Cinema{}, NewInvalidError("address", "must be at most 120 characters")
	}
	cinema := Cinema{ID: NewID(), Name: name, Timezone: timezone, Address: address}
	if err := s.store.CreateCinema(ctx, cinema); err != nil {
		return Cinema{}, err
	}
	return cinema, nil
}

// CreateRoom validates and stores a room for an existing cinema.
func (s *Service) CreateRoom(ctx context.Context, cinemaID, name string) (Room, error) {
	if err := requireID("cinemaId", cinemaID); err != nil {
		return Room{}, err
	}
	name, err := validateText("name", name)
	if err != nil {
		return Room{}, err
	}
	room := Room{ID: NewID(), CinemaID: cinemaID, Name: name}
	if err := s.store.CreateRoom(ctx, room); err != nil {
		return Room{}, err
	}
	return room, nil
}

// CreateLayout creates the next draft layout version for a room.
func (s *Service) CreateLayout(ctx context.Context, roomID string) (Layout, error) {
	if err := requireID("roomId", roomID); err != nil {
		return Layout{}, err
	}
	return s.store.CreateLayout(ctx, NewID(), roomID)
}

// SetRows replaces the geometry of a draft layout.
func (s *Service) SetRows(ctx context.Context, layoutID string, rows []Row) (Layout, error) {
	if err := requireID("layoutId", layoutID); err != nil {
		return Layout{}, err
	}
	rows, err := ValidateRows(rows)
	if err != nil {
		return Layout{}, err
	}
	return s.store.ReplaceRows(ctx, layoutID, rows)
}

// PublishLayout freezes a draft layout, which then becomes immutable.
func (s *Service) PublishLayout(ctx context.Context, layoutID string) (Layout, error) {
	if err := requireID("layoutId", layoutID); err != nil {
		return Layout{}, err
	}
	current, err := s.store.GetLayout(ctx, layoutID)
	if err != nil {
		return Layout{}, err
	}
	if current.Status == LayoutPublished {
		return Layout{}, fmt.Errorf("%w: layout %s is already published", ErrConflict, layoutID)
	}
	if EnabledSeatCount(current.Rows) == 0 {
		return Layout{}, NewInvalidError("layout", "must contain at least one enabled seat before publishing")
	}
	return s.store.PublishLayout(ctx, layoutID)
}

// CloneLayout copies a published layout into a new editable draft version.
func (s *Service) CloneLayout(ctx context.Context, layoutID string) (Layout, error) {
	if err := requireID("layoutId", layoutID); err != nil {
		return Layout{}, err
	}
	current, err := s.store.GetLayout(ctx, layoutID)
	if err != nil {
		return Layout{}, err
	}
	if current.Status != LayoutPublished {
		return Layout{}, NewInvalidError("layout", "only a published layout can be versioned")
	}
	return s.store.CloneLayout(ctx, layoutID, NewID())
}

// GetLayout returns one layout version with its rows and seats.
func (s *Service) GetLayout(ctx context.Context, layoutID string) (Layout, error) {
	if err := requireID("layoutId", layoutID); err != nil {
		return Layout{}, err
	}
	return s.store.GetLayout(ctx, layoutID)
}

// CreateFilm validates and stores a catalog entry.
func (s *Service) CreateFilm(ctx context.Context, title string, runtimeMinutes int, rating string) (Film, error) {
	title, err := validateText("title", title)
	if err != nil {
		return Film{}, err
	}
	if runtimeMinutes <= 0 || runtimeMinutes > 600 {
		return Film{}, NewInvalidError("runtimeMinutes", "must be between 1 and 600")
	}
	rating, err = validateText("rating", rating)
	if err != nil {
		return Film{}, err
	}
	film := Film{ID: NewID(), Title: title, RuntimeMinutes: runtimeMinutes, Rating: rating}
	if err := s.store.CreateFilm(ctx, film); err != nil {
		return Film{}, err
	}
	return film, nil
}

// CreateEvent schedules a screening; PostgreSQL materializes exactly one
// event seat per enabled layout seat inside the same transaction.
func (s *Service) CreateEvent(ctx context.Context, roomID, filmID, layoutVersionID string, startsAt time.Time) (Event, error) {
	for field, value := range map[string]string{"roomId": roomID, "filmId": filmID, "layoutVersionId": layoutVersionID} {
		if err := requireID(field, value); err != nil {
			return Event{}, err
		}
	}
	if startsAt.IsZero() {
		return Event{}, NewInvalidError("startsAt", "must be a valid timestamp")
	}
	event := Event{
		ID:              NewID(),
		RoomID:          roomID,
		FilmID:          filmID,
		LayoutVersionID: layoutVersionID,
		StartsAt:        startsAt.UTC(),
		Status:          "draft",
	}
	if err := s.store.CreateEvent(ctx, event); err != nil {
		return Event{}, err
	}
	return event, nil
}

// ListEventSeats returns the materialized seats of one event.
func (s *Service) ListEventSeats(ctx context.Context, eventID string) ([]EventSeat, error) {
	if err := requireID("eventId", eventID); err != nil {
		return nil, err
	}
	return s.store.ListEventSeats(ctx, eventID)
}

func requireID(field, value string) error {
	if trimOptional(value) == "" {
		return NewInvalidError(field, "must not be empty")
	}
	return nil
}
