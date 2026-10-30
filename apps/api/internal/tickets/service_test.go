// Behaviour tests for the ticket service that attaches the signed code to the
// projection and verifies a presented code against it, with no database.
//
// The rules pinned here are the ones a renderer and a door scanner depend on: the
// served detail always carries a code that names exactly the ticket it was served
// for, a ticket that cannot be signed is a failure instead of a ticket without a
// code, the same ticket signs to the same code twice, and verification answers
// "invalid token" for a code whose signed ticket, event or seats disagree with the
// stored ticket — the same answer whether or not the ticket exists.
package tickets_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// fakeReader is the projection reader the service is built over: it answers one
// configured detail and records how it was called.
type fakeReader struct {
	detail tickets.TicketDetail
	err    error

	calls    int
	ticketID string
}

func (f *fakeReader) Detail(_ context.Context, ticketID string) (tickets.TicketDetail, error) {
	f.calls++
	f.ticketID = ticketID
	if f.err != nil {
		return tickets.TicketDetail{}, f.err
	}
	return f.detail, nil
}

// serviceOver builds the service a deployment builds: the projection reader wrapped
// in the signer, so the code belongs to what is served.
func serviceOver(t *testing.T, reader tickets.Reader, key string) *tickets.Service {
	t.Helper()
	return tickets.NewService(reader, mustSigner(t, key))
}

// T5 (S4): the served detail carries the signed code of that ticket, and the code
// verifies to the ticket, event and seat the detail was read for.
func TestServiceAttachesTheSignedCodeToTheServedDetail(t *testing.T) {
	reader := &fakeReader{detail: qrDetail()}
	svc := serviceOver(t, reader, qrTestKey)

	detail, err := svc.Detail(context.Background(), qrDetail().TicketID)
	if err != nil {
		t.Fatalf("Detail() = unexpected error %v", err)
	}
	if reader.calls != 1 || reader.ticketID != qrDetail().TicketID {
		t.Fatalf("the reader was asked for %q in %d calls, want %q once",
			reader.ticketID, reader.calls, qrDetail().TicketID)
	}
	if detail.QRToken == "" {
		t.Fatal("the served detail carries no QR token")
	}

	// Everything except the code is the projection the reader answered: the code is
	// the only thing the service adds, and it changes no fact of the ticket.
	withoutToken := detail
	withoutToken.QRToken = ""
	want := qrDetail()
	if withoutToken.TicketID != want.TicketID || withoutToken.ReservationID != want.ReservationID ||
		withoutToken.Status != want.Status || !withoutToken.IssuedAt.Equal(want.IssuedAt) ||
		withoutToken.OrderAmountCents != want.OrderAmountCents ||
		withoutToken.Event != want.Event || len(withoutToken.Seats) != len(want.Seats) {
		t.Fatalf("the served detail changed the projection: %+v", withoutToken)
	}
	if withoutToken.Seats[0] != want.Seats[0] {
		t.Fatalf("served seat = %+v, want %+v", withoutToken.Seats[0], want.Seats[0])
	}

	// The code on the page and in the PDF therefore names this ticket: it verifies,
	// and the claims are the served ticket's own.
	claims, err := mustSigner(t, qrTestKey).Verify(detail.QRToken)
	if err != nil {
		t.Fatalf("Verify(served token) = unexpected error %v", err)
	}
	if claims.TicketID != detail.TicketID || claims.EventID != detail.Event.ID {
		t.Fatalf("claims = %+v, want ticket %q event %q", claims, detail.TicketID, detail.Event.ID)
	}
	if len(claims.SeatIDs) != 1 || claims.SeatIDs[0] != detail.Seats[0].EventSeatID {
		t.Fatalf("claim seats = %v, want [%d]", claims.SeatIDs, detail.Seats[0].EventSeatID)
	}
}

// T5 (S4): the web page and the PDF read the same served detail, so two reads of one
// ticket must sign to the same code — otherwise the two documents would show two
// different codes for one ticket.
func TestServiceSignsOneTicketIdenticallyOnEveryRead(t *testing.T) {
	svc := serviceOver(t, &fakeReader{detail: qrDetail()}, qrTestKey)

	first, err := svc.Detail(context.Background(), qrDetail().TicketID)
	if err != nil {
		t.Fatalf("Detail() = unexpected error %v", err)
	}
	second, err := svc.Detail(context.Background(), qrDetail().TicketID)
	if err != nil {
		t.Fatalf("Detail() = unexpected error %v", err)
	}
	if first.QRToken != second.QRToken {
		t.Fatalf("two reads signed %q and %q, want one code per ticket", first.QRToken, second.QRToken)
	}
}

