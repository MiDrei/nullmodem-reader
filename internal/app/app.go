package app

import (
	"context"
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

	// Fetch exchanges mail with the BBS (see session.go). Nil when no
	// system is configured for this packet, which disables the f key.
	// It returns an error wrapping ErrNeedsSetup when the login itself
	// is the problem.
	Fetch func(ctx context.Context) (FetchResult, error)
	// Latest opens the newest downloaded packet, to switch to after an
	// exchange brought one in.
	Latest func() (Loaded, error)
	// Setup checks a login against the BBS and saves it, returning a
	// note for the user (e.g. where the password ended up). Nil
	// disables the setup screen.
	Setup func(ctx context.Context, in SetupInput) (note string, err error)
	// SetupDefaults prefill the setup screen: the configured address
	// and username. The password is never prefilled.
	SetupDefaults SetupInput

	// Taglines are offered in the compose form (see offerTaglines);
	// none hides the choice. TaglineChoice is the one picked last time
	// -- TaglineNone, TaglineRandom or a tagline's text -- and
	// SaveTaglineChoice remembers a new pick for next time.
	Taglines          []string
	TaglineChoice     string
	SaveTaglineChoice func(choice string)
}

// The tagline choices besides a specific tagline.
const (
	TaglineNone   = "none"
	TaglineRandom = "random"
)

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
	// hasPacket is false on the start screen, before any packet is
	// open (see NewHome).
	hasPacket bool

	fetch         func(ctx context.Context) (FetchResult, error)
	latest        func() (Loaded, error)
	setup         func(ctx context.Context, in SetupInput) (string, error)
	setupDefaults SetupInput
	// setupNote waits to be shown after the fetch that setup starts.
	setupNote string

	taglines          []string
	taglineChoice     string
	saveTaglineChoice func(string)

	// busy names the background work in progress (see run), shown in
	// the status bar; done carries its outcome back to the frontend's
	// thread, and cancel stops it.
	busy   string
	done   chan func(*App)
	cancel context.CancelFunc

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
	// Wake asks the frontend to render again. A frontend that only
	// draws after input (the terminal) sets it, so the outcome of
	// background work shows up without a keypress.
	Wake func()
}

// New builds the reader's interface over an opened packet.
func New(path string, p *qwk.Packet, opts Options) (*App, error) {
	a := newApp(opts)
	a.m, a.hasPacket = newModel(path, p), true
	if a.from == "" {
		a.from = a.m.caller
	}
	if len(a.m.conferences) == 0 && a.m.welcome == nil {
		return nil, fmt.Errorf("app: %s holds no messages", path)
	}
	a.push(newAreaList(a.m))
	return a, nil
}

// NewMerged builds the reader's interface over several packets shown
// as one (see mergeModel), oldest first. The model copies what it
// shows, so the packets are closed before it returns.
func NewMerged(sources []Source, opts Options) (*App, error) {
	a := newApp(opts)
	if err := a.load(Loaded{Sources: sources, Queue: opts.Queue, Read: opts.Read, From: opts.From}); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	return a, nil
}

func newApp(opts Options) *App {
	a := &App{
		queue: opts.Queue, read: opts.Read, from: opts.From,
		fetch: opts.Fetch, latest: opts.Latest,
		setup: opts.Setup, setupDefaults: opts.SetupDefaults,
		taglines: opts.Taglines, taglineChoice: opts.TaglineChoice, saveTaglineChoice: opts.SaveTaglineChoice,
		done: make(chan func(*App), 1),
	}
	if a.read == nil {
		// An in-memory state keeps every view free of nil checks; it
		// simply has nowhere to save to.
		a.read = store.NewReadState()
	}
	return a
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
	a.settle()
	if a.busy != "" {
		// Nothing else may start while an exchange runs; quitting
		// still works and abandons it.
		if ev.Key() == tcell.KeyCtrlC || ev.Rune() == 'Q' {
			a.quit = true
		}
		return
	}
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
	case ev.Rune() == 'f' && a.fetch != nil:
		a.startFetch()
	case ev.Rune() == 'N' && a.hasPacket:
		a.newNetmail()
	case ev.Rune() == 's' && a.setup != nil:
		a.openSetup()
	}
}

// Render composes the whole interface into a w by h grid.
func (a *App) Render(w, h int) ansi.Grid {
	a.settle()
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
	if !a.hasPacket {
		drawText(g, 0, 0, w, fgBar, bgBar, " NullModem Reader")
		return
	}
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
	switch {
	case a.busy != "":
		text = a.busy
		if a.flash != "" {
			text += "  " + a.flash
		}
	case text == "":
		text = a.top().keyHelp()
		if _, isHome := a.top().(*homeView); !isHome && !a.inForm() && a.fetch != nil {
			text += "  f:fetch"
		}
		text += "  ?:help"
	default:
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
	"  Mail",
	"    N              write a new netmail",
	"    f              send replies and fetch new mail",
	"    s              set up or change the BBS login",
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

// newNetmail opens a new netmail from anywhere in the reader: the
// netmail conference may be far down the list, or scrolled away.
func (a *App) newNetmail() {
	for _, c := range a.m.conferences {
		if c.Netmail {
			a.compose(newMessageForm(c, a.from))
			return
		}
	}
	a.flash = "Your packets name no netmail conference -- fetch a new one with f."
}

// inForm reports whether a text form has the keyboard, where letters
// are typed rather than acting as commands.
func (a *App) inForm() bool {
	switch a.top().(type) {
	case *composeForm, *setupForm:
		return true
	}
	return false
}

// contentWidth is how wide a message body should be laid out: the
// window, less a small margin so text does not run into the frame.
func contentWidth(r rect) int {
	return max(r.w-2, 20)
}
