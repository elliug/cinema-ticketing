// One page of the catalogue: how a caller asks for it, and where the next one starts.
//
// The catalogue is paged by a **keyset**, not by an offset, and the reason is the nature
// of the list: a catalogue is precisely a list that changes under its readers. An
// operator puts a screening on sale, cancels one, or publishes a new week while a buyer
// is walking the pages. With `OFFSET`, inserting one row before the page a client is
// about to ask for pushes every later row down by one, so the client's next page repeats
// the entry it already saw; removing one row pulls the list up, so the next page skips an
// entry the client never saw. The client cannot tell either from a correct answer — it
// receives well-formed entries, one duplicated or one missing, and no error.
//
// A keyset asks for `(starts_at, id) > (…)`, which is a position in the data rather than a
// count of rows before it, so rows inserted or removed elsewhere in the catalogue cannot
// move it. That pair is exactly the order the catalogue already fixes — screening time
// ascending, with the identifier as the tiebreaker — so paging needs no second ordering
// and no column the projection does not already answer.
//
// The cursor is an opaque encoding of that pair: a client hands back a string, and this
// build decides what it means, so the encoding can change (a version prefix refuses a
// cursor from a previous scheme by name) without any client learning the format.

package catalog

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// DefaultEventLimit is how many screenings one page answers when the caller names no
	// limit: a fortnight of a single venue's programming, which is what the catalogue
	// screen shows, and small enough that one page is one round trip a client can render
	// without its own pagination.
	DefaultEventLimit = 20

	// MaxEventLimit is the largest page the catalogue answers. The read joins a seat
	// aggregate per screening, so the page size is the work one request may do; 100
	// screenings is more than any screen renders at once, and a caller that asks for more
	// is refused rather than quietly given 100.
	MaxEventLimit = 100

	// cursorVersion prefixes every encoded cursor. It is not a secret: it is the reason a
	// cursor issued by a previous encoding is refused with a message naming the field
	// instead of being decoded into a position that means something else.
	cursorVersion = "v1"
)

// ErrInvalidQuery reports that a page request cannot be answered as asked. It is the
// transport's cue to answer 400 and name the field, and it is deliberately distinct from
// [ErrUnavailable]: a request this catalogue cannot mean will not become valid by
// retrying it.
var ErrInvalidQuery = errors.New("catalog: the list query is invalid")

// QueryError names the request field this catalogue could not use. The field is the
// camelCase name the caller sent, so a refusal can be read without guessing which of the
// four parameters was wrong.
type QueryError struct {
	// Field is the query parameter that was refused: "limit", "cursor", "from" or "to".
	Field string
	// Reason says what the catalogue needed it to be, in words a client can act on and
	// without a value the caller did not send.
	Reason string
}

// Error renders the refusal as the field followed by what was wrong with it, which is the
// sentence a problem document's detail carries.
func (e *QueryError) Error() string { return e.Field + " " + e.Reason }

// Is makes every QueryError match [ErrInvalidQuery], so a transport classifies the
// refusal without matching strings.
func (e *QueryError) Is(target error) bool { return target == ErrInvalidQuery }

// Cursor is where a page continues from: the `(starts_at, id)` pair of the last screening
// the caller has already seen, which is the pair the catalogue's order is fixed by.
type Cursor struct {
	// StartsAt is that screening's stored start instant.
	StartsAt time.Time
	// ID is that screening's identifier, the tiebreaker between two screenings that start
	// at the same instant.
	ID string
}

// String encodes the cursor for a client to hand back unchanged.
//
// The encoding is intentionally not readable: base64url of a versioned, colon-separated
// pair, in UTC so the same position has one spelling. A client that asked which screening
// a page stopped at already had that screening in the page it just rendered.
func (c Cursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(
		cursorVersion + ":" + c.StartsAt.UTC().Format(time.RFC3339Nano) + ":" + c.ID))
}

