// The layout of the ticket PDF.
//
// A file comment rather than the package documentation: the package's contract —
// one projection, one artefact, one URL — is documented in doc.go, and this file is
// only about how the value is put on the page. Every string printed here comes from
// the projection or from one of its derived presentations, and the assertions in
// render_test.go read the page back the way a viewer does.

package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
	qrcode "github.com/skip2/go-qrcode"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// ContentType is the media type the rendered bytes are stored and served as. It is
// exported because the object store records it with the artefact, and a bucket that
// stored a ticket as an octet stream would make the browser download a file it
// cannot display.
const ContentType = "application/pdf"

// ErrIncomplete reports a ticket detail the document cannot be printed from: no
// ticket identity, no seat, or no signed code. It is a fail-closed answer — a
// document missing any of them is a ticket nobody can redeeem, and printing it would
// hand the buyer a useless page.
var ErrIncomplete = errors.New("pdf: the ticket detail is incomplete")

// Page geometry, in millimetres (A4 is 210 x 297).
const (
	// pageWidth is the A4 width the layout is measured against.
	pageWidth = 210.0
	// margin is the breathing room on every side.
	margin = 18.0
	// labelWidth is the column the field names occupy.
	labelWidth = 34.0
	// titleWidth bounds the film title so it can never run under the QR code.
	titleWidth = 118.0
)

// The QR code settings. The recovery level tolerates a creased or partly shadowed
// code, and the pixel size is generous enough that the fixed 46 mm print stays
// scannable; the encoder scales the module grid to fit.
const (
	qrRecoveryLevel = qrcode.Medium
	qrImageSize     = 512
	qrPrintSide     = 46.0
)

// QRCode renders the signed ticket code as a PNG image.
//
// It is exported because a surface that shows the code as an image (this document,
// and any later printed copy) needs exactly this and must not encode the token
// differently: the bytes are a QR code of the token the service signed, nothing
// else. A blank token is [ErrIncomplete] rather than an empty image.
func QRCode(token string) ([]byte, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: the ticket has no signed code to encode", ErrIncomplete)
	}
	image, err := qrcode.Encode(token, qrRecoveryLevel, qrImageSize)
	if err != nil {
		return nil, fmt.Errorf("pdf: encode the ticket code: %w", err)
	}
	return image, nil
}

