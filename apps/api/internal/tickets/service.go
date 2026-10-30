// The ticket service: the projection plus its signed code, in the one place both
// readers get it from.
//
// The web confirmation page (T7) and the PDF (T6) must show the same QR code for
// the same ticket, so the code cannot belong to either renderer. It belongs to what
// the surface is given: the served detail. This service wraps the projection reader,
// reads the ticket and signs it, and the signed token travels on the projection
// itself ([TicketDetail.QRToken]). A renderer prints what it was served and never
// mints a code of its own, so the two media cannot drift — and the PDF of a ticket
// is the same code as the web page of that ticket by construction, not by
// convention.
//
// The package's completeness rule survives the wrapping: a detail that cannot be
// signed is [ErrUnavailable], never a ticket served without its code. A ticket whose
// code is missing is a ticket nobody can scan, and a renderer would print it.

package tickets

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Service reads the ticket detail projection and attaches its signed QR code. It is
// the reader the public route is mounted with and the source T6 and T7 render.
type Service struct {
	reader Reader
	signer *Signer
}

// NewService wraps a projection reader and a signer. Both are required: without a
// reader there is no ticket, and without a signer there is no code, so a nil either
// way answers [ErrUnavailable] instead of a ticket a surface cannot use.
func NewService(reader Reader, signer *Signer) *Service {
	return &Service{reader: reader, signer: signer}
}

// Detail answers the projection of one ticket with its signed code.
//
// The read outcomes keep their meaning: an unknown ticket is [ErrNotFound], an
// unreadable one is [ErrUnavailable]. A ticket that was read but cannot be signed is
// also [ErrUnavailable], because the projection is either complete — code
// included — or absent.
func (s *Service) Detail(ctx context.Context, ticketID string) (TicketDetail, error) {
	detail, err := s.read(ctx, ticketID)
	if err != nil {
		return TicketDetail{}, err
	}
	token, err := s.signer.Sign(detail)
	if err != nil {
		return TicketDetail{}, fmt.Errorf("%w: the ticket code could not be signed: %w", ErrUnavailable, err)
	}
	detail.QRToken = token
	return detail, nil
}

// Verify checks a presented code and answers the stored ticket it names, so the door
// scans the ticket the buyer was served and never a value from the client.
//
// Every reason a code is not a ticket of ours is [ErrInvalidToken]: the signature
// does not verify, or it verifies but names a ticket this database does not know, or
// the signed ticket, event or seats disagree with the stored ticket. That is one
// answer on purpose. A signature over data that no longer matches the sale is not
// proof of anything, and a caller probing identifiers must not be able to tell "this
// ticket exists" from "it does not" — the refusal names no stored value.
//
// An unreadable database is [ErrUnavailable], not a forgery: a store that could not
// be asked is not evidence about the code.
func (s *Service) Verify(ctx context.Context, token string) (TicketDetail, error) {
	if s == nil || s.signer == nil {
		return TicketDetail{}, fmt.Errorf("%w: the ticket signer is not configured", ErrUnavailable)
	}
	claims, err := s.signer.Verify(token)
	if err != nil {
		if errors.Is(err, ErrInvalidKey) {
			// A signer without a key cannot check anything, which is our failure rather
			// than a statement about the presented code.
			return TicketDetail{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return TicketDetail{}, err
	}

	detail, err := s.read(ctx, claims.TicketID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// The code is genuine but names no ticket this database knows: it is not a
			// ticket, and it says nothing about which identifiers do exist.
			return TicketDetail{}, invalidToken("the token names no stored ticket")
		}
		return TicketDetail{}, err
	}

	if err := matchClaims(claims, detail); err != nil {
		return TicketDetail{}, err
	}
	return detail, nil
}

// read asks the projection reader for one ticket. A service without a reader is
// unavailable rather than empty, mirroring the store's own rule.
func (s *Service) read(ctx context.Context, ticketID string) (TicketDetail, error) {
	if s == nil || s.reader == nil {
		return TicketDetail{}, fmt.Errorf("%w: the ticket reader is not configured", ErrUnavailable)
	}
	return s.reader.Detail(ctx, ticketID)
}

// matchClaims requires the signed identity and the stored ticket to describe the
// same sale: the same ticket, the same screening and the same seats. A code that
// disagrees is refused with [ErrInvalidToken] and the refusal names no stored value.
func matchClaims(claims Claims, detail TicketDetail) error {
	if claims.TicketID != detail.TicketID {
		return invalidToken("the signed ticket is not the stored ticket")
	}
	if claims.EventID != detail.Event.ID {
		return invalidToken("the signed event is not the stored event")
	}

	stored := make([]int64, 0, len(detail.Seats))
	for _, seat := range detail.Seats {
		stored = append(stored, seat.EventSeatID)
	}
	// The projection orders seats by row and position and the claims are ascending by
	// identifier, so the comparison sorts the stored list into the claim order rather
	// than assuming the two orders agree.
	sort.Slice(stored, func(i, j int) bool { return stored[i] < stored[j] })
	if len(stored) != len(claims.SeatIDs) {
		return invalidToken("the signed seats are not the stored seats")
	}
	for index := range stored {
		if stored[index] != claims.SeatIDs[index] {
			return invalidToken("the signed seats are not the stored seats")
		}
	}
	return nil
}

// compile-time proof that the service satisfies the projection contract the route,
// the PDF renderer and the verification endpoint share.
var _ Reader = (*Service)(nil)
