// Package ui renders what the reader shows, from the same ansi.Grid
// matrix NullModem BBS itself parses screens into.
//
// The Grid is the single representation of screen content; everything
// here is a blitter over it. The BBS's own ansi.Grid.Encode is one
// such blitter, aimed at a terminal that speaks CP437 natively
// (SyncTERM, PuTTY over telnet). A local reader runs in a UTF-8
// terminal instead, which would render those raw bytes as mojibake,
// so this package provides the blitter for that target: same matrix,
// same colors, glyphs translated to UTF-8. A future GUI frontend is a
// third blitter over the identical Grid, drawing each cell's raw
// CP437 byte from an embedded VGA font.
package ui

import (
	"strconv"
	"strings"

	"git.maik.ch/nullmodem/kit/ansi"
)

// Reset returns the terminal to its default colors.
const Reset = "\x1b[0m"

// TerminalOptions control how a Grid is blitted to a UTF-8 terminal.
type TerminalOptions struct {
	// TrimTrailingBlanks stops each row at its last non-blank cell
	// rather than painting the full width. This is what a reader
	// usually wants -- it keeps art from stamping a background color
	// across the rest of the user's window -- but a caller compositing
	// a Grid into a fixed-size region wants the full rows.
	TrimTrailingBlanks bool
}

// GridToTerminal renders g for a modern UTF-8 terminal: one SGR run
// per color change, CP437 bytes mapped to their Unicode glyphs, rows
// separated by newlines, and a reset at the end so the caller's
// prompt comes back in default colors.
//
// Bright foregrounds are emitted as "1;3x" rather than the
// 90-97 aixterm codes. Both are widely supported, but the bold form
// is what BBS art itself uses and what ansi.Grid round-trips through,
// so staying with it keeps one spelling across the whole system.
//
// Background color is only emitted when it is not black. Black is the
// CP437 terminal default, and painting it explicitly would stamp
// opaque rectangles over a terminal whose own background is anything
// else -- the same reasoning ansi.ToHTML applies for the web preview.
func GridToTerminal(g ansi.Grid, opts TerminalOptions) string {
	var b strings.Builder
	b.Grow(g.Width * g.Height * 2)

	for row := 0; row < g.Height; row++ {
		last := g.Width - 1
		if opts.TrimTrailingBlanks {
			last = lastNonBlankCol(g, row)
		}
		prevFG, prevBG := -1, -1
		for col := 0; col <= last; col++ {
			cell := g.Cells[row*g.Width+col]
			if cell.FG != prevFG || cell.BG != prevBG {
				b.WriteString(sgr(cell.FG, cell.BG))
				prevFG, prevBG = cell.FG, cell.BG
			}
			b.WriteRune(ansi.Rune(cell.Char))
		}
		// Reset before the newline: a row ending in a background color
		// would otherwise bleed that color across the rest of the line
		// in terminals that fill to the right edge on scroll.
		if prevFG != -1 {
			b.WriteString(Reset)
		}
		if row < g.Height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// lastNonBlankCol returns the index of the rightmost cell in row that
// carries something worth drawing, or -1 for an entirely blank row.
// A cell counts as blank only if it is a space on a black background:
// a space on a colored background is a drawn block, not empty space.
func lastNonBlankCol(g ansi.Grid, row int) int {
	for col := g.Width - 1; col >= 0; col-- {
		c := g.Cells[row*g.Width+col]
		if c.Char != ' ' || c.BG != 0 {
			return col
		}
	}
	return -1
}

func sgr(fg, bg int) string {
	var b strings.Builder
	b.WriteString("\x1b[0")
	if fg >= 8 {
		b.WriteString(";1")
	}
	b.WriteString(";")
	b.WriteString(strconv.Itoa(30 + fg%8))
	if bg%8 != 0 {
		b.WriteString(";")
		b.WriteString(strconv.Itoa(40 + bg%8))
	}
	b.WriteString("m")
	return b.String()
}

// RenderScreen is the whole path from a raw screen file to terminal
// output: parse the CP437/ANSI bytes into the Grid matrix, then blit
// the Grid. width is the column count to parse at -- 80 for classic
// BBS art, which is what nearly everything in circulation was drawn
// for.
//
// Passing the raw bytes through unparsed would mostly work on a
// modern terminal and would be wrong in the ways that matter: the
// CP437 bytes would arrive as mojibake, and cursor-addressed art
// (which does not draw top-to-bottom) would land in the wrong places
// or scroll away. Going through the Grid resolves all of that into a
// finished picture first.
func RenderScreen(raw []byte, width int) string {
	return GridToTerminal(ansi.ParseGrid(string(raw), width), TerminalOptions{TrimTrailingBlanks: true})
}

// RenderMessageBody renders a QWK message body. Bodies are a mix:
// some are plain prose, some are ANSI art, some are ASCII art that
// only survives if left alone. The distinction matters because
// re-wrapping art destroys it while leaving prose unwrapped makes it
// unreadable in a narrow window.
//
// ansi.HasEscapeCodes and ansi.IsPreformatted are the BBS's own
// heuristics for telling those apart, reused here so a message looks
// the same in the reader as it does on the board.
//
// text arrives as CP437 bytes with "\n" line endings (qwk's
// ReadMessagesDAT has already converted the format's own separator).
func RenderMessageBody(text string, width int) string {
	if ansi.HasEscapeCodes(text) {
		return RenderScreen([]byte(text), width)
	}

	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	preformatted := ansi.IsPreformatted(text)
	for _, line := range lines {
		decoded := decodeLine(line)
		if preformatted || len([]rune(decoded)) <= width {
			out = append(out, decoded)
			continue
		}
		out = append(out, ansi.WrapText(decoded, width)...)
	}
	return strings.Join(out, "\n")
}

// decodeLine converts one line of CP437 to UTF-8. It goes line by
// line rather than handing the whole body to ansi.DecodeCP437 at
// once because that function maps 0x0A to CP437's "◙" glyph -- right
// for art, wrong for the line breaks a message body is built from.
func decodeLine(line string) string {
	return ansi.DecodeCP437([]byte(strings.TrimRight(line, "\r")))
}

// DecodeField converts one QWK header field -- To, From, Subject, a
// conference name, a QWKE kludge value -- from CP437 to UTF-8.
//
// These are as much CP437 as a message body is, and skipping them is
// an easy oversight because they are so often pure ASCII: the
// mojibake only shows up once someone writes from a system that uses
// the high half, which for a German-language board is immediately.
//
// Unlike a body, a field is a single line with no line breaks to
// protect, so it can go through DecodeCP437 whole.
func DecodeField(s string) string {
	return ansi.DecodeCP437([]byte(s))
}
