package app

import (
	"fmt"
	"strconv"

	"github.com/gdamore/tcell/v2"

	"git.maik.ch/nullmodem/kit/ansi"
	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/store"
)

// view is one screen of the reader. Views form a stack: opening a
// conference pushes, Escape pops, and the bottom of the stack is what
// quitting leaves.
type view interface {
	// draw paints the view into the content region r of g.
	draw(a *App, g *ansi.Grid, r rect)
	// key handles one keypress, returning false to let the App apply
	// its own global bindings.
	key(a *App, ev *tcell.EventKey) bool
	// keyHelp is the hint line shown in the status bar.
	keyHelp() string
}

// Options carry what the app needs beyond the packet itself.
type Options struct {
	// Queue receives messages the user writes. A nil queue disables
	// composing, with the reader saying so rather than losing what
	// someone just typed.
	Queue *store.Queue
	// From is the name replies are sent under. Empty falls back to the
	// packet's own caller name.
	From string
	// Read tracks which messages have been read. A nil state means the
	// reader still works, it just cannot remember across sessions --
	// so a packet opened from a read-only location is still readable.
	Read *store.ReadState
}

// App is the reader's interface: state, key handling, and a Grid.
//
// It owns no terminal and no window. A frontend drives it by calling
// HandleKey for input and Render for output, which is what lets the
// same interface run in a terminal and in a GUI without either one
// knowing about the other.
type App struct {
	m     model
	stack []view
	queue *store.Queue
	read  *store.ReadState
	from  string

	// flash is a transient message shown in the status bar instead of
	// the key hints, cleared by the next keypress.
	flash string
	help  bool
	quit  bool

	// width and height are the last size Render was called with, for
	// views that need to know the window before they are drawn.
	width, height int

	// cursor is where a text field wants the caret, when one does.
	cursorX, cursorY int
	cursorOn         bool

	// Suspend and Resume release the display around an external
	// editor. A frontend that cannot release it leaves these nil, and
	// the editor then runs on top of whatever is on screen.
	Suspend func() error
	Resume  func() error
}

// New builds the reader's interface over an opened packet.
func New(path string, p *qwk.Packet, opts Options) (*App, error) {
	a := &App{m: newModel(path, p), queue: opts.Queue, read: opts.Read, from: opts.From}
	if a.from == "" {
		a.from = a.m.caller
	}
	if a.read == nil {
		// An in-memory state keeps every view free of nil checks; it
		// simply has nowhere to save to.
		a.read = store.NewReadState()
	}
	if len(a.m.conferences) == 0 && a.m.welcome == nil {
		return nil, fmt.Errorf("app: %s holds no messages", path)
	}
	a.push(newAreaList(a.m))
	return a, nil
}

// Quit reports whether the user has asked to leave.
func (a *App) Quit() bool { return a.quit }

// Cursor is where the caret belongs, and whether to show one at all.
func (a *App) Cursor() (x, y int, visible bool) { return a.cursorX, a.cursorY, a.cursorOn }

// Save writes out the read markers. A frontend calls it on the way
// out.
//
// Saving once at the end rather than after every message keeps a disk
// write off the keypress path; the cost is losing the markers from a
// session that ends in a crash, which is a fair trade for bookkeeping
// the user can redo by pressing a key.
func (a *App) Save() error { return a.read.Save() }

func (a *App) push(v view) { a.stack = append(a.stack, v) }

// pop leaves the last view in place: popping it would leave nothing
// to draw, and Escape at the top level should do nothing rather than
// quit by surprise.
func (a *App) pop() {
	if len(a.stack) > 1 {
		a.stack = a.stack[:len(a.stack)-1]
	}
}

func (a *App) top() view { return a.stack[len(a.stack)-1] }

