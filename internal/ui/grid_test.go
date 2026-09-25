package ui

import (
	"strings"
	"testing"

	"git.maik.ch/nullmodem/kit/ansi"
)

// gridText reads one row back as a string, trailing blanks trimmed,
// so tests can assert on what a row says without caring about the
// padding the Grid keeps.
func gridRow(g ansi.Grid, row int) string {
	var b strings.Builder
	for col := 0; col < g.Width; col++ {
		b.WriteRune(ansi.Rune(g.Cells[row*g.Width+col].Char))
	}
	return strings.TrimRight(b.String(), " ")
}

func TestMessageGridWrapsProseAtWidth(t *testing.T) {
	g := MessageGrid(strings.Repeat("word ", 20), 20)

	if g.Width != 20 {
		t.Fatalf("Width = %d, want 20", g.Width)
	}
	for row := 0; row < g.Height; row++ {
		if got := gridRow(g, row); len(got) > 20 {
			t.Fatalf("row %d = %q, longer than the 20-column width", row, got)
		}
	}
	if g.Height < 4 {
		t.Fatalf("Height = %d, want the text spread over several rows", g.Height)
	}
}

func TestMessageGridBreaksBetweenWords(t *testing.T) {
	g := MessageGrid("alpha beta gamma delta", 12)

	first := gridRow(g, 0)
	if first != "alpha beta" {
		t.Fatalf("row 0 = %q, want the break to land between words", first)
	}
	if strings.HasPrefix(gridRow(g, 1), " ") {
		t.Fatalf("row 1 = %q, want the leading space eaten by the break", gridRow(g, 1))
	}
}

// A word longer than the line has to be broken somewhere; letting it
// run would just get it clipped by the Grid's width.
func TestMessageGridHardBreaksAnOverlongWord(t *testing.T) {
	g := MessageGrid(strings.Repeat("x", 25), 10)

	if g.Height != 3 {
		t.Fatalf("Height = %d, want 25 characters across 3 rows of 10", g.Height)
	}
	if got := gridRow(g, 0); got != strings.Repeat("x", 10) {
		t.Fatalf("row 0 = %q, want a full 10-character row", got)
	}
	if got := gridRow(g, 2); got != strings.Repeat("x", 5) {
		t.Fatalf("row 2 = %q, want the remaining 5 characters", got)
	}
}

func TestMessageGridKeepsCP437BytesForTheGlyphLookup(t *testing.T) {
	// 0xDB is the full block; the Grid must hold the raw byte so a
	// GUI can index a VGA font with it directly.
	g := MessageGrid("\xdb\xdb", 80)

	if g.Cells[0].Char != 0xDB {
		t.Fatalf("Cells[0].Char = %#x, want the raw CP437 byte 0xDB", g.Cells[0].Char)
	}
	if got := gridRow(g, 0); got != "██" {
		t.Fatalf("row 0 = %q, want the block glyphs", got)
	}
}

func TestMessageGridRoutesANSIThroughTheParser(t *testing.T) {
	g := MessageGrid("\x1b[1;31mX", 80)

	if g.Cells[0].Char != 'X' {
		t.Fatalf("Cells[0].Char = %q, want X with the escape consumed", g.Cells[0].Char)
	}
	if g.Cells[0].FG != 9 {
		t.Fatalf("Cells[0].FG = %d, want 9 (bright red)", g.Cells[0].FG)
	}
}

func TestMessageGridLeavesPreformattedTextUnwrapped(t *testing.T) {
	art := "+---+   +---+   +---+\n|  A|   |  B|   |  C|\n+---+   +---+   +---+"
	g := MessageGrid(art, 10)

	if g.Height != 3 {
		t.Fatalf("Height = %d, want the art's own 3 rows rather than a rewrap", g.Height)
	}
	// Kept whole on the 80-column canvas: the window clips what does
	// not fit when it draws, the grid itself never cuts art short.
	if got := gridRow(g, 0); got != "+---+   +---+   +---+" {
		t.Fatalf("row 0 = %q -- art should be kept whole, not reflowed or cut", got)
	}
}

func TestMessageGridLaysANSIArtOutAtEightyColumnsInANarrowerWindow(t *testing.T) {
	// A full 80-column row followed by a second one: at 78 columns the
	// first row would wrap and push everything below it down.
	art := "\x1b[0;36m" + strings.Repeat("\xdb", 80) + "\r\n\x1b[0;33mX"
	g := MessageGrid(art, 78)

	if g.Width != 80 {
		t.Fatalf("Width = %d, want the 80-column canvas", g.Width)
	}
	if g.Cells[80].Char != 'X' {
		t.Fatalf("row 2 col 1 = %q, want X -- the first row wrapped", g.Cells[80].Char)
	}
}

func TestMessageGridOnEmptyBodyStillHasARow(t *testing.T) {
	g := MessageGrid("", 80)

	if g.Height < 1 || g.Width != 80 {
		t.Fatalf("Grid = %dx%d, want a single blank row rather than an unusable empty grid", g.Width, g.Height)
	}
}

func TestScreenGridResolvesCursorAddressing(t *testing.T) {
	g := ScreenGrid([]byte("\x1b[2J\x1b[3;5HX"), 80)

	if g.Height < 3 {
		t.Fatalf("Height = %d, want at least 3 rows", g.Height)
	}
	if g.Cells[2*80+4].Char != 'X' {
		t.Fatalf("row 3 col 5 = %q, want the X the cursor was moved to", g.Cells[2*80+4].Char)
	}
}
