// Package app is the reader's user interface, composed into an
// ansi.Grid and nothing else.
//
// The Grid is the single representation of screen content in this
// system. The BBS parses its art into one; internal/ui turns a
// message into one; and everything drawn here -- lists, headers, the
// status bar, the compose form -- goes into one too. A frontend is
// then a blitter and an input source, not a second copy of the
// interface: internal/tui paints the Grid into tcell cells,
// internal/gui draws it with a CP437 bitmap font. Art and chrome
// travel the same path, which is why a message's colours look the
// same in both.
package app

import (
	"strings"

	"github.com/midrei/nullmodem-kit/ansi"
)

// The DOS palette indices the interface is drawn with. Chrome uses
// concrete colours rather than "whatever the terminal's theme says"
// because the same Grid also has to render in a GUI window, where
// there is no theme to inherit -- and because a reader whose
// furniture shifts colour between its two frontends looks broken.
//
// Body text stays at 7 on 0, the CP437 default pair, which the tcell
// blitter maps back to the terminal's own foreground and background.
// So ordinary text still follows the user's theme; only the accents
// are fixed.
const (
	fgText      = 7
	bgText      = 0
	fgDim       = 8
	fgAccent    = 15
	fgHighlight = 14 // bright yellow: the one "look here" colour
	fgBar       = 15
	bgBar       = 4 // blue, the classic BBS bar
	fgSelected  = 0
	bgSelected  = 7
	fgWarn      = 15
	bgWarn      = 1
)

// rect is a region of the grid to draw into.
type rect struct{ x, y, w, h int }

// setCell writes one cell, ignoring anything outside the grid so a
// caller never has to bounds-check before drawing.
func setCell(g *ansi.Grid, x, y int, r rune, fg, bg int) {
	if x < 0 || y < 0 || x >= g.Width || y >= g.Height {
		return
	}
	b, ok := ansi.Byte(r)
	if !ok {
		// A rune CP437 cannot represent would otherwise silently
		// become a blank hole in the layout; '?' at least shows that
		// something was meant to be there.
		b = '?'
	}
	g.Cells[y*g.Width+x] = ansi.Cell{Char: b, FG: fg, BG: bg}
}

// drawText writes a string clipped to width, returning the column
// just past what it wrote so callers can chain differently-coloured
// runs without tracking widths themselves.
func drawText(g *ansi.Grid, x, y, width int, fg, bg int, text string) int {
	col := 0
	for _, r := range text {
		if col >= width {
			break
		}
		setCell(g, x+col, y, r, fg, bg)
		col++
	}
	return x + col
}

// fill paints a region in one colour pair, used to lay down a bar or
// to clear a row before drawing a selection over it.
func fill(g *ansi.Grid, r rect, fg, bg int) {
	for y := r.y; y < r.y+r.h; y++ {
		for x := r.x; x < r.x+r.w; x++ {
			setCell(g, x, y, ' ', fg, bg)
		}
	}
}

// pad truncates or space-pads text to exactly width columns, so
// columns in a list line up regardless of content length.
//
// Truncation marks itself with a full stop rather than an ellipsis:
// CP437 has no ellipsis, and setCell would turn it into a question
// mark, which reads as part of the text instead of as a cut.
func pad(text string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(text)
	if len(r) > width {
		if width == 1 {
			return "."
		}
		return string(r[:width-1]) + "."
	}
	return text + strings.Repeat(" ", width-len(r))
}

// blitRegion copies rows [scroll, scroll+dst.h) of src into dst,
// offset horizontally by hScroll.
//
// Panning by narrowing the source window rather than by drawing at a
// negative x means a panned screen can never paint over the frame to
// its left.
func blitRegion(g *ansi.Grid, src ansi.Grid, dst rect, scroll, hScroll int) {
	for row := 0; row < dst.h; row++ {
		y := row + scroll
		if y < 0 || y >= src.Height {
			continue
		}
		for col := 0; col < dst.w; col++ {
			x := col + hScroll
			if x < 0 || x >= src.Width {
				continue
			}
			c := src.Cells[y*src.Width+x]
			if dst.x+col >= g.Width || dst.y+row >= g.Height {
				continue
			}
			g.Cells[(dst.y+row)*g.Width+dst.x+col] = c
		}
	}
}
