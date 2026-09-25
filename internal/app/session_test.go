package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"git.maik.ch/nullmodem/kit/qwk"
)

// fakeSession stands in for cmd/nmr's session: it records what setup
// received and serves a real packet after a fetch.
type fakeSession struct {
	t          *testing.T
	packetPath string

	mu         sync.Mutex
	setupGot   []SetupInput
	setupNote  string
	setupErr   error
	fetchCalls int
	fetchRes   FetchResult
	fetchErr   error
}

func (f *fakeSession) options() Options {
	return Options{
		Setup: func(ctx context.Context, in SetupInput) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.setupGot = append(f.setupGot, in)
			return f.setupNote, f.setupErr
		},
		Fetch: func(ctx context.Context) (FetchResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.fetchCalls++
			return f.fetchRes, f.fetchErr
		},
		Latest: func() (Loaded, error) {
			p, err := qwk.OpenPacket(f.packetPath)
			if err != nil {
				return Loaded{}, err
			}
			return Loaded{Sources: []Source{{Path: f.packetPath, Packet: p}}, From: "Alice Example"}, nil
		},
	}
}

func newHomeHarness(t *testing.T, f *fakeSession, start Home, note string) *harness {
	t.Helper()
	h := &harness{t: t, app: NewHome(f.options(), start, note), w: testWidth, h: testHeight}
	h.draw()
	return h
}

// settleBackground renders until background work has finished, the
// way a frontend's draw loop picks it up.
func (h *harness) settleBackground() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.draw()
		if h.app.Busy() == "" {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("background work did not finish; still %q", h.app.Busy())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFirstRunSetupThenFetchOpensThePacket(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t), fetchRes: FetchResult{Received: 4}, setupNote: "Password saved in the file."}
	h := newHomeHarness(t, f, HomeSetup, "")

	h.assertContains("Connect to your BBS", "a first run starts on the setup screen")
	h.typeText("bbs.example.ch")
	h.enter()
	h.typeText("alice")
	h.enter()
	h.typeText("s3cret!")
	if strings.Contains(h.text(), "s3cret!") {
		t.Fatalf("the password is shown in clear:\n%s", h.text())
	}
	h.assertContains("*******", "the password field is masked")
	h.enter()

	// Setup, then the fetch it starts, both in the background.
	h.settleBackground()
	h.settleBackground()

	if len(f.setupGot) != 1 || f.setupGot[0] != (SetupInput{URL: "bbs.example.ch", Username: "alice", Password: "s3cret!"}) {
		t.Fatalf("setup got %+v", f.setupGot)
	}
	if f.fetchCalls != 1 {
		t.Fatalf("fetch called %d times, want 1 right after setup", f.fetchCalls)
	}
	h.assertContains("Go Programming", "the fetched packet is open")
	h.assertContains("Received 4 message(s). Password saved in the file.", "the status bar reports the exchange and keeps setup's note")
}

func TestSetupRejectsMissingFieldsWithoutCallingOut(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t)}
	h := newHomeHarness(t, f, HomeSetup, "")

	h.typeText("bbs.example.ch")
	h.key(tcell.KeyTab)
	h.key(tcell.KeyTab) // skip the username
	h.typeText("pw")
	h.enter()

	h.assertContains("Username is missing.", "an empty field is named")
	if len(f.setupGot) != 0 {
		t.Fatalf("setup was called with an empty username: %+v", f.setupGot)
	}
}

func TestFailedSetupStaysOnTheFormWithTheReason(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t), setupErr: errors.New("the BBS rejected that username or password")}
	h := newHomeHarness(t, f, HomeSetup, "")

	h.typeText("bbs.example.ch")
	h.enter()
	h.typeText("alice")
	h.enter()
	h.typeText("wrong")
	h.enter()
	h.settleBackground()

	h.assertContains("rejected that username or password", "the reason is shown")
	h.assertContains("Connect to your BBS", "the form stays open to correct it")
	if f.fetchCalls != 0 {
		t.Fatal("a failed setup must not fetch")
	}
}

func TestConfiguredButEmptyStartsFetchingRightAway(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t), fetchRes: FetchResult{NoNewMail: true}}
	h := newHomeHarness(t, f, HomeFetch, "")
	h.settleBackground()

	if f.fetchCalls != 1 {
		t.Fatalf("fetch called %d times, want 1", f.fetchCalls)
	}
	h.assertContains("No new mail.", "the outcome is reported")
	h.assertContains("Press f to try again", "the start screen says what to do next")
}

