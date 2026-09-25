package app

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"

	"git.maik.ch/nullmodem/kit/ansi"

	"git.maik.ch/nullmodem/reader/internal/compose"
	"git.maik.ch/nullmodem/reader/internal/store"
	"git.maik.ch/nullmodem/reader/internal/ui"
)

// field is one line of text the user can edit in the compose form.
type field struct {
	label string
	value []rune
	cur   int
	// mask shows the value as asterisks, for a password.
	mask bool
	// selected means the whole value is highlighted, as when a dialog
	// field gets focus: typing replaces it, Backspace clears it, and a
	// cursor key keeps it and starts editing at that end.
	selected bool
}

// focus is called when the field gets the keyboard, in forms that
// select a prefilled value on entry.
func (f *field) focus() {
	f.selected = len(f.value) > 0
	f.cur = len(f.value)
}

func newField(label, value string) field {
	r := []rune(value)
	return field{label: label, value: r, cur: len(r)}
}

func (f *field) String() string { return string(f.value) }

// display is what the field shows on screen.
func (f *field) display() string {
	if f.mask {
		return strings.Repeat("*", len(f.value))
	}
	return f.String()
}

// paste inserts the first line of text at the cursor, replacing a
// selected value; tabs become spaces and other control characters go.
func (f *field) paste(text string) {
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	var clean []rune
	for _, r := range text {
		switch {
		case r == '\t':
			clean = append(clean, ' ')
		case r >= ' ':
			clean = append(clean, r)
		}
	}
	if f.selected {
		f.value, f.cur, f.selected = nil, 0, false
	}
	f.value = append(f.value[:f.cur], append(clean, f.value[f.cur:]...)...)
	f.cur += len(clean)
}

// edit applies one keypress, reporting whether it was consumed.
// Anything it does not consume falls through to the form's own
// bindings, which is what lets Tab and Enter keep working while a
// field has focus.
func (f *field) edit(ev *tcell.EventKey) bool {
	if f.selected {
		f.selected = false
		switch ev.Key() {
		case tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyDelete, tcell.KeyCtrlD:
			f.value, f.cur = nil, 0
			return true
		case tcell.KeyRune:
			if ev.Rune() >= ' ' {
				f.value, f.cur = nil, 0
			}
		case tcell.KeyLeft, tcell.KeyHome, tcell.KeyCtrlA:
			f.cur = 0
			return true
		case tcell.KeyRight, tcell.KeyEnd, tcell.KeyCtrlE:
			f.cur = len(f.value)
			return true
		}
	}
	switch ev.Key() {
	case tcell.KeyLeft:
		f.cur = clamp(f.cur-1, 0, len(f.value))
	case tcell.KeyRight:
		f.cur = clamp(f.cur+1, 0, len(f.value))
	case tcell.KeyHome, tcell.KeyCtrlA:
		f.cur = 0
	case tcell.KeyEnd, tcell.KeyCtrlE:
		f.cur = len(f.value)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if f.cur > 0 {
			f.value = append(f.value[:f.cur-1], f.value[f.cur:]...)
			f.cur--
		}
	case tcell.KeyDelete, tcell.KeyCtrlD:
		if f.cur < len(f.value) {
			f.value = append(f.value[:f.cur], f.value[f.cur+1:]...)
		}
	case tcell.KeyCtrlU:
		f.value, f.cur = nil, 0
	case tcell.KeyRune:
		r := ev.Rune()
		// A header field is one line; a tab or newline pasted into it
		// would corrupt the fixed-width record it ends up in.
		if r < ' ' {
			return false
		}
		f.value = append(f.value[:f.cur], append([]rune{r}, f.value[f.cur:]...)...)
		f.cur++
	default:
		return false
	}
	return true
}

// composeForm collects the header fields of a reply before handing
// the body to the user's editor.
//
// Only To and Subject are asked for. Everything else a QWK reply
// needs -- the conference, the message being answered, the sender --
// follows from where the user pressed the key, and asking about it
// would be asking them to restate what they just did.
type composeForm struct {
	conf    Conference
	from    string
	subject field
	to      field
	// address is the recipient's FTN address, asked for in a netmail
	// conference only: empty means someone on this BBS.
	address field
	focus   int
	// editing is the queued message being changed, nil for a new one.
	editing *store.Reply
	// taglines are the ones on offer (see offerTaglines); tagIdx picks
	// 0 none, 1 random -- showing randomPick -- or taglines[tagIdx-2].
	taglines   []string
	tagIdx     int
	randomPick string
	// refNumber is the message being answered, 0 for a new thread.
	refNumber int
	// quoted is the prepared quote seeded into the editor buffer.
	quoted string
	// private asks the door to post this as private mail.
	private bool
}

