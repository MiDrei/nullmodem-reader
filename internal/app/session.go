package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"

	"git.maik.ch/nullmodem/kit/ansi"
	"git.maik.ch/nullmodem/reader/internal/store"
)

// This file is the reader's connection to its BBS from inside the
// interface: first-run setup, exchanging mail, and moving on to the
// packet that exchange brought in. It is what lets someone start the
// reader by double-clicking it and never see a command line.
//
// The app itself still knows nothing about configuration files or
// HTTP: the frontend's caller supplies the work as functions in
// Options, and the app only decides when to run them and what to show.

// FetchResult reports one exchange.
type FetchResult struct {
	Sent, Received int
	NoNewMail      bool
	// Rejected are queued messages the BBS refused, with its reasons.
	// They stay in the outbox, held until edited or discarded.
	Rejected []store.Reply
	// Note is something worth telling the user even though the
	// exchange succeeded, e.g. characters that could not be sent.
	Note string
}

// rejectedNote says what the BBS refused, where the user will look
// right after an exchange: the status bar.
func rejectedNote(rejected []store.Reply) string {
	switch len(rejected) {
	case 0:
		return ""
	case 1:
		r := rejected[0]
		return fmt.Sprintf("Not delivered: %q -- %s. It waits in the outbox (o).", r.Subject, r.Error)
	default:
		return fmt.Sprintf("%d messages were not delivered -- see the outbox (o).", len(rejected))
	}
}

// Loaded is the packets to show, oldest first, plus everything the
// reader needs around them -- the same things Options carries for a
// packet opened at start-up.
type Loaded struct {
	Sources []Source
	Queue   *store.Queue
	Read    *store.ReadState
	From    string
}

// SetupInput is what the setup screen asks for.
type SetupInput struct {
	URL, Username, Password string
	// KeepDays is how long read packets are kept; 0 keeps everything.
	KeepDays int
	// HavePassword, in Options.SetupDefaults, says a password is
	// already stored: the field may then stay empty to keep it.
	HavePassword bool
}

// maxKeepDays bounds the setting to something that is plainly a
// number of days and not a typo.
const maxKeepDays = 3650

// ErrNeedsSetup tells the app that an exchange could not even log in --
// no system configured, no password, or a rejected one -- so the fix is
// the setup screen rather than trying again.
var ErrNeedsSetup = errors.New("the reader needs to be set up")

// Home says what a reader started without a packet does first.
type Home int

const (
	// HomeSetup opens the setup screen: nothing is configured yet.
	HomeSetup Home = iota
	// HomeFetch fetches mail straight away: configured, but nothing
	// downloaded yet.
	HomeFetch
	// HomeIdle only shows the start screen with its note.
	HomeIdle
)

// NewHome builds the reader with no packet open yet. note is shown on
// the start screen, e.g. why there is nothing to read.
func NewHome(opts Options, start Home, note string) *App {
	a := newApp(opts)
	a.push(&homeView{note: note})
	switch start {
	case HomeSetup:
		a.openSetup()
	case HomeFetch:
		a.startFetch()
	}
	return a
}

// run does work off the frontend's thread and applies its outcome on
// it: Render picks up the result, so the window keeps drawing (and
// Windows doesn't declare it hung) while a download is in progress.
func (a *App) run(label string, work func(ctx context.Context) func(*App)) {
	ctx, cancel := context.WithCancel(context.Background())
	a.busy, a.cancel = label, cancel
	go func() {
		apply := work(ctx)
		a.done <- apply
		if a.Wake != nil {
			a.Wake()
		}
	}()
}

// settle applies finished background work. Only ever called from the
// frontend's thread, which is what keeps App free of locks.
func (a *App) settle() {
	for {
		select {
		case apply := <-a.done:
			a.busy = ""
			if a.cancel != nil {
				a.cancel()
				a.cancel = nil
			}
			apply(a)
		default:
			return
		}
	}
}

