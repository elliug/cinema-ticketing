// The artefact archive: where a ticket PDF lives in the bucket, and the rule that
// governs it — written once, never rewritten.
//
// The key is derived from the sale itself, tickets/{eventID}/{ticketID}.pdf, so no
// column stores it and nothing can drift from it; because it is derived, a retry
// addresses the same object instead of accumulating a second copy. The store is asked
// whether the object exists before anything is rendered, which is what makes the
// stored bytes the artefact that matches what was sold. doc.go states the contract;
// this file is the key, the two write paths and the pre-signed URL.

package pdf

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cinema.local/ticketing/apps/api/internal/storage"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// PresignTTL is how long a ticket download link stays valid when the deployment does
// not say otherwise.
//
// Five minutes is the shortest lifetime that still covers the way a buyer uses the
// link: the confirmation page asks for it when the buyer clicks download, the browser
// follows the redirect inside the same request, and a download that a slow connection
// starts just before the deadline still completes. It is deliberately short because
// the URL is a credential — it needs no session, so possession is the whole of its
// authority — and a link pasted into a chat or left in a browser history should stop
// working quickly. It is also comfortably above the one-second floor SigV4 needs to
// sign anything at all, so the small clock skew between this process and the object
// store cannot expire the link between signing and use.
const PresignTTL = 5 * time.Minute

// The failures the archive reports. They are separate from the render failures: a
// caller decides the HTTP outcome, and this package never decides that a missing
// artefact is a missing ticket.
var (
	// ErrNotConfigured reports an archive that was never built, or was built without a
	// reader, without an object store, or with a lifetime no signature can carry. It is
	// a fail-closed answer, never a success.
	ErrNotConfigured = errors.New("pdf: the ticket artefact archive is not configured")

	// ErrUnavailable reports that the artefact could not be stored or served: the
	// object store failed, or an issued ticket disagreed with the payload that named
	// it. It is retryable for the caller and is never an artefact with missing pages.
	ErrUnavailable = errors.New("pdf: the ticket artefact is unavailable")
)

// ObjectStore is the part of the object-storage boundary the archive uses. It is a
// narrow interface on purpose: storing and serving a PDF needs three questions, and
// the adapter behind it (the S3 one in internal/storage) answers them for a real
// bucket, Floci or otherwise.
type ObjectStore interface {
	// Put stores data under key with contentType.
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Exists reports whether an object is already stored under key, without
	// downloading it. An unanswerable question is an error, never a false.
	Exists(ctx context.Context, key string) (bool, error)
	// PresignGet returns a URL that downloads key without credentials until ttl
	// elapses, declaring what opts declares about the delivery.
	PresignGet(ctx context.Context, key string, ttl time.Duration, opts storage.PresignOptions) (string, error)
}

// Archive stores and serves the ticket PDFs. The zero value is unusable: a caller
// that forgot NewArchive gets ErrNotConfigured from every method instead of a
// silently empty bucket.
type Archive struct {
	details tickets.Reader
	objects ObjectStore
	ttl     time.Duration
}

// NewArchive builds the archive over the projection reader and the object store.
//
// The reader is what the web page reads, so the PDF is rendered from the same
// projection; it is required. A zero ttl means [PresignTTL]; anything shorter than
// the one-second floor a SigV4 signature needs is refused rather than signed into a
// link that never works.
func NewArchive(details tickets.Reader, objects ObjectStore, ttl time.Duration) (*Archive, error) {
	if details == nil {
		return nil, fmt.Errorf("%w: the ticket detail reader is missing", ErrNotConfigured)
	}
	if objects == nil {
		return nil, fmt.Errorf("%w: the object store is missing", ErrNotConfigured)
	}
	if ttl == 0 {
		ttl = PresignTTL
	}
	if ttl < time.Second {
		return nil, fmt.Errorf("%w: a %s download lifetime is shorter than the one-second floor a SigV4 signature needs", ErrNotConfigured, ttl)
	}
	return &Archive{details: details, objects: objects, ttl: ttl}, nil
}

// ObjectKey derives the object key of one ticket's PDF: tickets/{eventID}/{ticketID}.pdf.
//
// Both identifiers are required, and the composed key is validated by the storage
// boundary's own rules, so a value that would escape the event's namespace — a
// traversal, an absolute path, a control character — is refused here instead of
// signed later. It returns no key alongside an error, so a caller can never store or
// serve a document under a key it was not given.
func ObjectKey(eventID, ticketID string) (string, error) {
	if strings.TrimSpace(eventID) == "" {
		return "", fmt.Errorf("%w: the ticket names no event, so its PDF has no place to live", ErrIncomplete)
	}
	if strings.TrimSpace(ticketID) == "" {
		return "", fmt.Errorf("%w: the ticket has no identifier to name its PDF by", ErrIncomplete)
	}
	// Both values are identifiers, so a separator in either one means it is not an
	// identifier of ours: concatenating them would silently move the object into
	// another prefix instead of refusing.
	for _, identifier := range []string{eventID, ticketID} {
		if strings.Contains(identifier, "/") {
			return "", fmt.Errorf("%w: %q is not a single identifier, so it cannot name an object segment", ErrIncomplete, identifier)
		}
	}
	key := "tickets/" + eventID + "/" + ticketID + ".pdf"
	if err := storage.ValidateKey(key); err != nil {
		return "", fmt.Errorf("%w: %w", ErrIncomplete, err)
	}
	return key, nil
}

