// Behaviour tests for the ticket artefact archive: the object key, the immutability
// rule, and the two paths that produce and serve the PDF.
//
// They need no bucket: the store boundary is a fake that records what was asked of
// it, so the tests can prove the questions — does this object exist, was anything
// written, which key was signed, for how long — instead of only the bytes. The one
// thing a fake cannot prove (that a put object round-trips and that a pre-signed URL
// really downloads) is gated in internal/storage, where the adapter that talks to S3
// lives.
package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/pdf"
	"cinema.local/ticketing/apps/api/internal/storage"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// fakeObjects is the object-storage boundary as the archive sees it. It keeps the
// stored bytes so an overwrite is observable, and it records every question so a
// path that writes when it should have served is a failure rather than a coincidence.
type fakeObjects struct {
	stored map[string][]byte

	existsErr  error
	putErr     error
	presignErr error
	presigned  string

	heads   []string
	puts    []putCall
	presign []presignCall

	// wrote receives one signal per stored object when it is set, so a test that runs the
	// worker in another goroutine can wait for progress instead of reading the recorded
	// puts concurrently with them.
	wrote chan struct{}
}

type putCall struct {
	key         string
	contentType string
	data        []byte
}

type presignCall struct {
	key string
	ttl time.Duration
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{stored: map[string][]byte{}, presigned: "https://objects.local/cinema-m3-tickets/ticket.pdf?X-Amz-Signature=stub"}
}

func (f *fakeObjects) Put(_ context.Context, key string, data []byte, contentType string) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.puts = append(f.puts, putCall{key: key, contentType: contentType, data: data})
	f.stored[key] = data
	if f.wrote != nil {
		select {
		case f.wrote <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeObjects) Exists(_ context.Context, key string) (bool, error) {
	f.heads = append(f.heads, key)
	if f.existsErr != nil {
		return false, f.existsErr
	}
	_, ok := f.stored[key]
	return ok, nil
}

func (f *fakeObjects) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	if f.presignErr != nil {
		return "", f.presignErr
	}
	f.presign = append(f.presign, presignCall{key: key, ttl: ttl})
	return f.presigned, nil
}

// fakeDetails is the projection reader: the tickets service is what a deployment
// passes, and the archive must ask it for the same projection the web page reads.
type fakeDetails struct {
	detail tickets.TicketDetail
	err    error
	asked  []string
}

func (f *fakeDetails) Detail(_ context.Context, ticketID string) (tickets.TicketDetail, error) {
	f.asked = append(f.asked, ticketID)
	if f.err != nil {
		return tickets.TicketDetail{}, f.err
	}
	detail := f.detail
	detail.TicketID = ticketID
	return detail, nil
}

// archiveFixture builds the archive over the fixture projection with a fake store.
func archiveFixture(t *testing.T, ttl time.Duration) (*pdf.Archive, *fakeObjects, *fakeDetails) {
	t.Helper()
	objects := newFakeObjects()
	details := &fakeDetails{detail: fixtureDetail()}
	archive, err := pdf.NewArchive(details, objects, ttl)
	if err != nil {
		t.Fatalf("NewArchive returned error: %v", err)
	}
	return archive, objects, details
}