// T5 (S4): a ticket that cannot be signed is a failure, never a ticket served
// without its code — a renderer would print a ticket nobody can scan.
func TestServiceRefusesToServeATicketItCannotSign(t *testing.T) {
	cases := []struct {
		name   string
		reader *fakeReader
		signer *tickets.Signer
	}{
		// The reader answers a detail that names no seat, so there is nothing to sign.
		{"a detail the signer refuses", &fakeReader{detail: func() tickets.TicketDetail {
			detail := qrDetail()
			detail.Seats = nil
			return detail
		}()}, mustSigner(t, qrTestKey)},
		// A signer that was never built from a key cannot mint codes.
		{"a signer without a key", &fakeReader{detail: qrDetail()}, new(tickets.Signer)},
		{"no signer at all", &fakeReader{detail: qrDetail()}, nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			svc := tickets.NewService(testCase.reader, testCase.signer)
			detail, err := svc.Detail(context.Background(), qrDetail().TicketID)
			if !errors.Is(err, tickets.ErrUnavailable) {
				t.Fatalf("Detail() = %+v, %v; want ErrUnavailable", detail, err)
			}
			if detail.QRToken != "" || detail.TicketID != "" {
				t.Fatalf("a ticket that cannot be signed was still served: %+v", detail)
			}
		})
	}

	// A service with no reader at all is unavailable, not an empty ticket.
	svc := tickets.NewService(nil, mustSigner(t, qrTestKey))
	if detail, err := svc.Detail(context.Background(), qrDetail().TicketID); !errors.Is(err, tickets.ErrUnavailable) {
		t.Fatalf("Detail() without a reader = %+v, %v; want ErrUnavailable", detail, err)
	}
}

// T5 (S4): the two read outcomes the route maps keep their meaning through the
// service, and no code is invented for a ticket that could not be read.
func TestServicePassesReadFailuresThrough(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"an unknown ticket", fmt.Errorf("%w: nope", tickets.ErrNotFound), tickets.ErrNotFound},
		{"an unreadable database", fmt.Errorf("%w: conn closed", tickets.ErrUnavailable), tickets.ErrUnavailable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			svc := serviceOver(t, &fakeReader{err: testCase.err}, qrTestKey)
			detail, err := svc.Detail(context.Background(), qrDetail().TicketID)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Detail() = %+v, %v; want %v", detail, err, testCase.want)
			}
			if detail.QRToken != "" {
				t.Fatalf("a failed read answered a token: %q", detail.QRToken)
			}
		})
	}
}

// T5 (S4): a presented code that verifies and names the stored ticket answers that
// ticket, so the door scans what the buyer was served.
func TestServiceVerifyAnswersTheStoredTicket(t *testing.T) {
	detail := qrDetail()
	token := signTicket(t, mustSigner(t, qrTestKey), detail)
	reader := &fakeReader{detail: detail}
	svc := serviceOver(t, reader, qrTestKey)

	verified, err := svc.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() = unexpected error %v", err)
	}
	if reader.calls != 1 || reader.ticketID != detail.TicketID {
		t.Fatalf("Verify() read %q in %d calls, want the signed ticket %q once",
			reader.ticketID, reader.calls, detail.TicketID)
	}
	if verified.TicketID != detail.TicketID || verified.Event.ID != detail.Event.ID {
		t.Fatalf("verified %+v, want the stored ticket", verified)
	}
	if !verified.Valid() {
		t.Fatal("a live issued ticket verified as not valid")
	}
}

// T5 (S4): a genuine code whose signed identity disagrees with the stored ticket is
// not proof of anything, so it verifies as invalid — never as the stored ticket and
// never as a different one. An unknown ticket answers identically, so probing cannot
// confirm that an identifier exists.
func TestServiceVerifyRejectsACodeThatDisagreesWithTheStoredTicket(t *testing.T) {
	cases := []struct {
		name  string
		store tickets.TicketDetail
		err   error
	}{
		{name: "another event", store: func() tickets.TicketDetail {
			detail := qrDetail()
			detail.Event.ID = "00000000-0000-4000-8000-0000000000cc"
			return detail
		}()},
		{name: "another seat", store: func() tickets.TicketDetail {
			detail := qrDetail()
			detail.Seats = []tickets.Seat{{EventSeatID: 22, RowLabel: "A", SeatNumber: "2"}}
			return detail
		}()},
		{name: "no seat", store: func() tickets.TicketDetail {
			detail := qrDetail()
			detail.Seats = nil
			return detail
		}()},
		{name: "an extra seat", store: func() tickets.TicketDetail {
			detail := qrDetail()
			detail.Seats = append(detail.Seats, tickets.Seat{EventSeatID: 22, RowLabel: "A", SeatNumber: "2"})
			return detail
		}()},
		{name: "an unknown ticket", err: fmt.Errorf("%w: gone", tickets.ErrNotFound)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			token := signTicket(t, mustSigner(t, qrTestKey), qrDetail())
			svc := serviceOver(t, &fakeReader{detail: testCase.store, err: testCase.err}, qrTestKey)

			verified, err := svc.Verify(context.Background(), token)
			if !errors.Is(err, tickets.ErrInvalidToken) {
				t.Fatalf("Verify() = %+v, %v; want ErrInvalidToken", verified, err)
			}
			// A refusal answers nothing about the stored ticket.
			if verified.TicketID != "" || len(verified.Seats) != 0 {
				t.Fatalf("a refused code answered the stored ticket: %+v", verified)
			}
			for _, leaked := range []string{qrTicketTicketID, qrTicketEventID, "22"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("the error names the stored value %q: %v", leaked, err)
				}
			}
		})
	}
}