// Ensure stores the PDF of one projection if it is not stored yet, and answers the
// key it lives under.
//
// The order is the immutability rule: ask whether the object exists first, and when
// it does, return without rendering and without writing. A store that cannot answer
// is ErrUnavailable — never "absent" — because a wrong answer in that direction
// overwrites the artefact.
//
// When the object is missing, the document is rendered from the detail it was given
// (the same projection the web page reads) and written once. Two paths racing to
// write the same missing object store the same bytes, because [Render] is a pure
// function of that projection.
func (a *Archive) Ensure(ctx context.Context, detail tickets.TicketDetail) (string, error) {
	if err := a.usable(); err != nil {
		return "", err
	}
	key, err := ObjectKey(detail.Event.ID, detail.TicketID)
	if err != nil {
		return "", err
	}
	exists, err := a.objects.Exists(ctx, key)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if exists {
		// The artefact as it was sold. Serving it is the whole point of writing it once.
		return key, nil
	}
	if err := a.write(ctx, key, detail); err != nil {
		return "", err
	}
	return key, nil
}

// EnsureIssued is the outbox worker's path: it stores the PDF of a ticket the outbox
// payload named, and it reads the ticket only when the artefact is missing.
//
// That order is what makes a redelivered or replayed outbox row cheap and harmless:
// an existing object is the answer, so no read, no render and no write happen. When
// the object is missing, the ticket is read through the same reader the web page uses
// and the projection must agree with the payload that named it — a payload naming a
// different event than the stored ticket is refused instead of filing one ticket's
// document in another event's namespace.
func (a *Archive) EnsureIssued(ctx context.Context, eventID, ticketID string) (string, error) {
	if err := a.usable(); err != nil {
		return "", err
	}
	key, err := ObjectKey(eventID, ticketID)
	if err != nil {
		return "", err
	}
	exists, err := a.objects.Exists(ctx, key)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if exists {
		return key, nil
	}

	detail, err := a.details.Detail(ctx, ticketID)
	if err != nil {
		// A ticket the database does not know (or cannot answer for) keeps its own
		// meaning: the worker reports it and retries, the route answers 404 or 503.
		return "", err
	}
	if detail.TicketID != ticketID || detail.Event.ID != eventID {
		return "", fmt.Errorf("%w: the issued ticket does not belong to the event the payload named", ErrUnavailable)
	}

	if err := a.write(ctx, key, detail); err != nil {
		return "", err
	}
	return key, nil
}

// DownloadURL answers a short-lived pre-signed URL for one ticket's PDF.
//
// The ticket is read first, because the route must not sign a link for a ticket that
// does not exist and because the key needs the event the ticket belongs to. The read
// outcome keeps its meaning: an unknown ticket is [tickets.ErrNotFound], an
// unreadable one is [tickets.ErrUnavailable]. A ticket that is readable but whose
// artefact is missing is generated here, so the download never depends on the eager
// worker having run.
//
// The URL **declares the download**: [TicketAttachmentDisposition] is signed into it, so
// the object store answers `Content-Disposition: attachment` with a filename derived from
// the ticket and every browser saves the document instead of deciding, from the media
// type alone, whether to render it in a viewer. A link whose delivery a browser chooses
// is a link a screen cannot honestly call a download.
//
// The URL is a credential. It is returned to the caller and never logged, never
// written into an error and never echoed in a problem detail.
func (a *Archive) DownloadURL(ctx context.Context, ticketID string) (string, error) {
	if err := a.usable(); err != nil {
		return "", err
	}
	detail, err := a.details.Detail(ctx, ticketID)
	if err != nil {
		return "", err
	}
	key, err := a.Ensure(ctx, detail)
	if err != nil {
		return "", err
	}
	url, err := a.objects.PresignGet(ctx, key, a.ttl, storage.PresignOptions{
		ContentDisposition: TicketAttachmentDisposition(ticketID),
	})
	if err != nil {
		// The adapter's error names the key and the reason, never a signed URL.
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return url, nil
}

// TicketAttachmentDisposition is the Content-Disposition the ticket download declares:
// an attachment named after the ticket, `attachment; filename="ticket-<ticketID>.pdf"`.
//
// The name comes from the ticket, not from the object key, because the key is a path in
// the bucket (`tickets/{eventID}/{ticketID}.pdf`) and a browser would otherwise save the
// file under the identifier of the event it happens to live in. The value is a quoted
// string, so a quote or a backslash inside the identifier is escaped rather than allowed
// to end the header early; control characters cannot reach here, because the object key
// carrying the same identifier is refused by the storage boundary when it has one.
func TicketAttachmentDisposition(ticketID string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(ticketID)
	return `attachment; filename="ticket-` + escaped + `.pdf"`
}

// write renders the projection and stores it. It is the one place the archive puts
// an object, so both write paths store the same document under the same content type.
func (a *Archive) write(ctx context.Context, key string, detail tickets.TicketDetail) error {
	document, err := Render(detail)
	if err != nil {
		return err
	}
	if err := a.objects.Put(ctx, key, document, ContentType); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// usable reports whether the archive was built, so the zero value fails closed.
func (a *Archive) usable() error {
	if a == nil || a.details == nil || a.objects == nil {
		return fmt.Errorf("%w: the archive was not built by NewArchive", ErrNotConfigured)
	}
	return nil
}