// TestObjectKeyIsDeterministicAndScopedToTheSale pins the key scheme:
// tickets/{eventID}/{ticketID}.pdf. It is derived from the sale itself, so the
// artefact is addressable without a stored column and a retry addresses the same
// object instead of a second one.
func TestObjectKeyIsDeterministicAndScopedToTheSale(t *testing.T) {
	const (
		eventID  = "00000000-0000-0000-0000-00000000d0a3"
		ticketID = "00000000-0000-0000-0000-00000000d0a1"
	)

	key, err := pdf.ObjectKey(eventID, ticketID)
	if err != nil {
		t.Fatalf("ObjectKey returned error: %v", err)
	}
	want := "tickets/" + eventID + "/" + ticketID + ".pdf"
	if key != want {
		t.Fatalf("ObjectKey = %q, want %q", key, want)
	}
	// The bucket boundary its rules, so the key has to be one it accepts.
	if err := storage.ValidateKey(key); err != nil {
		t.Fatalf("the derived key is not a safe object key: %v", err)
	}

	// The same sale addresses the same object every time: that is what makes a retry
	// rewrite the artefact instead of accumulating copies of it.
	for attempt := 0; attempt < 3; attempt++ {
		again, err := pdf.ObjectKey(eventID, ticketID)
		if err != nil {
			t.Fatalf("ObjectKey returned error: %v", err)
		}
		if again != key {
			t.Fatalf("ObjectKey = %q on attempt %d, want the same %q", again, attempt, key)
		}
	}

	// One object per ticket, in the event's own namespace.
	otherTicket, err := pdf.ObjectKey(eventID, "00000000-0000-0000-0000-00000000d0a9")
	if err != nil {
		t.Fatalf("ObjectKey returned error: %v", err)
	}
	if otherTicket == key {
		t.Fatal("two tickets of one event address the same object")
	}
	otherEvent, err := pdf.ObjectKey("00000000-0000-0000-0000-00000000d0b3", ticketID)
	if err != nil {
		t.Fatalf("ObjectKey returned error: %v", err)
	}
	if otherEvent == key {
		t.Fatal("two events address the same object for one ticket")
	}

	// Nothing an identifier could carry may escape the event's namespace.
	for _, broken := range [][2]string{
		{"", ticketID},
		{eventID, ""},
		{"  ", ticketID},
		{"../other-event", ticketID},
		{eventID, "../../escape"},
		{eventID, "nested/ticket"},
		{"event\x00id", ticketID},
	} {
		key, err := pdf.ObjectKey(broken[0], broken[1])
		if err == nil {
			t.Errorf("ObjectKey(%q, %q) = %q, want a refusal", broken[0], broken[1], key)
		}
		if key != "" {
			t.Errorf("ObjectKey(%q, %q) returned the key %q with an error, want no key", broken[0], broken[1], key)
		}
	}
}

// TestEnsureWritesThePDFWhenTheObjectIsMissing pins the writing path: the artefact
// is rendered from the projection, stored as application/pdf under the derived key,
// and the document carries what the projection carries.
func TestEnsureWritesThePDFWhenTheObjectIsMissing(t *testing.T) {
	archive, objects, details := archiveFixture(t, time.Minute)
	detail := fixtureDetail()

	key, err := archive.Ensure(t.Context(), detail)
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if want, _ := pdf.ObjectKey(detail.Event.ID, detail.TicketID); key != want {
		t.Fatalf("Ensure = %q, want the derived key %q", key, want)
	}
	if len(objects.puts) != 1 {
		t.Fatalf("Ensure wrote the object %d times, want once", len(objects.puts))
	}
	if objects.puts[0].contentType != pdf.ContentType {
		t.Fatalf("the object was stored as %q, want %q", objects.puts[0].contentType, pdf.ContentType)
	}
	if !bytes.HasPrefix(objects.puts[0].data, []byte("%PDF-1.")) {
		t.Fatalf("the stored bytes start with %q, want a PDF document", objects.puts[0].data[:8])
	}
	if !bytes.Contains(objects.puts[0].data, []byte("/Subtype /Image")) {
		t.Fatal("the stored document embeds no image, so it carries no QR code")
	}
	if len(details.asked) != 0 {
		t.Fatalf("Ensure asked the reader for %v, want no read: it renders the detail it was given", details.asked)
	}
}

// TestEnsureNeverOverwritesAnExistingArtefact is the immutability rule: the stored
// PDF is the immutable artefact that matches what was sold, so an existing object is
// served as it is. A projection that has moved on since (a new price, a withdrawn
// ticket) must not rewrite the document the buyer already holds.
func TestEnsureNeverOverwritesAnExistingArtefact(t *testing.T) {
	archive, objects, _ := archiveFixture(t, time.Minute)

	original := fixtureDetail()
	key, err := pdf.ObjectKey(original.Event.ID, original.TicketID)
	if err != nil {
		t.Fatalf("ObjectKey returned error: %v", err)
	}
	objects.stored[key] = []byte("%PDF-1.4 the artefact as it was sold")

	// The projection has changed since: another film title and another order amount.
	changed := original
	changed.Event.FilmTitle = "Otra película"
	changed.OrderAmountCents = 2500

	got, err := archive.Ensure(t.Context(), changed)
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if got != key {
		t.Fatalf("Ensure = %q, want the existing key %q", got, key)
	}
	if len(objects.puts) != 0 {
		t.Fatalf("Ensure overwrote an existing artefact (%d puts): the stored PDF must stay as it was sold", len(objects.puts))
	}
	if stored := string(objects.stored[key]); stored != "%PDF-1.4 the artefact as it was sold" {
		t.Fatalf("the stored artefact changed to %q", stored)
	}
}