// T5 (S4): a database that could not be read is not evidence of forgery, so the door
// is told the check is unavailable rather than that the code is bad.
func TestServiceVerifyKeepsAnUnreadableStoreUnavailable(t *testing.T) {
	token := signTicket(t, mustSigner(t, qrTestKey), qrDetail())
	svc := serviceOver(t, &fakeReader{err: fmt.Errorf("%w: conn closed", tickets.ErrUnavailable)}, qrTestKey)

	verified, err := svc.Verify(context.Background(), token)
	if !errors.Is(err, tickets.ErrUnavailable) {
		t.Fatalf("Verify() = %+v, %v; want ErrUnavailable", verified, err)
	}
	if errors.Is(err, tickets.ErrInvalidToken) {
		t.Fatalf("an unreadable store was reported as a forgery: %v", err)
	}
}

// T5 (S4): a service without a signer cannot check a code at all, which is a
// deployment failure and not a statement that the presented code is forged.
func TestServiceVerifyWithoutASignerIsUnavailable(t *testing.T) {
	token := signTicket(t, mustSigner(t, qrTestKey), qrDetail())
	svc := tickets.NewService(&fakeReader{detail: qrDetail()}, nil)

	verified, err := svc.Verify(context.Background(), token)
	if !errors.Is(err, tickets.ErrUnavailable) {
		t.Fatalf("Verify() = %+v, %v; want ErrUnavailable", verified, err)
	}
	if errors.Is(err, tickets.ErrInvalidToken) {
		t.Fatalf("a missing signer was reported as a forgery: %v", err)
	}
}

// T5 (S4): the reason a scanner is given is derived in the projection, next to
// Valid(), so the door cannot disagree with the web page about whether a ticket
// admits its holder — and it is machine readable, so "cancelled ticket" and
// "forgery" stay distinguishable.
func TestTicketDetailInvalidReason(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*tickets.TicketDetail)
		want   string
		valid  bool
	}{
		{
			name:   "an issued ticket of a live screening",
			mutate: func(*tickets.TicketDetail) {},
			want:   "",
			valid:  true,
		},
		{
			name:   "a voided ticket",
			mutate: func(detail *tickets.TicketDetail) { detail.Status = tickets.TicketStatusVoid },
			want:   tickets.InvalidReasonVoided,
		},
		{
			name: "a ticket of a cancelled screening",
			mutate: func(detail *tickets.TicketDetail) {
				detail.Event.Status = tickets.EventStatusCancelled
			},
			want: tickets.InvalidReasonEventCancelled,
		},
		{
			name: "a voided ticket of a cancelled screening",
			mutate: func(detail *tickets.TicketDetail) {
				detail.Status = tickets.TicketStatusVoid
				detail.Event.Status = tickets.EventStatusCancelled
			},
			want: tickets.InvalidReasonVoided,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			detail := qrDetail()
			testCase.mutate(&detail)
			if got := detail.InvalidReason(); got != testCase.want {
				t.Fatalf("InvalidReason() = %q, want %q", got, testCase.want)
			}
			// A reason exists exactly when the ticket is not valid: the scanner is
			// never told "invalid" without being told why, and never told a reason for
			// a ticket that admits its holder.
			if detail.Valid() != testCase.valid {
				t.Fatalf("Valid() = %t, want %t", detail.Valid(), testCase.valid)
			}
			if detail.Valid() != (testCase.want == "") {
				t.Fatalf("Valid() = %t and InvalidReason() = %q, want the two to agree",
					detail.Valid(), testCase.want)
			}
		})
	}
}
