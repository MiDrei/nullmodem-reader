package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/midrei/nullmodem-kit/ansi"
	"github.com/midrei/nullmodem-reader/internal/ui"
)

// cursor is the shared selection-and-scroll bookkeeping of a list.
// Keeping it in one place is what stops the two lists from drifting
// into subtly different scrolling behavior.
type cursor struct {
	sel    int
	scroll int
}

// move changes the selection by delta, clamped to [0, n). It does not
// wrap: wrapping from the last message back to the first reads as a
// glitch when you are holding the down key.
func (c *cursor) move(delta, n int) {
	if n == 0 {
		c.sel = 0
		return
	}
	c.sel = clamp(c.sel+delta, 0, n-1)
}

// follow scrolls the minimum distance needed to keep the selection
// on screen.
func (c *cursor) follow(height int) {
	if height <= 0 {
		return
	}
	if c.sel < c.scroll {
		c.scroll = c.sel
	}
	if c.sel >= c.scroll+height {
		c.scroll = c.sel - height + 1
	}
}

// listKey applies the bindings every list shares.
func (c *cursor) listKey(ev *tcell.EventKey, n, page int) bool {
	switch {
	case ev.Key() == tcell.KeyUp, ev.Rune() == 'k':
		c.move(-1, n)
	case ev.Key() == tcell.KeyDown, ev.Rune() == 'j':
		c.move(1, n)
	case ev.Key() == tcell.KeyPgUp:
		c.move(-page, n)
	case ev.Key() == tcell.KeyPgDn:
		c.move(page, n)
	case ev.Key() == tcell.KeyHome:
		c.move(-n, n)
	case ev.Key() == tcell.KeyEnd:
		c.move(n, n)
	default:
		return false
	}
	return true
}

// ------------------------------------------------------ conferences

type areaList struct {
	cursor
	m model
}

func newAreaList(m model) *areaList { return &areaList{m: m} }

func (v *areaList) keyHelp() string {
	h := "↑↓:move  Enter:open  e:write  o:outbox  Q:quit"
	if v.m.welcome != nil {
		h = "↑↓:move  Enter:open  e:write  w:welcome  o:outbox  Q:quit"
	}
	return h
}

func (v *areaList) draw(a *App, g *ansi.Grid, r rect) {
	unreadTotal := 0
	for _, c := range v.m.conferences {
		unreadTotal += a.read.UnreadCount(c.Number, c.Numbers())
	}
	summary := "no unread mail"
	if unreadTotal > 0 {
		summary = strconv.Itoa(unreadTotal) + " unread"
	}
	r = listFrame(g, r, "Conferences", summary,
		"   "+pad("#", 5)+pad("Conference", max(r.w-28, 10))+"     "+pad("Messages", 12))

	v.follow(r.h)
	for i := 0; i < r.h; i++ {
		idx := i + v.scroll
		if idx >= len(v.m.conferences) {
			break
		}
		c := v.m.conferences[idx]
		fg, bg := fgText, bgText
		if idx == v.sel {
			fg, bg = fgSelected, bgSelected
			fill(g, rect{r.x, r.y + i, r.w, 1}, fg, bg)
		}

		tag := "     "
		if c.Netmail {
			tag = " NET "
		}
		// "3/12" reads as "3 of 12 still unread"; a bare total says
		// nothing about whether the conference is worth opening.
		unread := a.read.UnreadCount(c.Number, c.Numbers())
		count := strconv.Itoa(len(c.Messages)) + " msg"
		if unread > 0 {
			count = strconv.Itoa(unread) + "/" + count
		}
		mark := " "
		if unread > 0 {
			mark = "\u2022"
		}
		line := " " + mark + " " + pad(strconv.Itoa(c.Number), 5) + pad(c.Name, max(r.w-28, 10)) +
			tag + pad(count, 12)
		drawText(g, r.x, r.y+i, r.w, fg, bg, line)
	}

	if len(v.m.conferences) == 0 {
		drawText(g, r.x+1, r.y, r.w, fgDim, bgText, "This packet carries no messages.")
	}
}