// TestEnsureIssuedSkipsTheReadWhenTheArtefactExists pins the worker's path: when the
// object is already stored, the eager path does not even read the ticket, so a
// redelivered or reprocessed outbox row is cheap and cannot rewrite anything.
func TestEnsureIssuedSkipsTheReadWhenTheArtefactExists(t *testing.T) {
	archive, objects, details := archiveFixture(t, time.Minute)
	detail := fixtureDetail()

	key, err := pdf.ObjectKey(detail.Event.ID, detail.TicketID)
	if err != nil {
		t.Fatalf("ObjectKey returned error: %v", err)
	}
	objects.stored[key] = []byte("%PDF-1.4 already stored")

	got, err := archive.EnsureIssued(t.Context(), detail.Event.ID, detail.TicketID)
	if err != nil {
		t.Fatalf("EnsureIssued returned error: %v", err)
	}
	if got != key {
		t.Fatalf("EnsureIssued = %q, want %q", got, key)
	}
	if len(details.asked) != 0 {
		t.Fatalf("EnsureIssued read the ticket %v although the artefact exists", details.asked)
	}
	if len(objects.puts) != 0 {
		t.Fatalf("EnsureIssued wrote the object %d times although it exists", len(objects.puts))
	}
}

// TestEnsureIssuedRendersFromTheProjectionWhenMissing pins the other half of the
// worker's path: a missing artefact is rendered from the same projection the web page
// reads, so the eager and the on-demand documents cannot differ.
func TestEnsureIssuedRendersFromTheProjectionWhenMissing(t *testing.T) {
	archive, objects, details := archiveFixture(t, time.Minute)
	detail := fixtureDetail()

	key, err := archive.EnsureIssued(t.Context(), detail.Event.ID, detail.TicketID)
	if err != nil {
		t.Fatalf("EnsureIssued returned error: %v", err)
	}
	if len(details.asked) != 1 || details.asked[0] != detail.TicketID {
		t.Fatalf("EnsureIssued read %v, want exactly the issued ticket %q", details.asked, detail.TicketID)
	}
	if len(objects.puts) != 1 || objects.puts[0].key != key {
		t.Fatalf("EnsureIssued stored %d objects, want one under %q", len(objects.puts), key)
	}

	// The on-demand path renders the same projection, so it finds the artefact already
	// there and writes nothing a second time.
	if _, err := archive.Ensure(t.Context(), detail); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if len(objects.puts) != 1 {
		t.Fatalf("the same sale was stored %d times, want once", len(objects.puts))
	}
}

// TestEnsureIssuedRefusesAPayloadThatNamesAnotherSale pins that the outbox payload is
// not trusted to decide which document goes under which key: a payload naming a
// different event than the stored ticket must not file this ticket's PDF in that
// event's namespace.
func TestEnsureIssuedRefusesAPayloadThatNamesAnotherSale(t *testing.T) {
	archive, objects, details := archiveFixture(t, time.Minute)

	_, err := archive.EnsureIssued(t.Context(), "00000000-0000-0000-0000-00000000ffff", details.detail.TicketID)
	if err == nil {
		t.Fatal("EnsureIssued accepted a payload whose event is not the ticket's event")
	}
	if !errors.Is(err, pdf.ErrUnavailable) {
		t.Fatalf("EnsureIssued error = %v, want ErrUnavailable", err)
	}
	if len(objects.puts) != 0 {
		t.Fatalf("a mismatched payload stored %d objects, want none", len(objects.puts))
	}
}

