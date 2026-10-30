// The ticket PDF download route, GET /api/v1/tickets/{ticketId}/pdf, lives in this
// file.
//
// It is public for the same reason the detail route next to it is: v1 sells to guests,
// the ticket identifier is the capability, and a buyer who has just paid has no
// account to sign in to. It answers a short-lived pre-signed URL and never the bytes:
// the bucket's own signature does the transfer, so the API never streams a ticket and
// the link stops working on its own.
//
// The route is mounted only when both the ticket service and the object store are
// configured (see [Handler.Mux] and the process wiring in cmd/api). A deployment
// without storage can neither generate nor serve the artefact, so it must not
// advertise a download it could not honour — and the detail route keeps working.
//
// The answers, per case:
//
//   - a ticket whose PDF exists, or whose PDF is generated now, is `302 Found` with the
//     pre-signed URL in `Location` and no body. It is a redirect rather than a stream
//     because the link is short-lived by design and because the API should not spend a
//     request moving a file the bucket can serve directly. `Cache-Control: no-store`
//     keeps the credential out of any cache;
//   - an unknown ticket is the same `404 not_found` problem document the detail route
//     answers, so a wrong guess and an identifier that never existed stay
//     indistinguishable and no link is signed;
//   - an artefact that cannot be stored or signed is `503 service_unavailable`: it is
//     retryable and it never claims the ticket does not exist, because a bucket that
//     was unreachable says nothing about the sale.
//
// The pre-signed URL is a credential: it needs no session, so possession is the whole
// of its authority. It is put in the redirect header and nowhere else — not in the
// body (a body would be a page the buyer could screenshot or forward), not in a problem
// detail, and never in a log line.

package publicapi

import (
	"context"
	"net/http"
)

// TicketPDFService is the ticket artefact contract the download route needs: a
// short-lived URL for one ticket's PDF, generated on demand when the stored artefact is
// missing.
//
// It is satisfied by the pdf archive, which renders the same [tickets.TicketDetail] the
// detail route serves. The read outcomes of that projection keep their meaning: an
// unknown ticket is tickets.ErrNotFound (404) and an artefact that could not be produced
// is pdf.ErrUnavailable (503).
type TicketPDFService interface {
	// DownloadURL answers a pre-signed URL for one ticket's PDF.
	DownloadURL(ctx context.Context, ticketID string) (string, error)
}

// WithTicketPDFs attaches the ticket artefact service and returns the same handler.
//
// The route is mounted only when the ticket service is attached as well: a download
// needs a ticket to download, and mounting half a capability would advertise a URL
// that could only fail.
func (h *Handler) WithTicketPDFs(svc TicketPDFService) *Handler {
	h.ticketPDFs = svc
	return h
}

// downloadTicketPDF answers a short-lived redirect to the stored PDF of one ticket.
//
// The document is produced on demand when it is missing (the archive asks the object
// store first), so a lost artefact or a worker that never ran does not cost the buyer
// their ticket. The bytes are never served from here.
func (h *Handler) downloadTicketPDF(w http.ResponseWriter, r *http.Request) {
	url, err := h.ticketPDFs.DownloadURL(r.Context(), r.PathValue("ticketId"))
	if err != nil {
		// The same mapping the detail route uses: the ticket's own failures keep their
		// meaning, and the route decides nothing about which ticket exists.
		writeTicketError(w, err)
		return
	}

	// The header carries the credential because that is the only place a redirect can
	// put it. The body stays empty on purpose: a written body would echo the URL into a
	// document a browser could keep, and the redirect is complete without one.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", url)
	w.WriteHeader(http.StatusFound)
}

// ticketPDFRoute is the download route pattern, relative to the /api/v1 mount point. It
// is a GET because it reads (and at most generates) an artefact; it never changes the
// sale.
const ticketPDFRoute = "GET /tickets/{ticketId}/pdf"