// newReplyForm prepares a reply to m.
func newReplyForm(conf Conference, m Message, from string, width int) *composeForm {
	name, addr := splitRecipient(m.From)
	return &composeForm{
		conf:      conf,
		from:      from,
		to:        newField("To", name),
		address:   newField("Address", addr),
		subject:   newField("Subject", compose.ReplySubject(m.Subject)),
		refNumber: m.Number,
		quoted:    compose.Quote(decodeBody(m.Body), name, width),
		private:   m.Private,
	}
}

// newEditForm reopens a queued message, e.g. one the BBS refused.
func newEditForm(conf Conference, r store.Reply) *composeForm {
	to, addr := r.To, ""
	if conf.Netmail {
		to, addr = splitRecipient(r.To)
	}
	rc := r
	return &composeForm{
		conf:      conf,
		from:      r.From,
		to:        newField("To", to),
		address:   newField("Address", addr),
		subject:   newField("Subject", r.Subject),
		refNumber: r.RefNumber,
		private:   r.Private,
		editing:   &rc,
	}
}

// ftnAddress matches zone:net/node[.point][@domain].
var ftnAddress = regexp.MustCompile(`^\d+:\d+/\d+(\.\d+)?(@[A-Za-z0-9_-]+)?$`)

// splitRecipient takes "Name@zone:net/node" -- how NullModem BBS names
// someone on another FTN system -- apart into name and address. Any
// other value is a plain name.
func splitRecipient(s string) (name, addr string) {
	if at := strings.LastIndex(s, "@"); at > 0 && ftnAddress.MatchString(strings.TrimSpace(s[at+1:])) {
		return strings.TrimSpace(s[:at]), strings.TrimSpace(s[at+1:])
	}
	return s, ""
}

// newMessageForm prepares a fresh message in a conference.
func newMessageForm(conf Conference, from string) *composeForm {
	// "All" is the QWK convention for an echo's broadcast recipient.
	to := "All"
	if conf.Netmail {
		// Netmail needs a real addressee, so the field starts empty
		// rather than quietly addressing a person named "All".
		to = ""
	}
	return &composeForm{
		conf:    conf,
		from:    from,
		to:      newField("To", to),
		address: newField("Address", ""),
		subject: newField("Subject", ""),
	}
}

func (v *composeForm) keyHelp() string {
	if v.onTagline() {
		return "←→:choose tagline  space:another random  Enter:write the message  Esc:cancel"
	}
	return "Tab:field  Enter:write the message  Esc:cancel"
}

// offerTaglines adds the tagline choice to a new message or reply,
// starting at last time's pick. Editing a queued message never gets
// one: its text already went through this once.
func (v *composeForm) offerTaglines(taglines []string, choice string) {
	if len(taglines) == 0 || v.editing != nil {
		return
	}
	v.taglines = taglines
	v.randomPick = taglines[rand.IntN(len(taglines))]
	switch choice {
	case TaglineNone:
		v.tagIdx = 0
	case TaglineRandom, "":
		v.tagIdx = 1
	default:
		v.tagIdx = 1
		for i, t := range taglines {
			if t == choice {
				v.tagIdx = i + 2
			}
		}
	}
}

// focusCount is the text fields plus the tagline row, if offered.
func (v *composeForm) focusCount() int {
	if len(v.taglines) > 0 {
		return len(v.fields()) + 1
	}
	return len(v.fields())
}

func (v *composeForm) onTagline() bool {
	return len(v.taglines) > 0 && v.focus == len(v.fields())
}

// tagline is the chosen tagline's text, "" for none; choice is how to
// remember the pick.
func (v *composeForm) tagline() (text, choice string) {
	switch {
	case len(v.taglines) == 0 || v.tagIdx == 0:
		return "", TaglineNone
	case v.tagIdx == 1:
		return v.randomPick, TaglineRandom
	default:
		t := v.taglines[v.tagIdx-2]
		return t, t
	}
}

func (v *composeForm) fields() []*field {
	if v.conf.Netmail {
		return []*field{&v.to, &v.address, &v.subject}
	}
	return []*field{&v.to, &v.subject}
}

// recipient is the To the queued message carries: in a netmail
// conference with an address, "Name@address" (or the bare address for
// that system's sysop), which is the form the BBS routes.
func (v *composeForm) recipient() (string, error) {
	to := strings.TrimSpace(v.to.String())
	addr := strings.TrimSpace(v.address.String())
	if !v.conf.Netmail || addr == "" {
		return to, nil
	}
	if !ftnAddress.MatchString(addr) {
		return "", fmt.Errorf("%q is not an FTN address -- it looks like 2:301/1 or 2:301/1.5", addr)
	}
	if to == "" {
		return addr, nil
	}
	return to + "@" + addr, nil
}