// ParseCursor decodes a cursor a client handed back.
//
// Anything this build did not issue is refused by naming the field: a malformed string, a
// cursor from a previous encoding, an instant that is not an instant, or an identifier
// that could never be a screening. The refusal matters more than it looks: an empty page
// is a well-formed answer that says "no more screenings", so answering a cursor this
// build cannot read with an empty page would tell a client the catalogue ended when it
// had only lost its place.
func ParseCursor(text string) (Cursor, error) {
	refuse := func(reason string) (Cursor, error) {
		return Cursor{}, &QueryError{Field: "cursor", Reason: reason}
	}
	if strings.TrimSpace(text) == "" {
		return refuse("is empty; omit it to read the first page")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return refuse("is not a cursor this catalogue issued; read the first page again")
	}
	// The version is the first field; the instant and the identifier are the last two,
	// split at the final colon because an RFC 3339 instant carries colons of its own and the
	// identifier is the only field that cannot.
	version, rest, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return refuse("is not a cursor this catalogue issued; read the first page again")
	}
	if version != cursorVersion {
		return refuse("was issued by a scheme this build does not read; read the first page again")
	}
	split := strings.LastIndex(rest, ":")
	if split < 0 {
		return refuse("is not a cursor this catalogue issued; read the first page again")
	}
	startsAt, err := time.Parse(time.RFC3339Nano, rest[:split])
	if err != nil {
		return refuse("does not carry a screening time; read the first page again")
	}
	id, err := canonicalScreeningID(rest[split+1:])
	if err != nil {
		return refuse("does not name a screening; read the first page again")
	}
	return Cursor{StartsAt: startsAt.UTC(), ID: id}, nil
}

// ListQuery is one page request: how many screenings, from where, and within which bounds.
//
// The zero value is deliberately not "everything": a request that names no limit is
// refused by [ListQuery.Normalize], so no caller can turn a forgotten field into a read of
// the whole programme.
type ListQuery struct {
	// Limit is how many screenings the page answers. It must be between 1 and
	// [MaxEventLimit]; the transport applies [DefaultEventLimit] when the caller names no
	// limit, so "not named" and "named out of range" stay different statements.
	Limit int
	// After is the position to continue from, or nil for the first page.
	After *Cursor
	// From and To bound the screening start instant, both inclusive, both optional. They
	// narrow which screenings are in the catalogue at all; they do not move the cursor.
	From *time.Time
	To   *time.Time
}

// Normalize validates the request and answers it in the one spelling the read uses: bounds
// in UTC, and a cursor whose pair has been validated.
//
// It refuses rather than repairs: a limit outside the documented range is not clamped to
// the nearest usable one, because a client that asked for 1000 screenings and silently
// received 100 would conclude the catalogue holds no more than that.
func (q ListQuery) Normalize() (ListQuery, error) {
	if q.Limit < 1 || q.Limit > MaxEventLimit {
		return ListQuery{}, &QueryError{
			Field:  "limit",
			Reason: fmt.Sprintf("must be between 1 and %d", MaxEventLimit),
		}
	}
	if q.From != nil {
		from := q.From.UTC()
		q.From = &from
	}
	if q.To != nil {
		to := q.To.UTC()
		q.To = &to
	}
	if q.From != nil && q.To != nil && q.To.Before(*q.From) {
		return ListQuery{}, &QueryError{Field: "to", Reason: "must not precede from"}
	}
	if q.After != nil {
		after := *q.After
		id, err := canonicalScreeningID(after.ID)
		if err != nil {
			return ListQuery{}, &QueryError{
				Field:  "cursor",
				Reason: "does not name a screening; read the first page again",
			}
		}
		after.ID = id
		after.StartsAt = after.StartsAt.UTC()
		q.After = &after
	}
	return q, nil
}

// Page is one page of the catalogue: the entries this page answers, and where the next
// page begins.
type Page struct {
	// Screenings are the entries this page answers, in the catalogue's order. It is never
	// nil, so a transport can answer an empty array without a nil check.
	Screenings []Screening
	// Next is the position the next page continues from, or nil when this page is the
	// last one. It is decided by the read, which is the only place that knows whether a
	// further row exists: the transport neither counts entries nor guesses.
	Next *Cursor
}

// canonicalScreeningID answers the canonical spelling of an identifier that could be a
// screening, and refuses anything else.
//
// The projection answers `cinema.events.id` as text, and the keyset compares that text, so
// the spelling a cursor carries has to be the one the database answers: an uppercase or
// brace-wrapped spelling of the same identifier would compare as a different position.
func canonicalScreeningID(text string) (string, error) {
	var id pgtype.UUID
	if err := id.Scan(text); err != nil || !id.Valid {
		return "", fmt.Errorf("catalog: %q is not a screening identifier", text)
	}
	return id.String(), nil
}