func TestRejectedLoginDuringFetchOpensSetup(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t),
		fetchErr: fmt.Errorf("%w: the BBS rejected the login -- check username and password", ErrNeedsSetup)}
	h := newHomeHarness(t, f, HomeFetch, "")
	h.settleBackground()

	h.assertContains("Connect to your BBS", "a login problem leads to the setup screen")
	h.assertContains("the BBS rejected the login", "with the reason")
}

func TestOtherFetchErrorsStayOnTheStartScreen(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t), fetchErr: errors.New("connection refused")}
	h := newHomeHarness(t, f, HomeFetch, "")
	h.settleBackground()

	h.assertContains("Could not exchange mail: connection refused", "the start screen explains")
	if strings.Contains(h.text(), "Connect to your BBS") {
		t.Fatal("a network error is not a reason to re-enter the login")
	}
}

func TestFetchKeyFromAnOpenPacketSwitchesToTheNewOne(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t), fetchRes: FetchResult{Sent: 1, Received: 4}}
	path := buildPacket(t)
	opts := f.options()
	a, err := New(path, openPacket(t, path), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, app: a, w: testWidth, h: testHeight}
	h.draw()
	h.assertContains("f:fetch", "the status bar offers fetching")

	h.enter() // into a conference, to see the switch reset the views
	h.typ('f')
	h.assertContains("Exchanging mail", "the status bar shows the exchange running")
	h.settleBackground()

	h.assertContains("Sent 1, received 4 message(s).", "the outcome is reported")
	h.assertContains("Go Programming", "back at the conference list of the new packet")
}

func TestKeysAreIgnoredWhileBusyButQuitWorks(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t)}
	block := make(chan struct{})
	opts := f.options()
	opts.Fetch = func(ctx context.Context) (FetchResult, error) {
		<-block
		return FetchResult{NoNewMail: true}, nil
	}
	h := &harness{t: t, app: NewHome(opts, HomeFetch, ""), w: testWidth, h: testHeight}
	h.draw()
	defer close(block)

	h.typ('s')
	if strings.Contains(h.text(), "Connect to your BBS") {
		t.Fatal("setup opened while an exchange was running")
	}
	h.typ('Q')
	if !h.app.Quit() {
		t.Fatal("Q must still quit while busy")
	}
}

func TestComposeHintMentionsTheFetchKeyWhenThereIsOne(t *testing.T) {
	h := newHarness(t)
	if got := h.app.sendHint(); !strings.Contains(got, "nmr fetch") {
		t.Fatalf("without a fetch hook the hint is %q, want the command line", got)
	}
	h.app.fetch = func(context.Context) (FetchResult, error) { return FetchResult{}, nil }
	if got := h.app.sendHint(); got != "press f to send" {
		t.Fatalf("hint = %q", got)
	}
}

func TestSetupSelectsAPrefilledFieldSoTypingReplacesIt(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t)}
	opts := f.options()
	opts.SetupDefaults = SetupInput{URL: "https://old.example", Username: "olduser"}
	h := &harness{t: t, app: NewHome(opts, HomeSetup, ""), w: testWidth, h: testHeight}
	h.draw()

	// Focus starts on the empty password; going up selects the username.
	h.key(tcell.KeyUp)
	h.typeText("alice")
	h.assertContains("alice", "the new name is shown")
	if strings.Contains(h.text(), "olduser") {
		t.Fatalf("typing appended instead of replacing:\n%s", h.text())
	}

	// A cursor key keeps the value and edits from there.
	h.key(tcell.KeyUp)
	h.key(tcell.KeyEnd)
	h.typeText("/bbs")
	h.assertContains("https://old.example/bbs", "End keeps the selected value and appends")

	// Backspace on a selected value clears it.
	h.key(tcell.KeyTab)
	h.key(tcell.KeyBackspace2)
	h.typeText("bob")
	h.key(tcell.KeyTab)
	h.typeText("pw")
	h.enter()
	h.settleBackground()
	h.settleBackground()
	if got := f.setupGot[0]; got.URL != "https://old.example/bbs" || got.Username != "bob" {
		t.Fatalf("setup got %+v", got)
	}
}