func (v *composeForm) draw(a *App, g *ansi.Grid, r rect) {
	title := "New message in " + v.conf.Name
	switch {
	case v.editing != nil:
		title = "Edit queued message in " + v.conf.Name
	case v.refNumber > 0:
		title = "Reply in " + v.conf.Name
	}
	drawText(g, r.x+1, r.y, r.w-1, fgAccent, bgText, title)

	row := r.y + 2
	drawText(g, r.x+1, row, r.w-1, fgDim, bgText, pad("From:", 10))
	drawText(g, r.x+11, row, r.w-11, fgText, bgText, v.from)
	row++

	for i, f := range v.fields() {
		if row >= r.y+r.h {
			return
		}
		drawText(g, r.x+1, row, r.w-1, fgDim, bgText, pad(f.label+":", 10))

		fg, bg := fgText, bgText
		if i == v.focus {
			fg, bg = fgSelected, bgSelected
		}
		// The input area is painted full width so the focused field
		// reads as a box rather than as highlighted text.
		box := rect{r.x + 11, row, max(r.w-12, 1), 1}
		fill(g, box, fg, bg)
		drawText(g, box.x, box.y, box.w, fg, bg, f.String())
		if i == v.focus {
			a.cursorX, a.cursorY, a.cursorOn = box.x+min(f.cur, box.w-1), box.y, true
		}
		row++
		if f == &v.address && row < r.y+r.h {
			// The hint sits right under the field it explains.
			drawText(g, r.x+11, row, r.w-11, fgDim, bgText,
				"empty for someone on this BBS, else e.g. 2:301/1")
			row++
		}
	}

	if len(v.taglines) > 0 && row < r.y+r.h {
		drawText(g, r.x+1, row, r.w-1, fgDim, bgText, pad("Tagline:", 10))
		text, _ := v.tagline()
		label := "... " + text
		switch v.tagIdx {
		case 0:
			label = "(none)"
		case 1:
			label = "(random) ... " + text
		}
		label = "← " + label + " →"
		fg, bg := fgText, bgText
		if v.onTagline() {
			fg, bg = fgSelected, bgSelected
			fill(g, rect{r.x + 11, row, max(r.w-12, 1), 1}, fg, bg)
		}
		drawText(g, r.x+11, row, r.w-12, fg, bg, label)
		row++
	}
	if v.editing != nil && v.editing.Error != "" && row < r.y+r.h {
		drawText(g, r.x+1, row, r.w-1, fgAccent, bgText, "Refused last time: "+v.editing.Error)
		row++
	}
	row++
	if v.private {
		drawText(g, r.x+1, row, r.w-1, fgDim, bgText, "Private message")
		row++
	}
	if v.quoted != "" {
		row++
		drawText(g, r.x+1, row, r.w-1, fgDim, bgText, "The original will be quoted in your editor:")
		row++
		for _, line := range strings.Split(v.quoted, "\n") {
			if row >= r.y+r.h {
				break
			}
			drawText(g, r.x+3, row, r.w-3, fgDim, bgText, line)
			row++
		}
	}
}

func (v *composeForm) paste(a *App, text string) {
	if v.onTagline() {
		return
	}
	v.fields()[v.focus].paste(text)
}

func (v *composeForm) key(a *App, ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyTab, tcell.KeyDown:
		v.focus = (v.focus + 1) % v.focusCount()
		return true
	case tcell.KeyBacktab, tcell.KeyUp:
		v.focus = (v.focus + v.focusCount() - 1) % v.focusCount()
		return true
	case tcell.KeyEnter:
		a.writeAndQueue(v)
		return true
	}
	if v.onTagline() {
		n := len(v.taglines) + 2
		switch {
		case ev.Key() == tcell.KeyRight:
			v.tagIdx = (v.tagIdx + 1) % n
		case ev.Key() == tcell.KeyLeft:
			v.tagIdx = (v.tagIdx + n - 1) % n
		case ev.Rune() == ' ':
			if v.tagIdx == 1 {
				v.randomPick = v.taglines[rand.IntN(len(v.taglines))]
			} else {
				v.tagIdx = (v.tagIdx + 1) % n
			}
		}
		// Letters mean nothing here, and must not fall through to the
		// reader's own single-key commands.
		return true
	}
	return v.fields()[v.focus].edit(ev)
}

