// Package pdf renders, stores and serves the ticket PDF of the M3 confirmation.
//
// It answers two of M3's requests at once: the buyer can download a PDF of the
// ticket from the web confirmation (S3), and that PDF is stored in S3 — Floci in
// development, behind the object-storage boundary of internal/storage — and served
// through a short-lived pre-signed URL (S5).
//
// # One projection, two media
//
// The document is rendered from [tickets.TicketDetail] and from nothing else. That is
// the same value the web confirmation page reads, so the page and the PDF cannot
// disagree about the film, the screening time, the seat or the money: there is one
// assembly of the sale (T4), one signing of the code (T5), and this package only lays
// the value out. The renderer adds no fact of its own — no price, no currency, no time,
// no status — and each derived presentation it prints comes from the projection's own
// rule ([tickets.EventDetail.LocalStartsAt],
// [tickets.TicketDetail.OrderAmountText], [tickets.TicketDetail.InvalidReason]), which
// are the rules the web page and the door use too.
//
// # What the page shows, and where
//
//	venue                            the building to walk into
//	film title                       what is being shown
//	room / screening / seat          which door, which hour, which seat
//	order and ticket identifiers     how a printed document is traced to a sale
//	order total                      the money taken for the whole reservation
//	QR code                          the signed code of this ticket
//
// The screening is printed in the venue's own timezone with an explicit offset, so
// neither the machine that renders the document nor the machine that reads it decides
// what hour the film starts. A venue whose stored zone cannot be resolved is refused
// rather than printed with an invented hour: a ticket that says the wrong hour is
// worse than a ticket that was not printed. The total is the order's, not a per-seat
// price, because M3 takes one payment for the reservation. A ticket that no longer
// admits its holder (withdrawn, or a cancelled screening) is printed as such, because
// the stored artefact never changes and a document that silently looked valid forever
// would be a lie of omission. The QR is an image of the signed token and nothing else:
// it carries no name, no film and no price.
//
// # The artefact: derived key, written once, served by URL
//
// The PDF lives at tickets/{eventID}/{ticketID}.pdf (see [ObjectKey]). The key is
// derived from the sale, so no column stores it, the row that records the sale is its
// only authority, and a retry addresses the same object instead of a second copy.
//
// It is immutable once written. [Archive.Ensure] asks the object store whether the
// object exists and, when it does, serves that object and renders nothing: the stored
// bytes are the artefact that matches what was sold. That is why the storage boundary
// carries Exists — inferring absence from a failed Get cannot tell a missing object
// from an unreachable bucket, and guessing wrong in that direction would overwrite the
// buyer's ticket with a document rendered from a projection that has since moved.
//
// Generation is eager and lazy at once, and both paths render the same projection
// through [Render]:
//
//   - [Worker] consumes the `ticket.issued` outbox rows the checkout already writes and
//     stores the PDF of every ticket the payload names, so the document is usually in
//     the bucket before the buyer asks for it;
//   - the download route calls [Archive.DownloadURL], which generates the document when
//     the object is missing. A crashed worker, a bucket outage or a deleted artefact
//     therefore never leaves the buyer without their ticket.
//
// Because the key and the renderer are shared, the two paths cannot produce two
// different documents for one ticket.
//
// # Serving is a redirect, never the bytes
//
// [Archive.DownloadURL] answers a pre-signed URL from the configured bucket, so the API
// never streams a ticket and the bucket's own signature does the transfer.
// [PresignTTL] documents the lifetime and why it is short. The URL is a credential —
// no session is needed, possession is the whole of its authority — so it is never
// logged, never written into an error and never echoed in a problem detail; a failure
// names the ticket and the reason, not the link.
//
// # Dependencies, and why exactly these two
//
// Two libraries, pinned exactly in go.mod, and no third:
//
//   - github.com/go-pdf/fpdf v0.9.0 (MIT, the maintained fork of gofpdf) writes the
//     document. It has no transitive dependency, needs no external font file for the
//     built-in core fonts, and emits valid PDF with text, an image and metadata. The
//     alternative — writing the file format by hand — would be a hand-rolled format in
//     a correctness-critical path, and the larger rendering stacks bring a font and
//     metrics chain this milestone does not need.
//   - github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e (MIT) encodes the QR
//     matrix. It is pure Go, has no transitive dependency and produces the PNG the
//     document embeds. Writing a QR encoder by hand would mean implementing
//     Reed-Solomon error correction and the version and mask selection rules, and a
//     subtly wrong QR code is a ticket that never scans.
//
// Both are permissively licensed and dependency-free, which keeps the module graph and
// the review surface small.
//
// # Text encoding
//
// The document uses the core Helvetica font, so the page carries code page 1252 bytes
// rather than UTF-8. Text is translated with the library's cp1252 translator, so a
// title such as "La película" is readable in a viewer; a character outside that code
// page prints as "?" instead of a broken glyph. Embedding a Unicode font is a later
// decision — it means shipping and licensing a font file — and v1's catalogue is
// Western European.
//
// # What the artefact cannot promise
//
// The document records the sale as it was at the moment it was written. If the
// projection changes afterwards — the screening is cancelled, the ticket is withdrawn,
// the event's price moves — the stored PDF is not re-rendered, so it keeps saying what
// was sold. That is the deliberate consequence of immutability: the page and the door
// answer with the current projection (and the door re-verifies the signed code server
// side), while the PDF remains the buyer's record of their purchase. Nothing here
// decides whether a ticket admits its holder at the door; that is the verification
// route of T5 and, for single use, M4's cashier workflow.
//
// # Fail closed
//
// A projection the document cannot honestly describe is [ErrIncomplete]; an artefact
// that cannot be stored or signed is [ErrUnavailable]; an archive or worker that was
// never configured is [ErrNotConfigured]. Nothing in this package answers with an
// empty document, a missing page or an invented link, and a ticket that cannot be read
// keeps the meaning its reader gave it ([tickets.ErrNotFound] or
// [tickets.ErrUnavailable]), so the route can answer 404 for an unknown ticket and 503
// for a store it could not reach.
package pdf