func (v *areaList) key(a *App, ev *tcell.EventKey) bool {
	if v.listKey(ev, len(v.m.conferences), 10) {
		return true
	}
	switch {
	case ev.Key() == tcell.KeyEnter, ev.Rune() == 'l':
		if len(v.m.conferences) == 0 {
			return true
		}
		a.push(newMessageList(a, v.m.conferences[v.sel]))
		return true
	case ev.Rune() == 'w':
		if v.m.welcome == nil {
			a.flash = "This packet carries no welcome screen."
			return true
		}
		a.push(newScreenView(v.m.welcomeName, v.m.welcome))
		return true
	case ev.Rune() == 'o':
		if a.queue == nil {
			a.flash = "No outgoing queue for this packet."
			return true
		}
		a.push(newOutbox(a.queue))
		return true
	case ev.Rune() == 'e':
		if len(v.m.conferences) == 0 {
			return true
		}
		a.compose(newMessageForm(v.m.conferences[v.sel], a.from))
		return true
	}
	return false
}

// --------------------------------------------------------- messages

type messageList struct {
	cursor
	conf Conference
}

// newMessageList opens a conference at its first unread message.
//
// Landing on the top of a conference you have mostly read means
// scrolling past what you have already seen every single time --
// which is the whole reason offline readers keep a last-read pointer.
// A fully-read conference opens at the top, since there is nothing
// better to point at.
func newMessageList(a *App, c Conference) *messageList {
	v := &messageList{conf: c}
	for i, m := range c.Messages {
		if !a.read.IsRead(c.Number, m.Number) {
			v.sel = i
			break
		}
	}
	return v
}

func (v *messageList) keyHelp() string {
	return "↑↓:move  Enter:read  e:write  m:mark read  Esc:back"
}

func (v *messageList) draw(a *App, g *ansi.Grid, r rect) {

	// Column widths: the date is fixed, the sender gets a quarter of
	// what is left, and the subject takes the rest -- it is the field
	// people actually scan a list by.
	dateW := 11
	fromW := clamp((r.w-dateW)/4, 8, 22)
	subjW := max(r.w-dateW-fromW-5, 10)

	unread := a.read.UnreadCount(v.conf.Number, v.conf.Numbers())
	summary := strconv.Itoa(len(v.conf.Messages)) + " messages"
	if len(v.conf.Messages) == 1 {
		summary = "1 message"
	}
	if unread > 0 {
		summary += ", " + strconv.Itoa(unread) + " unread"
	}
	r = listFrame(g, r, v.conf.Name, summary,
		"    "+pad("From", fromW)+" "+pad("Subject", subjW)+" "+pad("Date", dateW))
	v.follow(r.h)

	for i := 0; i < r.h; i++ {
		idx := i + v.scroll
		if idx >= len(v.conf.Messages) {
			break
		}
		m := v.conf.Messages[idx]
		fg, bg := fgText, bgText
		if idx == v.sel {
			fg, bg = fgSelected, bgSelected
			fill(g, rect{r.x, r.y + i, r.w, 1}, fg, bg)
		}

		// Two marker columns rather than one. Squeezing both into a
		// single column meant whichever lost became invisible -- and a
		// private message you have not read yet is precisely the one
		// you most want to spot.
		unread, private := " ", " "
		if !a.read.IsRead(v.conf.Number, m.Number) {
			unread = "\u2022"
		}
		if m.Private {
			private = "*"
		}
		date := ""
		if !m.Written.IsZero() {
			date = m.Written.Format("2006-01-02")
		}
		line := " " + unread + private + " " + pad(m.From, fromW) + " " + pad(m.Subject, subjW) + " " + pad(date, dateW)
		drawText(g, r.x, r.y+i, r.w, fg, bg, line)
	}
}

