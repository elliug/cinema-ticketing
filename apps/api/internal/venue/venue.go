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
	"regexp"
	"strings"
	"time"

	"cinema.local/ticketing/apps/api/internal/tickets"
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
	// PriceCents is the price of one seat, in minor units, from
	// cinema.events.price_cents. Zero is a legal free screening.
	PriceCents int64 `json:"priceCents"`

	// FilmTitle, RoomName, CinemaName and CinemaTimezone are the names of what the
	// screening is stored against, and the zone that the venue keeps its own clock in. The
	// administrative reads project them in the same statement as the columns above, so a
	// list of screenings is one query and never an n+1 of films, rooms and cinemas. A write
	// answers the stored columns only and leaves these empty: a name nobody read is not a
	// name to answer, and the zone is what [Event.LocalStartsAt] would render from.
	FilmTitle      string `json:"filmTitle,omitempty"`
	RoomName       string `json:"roomName,omitempty"`
	CinemaName     string `json:"cinemaName,omitempty"`
	CinemaTimezone string `json:"cinemaTimezone,omitempty"`
}

// EventSeat is the sale state of one layout seat for one event.
type EventSeat struct {
	ID           int64
	EventID      string
	LayoutSeatID string
	State        string
}

// Event statuses. The values are cinema.events.status's own CHECK constraint, spelled in
// db/migrations/0002_venue_layout.up.sql, and are carried verbatim on the wire.
const (
	// EventDraft is a stored screening nobody has offered yet: it is not for sale.
	EventDraft = "draft"
	// EventPublished is a recording state this milestone does not act on.
	EventPublished = "published"
	// EventOnSale is the one status the public catalogue answers, and only while the start
	// is still ahead.
	EventOnSale = "on_sale"
	// EventStarted and EventCompleted are recording states this milestone does not act on.
	EventStarted   = "started"
	EventCompleted = "completed"
	// EventCancelled is a screening the ticket surface reports as cancelled.
	EventCancelled = "cancelled"
)

// EventStatuses lists every value cinema.events.status accepts, in the schema's order. It
// is the list the administration validates a status against and the list its refusal
// spells, so the two cannot disagree.
var EventStatuses = []string{EventDraft, EventPublished, EventOnSale, EventStarted, EventCompleted, EventCancelled}

// EventUpdate is the set of screening columns the administration may change in one
// request. A nil field was not sent and leaves its stored column untouched; a pointer to a
// value was sent, including an explicit zero price, so "not sent" never means "set it to
// zero" or "set it to nothing".
type EventUpdate struct {
	PriceCents *int64
	Status     *string
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

// MaxPriceCents is the largest value cinema.events.price_cents can hold: the column is
// a PostgreSQL integer, and a price is never negative.
const MaxPriceCents int64 = 2147483647

// eventIDPattern is the canonical UUID spelling. A stored event is addressed by one of
// these, so a value that cannot be one names no event and is rejected before SQL.
var eventIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidateEventID rejects an event identifier that cannot name a stored event. It is
// shared by the administrative read and the price and status write so both answer the same
// rejection for the same bad value instead of one reaching SQL and one not.
func ValidateEventID(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return NewInvalidError("eventId", "must not be empty")
	}
	if !eventIDPattern.MatchString(trimmed) {
		return NewInvalidError("eventId", "must be a valid identifier")
	}
	return nil
}

// ValidatePriceCents enforces the column's range before any SQL. Zero is accepted: a
// free screening is a product decision, not an invalid price, and the checkout decides
// whether it can be sold.
func ValidatePriceCents(priceCents int64) error {
	if priceCents < 0 {
		return NewInvalidError("priceCents", "must be zero or greater")
	}
	if priceCents > MaxPriceCents {
		return NewInvalidError("priceCents", "must be at most 2147483647")
	}
	return nil
}

// ValidateEventStatus rejects a status cinema.events.status would refuse, naming the field
// and spelling the values the column allows. The comparison is exact: a status is one of
// the schema's own spellings and never a value that looks like one.
func ValidateEventStatus(status string) error {
	for _, allowed := range EventStatuses {
		if status == allowed {
			return nil
		}
	}
	return NewInvalidError("status", "must be one of "+strings.Join(EventStatuses, ", "))
}

// ValidateEventFields validates the two writable screening fields a request carries, and
// nothing else: a nil field was not sent and has nothing to validate. Zero is a legal price
// here, so "not sent" and "free" stay different answers.
//
// This is the single rule for what a legal price and a legal status are. The update applies
// it to a body that has to name at least one field and the create applies it to a body that
// may name neither, so the two routes cannot refuse the same value in two different words.
func ValidateEventFields(priceCents *int64, status *string) error {
	if priceCents != nil {
		if err := ValidatePriceCents(*priceCents); err != nil {
			return err
		}
	}
	if status != nil {
		if err := ValidateEventStatus(*status); err != nil {
			return err
		}
	}
	return nil
}

// ValidateEventUpdate rejects an update that would change nothing and validates every
// field it does carry. It is exported so the HTTP layer refuses a body with neither field
// in the same words the service uses rather than in a second copy of the rule. The two
// fields themselves are validated by ValidateEventFields, the rule the create shares.
func ValidateEventUpdate(update EventUpdate) error {
	if update.PriceCents == nil && update.Status == nil {
		return NewInvalidError("", "nothing to change: provide priceCents or status")
	}
	return ValidateEventFields(update.PriceCents, update.Status)
}

// LocalStartsAt renders the screening time in the venue's own timezone, with an explicit
// offset (RFC 3339), and it is added beside the stored instant rather than in place of it:
// the instant is what storage and every comparison use, and this is what a person reads.
//
// The rule is not reimplemented here. The stored instant and the venue zone are handed to
// [tickets.EventDetail], whose [tickets.EventDetail.LocalStartsAt] the ticket detail route
// and the PDF already use, so the administrative screen and the ticket cannot disagree about
// the hour a screening starts. A venue whose stored zone cannot be resolved (a row the
// timezone backfill has not repaired) is an error rather than a silent UTC answer: a screen
// that shows a wrong hour is worse than one that shows none.
func (e Event) LocalStartsAt() (string, error) {
	return tickets.EventDetail{ID: e.ID, StartsAt: e.StartsAt, Timezone: e.CinemaTimezone}.LocalStartsAt()
}

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