// Render lays out one ticket detail as a PDF document.
//
// It is a pure function of the projection: the same detail always renders the same
// bytes, which is what lets the eager worker and the on-demand download path agree
// without comparing notes — both call this. The local screening time comes from the
// venue's timezone and the amount from the money that was actually taken, so neither
// can be recomputed here.
//
// A detail that cannot be printed is [ErrIncomplete], and a venue timezone that does
// not resolve is an error: no bytes are returned in either case, so a caller can
// never store or serve a half-printed ticket.
func Render(detail tickets.TicketDetail) ([]byte, error) {
	if err := requirePrintable(detail); err != nil {
		return nil, err
	}
	// The venue's own hour, computed outside the document so a broken zone is an
	// error instead of a page printed with a guess.
	startsAtLocal, err := detail.Event.LocalStartsAt()
	if err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	image, err := QRCode(detail.QRToken)
	if err != nil {
		return nil, err
	}

	document := fpdf.New("P", "mm", "A4", "")
	document.SetMargins(margin, margin, margin)
	document.SetCreator("cinema-ticketing", true)
	document.SetTitle("Ticket "+detail.TicketID, true)
	// The document's own timestamps come from the projection, not from the clock, so
	// rendering the same detail twice produces the same bytes: a retry that has to
	// render again (a lost object, a crashed worker) writes an artefact identical to
	// the one it would have written the first time, and the file records when the
	// ticket was issued rather than when it was printed.
	document.SetCreationDate(detail.IssuedAt)
	document.SetModificationDate(detail.IssuedAt)
	// Fonts and images live in maps inside the library, and their object order decides
	// the bytes of the file. Sorting the catalogue is what makes two renders of one
	// projection byte-identical instead of merely equivalent.
	document.SetCatalogSort(true)
	document.AddPage()

	// The core fonts are single-byte, so UTF-8 is translated to cp1252 once and every
	// string below is printed through it.
	translate := document.UnicodeTranslatorFromDescriptor("")

	// The venue, in a quiet grey: the document is about the film, not the brand.
	document.SetTextColor(90, 90, 90)
	document.SetFont("Helvetica", "B", 13)
	document.SetXY(margin, margin)
	document.CellFormat(titleWidth, 8, translate(detail.Event.CinemaName), "", 0, "L", false, 0, "")

	// The film title, the one thing a buyer looks for.
	document.SetTextColor(0, 0, 0)
	document.SetFont("Helvetica", "B", 24)
	document.SetXY(margin, margin+12)
	document.MultiCell(titleWidth, 9, translate(detail.Event.FilmTitle), "", "L", false)

	// The code sits top right, clear of the title, and is the only image in the
	// document.
	qrX := pageWidth - margin - qrPrintSide
	qrY := margin + 10
	document.RegisterImageOptionsReader("ticket-qr", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(image))
	document.ImageOptions("ticket-qr", qrX, qrY, qrPrintSide, qrPrintSide, false,
		fpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	// A rule separates the screening from its details.
	ruleY := qrY + qrPrintSide + 6
	if titleBottom := document.GetY(); titleBottom > ruleY {
		ruleY = titleBottom + 6
	}
	document.SetDrawColor(200, 200, 200)
	document.Line(margin, ruleY, pageWidth-margin, ruleY)

	y := ruleY + 6
	rows := [][2]string{
		{"Room", detail.Event.RoomName},
		{"Screening", startsAtLocal},
		{"Seat", seatLine(detail.Seats)},
		{"Order", detail.ReservationID},
		{"Total", detail.OrderAmountText()},
		{"Ticket", detail.TicketID},
	}
	for _, row := range rows {
		document.SetTextColor(110, 110, 110)
		document.SetFont("Helvetica", "B", 10)
		document.SetXY(margin, y)
		document.CellFormat(labelWidth, 8, translate(row[0]), "", 0, "L", false, 0, "")

		document.SetTextColor(0, 0, 0)
		document.SetFont("Helvetica", "", 11)
		document.SetXY(margin+labelWidth, y)
		document.CellFormat(pageWidth-margin*2-labelWidth, 8, translate(row[1]), "", 0, "L", false, 0, "")
		y += 8
	}

	// A ticket that no longer admits its holder says so on its own page, because the
	// stored artefact never changes: this line is the projection's answer at the
	// moment it was printed, and the door re-checks the code.
	if reason := detail.InvalidReason(); reason != "" {
		y += 4
		document.SetTextColor(150, 0, 0)
		document.SetFont("Helvetica", "B", 12)
		document.SetXY(margin, y)
		document.CellFormat(pageWidth-margin*2, 8, translate("Not valid for entry: "+reason), "", 0, "L", false, 0, "")
		y += 10
	}

	// The legend under the code: what to do with it at the door.
	document.SetTextColor(110, 110, 110)
	document.SetFont("Helvetica", "", 9)
	document.SetXY(margin, y+8)
	document.MultiCell(pageWidth-margin*2, 5,
		translate("Present this code at the door. The code names this ticket; the venue checks it."), "", "L", false)

	var buffer bytes.Buffer
	if err := document.Output(&buffer); err != nil {
		return nil, fmt.Errorf("pdf: write the ticket document: %w", err)
	}
	return buffer.Bytes(), nil
}

// requirePrintable refuses a projection the document cannot honestly describe.
//
// The ticket identity and the signed code are what make the page a ticket rather than
// a leaflet, and the seat (with its painted labels) is what it admits. The order
// reference is required for the same reason: a document that cannot be traced back to
// a sale is not a record of one.
func requirePrintable(detail tickets.TicketDetail) error {
	if strings.TrimSpace(detail.TicketID) == "" {
		return fmt.Errorf("%w: the ticket has no identifier", ErrIncomplete)
	}
	if strings.TrimSpace(detail.ReservationID) == "" {
		return fmt.Errorf("%w: the ticket has no order to belong to", ErrIncomplete)
	}
	if strings.TrimSpace(detail.QRToken) == "" {
		return fmt.Errorf("%w: the ticket has no signed code", ErrIncomplete)
	}
	if detail.IssuedAt.IsZero() {
		// The document's own timestamps come from the issue instant, which is what makes
		// rendering the same detail twice produce the same bytes.
		return fmt.Errorf("%w: the ticket has no issue instant", ErrIncomplete)
	}
	if len(detail.Seats) == 0 {
		return fmt.Errorf("%w: the ticket admits no seat", ErrIncomplete)
	}
	for _, seat := range detail.Seats {
		if strings.TrimSpace(seat.RowLabel) == "" || strings.TrimSpace(seat.SeatNumber) == "" {
			return fmt.Errorf("%w: the ticket's seat has no row and seat label at the door", ErrIncomplete)
		}
	}
	return nil
}

// seatLine renders the seat as it is printed: the row and number painted in the room,
// in the projection's order. This milestone always answers exactly one seat, and the
// join is there so a later ticket that admits several prints all of them rather than
// the first.
func seatLine(seats []tickets.Seat) string {
	labels := make([]string, 0, len(seats))
	for _, seat := range seats {
		labels = append(labels, fmt.Sprintf("Row %s, seat %s", seat.RowLabel, seat.SeatNumber))
	}
	return strings.Join(labels, "; ")
}