func (v *messageList) key(a *App, ev *tcell.EventKey) bool {
	if v.listKey(ev, len(v.conf.Messages), 10) {
		return true
	}
	switch {
	case ev.Key() == tcell.KeyEnter, ev.Rune() == 'l':
		if len(v.conf.Messages) == 0 {
			return true
		}
		a.push(newMessageView(a, v.conf, v.sel, contentWidth(rect{w: termWidth(a)})))
		return true
	case ev.Rune() == 'e':
		a.compose(newMessageForm(v.conf, a.from))
		return true
	case ev.Rune() == 'm':
		a.read.MarkAllRead(v.conf.Number, v.conf.Numbers())
		a.flash = "Marked " + v.conf.Name + " read."
		return true
	}
	return false
}

// ----------------------------------------------------- message body

type messageView struct {
	conf  Conference
	index int
	// width the body was laid out at; a resize relays it.
	width  int
	grid   ansi.Grid
	scroll int
	// showRouting shows echomail's SEEN-BY/PATH lines under the text;
	// it stays on while stepping through messages.
	showRouting bool
}

func newMessageView(a *App, c Conference, index, width int) *messageView {
	v := &messageView{conf: c, index: index}
	v.layout(width)
	a.read.MarkRead(c.Number, c.Messages[index].Number)
	return v
}

// layout re-renders the current message into the Grid matrix. It is
// called on open, on moving between messages, and whenever the
// window width changes -- prose has to rewrap, and art has to not.
func (v *messageView) layout(width int) {
	v.width = width
	m := v.conf.Messages[v.index]
	body := m.Body
	if v.showRouting && len(m.Routing) > 0 {
		body = strings.TrimRight(body, "\n") + "\n\n" + strings.Join(m.Routing, "\n")
	}
	v.grid = ui.MessageGrid(body, width)
	v.scroll = 0
}

func (v *messageView) keyHelp() string {
	h := "↑↓:scroll  space:page  n/p:next/prev  r:reply  Esc:back"
	if len(v.conf.Messages[v.index].Routing) > 0 {
		if v.showRouting {
			return h + "  S:hide SEEN-BY"
		}
		return h + "  S:SEEN-BY"
	}
	return h
}

// headerLines is the block above the body: the fields, then a rule.
func (v *messageView) headerLines() []struct {
	label, value string
} {
	m := v.conf.Messages[v.index]
	out := []struct{ label, value string }{
		{"From", m.From},
		{"To", m.To},
		{"Subj", m.Subject},
	}
	if !m.Written.IsZero() {
		out = append(out, struct{ label, value string }{"Date", m.Written.Format("2006-01-02 15:04")})
	}
	return out
}

func (v *messageView) draw(a *App, g *ansi.Grid, r rect) {
	m := v.conf.Messages[v.index]

	if w := contentWidth(r); w != v.width {
		v.layout(w)
	}

	// Position line: where this message sits in the conference.
	pos := v.conf.Name + "  ·  " + strconv.Itoa(v.index+1) + "/" + strconv.Itoa(len(v.conf.Messages))
	if m.Number > 0 {
		pos += "  (msg #" + strconv.Itoa(m.Number) + ")"
	}
	drawText(g, r.x+1, r.y, r.w-1, fgAccent, bgText, pos)

	row := r.y + 1
	for _, f := range v.headerLines() {
		if row >= r.y+r.h {
			return
		}
		x := drawText(g, r.x+1, row, r.w-1, fgDim, bgText, pad(f.label+":", 6))
		drawText(g, x, row, r.x+r.w-x, fgText, bgText, f.value)
		row++
	}

	if row < r.y+r.h {
		// A drawn rule rather than a dim-styled blank row: dim on
		// spaces is invisible, which is not a separator at all.
		drawText(g, r.x+1, row, r.w-1, fgDim, bgText, strings.Repeat("─", max(r.w-2, 0)))
		row++
	}

	body := rect{x: r.x + 1, y: row, w: r.w - 1, h: r.y + r.h - row}
	if body.h > 0 {
		v.clampScroll(body.h)
		blitRegion(g, v.grid, body, v.scroll, 0)
	}
}