// Busy reports what the app is waiting on, if anything.
func (a *App) Busy() string { return a.busy }

// Close stops any exchange still running and saves the read markers.
func (a *App) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	return a.Save()
}

// startFetch exchanges mail in the background.
func (a *App) startFetch() {
	if a.fetch == nil {
		a.flash = "No BBS is configured for this packet."
		return
	}
	if a.busy != "" {
		return
	}
	a.run("Exchanging mail with the BBS...", func(ctx context.Context) func(*App) {
		res, err := a.fetch(ctx)
		if err != nil {
			return func(a *App) { a.fetchFailed(err) }
		}
		if res.NoNewMail {
			return func(a *App) { a.noNewMail(res) }
		}
		if a.latest == nil {
			return func(a *App) {
				a.flash = appendNote(fmt.Sprintf("Received %d message(s).", res.Received), rejectedNote(res.Rejected))
			}
		}
		l, err := a.latest()
		return func(a *App) {
			if err != nil {
				a.flash = "Mail arrived but could not be opened: " + err.Error()
				return
			}
			if err := a.load(l); err != nil {
				a.flash = err.Error()
				return
			}
			a.flash = a.withSetupNote(appendNote(appendNote(fetchSummary(res), rejectedNote(res.Rejected)), res.Note))
		}
	})
}

func fetchSummary(res FetchResult) string {
	if res.Sent > 0 {
		return fmt.Sprintf("Sent %d, received %d message(s).", res.Sent, res.Received)
	}
	return fmt.Sprintf("Received %d message(s).", res.Received)
}

func (a *App) noNewMail(res FetchResult) {
	text := "No new mail."
	if res.Sent > 0 {
		text = fmt.Sprintf("Sent %d message(s); no new mail.", res.Sent)
	}
	if h, ok := a.top().(*homeView); ok && !a.hasPacket {
		h.note = "Nothing to read yet -- the BBS had no new mail for you. Press f to try again later."
	}
	a.flash = a.withSetupNote(appendNote(appendNote(text, rejectedNote(res.Rejected)), res.Note))
}

func (a *App) fetchFailed(err error) {
	if errors.Is(err, ErrNeedsSetup) && a.setup != nil {
		a.openSetup()
		a.flash = strings.TrimPrefix(err.Error(), ErrNeedsSetup.Error()+": ")
		return
	}
	if h, ok := a.top().(*homeView); ok && !a.hasPacket {
		h.note = "Could not exchange mail: " + err.Error()
	}
	a.flash = a.withSetupNote("Exchange failed: " + err.Error())
}

// withSetupNote appends what setup had to say to the next status
// message: setup goes straight on to fetch on a first run, and the
// fetch's own outcome would otherwise replace the note before anyone
// could read it.
func (a *App) withSetupNote(text string) string {
	text = appendNote(text, a.setupNote)
	a.setupNote = ""
	return text
}

func appendNote(text, note string) string {
	if note == "" {
		return text
	}
	return text + " " + note
}

// load switches the reader to another packet: typically the one an
// exchange just brought in. The read markers of the old one are saved
// first, and the views start over at the conference list, since the
// message the user was on may not exist in the new packet.
func (a *App) load(l Loaded) error {
	if len(l.Sources) == 0 {
		return errors.New("no packets to show")
	}
	m := mergeModel(l.Sources)
	// The model copies everything it shows, so the packets can go.
	for _, src := range l.Sources {
		src.Packet.Close()
	}
	if len(m.conferences) == 0 && m.welcome == nil {
		return fmt.Errorf("%s holds no messages", m.path)
	}
	if err := a.read.Save(); err != nil {
		a.flash = "Could not save read markers: " + err.Error()
	}

	a.m, a.hasPacket = m, true
	a.queue, a.from = l.Queue, l.From
	if a.from == "" {
		a.from = m.caller
	}
	a.read = l.Read
	if a.read == nil {
		a.read = store.NewReadState()
	}
	a.stack = []view{newAreaList(a.m)}
	return nil
}

