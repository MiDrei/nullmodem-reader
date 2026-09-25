package app

import (
	"strings"

	"github.com/gdamore/tcell/v2"

	"git.maik.ch/nullmodem/kit/ansi"
)

// editorView is the reader's own message editor: a full-screen text
// area in the same window, so writing a message needs no external
// program -- and the window stays responsive while you write, which it
// cannot while it waits on Notepad.
//
// The text is kept as logical lines (paragraphs) and word-wrapped to
// the window for display only; the cursor moves over what is on
// screen. On save, lines longer than a classic message line are
// wrapped for real, since not every reader on the other end wraps.
type editorView struct {
	form *composeForm
	to   string

	lines    [][]rune
	row, col int // cursor: logical line, rune index in it
	// wantX is the column Up/Down aim for, kept across short lines the
	// way every editor does.
	wantX  int
	scroll int // first visual row shown
	width  int // wrap width of the last draw

	// confirmDiscard is set by a first Escape: the second one throws
	// the draft away, anything else keeps writing.
	confirmDiscard bool
}

func newEditorView(form *composeForm, to, initial string) *editorView {
	v := &editorView{form: form, to: to}
	for _, l := range strings.Split(initial, "\n") {
		v.lines = append(v.lines, []rune(l))
	}
	// Start at the end: below the quote for a reply, at the top of an
	// empty message.
	v.row = len(v.lines) - 1
	v.col = len(v.lines[v.row])
	return v
}

// text is the buffer as a message body.
func (v *editorView) text() string {
	out := make([]string, len(v.lines))
	for i, l := range v.lines {
		out[i] = string(l)
	}
	return strings.Join(out, "\n")
}

func (v *editorView) keyHelp() string {
	if v.confirmDiscard {
		return "Esc again: discard this message  any other key: keep writing"
	}
	return "Ctrl-S:save  Esc:discard  Ctrl-V:paste"
}

// segment is one screen row of a logical line: runes [start,end).
type segment struct{ start, end int }

// wrap splits a line into screen rows at word boundaries, the space
// staying at the end of the row it ends; a word longer than the width
// is cut. An empty line is one empty row.
func wrap(line []rune, width int) []segment {
	if width < 1 {
		width = 1
	}
	var out []segment
	start := 0
	for len(line)-start > width {
		cut := start + width
		for i := cut; i > start; i-- {
			if line[i-1] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, segment{start, cut})
		start = cut
	}
	return append(out, segment{start, len(line)})
}

// visualRow is where the cursor is on screen: the index of its row
// among all rows of the text, and the column within that row.
func (v *editorView) visualRow() (row, x int) {
	for i := 0; i < v.row; i++ {
		row += len(wrap(v.lines[i], v.width))
	}
	segs := wrap(v.lines[v.row], v.width)
	for i, s := range segs {
		last := i == len(segs)-1
		if v.col < s.end || last {
			return row + i, v.col - s.start
		}
	}
	return row, v.col
}

// moveVertical goes n screen rows up (negative) or down, aiming for
// wantX.
func (v *editorView) moveVertical(n int) {
	target, _ := v.visualRow()
	target += n
	if target < 0 {
		v.row, v.col = 0, 0
		return
	}
	row := 0
	for i, l := range v.lines {
		segs := wrap(l, v.width)
		if target < row+len(segs) {
			s := segs[target-row]
			limit := s.end
			if target-row < len(segs)-1 && limit > s.start {
				limit-- // stay on this row, not at the start of the next
			}
			v.row, v.col = i, min(s.start+v.wantX, limit)
			return
		}
		row += len(segs)
	}
	v.row = len(v.lines) - 1
	v.col = len(v.lines[v.row])
}

func (v *editorView) insert(r []rune) {
	line := v.lines[v.row]
	nl := make([]rune, 0, len(line)+len(r))
	nl = append(nl, line[:v.col]...)
	nl = append(nl, r...)
	nl = append(nl, line[v.col:]...)
	v.lines[v.row] = nl
	v.col += len(r)
}

func (v *editorView) newline() {
	line := v.lines[v.row]
	rest := append([]rune{}, line[v.col:]...)
	v.lines[v.row] = line[:v.col]
	v.lines = append(v.lines[:v.row+1], append([][]rune{rest}, v.lines[v.row+1:]...)...)
	v.row, v.col = v.row+1, 0
}

func (v *editorView) backspace() {
	switch {
	case v.col > 0:
		line := v.lines[v.row]
		v.lines[v.row] = append(line[:v.col-1], line[v.col:]...)
		v.col--
	case v.row > 0:
		prev := v.lines[v.row-1]
		v.col = len(prev)
		v.lines[v.row-1] = append(prev, v.lines[v.row]...)
		v.lines = append(v.lines[:v.row], v.lines[v.row+1:]...)
		v.row--
	}
}