// TestDownloadURLAnswersAShortLivedSignedURL pins the serving path: the archive
// returns a pre-signed URL for the derived key with the configured lifetime, and it
// stores the artefact first when the object is missing, so a lost object or a crashed
// worker never leaves the buyer without their ticket.
func TestDownloadURLAnswersAShortLivedSignedURL(t *testing.T) {
	const ttl = 3 * time.Minute
	archive, objects, details := archiveFixture(t, ttl)
	detail := fixtureDetail()

	url, err := archive.DownloadURL(t.Context(), detail.TicketID)
	if err != nil {
		t.Fatalf("DownloadURL returned error: %v", err)
	}
	if url != objects.presigned {
		t.Fatalf("DownloadURL = %q, want the signed URL %q", url, objects.presigned)
	}
	if len(details.asked) != 1 || details.asked[0] != detail.TicketID {
		t.Fatalf("DownloadURL read %v, want the requested ticket %q", details.asked, detail.TicketID)
	}
	if len(objects.puts) != 1 {
		t.Fatalf("DownloadURL stored %d objects for a missing artefact, want one", len(objects.puts))
	}
	if len(objects.presign) != 1 {
		t.Fatalf("DownloadURL signed %d URLs, want one", len(objects.presign))
	}
	want, _ := pdf.ObjectKey(detail.Event.ID, detail.TicketID)
	if objects.presign[0].key != want {
		t.Fatalf("the signed key = %q, want %q", objects.presign[0].key, want)
	}
	if objects.presign[0].ttl != ttl {
		t.Fatalf("the signed lifetime = %s, want %s", objects.presign[0].ttl, ttl)
	}
}

// TestDownloadURLDoesNotRewriteAnExistingArtefact pins immutability through the
// serving path: a download of an artefact that is already stored signs a URL and
// writes nothing.
func TestDownloadURLDoesNotRewriteAnExistingArtefact(t *testing.T) {
	archive, objects, _ := archiveFixture(t, time.Minute)
	detail := fixtureDetail()

	key, err := pdf.ObjectKey(detail.Event.ID, detail.TicketID)
	if err != nil {
		t.Fatalf("ObjectKey returned error: %v", err)
	}
	objects.stored[key] = []byte("%PDF-1.4 as sold")

	url, err := archive.DownloadURL(t.Context(), detail.TicketID)
	if err != nil {
		t.Fatalf("DownloadURL returned error: %v", err)
	}
	if url == "" {
		t.Fatal("DownloadURL answered no URL for an artefact that exists")
	}
	if len(objects.puts) != 0 {
		t.Fatalf("DownloadURL wrote the object %d times although it exists", len(objects.puts))
	}
	if stored := string(objects.stored[key]); stored != "%PDF-1.4 as sold" {
		t.Fatalf("the stored artefact changed to %q", stored)
	}
}

// TestDownloadURLPassesTheTicketReadFailureThrough pins the 404: an unknown ticket is
// the reader's answer, and no URL is signed and nothing is written for it.
func TestDownloadURLPassesTheTicketReadFailureThrough(t *testing.T) {
	archive, objects, details := archiveFixture(t, time.Minute)
	details.err = tickets.ErrNotFound

	url, err := archive.DownloadURL(t.Context(), "00000000-0000-0000-0000-00000000dead")
	if !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("DownloadURL error = %v, want tickets.ErrNotFound", err)
	}
	if url != "" {
		t.Fatalf("DownloadURL answered the URL %q for an unknown ticket", url)
	}
	if len(objects.presign) != 0 || len(objects.puts) != 0 {
		t.Fatalf("an unknown ticket caused %d signatures and %d writes, want none", len(objects.presign), len(objects.puts))
	}
}

// TestDownloadURLFailsClosedWhenTheStoreCannotAnswer pins that a store that could not
// be asked is never read as "the artefact is missing": the archive reports an
// unavailable ticket and writes nothing, so a permission problem at the bucket cannot
// turn into an overwrite of the artefact it guards.
func TestDownloadURLFailsClosedWhenTheStoreCannotAnswer(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*fakeObjects)
	}{
		{"the existence question fails", func(o *fakeObjects) { o.existsErr = errors.New("access denied") }},
		{"the write fails", func(o *fakeObjects) { o.putErr = errors.New("bucket unavailable") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive, objects, details := archiveFixture(t, time.Minute)
			tc.apply(objects)

			url, err := archive.DownloadURL(t.Context(), details.detail.TicketID)
			if !errors.Is(err, pdf.ErrUnavailable) {
				t.Fatalf("DownloadURL error = %v, want ErrUnavailable", err)
			}
			if url != "" {
				t.Fatalf("DownloadURL answered the URL %q although storage failed", url)
			}
			if len(objects.presign) != 0 {
				t.Fatalf("storage failed and %d URLs were still signed", len(objects.presign))
			}
		})
	}

	// A signature failure is a failure too: the buyer gets an error, never a stale or
	// invented link.
	archive, objects, details := archiveFixture(t, time.Minute)
	objects.presignErr = errors.New("cannot sign")
	if url, err := archive.DownloadURL(t.Context(), details.detail.TicketID); !errors.Is(err, pdf.ErrUnavailable) || url != "" {
		t.Fatalf("DownloadURL = (%q, %v), want (\"\", ErrUnavailable)", url, err)
	}
}