// HandleKey applies one keypress.
func (a *App) HandleKey(ev *tcell.EventKey) {
	a.flash = ""

	if a.help {
		// Any key dismisses help, and that key does nothing else --
		// otherwise closing the overlay would also act on whatever is
		// behind it.
		a.help = false
		return
	}
	if a.top().key(a, ev) {
		return
	}

	switch {
	case ev.Key() == tcell.KeyEscape:
		a.pop()
	case ev.Key() == tcell.KeyCtrlC:
		a.quit = true
	case ev.Rune() == 'q':
		if len(a.stack) == 1 {
			a.quit = true
			return
		}
		a.pop()
	case ev.Rune() == 'Q':
		a.quit = true
	case ev.Rune() == '?':
		a.help = true
	}
}

// Render composes the whole interface into a w by h grid.
func (a *App) Render(w, h int) ansi.Grid {
	g := ansi.NewGrid(max(w, 1), max(h, 1))
	a.width, a.height = w, h
	a.cursorOn = false

	if w < 20 || h < 5 {
		drawText(&g, 0, 0, w, fgWarn, bgWarn, "Window too small")
		return g
	}

	a.drawHeader(&g, w)
	a.top().draw(a, &g, rect{x: 0, y: 1, w: w, h: h - 2})
	a.drawStatus(&g, w, h)
	if a.help {
		a.drawHelp(&g, w, h)
		// The overlay covers whatever field wanted a caret.
		a.cursorOn = false
	}
	return g
}

func (a *App) drawHeader(g *ansi.Grid, w int) {
	fill(g, rect{0, 0, w, 1}, fgBar, bgBar)
	left := " " + a.m.title()
	if a.m.caller != "" {
		left += "  for " + a.m.caller
	}
	drawText(g, 0, 0, w, fgBar, bgBar, left)

	right := strconv.Itoa(a.m.messageCount()) + " msg"
	if a.m.qwke {
		right = "QWKE  " + right
	}
	if !a.m.packetTime.IsZero() {
		right = a.m.packetTime.Format("2006-01-02 15:04") + "  " + right
	}
	right += " "
	if x := w - len([]rune(right)); x > len([]rune(left)) {
		drawText(g, x, 0, w-x, fgBar, bgBar, right)
	}
}

func (a *App) drawStatus(g *ansi.Grid, w, h int) {
	fg, bg := fgBar, bgBar
	text := a.flash
	if text == "" {
		text = a.top().keyHelp() + "  ?:help"
	} else {
		// A flash is either a confirmation or a refusal, and both are
		// worth a colour the eye catches without reading first.
		fg, bg = fgWarn, bgWarn
	}
	fill(g, rect{0, h - 1, w, 1}, fg, bg)
	drawText(g, 0, h-1, w, fg, bg, " "+text)
}

var helpLines = []string{
	"",
	"  Navigation",
	"    ↑ ↓ / k j      move            PgUp PgDn    page",
	"    Home End       first / last    Enter        open",
	"    Esc / q        back            Q            quit",
	"",
	"  Read markers",
	"    •              an unread message",
	"    m              mark the conference read",
	"",
	"  Conferences",
	"    w              welcome screen",
	"    o              pending replies",
	"",
	"  Messages",
	"    e              write a new message here",
	"    r              reply (in a message)",
	"    n p            next / previous message",
	"    space          page down",
	"",
	"  Any key closes this help.",
	"",
}

func (a *App) drawHelp(g *ansi.Grid, w, h int) {
	boxW := 0
	for _, l := range helpLines {
		boxW = max(boxW, len([]rune(l))+2)
	}
	boxW = min(boxW, w)
	boxH := min(len(helpLines)+1, h)
	x, y := (w-boxW)/2, (h-boxH)/2

	fill(g, rect{x, y, boxW, boxH}, fgSelected, bgSelected)
	drawText(g, x, y, boxW, fgSelected, bgSelected, " Keys")
	for i, l := range helpLines {
		if y+1+i >= y+boxH {
			break
		}
		drawText(g, x, y+1+i, boxW, fgSelected, bgSelected, l)
	}
}

// contentWidth is how wide a message body should be laid out: the
// window, less a small margin so text does not run into the frame.
func contentWidth(r rect) int {
	return max(r.w-2, 20)
}
