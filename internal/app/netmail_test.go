package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/midrei/nullmodem-kit/qwk"
	"github.com/midrei/nullmodem-reader/internal/store"
)

// netmailHarness opens a packet shaped like NullModem BBS's: conference
// 0 flagged netmail in TOREADER.EXT, plus an echo. withNetmail puts a
// netmail from another FTN system into it.
func netmailHarness(t *testing.T, withNetmail bool) *harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NM.QWK")
	control := qwk.ControlInfo{BBSName: "NullModem BBS", BBSID: "NULLMODE", CallerName: "ALICE", Username: "alice",
		Conferences: []qwk.ConferenceInfo{{Number: 0, Name: "Personal"}, {Number: 3, Name: "General"}},
		Areas:       []qwk.AreaEntry{{Number: 0, Flags: "N"}}}
	msgs := []qwk.PackedMessage{{Header: qwk.MessageHeader{Number: 10, Conference: 3, To: "ALL", From: "BOB",
		Subject: "echo", Written: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)}, Text: "hi all"}}
	if withNetmail {
		msgs = append(msgs, qwk.PackedMessage{Header: qwk.MessageHeader{Number: 11, Conference: 0, To: "alice",
			From: "Hans@2:301/1.5", Subject: "from afar", Written: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)}, Text: "hello"})
	}
	if err := qwk.BuildQWKPacket(path, control, msgs); err != nil {
		t.Fatalf("BuildQWKPacket: %v", err)
	}
	q, err := store.OpenQueue(filepath.Join(t.TempDir(), "pending"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(path, openPacket(t, path), Options{Queue: q, From: "alice"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, app: a, w: testWidth, h: testHeight, queue: q}
	h.draw()
	return h
}

func TestNetmailConferenceIsListedEvenWhenEmpty(t *testing.T) {
	h := netmailHarness(t, false)
	h.assertContains("Personal", "the netmail conference is offered without mail in it")
	h.assertContains(" NET ", "and marked as netmail")
}

func TestNewNetmailToSomeoneElsewhere(t *testing.T) {
	h := netmailHarness(t, false)
	h.useFakeEditor("Hallo Hans")
	h.down() // anywhere but the netmail conference: N works from here
	h.typ('N')
	h.assertContains("New message in Personal", "N opens a netmail")
	h.assertContains("Address:", "with an address field")

	h.typeText("Hans Muster")
	h.key(tcell.KeyTab)
	h.typeText("2:301/1.5")
	h.key(tcell.KeyTab)
	h.typeText("Gruss")
	h.enter()

	queued, _ := h.queue.List()
	if len(queued) != 1 || queued[0].To != "Hans Muster@2:301/1.5" || queued[0].Conference != 0 {
		t.Fatalf("queued = %+v, want To Hans Muster@2:301/1.5 in conference 0", queued)
	}
}

func TestNetmailAddressIsChecked(t *testing.T) {
	h := netmailHarness(t, false)
	h.typ('N')
	h.typeText("Hans")
	h.key(tcell.KeyTab)
	h.typeText("hans@example.com")
	h.enter()
	h.assertContains("is not an FTN address", "a wrong address is refused before the editor opens")
	if n, _ := h.queue.Len(); n != 0 {
		t.Fatal("queued despite the bad address")
	}
}

func TestReplyToRemoteNetmailSplitsNameAndAddress(t *testing.T) {
	h := netmailHarness(t, true)
	h.enter() // Personal is first
	h.enter() // the netmail
	h.typ('r')
	h.assertContains("Hans", "the name is prefilled")
	h.assertContains("2:301/1.5", "and the address in its own field")
	if strings.Contains(h.text(), "Hans@2:301") {
		t.Fatalf("the combined form leaked into the To field:\n%s", h.text())
	}
}

func TestOutboxShowsARefusedMessageAndEditingReleasesIt(t *testing.T) {
	h := netmailHarness(t, false)
	stored, err := h.queue.Add(store.Reply{Conference: 0, ConferenceName: "Personal", To: "nobdy", From: "alice",
		Subject: "lost", Body: "text"})
	if err != nil {
		t.Fatal(err)
	}
	stored.Error = `unknown recipient "nobdy"`
	if err := h.queue.Update(stored); err != nil {
		t.Fatal(err)
	}
	h.app.fetch = func(context.Context) (FetchResult, error) { return FetchResult{}, nil }

	h.typ('o')
	h.assertContains(`! Refused: unknown recipient "nobdy"`, "the reason is shown on the held message")
	h.useFakeEditor("")
	h.typ('e')
	h.assertContains("Edit queued message in Personal", "e opens it for editing")
	h.assertContains("Refused last time", "with the reason")
	h.key(tcell.KeyCtrlU)
	h.typeText("nobody")
	h.enter()

	left, _ := h.queue.List()
	if len(left) != 1 || left[0].To != "nobody" || left[0].Held() || !strings.Contains(left[0].Body, "text") {
		t.Fatalf("queue = %+v, want the same message, recipient fixed, no longer held, text kept", left)
	}
	h.assertContains("Saved -- press f to send.", "and says what happens next")
}

func TestSplitRecipient(t *testing.T) {
	for in, want := range map[string][2]string{
		"Hans Muster@2:301/1.5": {"Hans Muster", "2:301/1.5"},
		"bob":                   {"bob", ""},
		"hans@example.com":      {"hans@example.com", ""},
		"2:301/1":               {"2:301/1", ""},
	} {
		if name, addr := splitRecipient(in); name != want[0] || addr != want[1] {
			t.Errorf("splitRecipient(%q) = %q, %q", in, name, addr)
		}
	}
}
