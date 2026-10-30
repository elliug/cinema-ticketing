// Behaviour tests for the ticket PDF renderer.
//
// They need no bucket and no database: a renderer is a pure function of the ticket
// detail projection, so the test reads the document back and asserts what a human
// and a scanner would see — the visible strings it prints, the venue's local time
// rather than the machine's, the order amount, and an image where the QR goes.
//
// The assertions read the PDF the way a viewer does: the page content streams are
// inflated and their text-showing operators are decoded, so the test proves what is
// printed instead of that some bytes were produced. Core fonts encode text as
// code page 1252, which is why the extractor decodes that code page: "La película"
// is exactly the kind of title a cp1252-aware renderer must not mangle.
package pdf_test

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"cinema.local/ticketing/apps/api/internal/pdf"
	"cinema.local/ticketing/apps/api/internal/tickets"
)

// screening is the instant every fixture renders: 18:30 UTC is 20:30 in Madrid, so
// a rendering that used the machine's zone or UTC cannot pass by accident.
var screening = time.Date(2026, time.May, 4, 18, 30, 0, 0, time.UTC)

// fixtureDetail is the projection a real checkout produces for one ticket of a
// two-seat order in a Madrid venue: one ticket is one seat, and the money is what
// the whole order cost.
func fixtureDetail() tickets.TicketDetail {
	return tickets.TicketDetail{
		TicketID:      "00000000-0000-0000-0000-00000000d0a1",
		Status:        tickets.TicketStatusIssued,
		IssuedAt:      screening.Add(-48 * time.Hour),
		ReservationID: "00000000-0000-0000-0000-00000000d0a2",
		Event: tickets.EventDetail{
			ID:         "00000000-0000-0000-0000-00000000d0a3",
			FilmTitle:  "La película",
			StartsAt:   screening,
			RoomName:   "Sala 1",
			CinemaName: "Cine Centro",
			Timezone:   "Europe/Madrid",
			Status:     "on_sale",
		},
		Seats: []tickets.Seat{
			{EventSeatID: 11, RowLabel: "A", SeatNumber: "1"},
		},
		OrderAmountCents: 1998,
		QRToken:          "djF8MDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDBkMGExfDAwMDAwMDAwLTAwMDAtMDAwMC0wMDAwLTAwMDAwMDAwZDBhM3wxMS5zaWduYXR1cmU",
	}
}

// TestRenderPrintsTheTicketDetail is the contract of the document: every fact the
// projection carries is printed, and nothing is invented. The strings are asserted
// literally, because a PDF that renders the machine's timezone, the wrong seat or a
// recomputed amount is worse than one that was never printed.
func TestRenderPrintsTheTicketDetail(t *testing.T) {
	detail := fixtureDetail()

	document, err := pdf.Render(detail)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if !bytes.HasPrefix(document, []byte("%PDF-1.")) {
		t.Fatalf("document starts with %q, want a PDF header", firstBytes(document, 8))
	}
	if !bytes.Contains(document, []byte("%%EOF")) {
		t.Fatal("document carries no EOF trailer")
	}

	text := visibleText(t, document)

	// The venue and the room: which building and which door.
	wants := []string{
		"Cine Centro",
		"Sala 1",
		// The film, including a non-ASCII letter, so the document is readable rather
		// than a row of question marks.
		"La película",
		// The screening in the venue's own timezone, with an explicit offset.
		"2026-05-04T20:30:00+02:00",
		// The seat this ticket admits, with the labels painted at the door.
		"Row A, seat 1",
		// The money taken for the whole order, without an invented currency.
		"19.98",
		// The order reference and the ticket identifier, so a document found on the
		// floor can be traced back to a sale.
		detail.ReservationID,
		detail.TicketID,
	}
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("the printed text does not contain %q\nprinted text:\n%s", want, text)
		}
	}

	// v1 stores no currency, so the document must not claim one.
	for _, forbidden := range []string{"€", "EUR", "$"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the printed text carries the currency %q, but this milestone stores none\nprinted text:\n%s", forbidden, text)
		}
	}

	// Nothing in the projection is a fact this document may omit: the seat is the
	// ticket's own, so a document with no seat would admit nobody in particular.
	if strings.Count(text, detail.TicketID) < 1 {
		t.Errorf("the ticket id is printed %d times, want at least once", strings.Count(text, detail.TicketID))
	}

	// The QR is an image, not text: a code printed as characters cannot be scanned.
	if !bytes.Contains(document, []byte("/Subtype /Image")) {
		t.Fatal("the document embeds no image, so it carries no QR code")
	}
}

// TestRenderIsDeterministicAndCarriesTheServedCode pins that the same projection
// renders the same document, and that the code the service signed is what the
// document carries: a different QR token is a different document.
func TestRenderIsDeterministicAndCarriesTheServedCode(t *testing.T) {
	detail := fixtureDetail()

	first, err := pdf.Render(detail)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	second, err := pdf.Render(detail)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("rendering the same projection twice produced two different documents")
	}

	other := detail
	other.QRToken = detail.QRToken + "-other"
	third, err := pdf.Render(other)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if bytes.Equal(first, third) {
		t.Fatal("a different QR token produced the same document, so the code is not what the PDF carries")
	}
}