// clampScroll keeps the scroll position inside the body. The grid can
// be shorter than the window, in which case the only valid offset is
// zero -- without this, paging down through a short message would
// scroll the text out of sight entirely.
func (v *messageView) clampScroll(height int) {
	v.scroll = clamp(v.scroll, 0, max(v.grid.Height-height, 0))
}

func (v *messageView) key(a *App, ev *tcell.EventKey) bool {
	page := max(a.bodyHeight()-1, 1)
	switch {
	case ev.Key() == tcell.KeyUp, ev.Rune() == 'k':
		v.scroll--
	case ev.Key() == tcell.KeyDown, ev.Rune() == 'j':
		v.scroll++
	case ev.Key() == tcell.KeyPgUp:
		v.scroll -= page
	case ev.Key() == tcell.KeyPgDn:
		v.scroll += page
	case ev.Rune() == ' ':
		// Space reads on: a page at a time, then the next message --
		// a whole conference with one key, as the old readers did.
		if v.scroll+a.bodyHeight() >= v.grid.Height+len(v.headerLines())+2 {
			v.step(1, a)
			return true
		}
		v.scroll += page
	case ev.Key() == tcell.KeyHome:
		v.scroll = 0
	case ev.Key() == tcell.KeyEnd:
		v.scroll = v.grid.Height
	case ev.Rune() == 'n', ev.Key() == tcell.KeyRight:
		v.step(1, a)
	case ev.Rune() == 'p', ev.Key() == tcell.KeyLeft:
		v.step(-1, a)
	case ev.Rune() == 'r':
		a.compose(newReplyForm(v.conf, v.conf.Messages[v.index], a.from, quoteWidth(v.width)))
	case ev.Rune() == 'S':
		if len(v.conf.Messages[v.index].Routing) == 0 {
			a.flash = "This message carries no SEEN-BY or PATH lines."
			return true
		}
		v.showRouting = !v.showRouting
		scroll := v.scroll
		v.layout(v.width)
		v.scroll = scroll
	default:
		return false
	}
	v.scroll = max(v.scroll, 0)
	return true
}

// step moves to the neighbouring message, saying so rather than
// silently doing nothing at either end.
func (v *messageView) step(delta int, a *App) {
	next := v.index + delta
	if next >= len(v.conf.Messages) {
		a.endOfConference(v.conf)
		return
	}
	if next < 0 {
		a.flash = "This is the first message in " + v.conf.Name + "."
		return
	}
	v.index = next
	v.layout(v.width)
	a.read.MarkRead(v.conf.Number, v.conf.Messages[next].Number)
}

// ---------------------------------------------------- screen viewer

// screenView shows a packet's welcome screen. Art is parsed at its
// own 80-column canvas regardless of the window: reflowing it to a
// narrower terminal would break the picture, so a narrow window
// scrolls horizontally instead of mangling it.
type screenView struct {
	name    string
	grid    ansi.Grid
	scroll  int
	hScroll int
}

func newScreenView(name string, raw []byte) *screenView {
	return &screenView{name: name, grid: ui.ScreenGrid(raw, 80)}
}

func (v *screenView) keyHelp() string { return "↑↓:scroll  ←→:pan  Esc:back" }

func (v *screenView) draw(a *App, g *ansi.Grid, r rect) {
	drawText(g, r.x+1, r.y, r.w-1, fgDim, bgText, v.name)

	body := rect{x: r.x, y: r.y + 1, w: r.w, h: r.h - 1}
	if body.h <= 0 {
		return
	}
	v.scroll = clamp(v.scroll, 0, max(v.grid.Height-body.h, 0))
	v.hScroll = clamp(v.hScroll, 0, max(v.grid.Width-body.w, 0))

	blitRegion(g, v.grid, body, v.scroll, v.hScroll)
}