// openSetup pushes the setup screen, prefilled from what is configured.
func (a *App) openSetup() {
	if a.setup == nil {
		a.flash = "Setup is not available here."
		return
	}
	if _, ok := a.top().(*setupForm); ok {
		return
	}
	a.push(newSetupForm(a.setupDefaults))
}

// ---------------------------------------------------------------- home

// homeView is what the reader shows with no packet open.
type homeView struct {
	note string
}

func (v *homeView) keyHelp() string { return "f:fetch mail  s:setup  Q:quit" }

func (v *homeView) draw(a *App, g *ansi.Grid, r rect) {
	lines := []struct {
		text string
		fg   int
	}{
		{"NullModem Reader", fgAccent},
		{"", fgText},
		{"An offline reader for your BBS mail: it fetches your messages as a", fgText},
		{"QWK packet, lets you read and answer them here, and sends your", fgText},
		{"replies the next time it exchanges mail.", fgText},
		{"", fgText},
	}
	if v.note != "" {
		lines = append(lines, struct {
			text string
			fg   int
		}{v.note, fgText}, struct {
			text string
			fg   int
		}{"", fgText})
	}
	lines = append(lines,
		struct {
			text string
			fg   int
		}{"f  fetch mail now", fgDim},
		struct {
			text string
			fg   int
		}{"s  set up or change your BBS login", fgDim},
		struct {
			text string
			fg   int
		}{"Q  quit", fgDim},
	)
	for i, l := range lines {
		if i >= r.h {
			break
		}
		drawText(g, r.x+2, r.y+1+i, r.w-2, l.fg, bgText, l.text)
	}
}

func (v *homeView) key(a *App, ev *tcell.EventKey) bool {
	// Escape and q on the start screen would leave an empty window;
	// quitting is Q, as everywhere else.
	return ev.Key() == tcell.KeyEscape || ev.Rune() == 'q'
}

// --------------------------------------------------------------- setup

// setupForm asks for the three things the reader needs to talk to a
// BBS, then hands them to Options.Setup, which checks them against the
// BBS before anything is saved.
type setupForm struct {
	url, username, password, keepDays field
	focus                             int
	havePassword                      bool
}

func newSetupForm(defaults SetupInput) *setupForm {
	v := &setupForm{
		url:          newField("BBS address", defaults.URL),
		username:     newField("Username", defaults.Username),
		password:     newField("Password", ""),
		keepDays:     newField("Keep days", strconv.Itoa(defaults.KeepDays)),
		havePassword: defaults.HavePassword,
	}
	v.password.mask = true
	// Start where there is something to type.
	switch {
	case defaults.URL == "":
		v.setFocus(0)
	case defaults.Username == "":
		v.setFocus(1)
	default:
		v.setFocus(2)
	}
	return v
}

// setFocus moves the keyboard to field i and selects what it holds, so
// typing replaces a prefilled value instead of appending to it.
func (v *setupForm) setFocus(i int) {
	v.focus = i
	v.fields()[i].focus()
}

func (v *setupForm) fields() []*field {
	return []*field{&v.url, &v.username, &v.password, &v.keepDays}
}

func (v *setupForm) keyHelp() string { return "Tab:next field  Enter:connect  Esc:cancel" }