// TestQRCodeEncodesTheServedToken pins the image itself: the PNG the renderer
// embeds is a QR code of exactly the token the service signed, not of some other
// identifier of the ticket.
func TestQRCodeEncodesTheServedToken(t *testing.T) {
	detail := fixtureDetail()

	image, err := pdf.QRCode(detail.QRToken)
	if err != nil {
		t.Fatalf("QRCode returned error: %v", err)
	}
	if !bytes.HasPrefix(image, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("QRCode returned %q, want a PNG image", firstBytes(image, 8))
	}

	// The encoder is deterministic for the same content, so identical bytes prove
	// the content was this token and not a value derived from it.
	want, err := qrcode.Encode(detail.QRToken, qrRecoveryLevel, qrImageSize)
	if err != nil {
		t.Fatalf("encode the expected QR code: %v", err)
	}
	if !bytes.Equal(image, want) {
		t.Fatal("the QR image does not encode the served token")
	}

	other, err := pdf.QRCode(detail.TicketID)
	if err != nil {
		t.Fatalf("QRCode returned error: %v", err)
	}
	if bytes.Equal(image, other) {
		t.Fatal("the QR image is the same for the token and for the bare ticket id")
	}
}

// qrRecoveryLevel and qrImageSize are the encoder settings the renderer documents;
// the test names them so a change to either is a deliberate change to the artefact.
const (
	qrRecoveryLevel = qrcode.Medium
	qrImageSize     = 512
)

// TestRenderMarksATicketThatDoesNotAdmitItsHolder pins that a withdrawn ticket and a
// cancelled screening are printed as such. The stored artefact never changes, so the
// document it was written from has to say what the projection said at that moment
// instead of looking like a valid ticket forever.
func TestRenderMarksATicketThatDoesNotAdmitItsHolder(t *testing.T) {
	void := fixtureDetail()
	void.Status = tickets.TicketStatusVoid

	document, err := pdf.Render(void)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if text := visibleText(t, document); !strings.Contains(text, "Not valid for entry: "+tickets.InvalidReasonVoided) {
		t.Errorf("a withdrawn ticket is not printed as withdrawn\nprinted text:\n%s", text)
	}

	cancelled := fixtureDetail()
	cancelled.Event.Status = tickets.EventStatusCancelled

	document, err = pdf.Render(cancelled)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if text := visibleText(t, document); !strings.Contains(text, "Not valid for entry: "+tickets.InvalidReasonEventCancelled) {
		t.Errorf("a ticket of a cancelled screening is not printed as such\nprinted text:\n%s", text)
	}

	// A valid ticket says nothing about validity: the absence of the line is what a
	// reader sees as "nothing wrong here".
	document, err = pdf.Render(fixtureDetail())
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if text := visibleText(t, document); strings.Contains(text, "Not valid for entry") {
		t.Errorf("a valid ticket is printed as invalid\nprinted text:\n%s", text)
	}
}

// TestRenderRefusesAnIncompleteProjection pins fail-closed rendering: a document
// without its ticket identity, its seat or its code would be a ticket nobody can
// redeem, so it is refused instead of printed.
func TestRenderRefusesAnIncompleteProjection(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*tickets.TicketDetail)
	}{
		{"no ticket id", func(d *tickets.TicketDetail) { d.TicketID = "" }},
		{"no seat", func(d *tickets.TicketDetail) { d.Seats = nil }},
		{"no signed code", func(d *tickets.TicketDetail) { d.QRToken = "" }},
		// The document records its own timestamps from the issue instant, so a detail
		// without one would render differently on every attempt.
		{"no issue instant", func(d *tickets.TicketDetail) { d.IssuedAt = time.Time{} }},
		{"no order", func(d *tickets.TicketDetail) { d.ReservationID = "" }},
		{"a seat with no painted label", func(d *tickets.TicketDetail) { d.Seats = []tickets.Seat{{EventSeatID: 11}} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := fixtureDetail()
			tc.mutate(&detail)

			document, err := pdf.Render(detail)
			if err == nil {
				t.Fatalf("Render printed %d bytes of a ticket it should have refused", len(document))
			}
			if !errors.Is(err, pdf.ErrIncomplete) {
				t.Fatalf("Render error = %v, want ErrIncomplete", err)
			}
			if len(document) != 0 {
				t.Fatalf("a refused render returned %d bytes, want none", len(document))
			}
		})
	}
}

