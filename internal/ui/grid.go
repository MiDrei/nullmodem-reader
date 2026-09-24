package ui

import (
	"bytes"

	"git.maik.ch/nullmodem/kit/ansi"
)

// MessageGrid renders a QWK message body into the Grid matrix at the
// given width.
//
// Everything the reader displays becomes a Grid, art or not. A
// full-screen frontend needs to scroll a body, measure it, and paint
// a window onto it; doing that on a Grid means one implementation
// covers ANSI art, ASCII art and ordinary prose alike, instead of a
// scroll path for pictures and a separate one for text that would
// drift apart.
//
// text is CP437 with "\n" line endings, as qwk.ReadMessagesDAT
// returns it.
func MessageGrid(text string, width int) ansi.Grid {
	if width <= 0 {
		width = 80
	}
	// Art carries its own colors and cursor moves, so it goes through
	// the parser that understands them.
	if ansi.HasEscapeCodes(text) {
		return ansi.ParseGrid(text, width)
	}

	lines := layoutProse([]byte(text), width, ansi.IsPreformatted(text))

	g := ansi.NewGrid(width, max(len(lines), 1))
	for row, line := range lines {
		for col, ch := range line {
			if col >= width {
				break
			}
			// FG 7 / BG 0 is the CP437 terminal default, the same
			// blank ansi.NewGrid already filled the row with; only
			// the character differs.
			g.Cells[row*width+col] = ansi.Cell{Char: ch, FG: 7, BG: 0}
		}
	}
	return g
}

// layoutProse splits a CP437 body into display lines. Preformatted
// bodies (ASCII art, hand-aligned tables) are split on newlines only
// -- rewrapping them destroys the alignment they depend on, which is
// exactly what ansi.IsPreformatted exists to detect.
func layoutProse(text []byte, width int, preformatted bool) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(text, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if preformatted || len(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, wrapCP437(line, width)...)
	}
	return out
}

// wrapCP437 word-wraps one line of CP437 bytes to width.
//
// It works on bytes rather than runes on purpose: in CP437 every
// character is exactly one byte and exactly one column, so byte
// length is display width. Decoding to UTF-8 first and wrapping there
// would give the same answer at more cost -- and would then need
// re-encoding, since a Grid cell stores the CP437 byte.
//
// A word longer than the whole line (a URL, a run of block glyphs)
// is hard-broken rather than allowed to overflow: an overflowing line
// would be silently clipped by the Grid's own width.
func wrapCP437(line []byte, width int) [][]byte {
	var out [][]byte
	for len(line) > width {
		// Break at the last space that fits, so the break lands
		// between words where there is one.
		brk := bytes.LastIndexByte(line[:width+1], ' ')
		if brk <= 0 {
			brk = width // no space to break at: hard-break the word
		}
		out = append(out, bytes.TrimRight(line[:brk], " "))
		line = bytes.TrimLeft(line[brk:], " ")
	}
	return append(out, line)
}

// ScreenGrid parses a raw screen file (.ANS art, or plain text) into
// the Grid matrix. width should be 80 for classic BBS art: that is
// the canvas it was drawn for, and parsing at another width reflows
// it into nonsense.
func ScreenGrid(raw []byte, width int) ansi.Grid {
	if width <= 0 {
		width = 80
	}
	return ansi.ParseGrid(string(raw), width)
}
