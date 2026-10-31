// Behaviour tests for the door: admitting a ticket exactly once, and saying what
// happened. The first three cases need no database. The last one is PostgreSQL-gated on
// M3_CHECKOUT_DATABASE_URL, because what only a real database can prove is what this
// primitive exists for: two doors presenting one code at the same instant, exactly one
// write, and the loser told the first redemption's instant rather than an error.
package tickets_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// T7a (S1, S2): an identifier that cannot be a ticket id never reaches SQL, so a
// redemption attempt on a wrong guess cannot create a row, cannot write anything
// and cannot be told apart from a ticket that never existed.
func TestRedeemRefusesAnIdentifierThatCannotBeATicketWithoutAQuery(t *testing.T) {
	store := &tickets.PostgresStore{}
	for _, garbage := range []string{"", "not-a-ticket-id", "1", "' OR 1=1 --"} {
		redemption, err := store.Redeem(context.Background(), garbage, "door-north")
		if !errors.Is(err, tickets.ErrNotFound) {
			t.Fatalf("Redeem(%q) = %v, want ErrNotFound", garbage, err)
		}
		// A refusal answers nothing about a redemption: no outcome, no instant, no
		// label, so a caller that ignores the error cannot print one.
		if redemption != (tickets.Redemption{}) {
			t.Fatalf("Redeem(%q) answered %+v, want an empty redemption", garbage, redemption)
		}
	}
}

// T7a (S1): a store that cannot be asked is unavailable, not a refused code. A
// database failure says nothing about the ticket the cashier is holding.
func TestRedeemWithoutAStoreIsUnavailable(t *testing.T) {
	store := &tickets.PostgresStore{}
	_, err := store.Redeem(context.Background(), "00000000-0000-0000-0000-0000000000ff", "door-north")
	if !errors.Is(err, tickets.ErrUnavailable) {
		t.Fatalf("Redeem() without a pool = %v, want ErrUnavailable", err)
	}
}

// T7a (S1): the detail projection answers the door's question from what it already
// served, so a later screen needs no second read to know a ticket was used — and
// the two answers that existed before keep their meaning for a used ticket, because
// the sale is unchanged by the admission.
func TestTicketDetailSaysWhenTheDoorAdmittedIt(t *testing.T) {
	detail := issuedTicket(publishedEvent("Europe/Madrid", "on_sale"))
	if detail.Redeemed() {
		t.Fatal("a ticket that was never admitted reports itself redeemed")
	}
	if !detail.Admissible() {
		t.Fatal("an issued ticket the door never saw is not admissible")
	}

	instant := screeningInstant
	detail.RedeemedAt = &instant
	detail.RedeemedBy = "door-north"
	if !detail.Redeemed() || detail.Admissible() {
		t.Fatalf("a redeemed ticket reports redeemed = %t admissible = %t, want true and false",
			detail.Redeemed(), detail.Admissible())
	}
	// Preserve: redemption is not a sale change. The ticket is still an issued ticket
	// and still a valid one, and the reason a door refuses is still about the sale, so
	// the printed PDF and the web page cannot start disagreeing about it.
	if detail.Status != tickets.TicketStatusIssued || !detail.Valid() {
		t.Fatalf("a redeemed ticket lost its sale: status %q valid %t", detail.Status, detail.Valid())
	}
	if reason := detail.InvalidReason(); reason != "" {
		t.Fatalf("InvalidReason() of a redeemed ticket = %q, want the sale's own answer", reason)
	}

	// A voided ticket and a ticket of a cancelled screening stay inadmissible after a
	// redemption, with the reason the projection already knew.
	voided := detail
	voided.Status = tickets.TicketStatusVoid
	cancelled := detail
	cancelled.Event.Status = tickets.EventStatusCancelled
	if voided.Admissible() || cancelled.Admissible() {
		t.Fatal("a voided or cancelled ticket reports itself admissible")
	}
	if voided.InvalidReason() != tickets.InvalidReasonVoided ||
		cancelled.InvalidReason() != tickets.InvalidReasonEventCancelled {
		t.Fatalf("reasons = %q, %q; want voided and event_cancelled",
			voided.InvalidReason(), cancelled.InvalidReason())
	}
}