func (v *screenView) key(a *App, ev *tcell.EventKey) bool {
	page := max(a.bodyHeight()-1, 1)
	switch {
	case ev.Key() == tcell.KeyUp, ev.Rune() == 'k':
		v.scroll--
	case ev.Key() == tcell.KeyDown, ev.Rune() == 'j':
		v.scroll++
	case ev.Key() == tcell.KeyLeft, ev.Rune() == 'h':
		v.hScroll--
	case ev.Key() == tcell.KeyRight:
		v.hScroll++
	case ev.Key() == tcell.KeyPgUp:
		v.scroll -= page
	case ev.Key() == tcell.KeyPgDn, ev.Rune() == ' ':
		v.scroll += page
	case ev.Key() == tcell.KeyHome:
		v.scroll, v.hScroll = 0, 0
	default:
		return false
	}
	v.scroll = max(v.scroll, 0)
	v.hScroll = max(v.hScroll, 0)
	return true
}

// ----------------------------------------------------------- shared

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func termWidth(a *App) int { return a.width }

// bodyHeight is how many rows a scrolling view has to work with:
// the window less the header and status bars.
func (a *App) bodyHeight() int { return max(a.height-2, 1) }

// quoteWidth is the column budget a quote is wrapped to. It is
// narrower than the window on purpose: the quote will be indented by
// the recipient's own reader when they quote it again, and a quote
// wrapped to the full width grows past the margin on every round.
func quoteWidth(width int) int {
	return clamp(width-8, 40, 72)
}

// compose pushes a compose form, or explains why it cannot.
func (a *App) compose(form *composeForm) {
	if a.queue == nil {
		a.flash = "Replies cannot be written: no data directory for this packet."
		return
	}
	form.offerTaglines(a.taglines, a.taglineChoice)
	a.push(form)
}

// listFrame draws a list's title line, its column headings and a rule
// under them, and returns the rows left for the list itself -- so a
// list does not start right under the header bar with nothing to say
// what its columns are.
func listFrame(g *ansi.Grid, r rect, title, summary, headings string) rect {
	x := drawText(g, r.x+1, r.y, r.w-1, fgAccent, bgText, title)
	if summary != "" {
		drawText(g, x, r.y, r.x+r.w-x, fgDim, bgText, "  \u00b7  "+summary)
	}
	drawText(g, r.x, r.y+1, r.w, fgDim, bgText, headings)
	drawText(g, r.x+1, r.y+2, r.w-1, fgDim, bgText, strings.Repeat("\u2500", max(r.w-2, 0)))
	return rect{x: r.x, y: r.y + 3, w: r.w, h: max(r.h-3, 0)}
}

// endOfConference leaves a conference read to its end: back to the
// conference list with the next one holding unread mail selected, so
// Enter goes straight on -- rather than a dead end at the last message.
func (a *App) endOfConference(done Conference) {
	a.stack = a.stack[:1]
	list, ok := a.stack[0].(*areaList)
	if !ok {
		return
	}
	confs := list.m.conferences
	at := 0
	for i, c := range confs {
		if c.Number == done.Number {
			at = i
		}
	}
	for step := 1; step <= len(confs); step++ {
		i := (at + step) % len(confs)
		c := confs[i]
		if n := a.read.UnreadCount(c.Number, c.Numbers()); n > 0 {
			list.sel = i
			a.flash = fmt.Sprintf("End of %s. Next with unread mail: %s (%d) -- Enter opens it.", done.Name, c.Name, n)
			return
		}
	}
	list.sel = at
	a.flash = "End of " + done.Name + ". No unread mail left."
}