// TestRenderRefusesAnUnusableVenueTimezone pins the rule the detail route already
// enforces: a ticket printed with the wrong hour is worse than one not printed, so an
// unresolvable venue zone is an error rather than a silent UTC answer.
func TestRenderRefusesAnUnusableVenueTimezone(t *testing.T) {
	detail := fixtureDetail()
	detail.Event.Timezone = "Mars/Olympus_Mons"

	document, err := pdf.Render(detail)
	if err == nil {
		t.Fatalf("Render printed %d bytes of a ticket whose venue timezone cannot be resolved", len(document))
	}
	if len(document) != 0 {
		t.Fatalf("a refused render returned %d bytes, want none", len(document))
	}
}

// visibleText returns the text the document shows, decoded from the PDF's own
// encoding. Every stream is considered, because the page content is compressed and
// the fonts, images and metadata are not the text anybody reads.
func visibleText(t *testing.T, document []byte) string {
	t.Helper()
	var shown strings.Builder
	for _, payload := range streamPayloads(document) {
		content := payload
		if inflated, err := inflate(payload); err == nil {
			content = inflated
		}
		for _, literal := range textLiterals(content) {
			shown.WriteString(decodeCP1252(literal))
			shown.WriteString("\n")
		}
	}
	return shown.String()
}

// streamPayloads returns the bytes between every "stream" and the "endstream" that
// closes it.
func streamPayloads(document []byte) [][]byte {
	var payloads [][]byte
	for offset := 0; offset < len(document); {
		start := bytes.Index(document[offset:], []byte("stream"))
		if start < 0 {
			break
		}
		start += offset + len("stream")
		if start < len(document) && document[start] == '\r' {
			start++
		}
		if start < len(document) && document[start] == '\n' {
			start++
		}
		end := bytes.Index(document[start:], []byte("endstream"))
		if end < 0 {
			break
		}
		payloads = append(payloads, document[start:start+end])
		offset = start + end + len("endstream")
	}
	return payloads
}

// inflate decompresses a zlib stream. fpdf compresses page content by default, so
// every assertion below reads the page the way a viewer would.
func inflate(payload []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var out bytes.Buffer
	if _, err := out.ReadFrom(reader); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// textLiterals returns the string operands of the text-showing operators, which is
// what a PDF viewer paints. A literal is collected only when the operator after it
// shows text ("Tj", or a "TJ" array), so the same parentheses in a font dictionary
// or a document property are not mistaken for visible text.
func textLiterals(content []byte) [][]byte {
	var literals [][]byte
	for index := 0; index < len(content); index++ {
		if content[index] != '(' {
			continue
		}
		literal, next := readLiteral(content, index)
		index = next

		operator := next + 1
		for operator < len(content) && (content[operator] == ' ' || content[operator] == '\n' || content[operator] == '\r') {
			operator++
		}
		shows := bytes.HasPrefix(content[operator:], []byte("Tj"))
		if !shows && bytes.HasPrefix(content[operator:], []byte("]")) {
			shows = bytes.Contains(content[operator:], []byte("TJ"))
		}
		if shows {
			literals = append(literals, literal)
		}
	}
	return literals
}

// readLiteral reads one PDF string literal and returns it with the index of its
// closing parenthesis.
func readLiteral(content []byte, open int) ([]byte, int) {
	var literal []byte
	for index := open + 1; index < len(content); index++ {
		switch content[index] {
		case '\\':
			if index+1 >= len(content) {
				return literal, index
			}
			index++
			switch content[index] {
			case 'n':
				literal = append(literal, '\n')
			case 'r':
				literal = append(literal, '\r')
			case 't':
				literal = append(literal, '\t')
			case 'b':
				literal = append(literal, '\b')
			case 'f':
				literal = append(literal, '\f')
			default:
				literal = append(literal, content[index])
			}
		case ')':
			return literal, index
		default:
			literal = append(literal, content[index])
		}
	}
	return literal, len(content)
}

// cp1252High maps the code points Windows-1252 adds above Latin-1.
var cp1252High = map[byte]rune{
	0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†', 0x87: '‡',
	0x88: 'ˆ', 0x89: '‰', 0x8a: 'Š', 0x8b: '‹', 0x8c: 'Œ', 0x8e: 'Ž', 0x91: '‘',
	0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—', 0x98: '˜',
	0x99: '™', 0x9a: 'š', 0x9b: '›', 0x9c: 'œ', 0x9e: 'ž', 0x9f: 'Ÿ',
}

// decodeCP1252 turns the bytes a core-font PDF carries back into text, so "La
// película" is compared as the string a reader sees.
func decodeCP1252(literal []byte) string {
	var text strings.Builder
	for _, char := range literal {
		if mapped, ok := cp1252High[char]; ok {
			text.WriteRune(mapped)
			continue
		}
		text.WriteRune(rune(char))
	}
	return text.String()
}

// firstBytes keeps a failure message readable.
func firstBytes(data []byte, count int) string {
	if len(data) < count {
		return fmt.Sprintf("%q", data)
	}
	return fmt.Sprintf("%q", data[:count])
}
