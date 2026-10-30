// The ticket QR image route, GET /api/v1/tickets/{ticketId}/qr.svg, lives in this
// file.
//
// It serves the signed code of one ticket as an SVG image, which is what the ticket
// page shows and what the PDF prints. The alternative was to encode the token in the
// browser, and that alternative is worse in both directions: it would be a second QR
// implementation that could disagree with the printed copy, and it would put a QR
// dependency into the web application to draw a value the API already renders.
//
// The route is public for the same reason the detail route next to it is: v1 sells to
// guests, and the ticket identifier is the capability. It is mounted with the ticket
// route (see [Handler.Mux]) and needs nothing else — the code is served from the token
// the ticket service already signs, so no bucket and no extra configuration are
// involved.
//
// The answers, per case:
//
//   - a ticket the service can read is `200` with `Content-Type: image/svg+xml` and the
//     matrix of its signed token. Two reads answer identical bytes, because the image
//     is a pure function of the token;
//   - an unknown ticket is the detail route's own `404 not_found` problem document, so
//     a wrong guess learns nothing new and no image is drawn;
//   - anything that could not be read or rendered is `503 service_unavailable`: a store
//     that could not be asked says nothing about the ticket, and an empty image would be
//     a document a page would print.
//
// `Cache-Control: no-store` is set on the success answer because the code is a
// credential: it opens the ticket at the door, so no browser cache, proxy or CDN may
// keep a copy. The refusal answers are the shared problem documents and carry their own
// headers.

package publicapi

import (
	"net/http"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// getTicketQR serves the QR image of the ticket named by the path.
//
// The image is rendered from `detail.QRToken`, the code the service signed for this
// ticket, so the page, the PDF and the door all speak about one code: this route never
// receives a token and never mints one.
func (h *Handler) getTicketQR(w http.ResponseWriter, r *http.Request) {
	detail, err := h.tickets.Detail(r.Context(), r.PathValue("ticketId"))
	if err != nil {
		// The same mapping the detail route uses, so the two routes cannot disagree
		// about whether a ticket exists.
		writeTicketError(w, err)
		return
	}

	document, err := tickets.QRCodeSVG(detail.QRToken)
	if err != nil {
		// A ticket whose code cannot be drawn is not served as an empty image: the
		// buyer gets a retryable failure instead of a document that would be refused at
		// the door.
		writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
			"the ticket code cannot be presented right now; retry the request")
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	// The code is a credential: it must not be kept by a cache on the way to the buyer.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(document)
}

// ticketQRRoute is the ticket QR image route pattern, relative to the /api/v1 mount
// point.
const ticketQRRoute = "GET /tickets/{ticketId}/qr.svg"