func (v *editorView) deleteForward() {
	line := v.lines[v.row]
	switch {
	case v.col < len(line):
		v.lines[v.row] = append(line[:v.col], line[v.col+1:]...)
	case v.row < len(v.lines)-1:
		v.lines[v.row] = append(line, v.lines[v.row+1]...)
		v.lines = append(v.lines[:v.row+1], v.lines[v.row+2:]...)
	}
}

// paste inserts clipboard text: line breaks become new lines, tabs
// spaces, and other control characters are dropped.
func (v *editorView) paste(a *App, text string) {
	v.confirmDiscard = false
	v.insertText(text)
	_, v.wantX = v.visualRow()
}

func (v *editorView) insertText(text string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for i, part := range strings.Split(text, "\n") {
		if i > 0 {
			v.newline()
		}
		var clean []rune
		for _, r := range part {
			switch {
			case r == '\t':
				clean = append(clean, ' ', ' ', ' ', ' ')
			case r >= ' ':
				clean = append(clean, r)
			}
		}
		v.insert(clean)
	}
}

func (v *editorView) key(a *App, ev *tcell.EventKey) bool {
	if v.confirmDiscard {
		v.confirmDiscard = false
		if ev.Key() == tcell.KeyEscape {
			a.popTo(v.form)
			a.flash = "Message discarded."
			return true
		}
	}

	page := max(a.bodyHeight()-3, 1)
	switch ev.Key() {
	case tcell.KeyEscape:
		v.confirmDiscard = true
		return true
	case tcell.KeyCtrlS:
		a.finishCompose(v.form, v.to, v.text(), true)
		return true
	case tcell.KeyEnter:
		v.newline()
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		v.backspace()
	case tcell.KeyDelete:
		v.deleteForward()
	case tcell.KeyLeft:
		switch {
		case v.col > 0:
			v.col--
		case v.row > 0:
			v.row--
			v.col = len(v.lines[v.row])
		}
	case tcell.KeyRight:
		switch {
		case v.col < len(v.lines[v.row]):
			v.col++
		case v.row < len(v.lines)-1:
			v.row, v.col = v.row+1, 0
		}
	case tcell.KeyUp:
		v.moveVertical(-1)
		return true // keeps wantX
	case tcell.KeyDown:
		v.moveVertical(1)
		return true
	case tcell.KeyPgUp:
		v.moveVertical(-page)
		return true
	case tcell.KeyPgDn:
		v.moveVertical(page)
		return true
	case tcell.KeyHome, tcell.KeyCtrlA:
		_, x := v.visualRow()
		v.col -= x
	case tcell.KeyEnd, tcell.KeyCtrlE:
		segs := wrap(v.lines[v.row], v.width)
		for i, s := range segs {
			if last := i == len(segs)-1; v.col < s.end || last {
				v.col = s.end
				if !last && v.col > s.start {
					v.col-- // the end of this row, not the start of the next
				}
				break
			}
		}
	case tcell.KeyTab:
		v.insert([]rune("    "))
	case tcell.KeyRune:
		if ev.Rune() >= ' ' {
			v.insert([]rune{ev.Rune()})
		}
	default:
		// Every other key is swallowed: in a text area, letters are
		// text, never the reader's single-key commands.
		return true
	}
	_, v.wantX = v.visualRow()
	return true
}

func (v *editorView) draw(a *App, g *ansi.Grid, r rect) {
	title := "Writing to " + v.to + " in " + v.form.conf.Name
	if s := strings.TrimSpace(v.form.subject.String()); s != "" {
		title += "  ·  " + s
	}
	drawText(g, r.x+1, r.y, r.w-1, fgAccent, bgText, title)
	drawText(g, r.x+1, r.y+1, r.w-1, fgDim, bgText, strings.Repeat("─", max(r.w-2, 0)))

	body := rect{x: r.x + 1, y: r.y + 2, w: r.w - 2, h: r.h - 2}
	if body.h <= 0 || body.w <= 0 {
		return
	}
	v.width = body.w

	// Keep the cursor on screen.
	cur, x := v.visualRow()
	if cur < v.scroll {
		v.scroll = cur
	}
	if cur >= v.scroll+body.h {
		v.scroll = cur - body.h + 1
	}

	row := 0
	for _, l := range v.lines {
		for _, s := range wrap(l, v.width) {
			if y := row - v.scroll; y >= 0 && y < body.h {
				fg := fgText
				if isQuoteLine(l) {
					fg = fgDim
				}
				drawText(g, body.x, body.y+y, body.w, fg, bgText, string(l[s.start:s.end]))
			}
			row++
		}
	}
	a.cursorX, a.cursorY, a.cursorOn = body.x+min(x, body.w-1), body.y+cur-v.scroll, true
}

// isQuoteLine spots a quoted line ("AB> text") to show it dimmed.
func isQuoteLine(l []rune) bool {
	s := strings.TrimLeft(string(l), " ")
	i := strings.Index(s, ">")
	return i >= 0 && i <= 3
}