// writeAndQueue takes the form to the editor and, if anything came
// back, puts the result in the outgoing queue.
func (a *App) writeAndQueue(v *composeForm) {
	if a.queue == nil {
		a.flash = "Replies cannot be queued: no data directory for this packet."
		return
	}
	to, err := v.recipient()
	if err != nil {
		a.flash = err.Error()
		return
	}
	if to == "" {
		a.flash = "This message needs a recipient."
		return
	}

	// The built-in editor, unless someone has chosen their own with
	// VISUAL or EDITOR -- which is also the only way to get the old
	// external-editor behaviour back.
	if !compose.ExternalEditorChosen() {
		initial := ""
		switch {
		case v.editing != nil:
			initial = v.editing.Body
		case v.quoted != "":
			initial = v.quoted + "\n\n"
		}
		a.push(newEditorView(v, to, initial))
		return
	}

	initial := compose.Template(v.quoted)
	if v.editing != nil {
		initial = compose.EditTemplate(v.editing.Body)
	}
	body, err := a.runEditor(initial)
	if err != nil {
		a.flash = err.Error()
		return
	}
	a.finishCompose(v, to, compose.StripTemplate(body), false)
}

// finishCompose queues what was written -- or, for a queued message
// being edited, saves it back. builtin says the text came from the
// reader's own editor, which shows no tearline: it is added here.
func (a *App) finishCompose(v *composeForm, to, body string, builtin bool) {
	body = strings.TrimRight(body, " \n")
	if compose.IsEmpty(body) {
		a.popTo(v)
		a.flash = "Nothing written -- the message was discarded."
		return
	}
	if builtin {
		body = compose.WrapForSending(body)
		if v.editing == nil {
			body = compose.WithTearline(body)
		}
	}

	if v.editing != nil {
		// Changing it is the user's answer to whatever the BBS
		// objected to, so it goes out again with the next exchange.
		r := *v.editing
		r.To, r.Subject, r.Body, r.Error = to, strings.TrimSpace(v.subject.String()), body, ""
		if err := a.queue.Update(r); err != nil {
			a.flash = "Could not save the message: " + err.Error()
			return
		}
		a.popTo(v)
		if o, ok := a.top().(*outbox); ok {
			o.reload()
		}
		a.flash = "Saved -- " + a.sendHint() + "."
		return
	}

	tagline, choice := v.tagline()
	body = compose.WithTagline(body, tagline)
	if len(v.taglines) > 0 && choice != a.taglineChoice {
		a.taglineChoice = choice
		if a.saveTaglineChoice != nil {
			a.saveTaglineChoice(choice)
		}
	}

	stored, err := a.queue.Add(store.Reply{
		Conference:     v.conf.Number,
		ConferenceName: v.conf.Name,
		To:             to,
		From:           v.from,
		Subject:        strings.TrimSpace(v.subject.String()),
		Body:           body,
		RefNumber:      v.refNumber,
		Private:        v.private,
	})
	if err != nil {
		a.flash = "Could not queue the message: " + err.Error()
		return
	}

	a.popTo(v)
	n, _ := a.queue.Len()
	a.flash = fmt.Sprintf("Queued for %s (%d waiting) -- %s. [%s]",
		v.conf.Name, n, a.sendHint(), stored.ID)
}

// popTo removes v and everything above it -- the form and, when the
// built-in editor was used, the editor on top of it.
func (a *App) popTo(v view) {
	for i := len(a.stack) - 1; i > 0; i-- {
		if a.stack[i] == v {
			a.stack = a.stack[:i]
			return
		}
	}
}

// sendHint says how queued messages go out: from inside the reader
// when it can exchange mail, otherwise from the command line.
func (a *App) sendHint() string {
	if a.fetch != nil {
		return "press f to send"
	}
	return "run \"nmr fetch\" to send"
}

// runEditor hands the display to the user's editor and takes it back
// afterwards.
//
// A terminal frontend has to suspend tcell for this: it owns the
// terminal's raw mode, and an editor started underneath it would
// fight it for every keystroke. Resume is deferred so the frontend
// comes back even when the editor exits badly -- otherwise a mistyped
// $EDITOR would leave the user staring at a dead screen. A frontend
// with no terminal to give up (a GUI) leaves Suspend nil and the
// editor opens in its own window.
func (a *App) runEditor(initial string) (string, error) {
	if a.Suspend != nil {
		if err := a.Suspend(); err != nil {
			return "", fmt.Errorf("could not release the display: %w", err)
		}
		defer func() {
			if a.Resume != nil && a.Resume() != nil {
				// Nothing can be drawn to report this, so quitting is
				// the honest outcome: the alternative is an
				// unresponsive frontend the user has to kill.
				a.quit = true
			}
		}()
	}

	body, err := compose.Edit(initial)
	if errors.Is(err, compose.ErrReturnedAtOnce) {
		return "", err
	}
	if err != nil {
		name, _ := compose.EditorCommand()
		return "", fmt.Errorf("editor %s failed: %w", name, err)
	}
	return body, nil
}

