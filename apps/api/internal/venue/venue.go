// Package venue implements M1 venue and layout domain behavior: cinemas,
// rooms, immutable versioned layouts, films, and events with materialized
// event seats.
//
// Correctness invariants are enforced in PostgreSQL; this package supplies
// request validation, identifiers, and the application-facing service.
package venue

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SeatKind enumerates the supported physical seat kinds.
type SeatKind string

// Supported seat kinds.
const (
	SeatStandard   SeatKind = "standard"
	SeatAccessible SeatKind = "accessible"
	SeatCompanion  SeatKind = "companion"
)

// Layout statuses.
const (
	LayoutDraft     = "draft"
	LayoutPublished = "published"
)

// Cinema is a venue with its own locale.
type Cinema struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	Address  string `json:"address,omitempty"`
}

// Room is a physical screening room belonging to one cinema.
type Room struct {
	ID       string `json:"id"`
	CinemaID string `json:"cinemaId"`
	Name     string `json:"name"`
}

// Seat is one physical seat position inside a layout row.
type Seat struct {
	ID       string   `json:"id"`
	Number   string   `json:"number"`
	Position int      `json:"position"`
	Kind     SeatKind `json:"kind"`
	Enabled  bool     `json:"enabled"`
}

// Row is an ordered layout row with its own seat collection.
type Row struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Position int    `json:"position"`
	Seats    []Seat `json:"seats"`
}

// Layout is one version of a room geometry.
type Layout struct {
	ID      string `json:"id"`
	RoomID  string `json:"roomId"`
	Version int    `json:"version"`
	Status  string `json:"status"`
	Rows    []Row  `json:"rows"`
}

// Film is the minimal catalog entry required to schedule an event.
type Film struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	RuntimeMinutes int    `json:"runtimeMinutes"`
	Rating         string `json:"rating"`
}

// Event is one scheduled screening.
type Event struct {
	ID              string    `json:"id"`
	RoomID          string    `json:"roomId"`
	FilmID          string    `json:"filmId"`
	LayoutVersionID string    `json:"layoutVersionId"`
	StartsAt        time.Time `json:"startsAt"`
	Status          string    `json:"status"`
}

// EventSeat is the sale state of one layout seat for one event.
type EventSeat struct {
	ID           int64
	EventID      string
	LayoutSeatID string
	State        string
}

// InvalidError reports caller input a domain rule rejected.
type InvalidError struct {
	Field   string
	Message string
}

func (e *InvalidError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// NewInvalidError builds an InvalidError.
func NewInvalidError(field, message string) error {
	return &InvalidError{Field: field, Message: message}
}

// ErrConflict reports a rejected duplicate or a state that forbids the change.
var ErrConflict = errors.New("conflict")

// ErrNotFound reports a missing aggregate.
var ErrNotFound = errors.New("not found")

const maxNameLength = 120

// validateText rejects empty, over-long, or padded-only text.
func validateText(field, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", NewInvalidError(field, "must not be empty")
	}
	if len(trimmed) > maxNameLength {
		return "", NewInvalidError(field, "must be at most 120 characters")
	}
	return trimmed, nil
}

// NewID returns a random version-4 UUID string.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidateRow checks one row and its seats; each row may hold a different
// seat count, but numbers and positions must be unique inside the row.
func ValidateRow(row Row) (Row, error) {
	label, err := validateText("row.label", row.Label)
	if err != nil {
		return Row{}, err
	}
	if row.Position <= 0 {
		return Row{}, NewInvalidError("row.position", "must be greater than zero")
	}
	if len(row.Seats) == 0 {
		return Row{}, NewInvalidError("row.seats", "must contain at least one seat")
	}
	numbers := make(map[string]struct{}, len(row.Seats))
	positions := make(map[int]struct{}, len(row.Seats))
	seats := make([]Seat, 0, len(row.Seats))
	for _, seat := range row.Seats {
		number, err := validateText("seat.number", seat.Number)
		if err != nil {
			return Row{}, err
		}
		if seat.Position <= 0 {
			return Row{}, NewInvalidError("seat.position", "must be greater than zero")
		}
		switch seat.Kind {
		case SeatStandard, SeatAccessible, SeatCompanion:
		default:
			return Row{}, NewInvalidError("seat.kind", "must be standard, accessible, or companion")
		}
		if _, exists := numbers[number]; exists {
			return Row{}, NewInvalidError("seat.number", "must be unique within its row")
		}
		if _, exists := positions[seat.Position]; exists {
			return Row{}, NewInvalidError("seat.position", "must be unique within its row")
		}
		numbers[number] = struct{}{}
		positions[seat.Position] = struct{}{}
		id := seat.ID
		if id == "" {
			id = NewID()
		}
		seats = append(seats, Seat{ID: id, Number: number, Position: seat.Position, Kind: seat.Kind, Enabled: seat.Enabled})
	}
	return Row{ID: row.ID, Label: label, Position: row.Position, Seats: seats}, nil
}

// ValidateRows checks every row and the row-level uniqueness rules.
func ValidateRows(rows []Row) ([]Row, error) {
	if len(rows) == 0 {
		return nil, NewInvalidError("rows", "must contain at least one row")
	}
	labels := make(map[string]struct{}, len(rows))
	positions := make(map[int]struct{}, len(rows))
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		validated, err := ValidateRow(row)
		if err != nil {
			return nil, err
		}
		if _, exists := labels[validated.Label]; exists {
			return nil, NewInvalidError("row.label", "must be unique within its layout")
		}
		if _, exists := positions[validated.Position]; exists {
			return nil, NewInvalidError("row.position", "must be unique within its layout")
		}
		labels[validated.Label] = struct{}{}
		positions[validated.Position] = struct{}{}
		out = append(out, validated)
	}
	return out, nil
}

// EnabledSeatCount counts seats that can be sold.
func EnabledSeatCount(rows []Row) int {
	total := 0
	for _, row := range rows {
		for _, seat := range row.Seats {
			if seat.Enabled {
				total++
			}
		}
	}
	return total
}

// trimOptional normalizes optional text fields.
func trimOptional(value string) string { return strings.TrimSpace(value) }
