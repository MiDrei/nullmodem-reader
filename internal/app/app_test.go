package app

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"git.maik.ch/nullmodem/kit/ansi"
	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/store"
)

const testWidth, testHeight = 80, 24

// welcomeArt is a miniature CP437/ANSI screen: box-drawing glyphs,
// colors, and a cursor jump that only lands correctly once the bytes
// have been through the Grid.
const welcomeArt = "\x1b[2J\x1b[H\x1b[0;1;36m\xc9\xcd\xcd\xbb\r\n\xba\x1b[0;1;33mHI\x1b[0;1;36m\xba\r\n\xc8\xcd\xcd\xbc\r\n" +
	"\x1b[8;20H\x1b[0;1;35mplaced by cursor\x1b[0m"

// buildPacket writes a .QWK covering what the frontend has to cope
// with: several conferences, CP437 in the header fields, a body long
// enough to scroll, ANSI art in a message, and a welcome screen.
func buildPacket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "TEST.QWK")

	control := qwk.ControlInfo{
		BBSName:     "NullModem BBS",
		BBSID:       "NULLMDM",
		PacketTime:  time.Date(2026, 9, 24, 17, 5, 0, 0, time.UTC),
		CallerName:  "ALICE",
		Username:    "alice",
		WelcomeFile: "WELCOME.ANS",
		Conferences: []qwk.ConferenceInfo{
			{Number: 0, Name: "Personal"},
			{Number: 3, Name: "Go Programming"},
			{Number: 7, Name: "ANSI Art"},
		},
	}

	// Numbered lines: identical ones would make scrolling look like a
	// no-op even when it worked.
	var body strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&body, "Zeile %02d Flie\xe1text.\n", i)
	}
	long := strings.TrimSpace(body.String())
	messages := []qwk.PackedMessage{
		// 0xE1 is ß, 0x81 is ü -- header fields are CP437 too.
		{Header: qwk.MessageHeader{Number: 1, Conference: 3, To: "ALICE", From: "BOB",
			Subject: "Gr\x81\xe1e", Written: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)},
			Text: long},
		{Header: qwk.MessageHeader{Number: 2, Conference: 3, To: "ALICE", From: "CAROL",
			Subject: "Zweite Nachricht", Written: time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC)},
			Text: "kurz"},
		{Header: qwk.MessageHeader{Number: 3, Conference: 7, To: "ALL", From: "SYSOP",
			Subject: "Art", Written: time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)},
			Text: "\x1b[0;1;31m\xdb\xdb\xdb\x1b[0m"},
		{Header: qwk.MessageHeader{Number: 4, Status: '*', Conference: 0, To: "ALICE", From: "SYSOP",
			Subject: "Privat", Written: time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)},
			Text: "vertraulich"},
	}

	if err := qwk.BuildQWKPacket(path, control, messages); err != nil {
		t.Fatalf("BuildQWKPacket: %v", err)
	}
	appendToZip(t, path, "WELCOME.ANS", []byte(welcomeArt))
	return path
}

