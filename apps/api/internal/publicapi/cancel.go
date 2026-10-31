// The cancel route, POST /api/v1/reservations/{reservationID}/cancel, lives in this file.
//
// It exists so a buyer who abandons a hold stops holding its seats now instead of waiting
// out the `expires_at` the reservation reports. It is public for the reason the read next
// to it is: in v1 the reservation identifier is the capability, because the demonstration
// session authenticates nobody and a guest order has no owner to check (see the catalog
// and tickets package documentation).
//
// Its answer is the confirmation read's own body, built by the same function, so a client
// that already shows an order does not learn a second vocabulary for the same order after
// cancelling it.

package publicapi

import (
	"context"
	"errors"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/reservation"
)

// CancellationService is the cancel contract: the application service that ends one live
// hold. It is satisfied by the reservation service.
type CancellationService interface {
	// Cancel ends a valid, unpaid hold and answers its new state as stored. A hold that
	// was already cancelled, and one whose window closed, answer the same way with their
	// own state; a paid order is a conflict, and so is a hold whose payment is in flight;
	// an identifier that names no reservation is an error wrapping
	// reservation.ErrReservationNotFound.
	Cancel(ctx context.Context, reservationID string) (reservation.Cancellation, error)
}

// WithCancellation attaches the cancel service and returns the same handler, so the cancel
// route is mounted only when one is wired.
//
// The route needs the confirmation reader as well as the service: its answer is that
// read's body, and a deployment without it has no way to describe the order it just
// cancelled. A nil service therefore leaves the route unmounted, exactly like a nil reader
// does.
func (h *Handler) WithCancellation(svc CancellationService) *Handler {
	h.cancels = svc
	return h
}

// cancelReservation ends the hold named by the path and answers the order's new state.
//
// The order matters and each step has one reason:
//
//  1. Cancel first, so nothing is answered about a transition that has not happened: a
//     refusal (a paid order, an unknown identifier) must not render a reservation body.
//  2. Read the order afterwards, in the body the confirmation read serves, so the answer is
//     the state a reload would show rather than a second projection of it.
//  3. Answer the state the cancel established, which is authoritative over that read for
//     the one case they can disagree: a hold whose window has closed but that the expiry
//     sweep has not rewritten yet. A cancel answers "expired" there, and a buyer told
//     "held" would keep waiting for something that is already over.
func (h *Handler) cancelReservation(w http.ResponseWriter, r *http.Request) {
	reservationID := r.PathValue("reservationID")
	cancellation, err := h.cancels.Cancel(r.Context(), reservationID)
	if err != nil {
		writeCancelError(w, err)
		return
	}
	detail, err := h.reservations.Reservation(r.Context(), reservationID)
	if err != nil {
		writeReservationError(w, err)
		return
	}
	detail.Status = cancellation.Status
	writeReservationState(w, detail)
}

// writeCancelError maps the cancel outcomes to their status and problem code.
//
// The vocabulary is the one the routes next to it already use, so a client meeting both
// surfaces reads one set of codes: an identifier that names no reservation is the read's
// `404 not_found`, a paid order is the payment route's `409 already_paid`, and a payment
// still in flight is the payment route's `409 payment_in_flight`.
func writeCancelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reservation.ErrPaymentInFlight):
		// A capture is out with the provider and the attempt is stored as pending, so the
		// seats are being paid for right now and this route cannot hand them back. Same
		// code and same words as the payment route's refusal of a second charge, because
		// it is the same fact seen from the other surface; the action it names is the one
		// a cancelling client can take.
		writeProblem(w, http.StatusConflict, "payment_in_flight", "Conflict",
			"a payment for this reservation is already being processed; the hold cannot be cancelled until it ends")
	case errors.Is(err, reservation.ErrAlreadyPaid):
		// The money was taken and the tickets exist, so the seats cannot be handed back by
		// this route. Same code and same words as the payment route's refusal, because it
		// is the same fact.
		writeProblem(w, http.StatusConflict, "already_paid", "Conflict",
			"the reservation already carries a captured payment")
	case errors.Is(err, reservation.ErrReservationNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "Not found",
			"the requested reservation does not exist")
	case errors.Is(err, reservation.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
	default:
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the reservation is temporarily unavailable; retry the request")
	}
}

// cancelRoute is the cancel route pattern, relative to the /api/v1 mount point. It lives
// on the reservation subtree next to the read and the payment routes, and answers POST on
// the order's own cancel path.
const cancelRoute = "POST /reservations/{reservationID}/cancel"