// TestNewArchiveFailsClosed pins the construction rules: an archive without a reader
// or without a store can produce nothing, and a lifetime SigV4 cannot carry would sign
// a link that never works.
func TestNewArchiveFailsClosed(t *testing.T) {
	objects := newFakeObjects()
	details := &fakeDetails{detail: fixtureDetail()}

	if _, err := pdf.NewArchive(nil, objects, time.Minute); !errors.Is(err, pdf.ErrNotConfigured) {
		t.Errorf("NewArchive without a reader = %v, want ErrNotConfigured", err)
	}
	if _, err := pdf.NewArchive(details, nil, time.Minute); !errors.Is(err, pdf.ErrNotConfigured) {
		t.Errorf("NewArchive without a store = %v, want ErrNotConfigured", err)
	}
	for _, ttl := range []time.Duration{-time.Minute, time.Millisecond, 999 * time.Millisecond} {
		if _, err := pdf.NewArchive(details, objects, ttl); !errors.Is(err, pdf.ErrNotConfigured) {
			t.Errorf("NewArchive with a %s lifetime = %v, want ErrNotConfigured", ttl, err)
		}
	}

	// A zero lifetime means "the documented default", not "no expiry": the constant is
	// what a caller with nothing to say about the lifetime gets.
	archive, err := pdf.NewArchive(details, objects, 0)
	if err != nil {
		t.Fatalf("NewArchive with the default lifetime returned error: %v", err)
	}
	if _, err := archive.DownloadURL(t.Context(), details.detail.TicketID); err != nil {
		t.Fatalf("DownloadURL returned error: %v", err)
	}
	if len(objects.presign) != 1 || objects.presign[0].ttl != pdf.PresignTTL {
		t.Fatalf("the default lifetime is not %s: %+v", pdf.PresignTTL, objects.presign)
	}
	if pdf.PresignTTL <= 0 || pdf.PresignTTL > 24*time.Hour {
		t.Fatalf("pdf.PresignTTL = %s, want a short lifetime of at most a day", pdf.PresignTTL)
	}
}

// TestArchiveIsNilSafe pins the fail-closed zero value: a deployment that mounted
// nothing answers an error instead of dereferencing a nil archive.
func TestArchiveIsNilSafe(t *testing.T) {
	var archive *pdf.Archive
	if _, err := archive.Ensure(t.Context(), fixtureDetail()); !errors.Is(err, pdf.ErrNotConfigured) {
		t.Errorf("a nil archive Ensure error = %v, want ErrNotConfigured", err)
	}
	if _, err := archive.DownloadURL(t.Context(), "00000000-0000-0000-0000-00000000d0a1"); !errors.Is(err, pdf.ErrNotConfigured) {
		t.Errorf("a nil archive DownloadURL error = %v, want ErrNotConfigured", err)
	}
}

// TestArchiveErrorMessagesCarryNoCredential pins that a failure never quotes the
// signed URL it failed to produce: the link is a credential and nothing else may leak
// it into a log or a problem detail.
func TestArchiveErrorMessagesCarryNoCredential(t *testing.T) {
	archive, objects, details := archiveFixture(t, time.Minute)
	objects.presignErr = errors.New("cannot sign")

	_, err := archive.DownloadURL(t.Context(), details.detail.TicketID)
	if err == nil {
		t.Fatal("DownloadURL answered no error although signing failed")
	}
	if strings.Contains(err.Error(), "X-Amz-Signature") || strings.Contains(err.Error(), objects.presigned) {
		t.Fatalf("the error carries the signed URL: %v", err)
	}
}