func (v *setupForm) draw(a *App, g *ansi.Grid, r rect) {
	drawText(g, r.x+1, r.y, r.w-1, fgAccent, bgText, "Connect to your BBS")
	drawText(g, r.x+1, r.y+2, r.w-1, fgDim, bgText, "The same login you use on the BBS's website. The address is the")
	drawText(g, r.x+1, r.y+3, r.w-1, fgDim, bgText, "one you open in the browser, e.g. https://bbs.example.ch")

	row := r.y + 5
	for i, f := range v.fields() {
		if row >= r.y+r.h {
			return
		}
		drawText(g, r.x+1, row, r.w-1, fgDim, bgText, pad(f.label+":", 14))
		fg, bg := fgText, bgText
		if i == v.focus {
			fg, bg = fgSelected, bgSelected
		}
		box := rect{r.x + 15, row, max(r.w-16, 1), 1}
		fill(g, box, fg, bg)
		if i == v.focus && f.selected {
			// The selection reads as text on a contrasting bar within
			// the field, the way a highlighted value looks in a dialog.
			sel := f.display()
			fill(g, rect{box.x, box.y, min(len([]rune(sel)), box.w), 1}, fgBar, bgBar)
			drawText(g, box.x, box.y, box.w, fgBar, bgBar, sel)
		} else {
			drawText(g, box.x, box.y, box.w, fg, bg, f.display())
		}
		if i == v.focus {
			a.cursorX, a.cursorY, a.cursorOn = box.x+min(f.cur, box.w-1), box.y, true
		}
		row += 2
	}
	notes := []string{
		"Keep days: packets whose messages are all read are deleted after",
		"this many days; 0 keeps everything.",
		"",
		"Your password is kept in the system's password store, not in a file.",
	}
	if v.havePassword {
		notes = append(notes, "Leave it empty to keep the one already stored.")
	}
	for _, n := range notes {
		if row >= r.y+r.h {
			return
		}
		drawText(g, r.x+1, row, r.w-1, fgDim, bgText, n)
		row++
	}
}

func (v *setupForm) key(a *App, ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyTab, tcell.KeyDown:
		v.setFocus((v.focus + 1) % len(v.fields()))
		return true
	case tcell.KeyBacktab, tcell.KeyUp:
		v.setFocus((v.focus + len(v.fields()) - 1) % len(v.fields()))
		return true
	case tcell.KeyEnter:
		// Enter goes on to the next field that still needs typing and
		// connects once none does: a first run is address, Enter,
		// name, Enter, password, Enter -- and changing one setting
		// later is that change and a single Enter.
		if i := v.nextMissing(); i >= 0 {
			v.setFocus(i)
			return true
		}
		v.submit(a)
		return true
	}
	return v.fields()[v.focus].edit(ev)
}

// nextMissing is the first empty required field after the focused one,
// wrapping around, or -1 when all are filled.
func (v *setupForm) nextMissing() int {
	required := []*field{&v.url, &v.username, &v.password}
	if v.havePassword {
		required = required[:2]
	}
	for step := 1; step <= len(required); step++ {
		i := (v.focus + step) % len(required)
		if strings.TrimSpace(required[i].String()) == "" {
			return i
		}
	}
	return -1
}

func (v *setupForm) submit(a *App) {
	in := SetupInput{
		URL:      strings.TrimSpace(v.url.String()),
		Username: strings.TrimSpace(v.username.String()),
		Password: v.password.String(),
	}
	required := []string{in.URL, in.Username, in.Password}
	if v.havePassword {
		required = required[:2]
	}
	for i, f := range required {
		if f == "" {
			v.setFocus(i)
			a.flash = v.fields()[i].label + " is missing."
			return
		}
	}
	days, err := strconv.Atoi(strings.TrimSpace(v.keepDays.String()))
	if err != nil || days < 0 || days > maxKeepDays {
		v.setFocus(3)
		a.flash = fmt.Sprintf("Keep days must be a number from 0 to %d.", maxKeepDays)
		return
	}
	in.KeepDays = days
	a.run("Connecting to "+in.URL+"...", func(ctx context.Context) func(*App) {
		note, err := a.setup(ctx, in)
		return func(a *App) {
			if err != nil {
				a.flash = err.Error()
				return
			}
			if top, ok := a.top().(*setupForm); ok && top == v {
				a.pop()
			}
			a.setupDefaults = SetupInput{URL: in.URL, Username: in.Username, KeepDays: in.KeepDays, HavePassword: true}
			if !a.hasPacket {
				a.setupNote = note
				a.startFetch()
				return
			}
			a.flash = appendNote("Connected and saved.", note)
		}
	})
}
