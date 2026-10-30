// Behaviour tests for the SVG rendering of the signed ticket code: the image the
// ticket page shows and the PDF prints.
//
// They need no database and no HTTP: the token is a string and the rendering is a
// pure function of it. What they pin is the contract the page depends on — the SVG
// is the QR matrix of the served token and not a drawing of our own invention, the
// same token always produces the same bytes, the quiet zone a scanner needs is
// there, and the token's text is never embedded beside the modules.
package tickets_test

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"

	"cinema.local/ticketing/apps/api/internal/tickets"
)

// qrToken is a stand-in for the token a service signs. Its shape does not matter to
// the renderer, only that it is the value served to the page and to the door.
const qrToken = "djF8dGlja2V0LTE4fGV2ZW50LTE3fDIxfDEuc2lnbmF0dXJl"

// svgModule is one rectangle of the rendered grid, in modules.
type svgModule struct {
	x, y, width, height int
}

var (
	svgViewBoxPattern = regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`)
	svgDarkGroup      = regexp.MustCompile(`(?s)<g fill="#000000">(.*?)</g>`)
	svgDarkRect       = regexp.MustCompile(`<rect x="(\d+)" y="(\d+)" width="(\d+)" height="(\d+)"/>`)
	svgBackgroundRect = regexp.MustCompile(`<rect width="(\d+)" height="(\d+)" fill="#ffffff"/>`)
)

// renderQR renders the token and answers the document and its module grid.
func renderQR(t *testing.T) (string, [][]bool) {
	t.Helper()
	document, err := tickets.QRCodeSVG(qrToken)
	if err != nil {
		t.Fatalf("QRCodeSVG() = unexpected error %v", err)
	}
	return string(document), parseModules(t, string(document))
}

// parseModules reads the rendered rectangles back into a module grid, so the test
// compares the image cell by cell with the encoder's own matrix instead of trusting
// a count of rectangles.
func parseModules(t *testing.T, document string) [][]bool {
	t.Helper()
	match := svgViewBoxPattern.FindStringSubmatch(document)
	if match == nil {
		t.Fatalf("the SVG carries no viewBox: %s", document)
	}
	width, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("viewBox width %q: %v", match[1], err)
	}
	height, err := strconv.Atoi(match[2])
	if err != nil {
		t.Fatalf("viewBox height %q: %v", match[2], err)
	}

	group := svgDarkGroup.FindStringSubmatch(document)
	if group == nil {
		t.Fatalf("the SVG carries no dark module group: %s", document)
	}

	grid := make([][]bool, height)
	for row := range grid {
		grid[row] = make([]bool, width)
	}
	for _, rect := range svgDarkRect.FindAllStringSubmatch(group[1], -1) {
		x := mustAtoi(t, rect[1])
		y := mustAtoi(t, rect[2])
		w := mustAtoi(t, rect[3])
		h := mustAtoi(t, rect[4])
		if h != 1 {
			t.Fatalf("dark rect at %d,%d has height %d, want 1: one rect is one module row", x, y, h)
		}
		for column := x; column < x+w; column++ {
			if y >= height || column >= width {
				t.Fatalf("dark rect %d,%d +%d falls outside the %dx%d grid", x, y, w, width, height)
			}
			grid[y][column] = true
		}
	}
	return grid
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	number, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("atoi %q: %v", value, err)
	}
	return number
}

// TestTicketQRCodeSVGIsTheQRMatrix is the load-bearing rule: the image is the
// encoder's matrix for the served token, so the page and the PDF cannot show two
// different codes.
func TestTicketQRCodeSVGIsTheQRMatrix(t *testing.T) {
	_, grid := renderQR(t)

	// The PDF renders the same token at the same recovery level (pdf.QRCode), so the
	// matrix is pinned against that level rather than against whatever this renderer
	// happens to choose.
	code, err := qrcode.New(qrToken, qrcode.Medium)
	if err != nil {
		t.Fatalf("qrcode.New() = unexpected error %v", err)
	}
	matrix := code.Bitmap()

	if len(grid) != len(matrix) {
		t.Fatalf("SVG rows = %d, want %d", len(grid), len(matrix))
	}
	dark := 0
	for row := range matrix {
		if len(grid[row]) != len(matrix[row]) {
			t.Fatalf("SVG row %d width = %d, want %d", row, len(grid[row]), len(matrix[row]))
		}
		for column, expected := range matrix[row] {
			if grid[row][column] != expected {
				t.Fatalf("module %d,%d = %v, want %v: the SVG must be the QR matrix of the served token",
					column, row, grid[row][column], expected)
			}
			if expected {
				dark++
			}
		}
	}
	if dark == 0 {
		t.Fatal("the rendered code carries no dark module")
	}
}

// TestTicketQRCodeSVGIsTheSameOnEveryRead pins that the code is a function of the
// ticket and nothing else: no timestamp, no random identifier, no counter, so two
// reads of one ticket answer identical bytes.
func TestTicketQRCodeSVGIsTheSameOnEveryRead(t *testing.T) {
	first, err := tickets.QRCodeSVG(qrToken)
	if err != nil {
		t.Fatalf("QRCodeSVG() = unexpected error %v", err)
	}
	second, err := tickets.QRCodeSVG(qrToken)
	if err != nil {
		t.Fatalf("QRCodeSVG() = unexpected error %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("two renderings of one token differ: the image must be stable")
	}
}

// TestTicketQRCodeSVGKeepsTheQuietZone is what makes the image scannable: the
// encoder's border is white, and the document paints a light background behind it so
// a page in a dark colour scheme cannot invert the code.
func TestTicketQRCodeSVGKeepsTheQuietZone(t *testing.T) {
	document, grid := renderQR(t)

	background := svgBackgroundRect.FindStringSubmatch(document)
	if background == nil {
		t.Fatalf("the SVG paints no light background rect: %s", document)
	}
	if background[1] != background[2] || background[1] != strconv.Itoa(len(grid)) {
		t.Fatalf("background rect = %sx%s, want the whole %dx%d viewBox",
			background[1], background[2], len(grid), len(grid))
	}

	const quietZone = 4
	for row := 0; row < len(grid); row++ {
		for column := 0; column < len(grid[row]); column++ {
			inQuietZone := row < quietZone || column < quietZone ||
				row >= len(grid)-quietZone || column >= len(grid[row])-quietZone
			if inQuietZone && grid[row][column] {
				t.Fatalf("module %d,%d is dark inside the %d module quiet zone", column, row, quietZone)
			}
		}
	}
}

// TestTicketQRCodeSVGDoesNotEmbedTheToken pins that the document is a picture and not
// a place the credential is written in text: it is shown on a screen, forwarded and
// screenshotted, and its text has no business being there.
func TestTicketQRCodeSVGDoesNotEmbedTheToken(t *testing.T) {
	document, _ := renderQR(t)
	if strings.Contains(document, qrToken) {
		t.Fatal("the SVG embeds the token as text; the code must be the modules only")
	}
}

// TestTicketQRCodeSVGRefusesAnEmptyToken: a ticket with no code is not a ticket this
// package can draw, and an empty image would be a document the page would print.
func TestTicketQRCodeSVGRefusesAnEmptyToken(t *testing.T) {
	for _, token := range []string{"", "   "} {
		document, err := tickets.QRCodeSVG(token)
		if err == nil {
			t.Fatalf("QRCodeSVG(%q) = a document, want a refusal", token)
		}
		if document != nil {
			t.Fatalf("QRCodeSVG(%q) = %d bytes, want no document", token, len(document))
		}
		if !errors.Is(err, tickets.ErrUnavailable) {
			t.Fatalf("QRCodeSVG(%q) error = %v, want ErrUnavailable", token, err)
		}
	}
}