// Gated bucket evidence. Set M3_S3_ENDPOINT and M3_S3_BUCKET to run it against a
// running Floci container (`make deps-up` starts the compose service on
// http://127.0.0.1:4566); the default unit suite skips it and needs no Docker.
// Credentials default to the dummy pair Floci accepts and can be overridden with
// M3_S3_ACCESS_KEY_ID, M3_S3_SECRET_ACCESS_KEY and M3_S3_REGION.
//
// It is the one test that proves the archive against a real object store: the document
// is rendered and stored, the store confirms it exists, and the pre-signed URL the
// download route answers really downloads those bytes over plain HTTP. It is safe to
// re-run: the key is fixed, and the immutability rule means a second run serves the
// artefact the first one wrote instead of accumulating copies.
func TestArchiveRoundTripAgainstFloci(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("M3_S3_ENDPOINT"))
	bucket := strings.TrimSpace(os.Getenv("M3_S3_BUCKET"))
	if endpoint == "" || bucket == "" {
		t.Skipf("M3_S3_ENDPOINT and M3_S3_BUCKET are not set: this test needs a running S3-compatible endpoint (make deps-up starts Floci)")
	}
	if !strings.HasPrefix(bucket, "cinema-m3-") {
		t.Fatalf("M3_S3_BUCKET = %q, want a name starting with %q so this test can only touch its own bucket", bucket, "cinema-m3-")
	}

	orDefault := func(value, fallback string) string {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
		return fallback
	}

	ctx := t.Context()
	objects, err := storage.NewS3(storage.Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		Region:          orDefault(os.Getenv("M3_S3_REGION"), "us-east-1"),
		AccessKeyID:     orDefault(os.Getenv("M3_S3_ACCESS_KEY_ID"), "floci"),
		SecretAccessKey: orDefault(os.Getenv("M3_S3_SECRET_ACCESS_KEY"), "floci"),
	})
	if err != nil {
		t.Fatalf("NewS3 returned error: %v", err)
	}
	if err := objects.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket(%q) returned error: %v", bucket, err)
	}
	t.Logf("bucket %q exists at %s (created when missing)", bucket, endpoint)

	archive, err := pdf.NewArchive(&fakeDetails{detail: fixtureDetail()}, objects, pdf.PresignTTL)
	if err != nil {
		t.Fatalf("NewArchive returned error: %v", err)
	}

	// The eager path: render the projection and store it, then confirm the store really
	// carries the object.
	detail := fixtureDetail()
	key, err := archive.Ensure(ctx, detail)
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	exists, err := objects.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists(%q) returned error: %v", key, err)
	}
	if !exists {
		t.Fatalf("Exists(%q) = false after the archive stored the artefact", key)
	}
	stored, err := objects.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q) returned error: %v", key, err)
	}
	if !bytes.HasPrefix(stored, []byte("%PDF-1.")) {
		t.Fatalf("the stored object starts with %q, want a PDF document", stored[:8])
	}
	t.Logf("stored %d PDF bytes at %q", len(stored), key)

	// The lazy path: the download route's URL really downloads those bytes.
	url, err := archive.DownloadURL(ctx, detail.TicketID)
	if err != nil {
		t.Fatalf("DownloadURL returned error: %v", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("pre-signed download failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pre-signed download status = %d, want 200", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "pdf") {
		t.Fatalf("pre-signed download content type = %q, want a PDF", contentType)
	}
	downloaded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the pre-signed download: %v", err)
	}
	if !bytes.Equal(downloaded, stored) {
		t.Fatalf("the pre-signed download returned %d bytes that differ from the %d stored bytes", len(downloaded), len(stored))
	}
	if text := visibleText(t, downloaded); !strings.Contains(text, detail.Event.FilmTitle) {
		t.Fatalf("the downloaded document does not print the film %q\nprinted text:\n%s", detail.Event.FilmTitle, text)
	}
	t.Logf("pre-signed download returned 200 with %d identical PDF bytes", len(downloaded))

	// Immutability against the real bucket: a projection that has moved on since must not
	// rewrite the artefact it already stored.
	changed := detail
	changed.Event.FilmTitle = "Otra película"
	changed.OrderAmountCents = 2500
	if _, err := archive.Ensure(ctx, changed); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	afterChange, err := objects.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q) returned error: %v", key, err)
	}
	if !bytes.Equal(afterChange, stored) {
		t.Fatalf("the stored artefact changed after a projection update: %d bytes became %d", len(stored), len(afterChange))
	}
	t.Logf("the stored artefact is unchanged after the projection moved on")
}