// T7a (S1, S2): the race, against a real database. Two doors present the same code
// at the same instant; exactly one redemption writes, the other is told the first
// one's instant instead of an error, and a third presentation after both answers
// the same instant. The rest of the scenario states the other outcomes this
// primitive owes its caller: a ticket that does not exist, one that was withdrawn,
// one whose screening was cancelled, and a redemption with no label.
func TestRedeemAdmitsOneOfTwoRacingDoorsAndAnswersTheFirstInstant(t *testing.T) {
	ctx := context.Background()
	pool, url := ticketPool(t, ctx)
	seats := seedTicketFixture(t, ctx, pool)
	t.Cleanup(func() {
		cleanupTicketFixture(pool)
		pool.Close()
	})

	store, err := tickets.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the ticket store: %v", err)
	}
	defer store.Close()
	if present, err := store.TicketDetailSchemaPresent(ctx); err != nil || !present {
		t.Fatalf("TicketDetailSchemaPresent() = %t, %v", present, err)
	}

	checkoutStore, err := checkout.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("open the checkout store: %v", err)
	}
	defer checkoutStore.Close()
	checkoutService := checkout.NewService(payment.NewSimulated(), checkoutStore, noRelease{}, payment.ProviderName)

	// The tickets are issued by the real checkout, so the primitive is proven against
	// the sale the system produces rather than a hand-inserted row.
	sold := map[string]checkout.Result{}
	for _, reservationID := range fixtureReservations {
		result, err := checkoutService.Charge(ctx, reservationID, "key-"+reservationID)
		if err != nil {
			t.Fatalf("checkout of %s = unexpected error %v", reservationID, err)
		}
		sold[reservationID] = result
	}
	ticketA, ticketB := sold[orderReservation].TicketIDs[0], sold[orderReservation].TicketIDs[1]

	const doors = 2
	results := make([]tickets.Redemption, doors)
	failures := make([]error, doors)
	start := make(chan struct{})
	var group sync.WaitGroup
	for door := 0; door < doors; door++ {
		group.Add(1)
		go func(door int) {
			defer group.Done()
			<-start
			results[door], failures[door] = store.Redeem(ctx, ticketA, fmt.Sprintf("door-%d", door+1))
		}(door)
	}
	close(start)
	group.Wait()

	var admitted, told tickets.Redemption
	for door := 0; door < doors; door++ {
		if failures[door] != nil {
			t.Fatalf("door %d = unexpected error %v, a losing door is not a failure", door+1, failures[door])
		}
		switch results[door].Outcome {
		case tickets.RedemptionRedeemed:
			admitted = results[door]
		case tickets.RedemptionAlreadyRedeemed:
			told = results[door]
		default:
			t.Fatalf("door %d answered outcome %q, want one redemption and one already-redeemed",
				door+1, results[door].Outcome)
		}
	}
	if admitted.Outcome == "" || told.Outcome == "" {
		t.Fatalf("the race answered %+v, want exactly one redemption and one already-redeemed", results)
	}
	if admitted.RedeemedAt.IsZero() {
		t.Fatal("the redemption that won answers no instant")
	}
	// The loser is told when it happened, not that something failed: the same instant
	// the winner wrote, and the label the winner supplied rather than its own.
	if !told.RedeemedAt.Equal(admitted.RedeemedAt) {
		t.Fatalf("the losing door was told %v, want the first redemption's %v",
			told.RedeemedAt, admitted.RedeemedAt)
	}
	if told.RedeemedBy != admitted.RedeemedBy {
		t.Fatalf("the losing door was told label %q, want the first redemption's %q",
			told.RedeemedBy, admitted.RedeemedBy)
	}

	// Exactly one write reached the row: the instant and the label the winner supplied,
	// and no trace of the loser's.
	var storedAt time.Time
	var storedBy *string
	if err := pool.QueryRow(ctx,
		`SELECT redeemed_at, redeemed_by FROM cinema.tickets WHERE id = $1`, ticketA).
		Scan(&storedAt, &storedBy); err != nil {
		t.Fatalf("read the redeemed ticket: %v", err)
	}
	if !storedAt.Equal(admitted.RedeemedAt) {
		t.Fatalf("the stored instant %v is not the one the winner was told %v", storedAt, admitted.RedeemedAt)
	}
	if storedBy == nil || *storedBy != admitted.RedeemedBy {
		t.Fatalf("the stored label = %v, want the winner's %q", storedBy, admitted.RedeemedBy)
	}

	// A third presentation, after both doors answered, still answers the first instant.
	if again, err := store.Redeem(ctx, ticketA, "door-3"); err != nil {
		t.Fatalf("a third presentation = unexpected error %v", err)
	} else if again.Outcome != tickets.RedemptionAlreadyRedeemed || !again.RedeemedAt.Equal(admitted.RedeemedAt) {
		t.Fatalf("a third presentation = %+v, want already-redeemed at %v", again, admitted.RedeemedAt)
	}

	// Nothing is invented about identity: a door that names no label leaves the record
	// empty rather than writing a made-up user.
	if blank, err := store.Redeem(ctx, ticketB, "   "); err != nil {
		t.Fatalf("Redeem() without a label = unexpected error %v", err)
	} else if blank.Outcome != tickets.RedemptionRedeemed || blank.RedeemedBy != "" {
		t.Fatalf("Redeem() without a label = %+v, want a redemption with no label", blank)
	}
	var blankLabel *string
	if err := pool.QueryRow(ctx, `SELECT redeemed_by FROM cinema.tickets WHERE id = $1`, ticketB).
		Scan(&blankLabel); err != nil {
		t.Fatalf("read the unlabelled redemption: %v", err)
	}
	if blankLabel != nil {
		t.Fatalf("a redemption without a label stored %q, want no label at all", *blankLabel)
	}

	// A voided ticket and a ticket of a cancelled screening are each their own outcome,
	// and neither changes the stored row.
	voidedTicket := sold[voidReservation].TicketIDs[0]
	if _, err := pool.Exec(ctx, `UPDATE cinema.tickets SET status = 'void' WHERE id = $1`, voidedTicket); err != nil {
		t.Fatalf("void the ticket: %v", err)
	}
	refused, err := store.Redeem(ctx, voidedTicket, "door-north")
	if err != nil {
		t.Fatalf("redeeming a voided ticket = unexpected error %v", err)
	}
	if refused.Outcome != tickets.RedemptionRefusedVoided {
		t.Fatalf("a voided ticket answered %q, want %q", refused.Outcome, tickets.RedemptionRefusedVoided)
	}
	// A refusal is not "already used": it answers no instant, because no door ever
	// admitted this ticket.
	if !refused.RedeemedAt.IsZero() {
		t.Fatalf("a voided ticket answered the instant %v, want no instant", refused.RedeemedAt)
	}

	cancelledTicket := sold[cancelledReservation].TicketIDs[0]
	if _, err := pool.Exec(ctx, `UPDATE cinema.events SET status = 'cancelled' WHERE id = $1`, fixtureEvent); err != nil {
		t.Fatalf("cancel the event: %v", err)
	}
	refused, err = store.Redeem(ctx, cancelledTicket, "door-north")
	if err != nil {
		t.Fatalf("redeeming a ticket of a cancelled screening = unexpected error %v", err)
	}
	if refused.Outcome != tickets.RedemptionRefusedEventCancelled {
		t.Fatalf("a cancelled screening answered %q, want %q",
			refused.Outcome, tickets.RedemptionRefusedEventCancelled)
	}
	var written int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cinema.tickets WHERE redeemed_at IS NOT NULL`).Scan(&written); err != nil {
		t.Fatalf("count the redeemed tickets: %v", err)
	}
	if written != 2 {
		t.Fatalf("%d tickets carry a redemption, want only the two the door admitted", written)
	}
	if _, err := pool.Exec(ctx, `UPDATE cinema.events SET status = 'on_sale' WHERE id = $1`, fixtureEvent); err != nil {
		t.Fatalf("restore the event status: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cinema.tickets SET status = 'issued' WHERE id = $1`, voidedTicket); err != nil {
		t.Fatalf("restore the voided ticket: %v", err)
	}

	// A code that names no stored ticket is not found, exactly like an identifier that
	// could never be one.
	if _, err := store.Redeem(ctx, "00000000-0000-0000-0000-00000000abcd", "door-north"); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("Redeem() of an unknown ticket = %v, want ErrNotFound", err)
	}

	// Preserve: the ticket the door admitted is still the ticket the buyer holds. The
	// detail answers the same seat, order amount and status, and its signed code still
	// verifies — redemption never touches the PDF's record of the sale.
	signer, err := tickets.NewSigner(strings.Repeat("k", tickets.MinKeyLength))
	if err != nil {
		t.Fatalf("build the signer: %v", err)
	}
	service := tickets.NewService(store, signer)
	detail, err := service.Detail(ctx, ticketA)
	if err != nil {
		t.Fatalf("Detail() of the redeemed ticket = unexpected error %v", err)
	}
	if !detail.Redeemed() || detail.RedeemedAt == nil || !detail.RedeemedAt.Equal(admitted.RedeemedAt) {
		t.Fatalf("the detail answers redeemed = %t at %v, want the door's %v",
			detail.Redeemed(), detail.RedeemedAt, admitted.RedeemedAt)
	}
	if detail.Status != tickets.TicketStatusIssued || detail.OrderAmountCents != fixturePriceCents*2 {
		t.Fatalf("the redeemed ticket lost its sale: status %q amount %d",
			detail.Status, detail.OrderAmountCents)
	}
	if len(detail.Seats) != 1 || detail.Seats[0].EventSeatID != seats[0] {
		t.Fatalf("the redeemed ticket answers seats %+v, want its own seat %d", detail.Seats, seats[0])
	}
	if verified, err := service.Verify(ctx, detail.QRToken); err != nil {
		t.Fatalf("Verify() of a redeemed ticket's code = unexpected error %v", err)
	} else if verified.TicketID != ticketA {
		t.Fatalf("Verify() answered ticket %q, want %q", verified.TicketID, ticketA)
	}
}