// appendToZip rewrites the archive with one more entry, since
// archive/zip cannot append in place.
func appendToZip(t *testing.T, path, name string, data []byte) {
	t.Helper()

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			zr.Close()
			t.Fatalf("opening %s in %s: %v", f.Name, path, err)
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			zr.Close()
			t.Fatalf("reading %s in %s: %v", f.Name, path, err)
		}
		entries = append(entries, entry{f.Name, buf})
	}
	zr.Close()
	entries = append(entries, entry{name, data})

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("recreating %s: %v", path, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("creating %s: %v", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatalf("writing %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("finishing %s: %v", path, err)
	}
}

// harness drives the interface directly: keys in, grid out.
//
// No terminal and no window is involved, which is the point of the
// App owning neither -- the interface can be tested as the pure
// function of state and keypresses that it is.
type harness struct {
	t     *testing.T
	app   *App
	w, h  int
	grid  ansi.Grid
	queue *store.Queue
	read  *store.ReadState
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	base := t.TempDir()
	q, err := store.OpenQueue(filepath.Join(base, "pending"))
	if err != nil {
		t.Fatalf("OpenQueue: %v", err)
	}
	read, err := store.LoadReadState(filepath.Join(base, "readstate.json"))
	if err != nil {
		t.Fatalf("LoadReadState: %v", err)
	}

	path := buildPacket(t)
	a, err := New(path, openPacket(t, path), Options{Queue: q, Read: read, From: "Alice Example"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, app: a, w: testWidth, h: testHeight, queue: q, read: read}
	h.draw()
	return h
}

func (h *harness) draw() { h.grid = h.app.Render(h.w, h.h) }

func openPacket(t *testing.T, path string) *qwk.Packet {
	t.Helper()
	p, err := qwk.OpenPacket(path)
	if err != nil {
		t.Fatalf("OpenPacket: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func (h *harness) resize(w, height int) {
	h.w, h.h = w, height
	h.draw()
}

// typeText enters a string into the focused field, one keypress at a
// time, the way a person would.
func (h *harness) typeText(text string) {
	h.t.Helper()
	for _, r := range text {
		h.typ(r)
	}
}

// useFakeEditor points $VISUAL at a script that appends body to
// whatever file it is handed, so the compose flow can be driven end
// to end without a human at a terminal.
func (h *harness) useFakeEditor(body string) {
	h.t.Helper()
	if runtime.GOOS == "windows" {
		h.t.Skip("the fake editor is a shell script")
	}
	dir := h.t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	content := "#!/bin/sh\ncat >> \"$1\" <<'NMREOF'\n" + body + "\nNMREOF\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		h.t.Fatalf("writing the fake editor: %v", err)
	}
	h.t.Setenv("VISUAL", script)
	h.t.Setenv("EDITOR", script)
}

// press delivers one keypress and redraws, the way the event loop
// would.
func (h *harness) press(key tcell.Key, r rune) {
	h.t.Helper()
	h.app.HandleKey(tcell.NewEventKey(key, r, tcell.ModNone))
	h.draw()
}

func (h *harness) typ(r rune)      { h.press(tcell.KeyRune, r) }
func (h *harness) enter()          { h.press(tcell.KeyEnter, 0) }
func (h *harness) esc()            { h.press(tcell.KeyEscape, 0) }
func (h *harness) down()           { h.press(tcell.KeyDown, 0) }
func (h *harness) key(k tcell.Key) { h.press(k, 0) }

// row returns one grid row as text, trailing blanks trimmed.
func (h *harness) row(y int) string {
	var b strings.Builder
	for x := 0; x < h.grid.Width; x++ {
		b.WriteRune(ansi.Rune(h.grid.Cells[y*h.grid.Width+x].Char))
	}
	return strings.TrimRight(b.String(), " ")
}

// text is the whole grid as one string, for substring assertions.
func (h *harness) text() string {
	var b strings.Builder
	for y := 0; y < h.grid.Height; y++ {
		b.WriteString(h.row(y))
		b.WriteByte('\n')
	}
	return b.String()
}

func (h *harness) assertContains(want, why string) {
	h.t.Helper()
	if !strings.Contains(h.text(), want) {
		h.t.Fatalf("%s\nscreen does not contain %q:\n%s", why, want, h.text())
	}
}

// ------------------------------------------------------------ tests

func TestOpensOnTheConferenceList(t *testing.T) {
	h := newHarness(t)

	h.assertContains("NullModem BBS", "the header bar names the board")
	h.assertContains("ALICE", "the header bar names the caller")
	h.assertContains("Personal", "conference 0")
	h.assertContains("Go Programming", "conference 3")
	h.assertContains("ANSI Art", "conference 7")
	h.assertContains("4 msg", "the header bar counts every message in the packet")
}

func TestConferencesAreSortedByNumberWithCounts(t *testing.T) {
	h := newHarness(t)

	// Row 1 is the first content row, under the header bar.
	if got := h.row(1); !strings.Contains(got, "Personal") || !strings.Contains(got, "1 msg") {
		t.Fatalf("first row = %q, want conference 0 with its count", got)
	}
	if got := h.row(2); !strings.Contains(got, "Go Programming") || !strings.Contains(got, "2 msg") {
		t.Fatalf("second row = %q, want conference 3 with its count", got)
	}
}

func TestEnterOpensTheMessageListAndEscapeGoesBack(t *testing.T) {
	h := newHarness(t)
	h.down() // Go Programming
	h.enter()

	h.assertContains("BOB", "the message list shows senders")
	h.assertContains("CAROL", "the message list shows every message in the conference")
	h.assertContains("Zweite Nachricht", "the message list shows subjects")

	h.esc()
	h.assertContains("ANSI Art", "Escape returns to the conference list")
}

// Header fields are CP437 like bodies are; the mojibake only shows up
// once someone writes from a German-language board.
func TestSubjectsAreDecodedFromCP437(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()

	h.assertContains("Grüße", "the subject's CP437 umlauts are decoded")
	if strings.Contains(h.text(), "\ufffd") {
		t.Fatalf("screen carries replacement characters:\n%s", h.text())
	}
}

// Unread and private get a column each: a private message you have
// not read yet is exactly the one you most want to spot, and one
// shared column would hide whichever marker lost.
func TestUnreadAndPrivateAreMarkedIndependently(t *testing.T) {
	h := newHarness(t)
	h.enter() // Personal, holding the one private message

	if got := h.row(1); !strings.HasPrefix(got, " \u2022*") {
		t.Fatalf("row = %q, want it marked both unread and private", got)
	}

	h.enter() // read it
	h.esc()
	if got := h.row(1); !strings.HasPrefix(got, "  *") {
		t.Fatalf("row = %q, want the private marker kept once it is read", got)
	}
}

func TestReadingAMessageShowsItsHeaderAndBody(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()
	h.enter()

	h.assertContains("From:", "the message view labels its fields")
	h.assertContains("BOB", "the sender")
	h.assertContains("Grüße", "the subject")
	h.assertContains("2026-09-24", "the date")
	h.assertContains("Fließtext", "the decoded body")
	h.assertContains("1/2", "the position within the conference")
}

func TestLongBodyScrolls(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()
	h.enter()

	before := h.text()
	if !strings.Contains(before, "Zeile 01") {
		t.Fatalf("the message does not open at its first line:\n%s", before)
	}

	h.key(tcell.KeyPgDn)
	after := h.text()
	if after == before {
		t.Fatal("PgDn changed nothing -- a body longer than the window must scroll")
	}
	if strings.Contains(after, "Zeile 01") {
		t.Fatalf("PgDn did not move past the first line:\n%s", after)
	}

	h.key(tcell.KeyEnd)
	if !strings.Contains(h.text(), "Zeile 60") {
		t.Fatalf("End did not reach the last line:\n%s", h.text())
	}

	h.key(tcell.KeyHome)
	if h.text() != before {
		t.Fatal("Home did not return to the top of the message")
	}
}

// A short message must not be scrollable off the screen.
func TestShortBodyDoesNotScrollAway(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()
	h.down() // the "kurz" message
	h.enter()

	h.assertContains("kurz", "the short body is visible")
	for i := 0; i < 5; i++ {
		h.key(tcell.KeyPgDn)
	}
	h.assertContains("kurz", "paging past the end of a short body must not scroll it out of view")
}

func TestNextAndPreviousMessage(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()
	h.enter()

	h.typ('n')
	h.assertContains("CAROL", "n moves to the next message")
	h.assertContains("2/2", "the position updates")

	h.typ('p')
	h.assertContains("BOB", "p moves back")

	// At the first message, p must say so rather than doing nothing.
	h.typ('p')
	h.assertContains("No further message", "stepping past the first message explains itself")
}

func TestANSIArtInAMessageKeepsItsGlyphsAndColor(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.down() // ANSI Art
	h.enter()
	h.enter()

	h.assertContains("███", "the CP437 block glyphs are rendered")

	var found bool
	for _, c := range h.grid.Cells {
		if c.Char == 0xDB && c.FG == 9 { // full block, bright red
			found = true
		}
	}
	if !found {
		t.Fatal("the blocks did not keep the art's bright red")
	}
}

func TestWelcomeScreenIsResolvedThroughTheGrid(t *testing.T) {
	h := newHarness(t)
	h.typ('w')

	h.assertContains("╔══╗", "the welcome screen's box-drawing glyphs")
	h.assertContains("HI", "its text")
	h.assertContains("placed by cursor", "the cursor-addressed line")

	// The cursor-addressed text is written to row 8 of the art, which
	// is well below the three rows drawn before it -- proof the art
	// went through the Grid rather than being echoed in byte order.
	lines := strings.Split(h.text(), "\n")
	var boxRow, cursorRow = -1, -1
	for i, l := range lines {
		if strings.Contains(l, "HI") {
			boxRow = i
		}
		if strings.Contains(l, "placed by cursor") {
			cursorRow = i
		}
	}
	if boxRow < 0 || cursorRow < 0 || cursorRow-boxRow < 5 {
		t.Fatalf("box at row %d, cursor text at row %d -- the cursor move was not honored", boxRow, cursorRow)
	}
}

func TestWelcomeKeyExplainsItselfWhenThereIsNoScreen(t *testing.T) {

	// A packet with messages but no welcome screen.
	path := filepath.Join(t.TempDir(), "BARE.QWK")
	err := qwk.BuildQWKPacket(path, qwk.ControlInfo{
		BBSName: "Bare", BBSID: "BARE",
		Conferences: []qwk.ConferenceInfo{{Number: 0, Name: "Personal"}},
	}, []qwk.PackedMessage{{Header: qwk.MessageHeader{Number: 1, Conference: 0}, Text: "hi"}})
	if err != nil {
		t.Fatalf("BuildQWKPacket: %v", err)
	}

	a, err := New(path, openPacket(t, path), Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, app: a, w: testWidth, h: testHeight}
	h.draw()
	h.typ('w')

	h.assertContains("no welcome screen", "the reader says why nothing happened")
}

func TestHelpOverlayOpensAndTheDismissingKeyDoesNothingElse(t *testing.T) {
	h := newHarness(t)
	h.typ('?')
	h.assertContains("Keys", "the help overlay is shown")

	// Enter dismisses the overlay -- and must not also descend into
	// the selected conference.
	h.enter()
	h.assertContains("Go Programming", "the conference list is back")
	if strings.Contains(h.text(), "Enter:read") {
		t.Fatal("the key that closed help also acted on the view behind it")
	}
}

func TestQuitFromTheTopLevelAndBackOutFromDeeper(t *testing.T) {
	h := newHarness(t)

	h.enter() // into a conference
	h.typ('q')
	if h.app.quit {
		t.Fatal("q inside a conference should go back, not quit")
	}
	h.assertContains("Go Programming", "q returned to the conference list")

	h.typ('q')
	if !h.app.quit {
		t.Fatal("q at the top level should quit")
	}
}

func TestEscapeAtTheTopLevelDoesNotQuit(t *testing.T) {
	h := newHarness(t)
	h.esc()

	if h.app.quit {
		t.Fatal("Escape at the top level must not quit by surprise")
	}
	h.assertContains("Personal", "the conference list is still shown")
}

func TestNarrowWindowRelaysTheBody(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()
	h.enter()

	wide := h.text()
	h.resize(40, testHeight)

	if h.grid.Width != 40 {
		t.Fatalf("width = %d, want the resized 40", h.grid.Width)
	}
	for y := 0; y < h.grid.Height; y++ {
		if got := h.row(y); len([]rune(got)) > 40 {
			t.Fatalf("row %d = %q, wider than the window", y, got)
		}
	}
	if h.text() == wide {
		t.Fatal("the body was not relaid out for the narrower window")
	}
}

func TestTinyWindowSaysSoRatherThanMisdrawing(t *testing.T) {
	h := newHarness(t)
	h.resize(10, 3)

	if got := h.row(0); !strings.HasPrefix(got, "Window too") {
		t.Fatalf("row 0 = %q, want the too-small notice", got)
	}
}

// ------------------------------------------------- composing replies

func TestReplyPrefillsRecipientSubjectAndQuote(t *testing.T) {
	h := newHarness(t)
	h.down() // Go Programming
	h.enter()
	h.enter() // read BOB's message
	h.typ('r')

	h.assertContains("Reply in Go Programming", "the form says what is being answered")
	h.assertContains("Alice Example", "the form shows who it will be sent as")
	h.assertContains("BOB", "the recipient is prefilled from the original sender")
	h.assertContains("Re: Grüße", "the subject is prefilled with Re:")
	h.assertContains("BO> Zeile 01", "the original is quoted with the sender's initials")
}

func TestReplyGoesThroughTheEditorIntoTheQueue(t *testing.T) {
	h := newHarness(t)
	h.useFakeEditor("Das ist meine Antwort.")

	h.down()
	h.enter()
	h.enter()
	h.typ('r')
	h.enter() // hand off to the editor

	pending, err := h.queue.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("queue holds %d replies, want 1", len(pending))
	}
	got := pending[0]
	if got.To != "BOB" || got.Subject != "Re: Grüße" {
		t.Fatalf("queued reply = %+v, want the form's fields", got)
	}
	if got.Conference != 3 {
		t.Fatalf("Conference = %d, want 3 -- the conference it was written in", got.Conference)
	}
	if got.RefNumber != 1 {
		t.Fatalf("RefNumber = %d, want the message being answered", got.RefNumber)
	}
	if !strings.Contains(got.Body, "Das ist meine Antwort.") {
		t.Fatalf("Body = %q, want what the editor wrote", got.Body)
	}
	if !strings.Contains(got.Body, "BO> Zeile 01") {
		t.Fatalf("Body = %q, want the quote kept", got.Body)
	}
	if strings.Contains(got.Body, ";") && strings.Contains(got.Body, "Lines starting") {
		t.Fatalf("Body = %q, want the template's hint line stripped", got.Body)
	}

	h.assertContains("Queued", "the reader confirms the message is waiting")
}

func TestEditedFieldsReachTheQueue(t *testing.T) {
	h := newHarness(t)
	h.useFakeEditor("kurz und gut")

	h.down()
	h.enter()
	h.enter()
	h.typ('r')

	// Clear the recipient and type another.
	h.press(tcell.KeyCtrlU, 0)
	h.typeText("CAROL")
	h.press(tcell.KeyTab, 0)
	h.press(tcell.KeyCtrlU, 0)
	h.typeText("Ein anderer Betreff")
	h.enter()

	pending, _ := h.queue.List()
	if len(pending) != 1 {
		t.Fatalf("queue holds %d replies, want 1", len(pending))
	}
	if pending[0].To != "CAROL" || pending[0].Subject != "Ein anderer Betreff" {
		t.Fatalf("queued reply = %+v, want the edited fields", pending[0])
	}
}

// An empty editor buffer means the user changed their mind.
func TestEmptyDraftIsDiscardedRatherThanQueued(t *testing.T) {
	h := newHarness(t)
	h.useFakeEditor("")

	h.down()
	h.enter()
	h.enter()
	h.typ('r')
	h.enter()

	if n, _ := h.queue.Len(); n != 0 {
		t.Fatalf("queue holds %d replies, want none for an empty draft", n)
	}
	h.assertContains("discarded", "the reader says the message was dropped")
}

func TestReplyWithNoRecipientIsRefused(t *testing.T) {
	h := newHarness(t)
	h.useFakeEditor("etwas Text")

	h.down()
	h.enter()
	h.enter()
	h.typ('r')
	h.press(tcell.KeyCtrlU, 0) // clear the recipient
	h.enter()

	if n, _ := h.queue.Len(); n != 0 {
		t.Fatalf("queue holds %d replies, want none", n)
	}
	h.assertContains("needs a recipient", "the reader says what is missing")
}

func TestWriteNewMessageFromTheMessageList(t *testing.T) {
	h := newHarness(t)
	h.useFakeEditor("eine neue Nachricht")

	h.down() // Go Programming
	h.enter()
	h.typ('e')

	h.assertContains("New message in Go Programming", "the form is for a new message")
	h.assertContains("All", "an echo's default recipient is All")

	h.press(tcell.KeyTab, 0)
	h.typeText("Betreff")
	h.enter()

	pending, _ := h.queue.List()
	if len(pending) != 1 {
		t.Fatalf("queue holds %d replies, want 1", len(pending))
	}
	if pending[0].To != "All" || pending[0].RefNumber != 0 {
		t.Fatalf("queued message = %+v, want a new thread addressed to All", pending[0])
	}
}

func TestEscapeLeavesTheFormWithoutQueueing(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()
	h.enter()
	h.typ('r')
	h.esc()

	if n, _ := h.queue.Len(); n != 0 {
		t.Fatalf("queue holds %d replies, want none after cancelling", n)
	}
	h.assertContains("Grüße", "the message is shown again")
}

func TestOutboxListsAndDiscards(t *testing.T) {
	h := newHarness(t)
	h.useFakeEditor("zum Verwerfen")

	h.down()
	h.enter()
	h.enter()
	h.typ('r')
	h.enter()

	h.esc() // back to the message list
	h.esc() // back to the conferences
	h.typ('o')

	h.assertContains("1 message(s) waiting", "the outbox counts what is queued")
	h.assertContains("Re: Grüße", "the outbox shows the subject")

	h.typ('d')
	if n, _ := h.queue.Len(); n != 0 {
		t.Fatalf("queue holds %d replies, want it emptied", n)
	}
	h.assertContains("Nothing waiting", "the outbox is empty again")
}

// Without a queue the reader must say so rather than losing what
// someone just typed.
func TestComposingIsRefusedWithoutAQueue(t *testing.T) {

	path := buildPacket(t)
	a, err := New(path, openPacket(t, path), Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, app: a, w: testWidth, h: testHeight}
	h.draw()

	h.down()
	h.enter()
	h.typ('e')
	h.assertContains("cannot be written", "the reader explains why composing is unavailable")

	h.esc()
	h.typ('o')
	h.assertContains("No outgoing queue", "the outbox says the same")
}

// A netmail conference needs a real addressee, not a person named
// "All".
func TestNewMessageInNetmailStartsWithAnEmptyRecipient(t *testing.T) {
	h := newHarness(t)
	h.app.m.conferences[1].Netmail = true

	h.down()
	h.enter()
	h.typ('e')

	if strings.Contains(h.text(), "All") {
		t.Fatalf("netmail form prefilled a recipient:\n%s", h.text())
	}
}

// ---------------------------------------------------- read markers

func TestUnreadMessagesAreMarkedAndCounted(t *testing.T) {
	h := newHarness(t)

	// Go Programming holds two messages, both unread to begin with.
	if got := h.row(2); !strings.Contains(got, "2/2 msg") {
		t.Fatalf("conference row = %q, want it to show 2 of 2 unread", got)
	}
	if !strings.Contains(h.row(2), "•") {
		t.Fatalf("conference row = %q, want the unread bullet", h.row(2))
	}

	h.down()
	h.enter()
	if got := h.row(1); !strings.Contains(got, "•") {
		t.Fatalf("message row = %q, want the unread bullet", got)
	}
}

func TestReadingAMessageMarksIt(t *testing.T) {
	h := newHarness(t)
	h.down() // Go Programming
	h.enter()
	h.enter() // read message 1

	if !h.read.IsRead(3, 1) {
		t.Fatal("opening a message should mark it read")
	}
	if h.read.IsRead(3, 2) {
		t.Fatal("reading one message must not mark the next")
	}

	h.esc()
	if strings.Contains(h.row(1), "•") {
		t.Fatalf("message row = %q, want the bullet gone once it is read", h.row(1))
	}
	h.esc()
	if got := h.row(2); !strings.Contains(got, "1/2 msg") {
		t.Fatalf("conference row = %q, want 1 of 2 still unread", got)
	}
}

func TestSteppingToTheNextMessageMarksItToo(t *testing.T) {
	h := newHarness(t)
	h.down()
	h.enter()
	h.enter()
	h.typ('n')

	if !h.read.IsRead(3, 2) {
		t.Fatal("stepping to the next message should mark it read")
	}
}

// Landing on the top of a mostly-read conference means scrolling past
// what you have already seen, every time.
func TestConferenceOpensAtTheFirstUnreadMessage(t *testing.T) {
	h := newHarness(t)
	h.read.MarkRead(3, 1)

	h.down() // Go Programming
	h.enter()
	h.enter() // open whatever the cursor landed on

	h.assertContains("CAROL", "the conference opened at the first unread message, not the top")
	h.assertContains("2/2", "and that is the second of two")
}

func TestFullyReadConferenceOpensAtTheTop(t *testing.T) {
	h := newHarness(t)
	h.read.MarkAllRead(3, []int{1, 2})

	h.down()
	h.enter()
	h.enter()

	h.assertContains("1/2", "with nothing unread there is nothing better to point at than the top")
}

func TestMarkConferenceRead(t *testing.T) {
	h := newHarness(t)
	h.down() // Go Programming
	h.enter()
	h.typ('m')

	if !h.read.IsRead(3, 1) || !h.read.IsRead(3, 2) {
		t.Fatal("m should mark every message in the conference read")
	}
	h.assertContains("Marked Go Programming read", "the reader confirms it")

	h.esc()
	if got := h.row(2); strings.Contains(got, "/2 msg") {
		t.Fatalf("conference row = %q, want no unread count left", got)
	}
}

func TestReadMarkersAreSavedOnTheWayOut(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "readstate.json")

	read, err := store.LoadReadState(path)
	if err != nil {
		t.Fatalf("LoadReadState: %v", err)
	}
	read.MarkRead(3, 1)
	if err := read.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A second session must see what the first one read.
	again, err := store.LoadReadState(path)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if !again.IsRead(3, 1) {
		t.Fatal("the read marker did not survive the session")
	}
}

// A packet opened from a read-only location must still be readable.
func TestReaderWorksWithoutPersistentReadState(t *testing.T) {

	path := buildPacket(t)
	a, err := New(path, openPacket(t, path), Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, app: a, w: testWidth, h: testHeight}
	h.draw()

	h.down()
	h.enter()
	h.enter()
	h.assertContains("Fließtext", "the message is readable")

	// Saving nowhere must not fail.
	if err := h.app.Save(); err != nil {
		t.Fatalf("Save with nowhere to save to: %v", err)
	}
}

// The interface is drawn into a CP437 grid, so a character CP437
// cannot represent turns into a question mark that reads as part of
// the text. Catching that here is cheaper than spotting it in a
// screenshot.
func TestInterfaceUsesOnlyCharactersCP437Has(t *testing.T) {
	h := newHarness(t)

	screens := []func(){
		func() {},                               // conference list
		func() { h.down(); h.enter() },          // message list
		func() { h.enter() },                    // a message
		func() { h.esc(); h.esc(); h.typ('w') }, // welcome screen
		func() { h.esc(); h.typ('?') },          // help overlay
		func() { h.typ(' '); h.typ('o') },       // outbox
	}
	for i, step := range screens {
		step()
		for y := 0; y < h.grid.Height; y++ {
			if strings.ContainsRune(h.row(y), '?') && !strings.Contains(h.row(y), "?:help") {
				t.Fatalf("screen %d row %d = %q holds a '?' -- a character with no CP437 code point", i, y, h.row(y))
			}
		}
	}
}
