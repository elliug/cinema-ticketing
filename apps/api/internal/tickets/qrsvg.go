// The SVG rendering of the signed ticket code.
//
// The ticket page shows the code as an image and the PDF prints it. Both must be the
// same code, so neither renderer encodes anything: the API encodes the signed token
// once, here, and the page points at this document (GET /api/v1/tickets/{id}/qr.svg).
// The alternative — a QR library in the browser — would be a second implementation
// that could disagree with the printed copy, and a dependency the web does not need.
//
// The module matrix comes from github.com/skip2/go-qrcode, the same encoder the PDF
// renders with (pdf.QRCode), at the same recovery level, so a page and a printout of
// one ticket scan as one code. Nothing in this file writes a QR encoder: it maps the
// encoder's own bitmap onto rectangles.
//
// Two properties are deliberate:
//
//   - the document is a picture, not a data carrier in text. The token never appears
//     as a string in the markup, so a screenshot or a forwarded file contains the
//     modules and nothing that reads like an identifier;
//   - the rendering is a pure function of the token. No timestamp, no identifier, no
//     counter: two reads of one ticket answer identical bytes, which is what lets the
//     page and the PDF be compared, and what a cache could hold if the token were not
//     a credential. (It is one, so the route serves it with Cache-Control: no-store.)
package tickets

import (
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// qrSVGRecoveryLevel is the error-correction level of the rendered image. It is
// Medium, matching pdf.QRCode: the level is part of the code's identity, so a page and
// a printout encoded at different levels would be different images of the same ticket.
const qrSVGRecoveryLevel = qrcode.Medium

// qrSVGScale is the pixel size of one module in the document's intrinsic size. It does
// not change the matrix, only the size the image has when nothing scales it; the page
// sizes it in CSS and relies on crispEdges rather than on a large bitmap.
const qrSVGScale = 8

// QRCodeSVG renders a signed ticket token as an SVG document of one rectangle per run
// of dark modules.
//
// The bitmap the encoder answers already carries the quiet zone a scanner needs, and it
// is reproduced module for module: a run of adjacent dark modules in a row becomes one
// `<rect>` of that width, which is the same picture as one rectangle per module and a
// third of the markup. A light background rectangle is painted behind every module so
// the code stays dark-on-light even on a page rendered in a dark colour scheme — an
// inverted QR code is a code many scanners refuse.
//
// A blank token is [ErrUnavailable]: a ticket without a code is not a ticket anyone can
// scan, and a renderer must fail rather than serve an empty image a page would print.
func QRCodeSVG(token string) ([]byte, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: the ticket has no signed code to render", ErrUnavailable)
	}
	code, err := qrcode.New(token, qrSVGRecoveryLevel)
	if err != nil {
		return nil, fmt.Errorf("%w: the ticket code cannot be rendered as a QR image: %w", ErrUnavailable, err)
	}

	// Bitmap includes the quiet zone, indexed [y][x] with true for a dark module.
	bitmap := code.Bitmap()
	modules := len(bitmap)
	pixels := modules * qrSVGScale

	var document strings.Builder
	document.Grow(modules * modules)
	fmt.Fprintf(&document,
		`<svg xmlns="http://www.w3.org/2000/svg" version="1.1" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`+"\n",
		modules, modules, pixels, pixels)
	fmt.Fprintf(&document, `<rect width="%d" height="%d" fill="#ffffff"/>`+"\n", modules, modules)
	document.WriteString(`<g fill="#000000">` + "\n")
	for y, row := range bitmap {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			// One rectangle per horizontal run of dark modules: the same picture with
			// far less markup than one rectangle per module.
			run := 0
			for x+run < len(row) && row[x+run] {
				run++
			}
			fmt.Fprintf(&document, `<rect x="%d" y="%d" width="%d" height="1"/>`, x, y, run)
			x += run
		}
		document.WriteString("\n")
	}
	document.WriteString("</g>\n</svg>\n")
	return []byte(document.String()), nil
}