// decodeBody converts a CP437 message body to UTF-8 for quoting. The
// queue and the editor both work in UTF-8; the conversion back to
// CP437 happens once, when the .REP is built.
//
// It goes line by line because ui.DecodeField maps 0x0A to CP437's
// "◙" glyph -- right for art, wrong for the line breaks a body is
// built from.
func decodeBody(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = ui.DecodeField(l)
	}
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------ outbox

// outbox lists the replies waiting to go out, so someone can see what
// they wrote offline and drop a message before it is sent. Once
// "nmr fetch" has run there is no taking it back, which is precisely
// why the list exists.
type outbox struct {
	cursor
	queue   *store.Queue
	replies []store.Reply
	err     error
}

func newOutbox(q *store.Queue) *outbox {
	o := &outbox{queue: q}
	o.reload()
	return o
}

func (v *outbox) reload() {
	v.replies, v.err = v.queue.List()
	if v.sel >= len(v.replies) {
		v.sel = max(len(v.replies)-1, 0)
	}
}

func (v *outbox) keyHelp() string { return "↑↓:move  e:edit  d:discard  Esc:back" }

func (v *outbox) draw(a *App, g *ansi.Grid, r rect) {
	if v.err != nil {
		drawText(g, r.x+1, r.y, r.w-1, fgText, bgText, "Could not read the queue: "+v.err.Error())
		return
	}
	if len(v.replies) == 0 {
		drawText(g, r.x+1, r.y, r.w-1, fgDim, bgText, "Nothing waiting to be sent.")
		return
	}

	drawText(g, r.x+1, r.y, r.w-1, fgAccent, bgText,
		strconv.Itoa(len(v.replies))+" message(s) waiting -- "+a.sendHint())
	if sel := v.replies[v.sel]; sel.Held() {
		// Why the selected one is held, and what to do about it.
		drawText(g, r.x+1, r.y+1, r.w-1, fgWarn, bgWarn,
			" ! Refused: "+sel.Error+" -- e: fix and resend, d: discard ")
	}

	body := rect{r.x, r.y + 2, r.w, r.h - 2}
	v.follow(body.h)

	confW := clamp(body.w/4, 10, 24)
	toW := clamp(body.w/6, 8, 18)
	subjW := max(body.w-confW-toW-8, 10)

	for i := 0; i < body.h; i++ {
		idx := i + v.scroll
		if idx >= len(v.replies) {
			break
		}
		m := v.replies[idx]
		fg, bg := fgText, bgText
		if idx == v.sel {
			fg, bg = fgSelected, bgSelected
			fill(g, rect{body.x, body.y + i, body.w, 1}, fg, bg)
		}
		name := m.ConferenceName
		if name == "" {
			name = "Conference " + strconv.Itoa(m.Conference)
		}
		mark := " "
		switch {
		case m.Held():
			mark = "!"
		case m.Private:
			mark = "*"
		}
		line := " " + mark + " " + pad(name, confW) + " " + pad(m.To, toW) + " " + pad(m.Subject, subjW)
		drawText(g, body.x, body.y+i, body.w, fg, bg, line)
	}
}

func (v *outbox) key(a *App, ev *tcell.EventKey) bool {
	if v.listKey(ev, len(v.replies), 10) {
		return true
	}
	if ev.Rune() == 'e' && len(v.replies) > 0 {
		r := v.replies[v.sel]
		a.push(newEditForm(a.conferenceFor(r), r))
		return true
	}
	if ev.Rune() == 'd' {
		if len(v.replies) == 0 {
			return true
		}
		m := v.replies[v.sel]
		if err := v.queue.Remove(m.ID); err != nil {
			a.flash = "Could not discard it: " + err.Error()
			return true
		}
		v.reload()
		a.flash = "Discarded: " + m.Subject
		return true
	}
	return false
}

// conferenceFor is the conference a queued message belongs to: the
// open packets' own, or one rebuilt from what the queue remembers when
// those packets are gone. Conference 0 is netmail by QWK convention.
func (a *App) conferenceFor(r store.Reply) Conference {
	for _, c := range a.m.conferences {
		if c.Number == r.Conference {
			return c
		}
	}
	return Conference{Number: r.Conference, Name: r.ConferenceName, Netmail: r.Conference == 0}
}
