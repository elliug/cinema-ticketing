// The checkout route, POST /api/v1/reservations/{reservationID}/payment, lives in
// this file. It is public, like the hold routes next to it: the buyer paying for
// the seats they just held has no administrative token, so it is mounted outside
// the administrative guard (see httpserver.Server.WithPublic). It is idempotent by
// the Idempotency-Key header.
//
// The route accepts exactly one optional field, and it is not money: the address the
// buyer asked the tickets to be mailed to. The amount is still computed server side from
// the event price and the seats the reservation holds, so the client cannot state a price
// — the promise the previous version of this comment made was about *money*, and it still
// holds. The address is different in kind: it cannot change what is charged, only where a
// copy of the ticket goes, and it is stored in the same transaction as the attempt so a
// crash during the provider call cannot leave a sale with nowhere to send it.

package publicapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/checkout"
)

// PaymentService is the checkout application service contract.
type PaymentService interface {
	// ChargeWithContact pays one held reservation, carrying the address the buyer asked
	// the tickets to be mailed to. A decline is a successful answer with PaymentStatus
	// declined, not an error.
	ChargeWithContact(ctx context.Context, request checkout.ChargeRequest) (checkout.Result, error)
}

// WithPayments attaches the checkout service and returns the same handler, so the
// payment route is mounted only when one is wired.
//
// A nil service leaves the route unmounted: a payment route without a service
// could only answer 503, and a deployment without the checkout schema must not
// advertise it at all.
func (h *Handler) WithPayments(svc PaymentService) *Handler {
	h.payments = svc
	return h
}

// createPayment pays the reservation addressed by the path. The Idempotency-Key
// header names the payment attempt: a replay of the same key answers with the
// stored result instead of charging again, so the client is expected to keep its
// key across retries.
//
// An approved and a declined payment are both 200: the request succeeded, and the
// body says what the provider decided.
func (h *Handler) createPayment(w http.ResponseWriter, r *http.Request) {
	email, err := paymentContactEmail(r)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	result, err := h.payments.ChargeWithContact(r.Context(), checkout.ChargeRequest{
		ReservationID:  r.PathValue("reservationID"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		ContactEmail:   email,
	})
	if err != nil {
		writePaymentError(w, err)
		return
	}

	// The ticket list is always an array, so a client can iterate it without a nil
	// check and a decline stays readable.
	ticketIDs := result.TicketIDs
	if ticketIDs == nil {
		ticketIDs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reservationId": result.ReservationID,
		"attemptId":     result.AttemptID,
		"paymentStatus": result.PaymentStatus,
		"amountCents":   result.AmountCents,
		"ticketIds":     ticketIDs,
	})
}

// maxPaymentBodyBytes bounds the one optional field this route reads, so a client cannot
// make the server buffer a body it has no use for.
const maxPaymentBodyBytes = 1 << 10

// paymentContactEmail reads the address the tickets are to be mailed to from an optional
// body. An absent body, an empty body and `{}` all mean "no address", which is the
// ordinary case: the field is optional and most callers — the gates, the tests, the
// simulated flows — pay without one.
//
// Unknown fields are refused instead of ignored, and that is not strictness for its own
// sake: a misspelt key (`emial`) would silently drop the address, and the buyer would
// discover it only when no ticket ever arrived. Failing loudly at the edge is cheaper than
// that silence.
func paymentContactEmail(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxPaymentBodyBytes))
	if err != nil {
		return "", fmt.Errorf("%w: the payment body could not be read", checkout.ErrInvalidInput)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}

	var body struct {
		Email string `json:"email"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return "", fmt.Errorf("%w: the payment body must be {\"email\": \"…\"} and nothing else", checkout.ErrInvalidInput)
	}
	return body.Email, nil
}

// writePaymentError maps each checkout outcome to its own status and problem code.
// The cases are deliberately distinct: a reused key, a disagreeing amount, a closed
// hold window and an already paid reservation are four different situations with
// four different things the buyer can do, and a provider or storage failure is
// retryable rather than a success.
func writePaymentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, checkout.ErrIdempotencyKeyReused):
		writeProblem(w, http.StatusConflict, "idempotency_key_reused", "Conflict",
			"the Idempotency-Key was already used for a different payment request")
	case errors.Is(err, checkout.ErrAmountMismatch):
		// The attempt and the reservation disagree about the amount. The message names
		// both amounts, which is what the caller needs and leaks nothing.
		writeProblem(w, http.StatusConflict, "amount_mismatch", "Conflict", err.Error())
	case errors.Is(err, checkout.ErrPaymentInFlight):
		// The reservation already carries an open payment attempt, refused by the schema
		// (one open attempt per reservation), so a second key would be a second charge.
		// The buyer can act on this: retry with the key of the payment that is already
		// being processed. It is never a retryable failure, because waiting does not
		// change it — only that key, or the end of the hold, resolves it.
		writeProblem(w, http.StatusConflict, "payment_in_flight", "Conflict",
			"a payment for this reservation is already being processed; retry with the same Idempotency-Key")
	case errors.Is(err, checkout.ErrHoldExpired):
		writeProblem(w, http.StatusConflict, "hold_expired", "Hold expired",
			"the hold window closed before the payment was completed")
	case errors.Is(err, checkout.ErrAlreadyPaid):
		writeProblem(w, http.StatusConflict, "already_paid", "Conflict",
			"the reservation already carries a captured payment")
	case errors.Is(err, checkout.ErrReservationNotPayable):
		writeProblem(w, http.StatusConflict, "reservation_not_payable", "Conflict",
			"the reservation is not awaiting payment")
	case errors.Is(err, checkout.ErrSeatAlreadySold):
		writeProblem(w, http.StatusConflict, "seat_already_sold", "Conflict",
			"a seat of the reservation has already been sold")
	case errors.Is(err, checkout.ErrProviderReferenceTaken):
		writeProblem(w, http.StatusConflict, "provider_reference_taken", "Conflict",
			"the provider reference was already recorded for another payment")
	case errors.Is(err, checkout.ErrNothingToSell):
		// The event has no price or the reservation holds no seat: the request is
		// valid and this reservation simply cannot be sold.
		writeProblem(w, http.StatusBadRequest, "not_sellable", "Not sellable",
			"this reservation has no payable amount: the event has no price or the reservation holds no seats")
	case errors.Is(err, checkout.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
	case errors.Is(err, checkout.ErrReservationNotFound):
		// A PostgreSQL reference failure names tables and constraints, so the details
		// stay in the operator's log while the client is told what is missing.
		writeProblem(w, http.StatusNotFound, "not_found", "Not found",
			"the requested reservation does not exist")
	case errors.Is(err, checkout.ErrProviderUnavailable):
		writeProblem(w, http.StatusServiceUnavailable, "payment_unavailable", "Service unavailable",
			"the payment provider is temporarily unavailable; retry the same Idempotency-Key")
	default:
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the checkout is temporarily unavailable; retry the same Idempotency-Key")
	}
}

// paymentRoute is the checkout route pattern, relative to the /api/v1 mount point.
const paymentRoute = "POST /reservations/{reservationID}/payment"
