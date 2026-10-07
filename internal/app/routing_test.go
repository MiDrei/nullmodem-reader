package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/midrei/nullmodem-kit/qwk"
	"github.com/midrei/nullmodem-reader/internal/store"
)

const routedBody = "Ping!\nZweite Zeile\nDritte Zeile\n--- SomeTosser\n * Origin: Somewhere (2:301/1)\nSEEN-BY: 301/1 100\n\x01PATH: 301/1 1"

func routedHarness(t *testing.T) *harness {
	t.Helper()
	builtinEditor(t)
	path := filepath.Join(t.TempDir(), "R.QWK")
	control := qwk.ControlInfo{BBSName: "NullModem BBS", BBSID: "NULLMODE", CallerName: "ALICE",
		Conferences: []qwk.ConferenceInfo{{Number: 3, Name: "General"}}}
	msgs := []qwk.PackedMessage{{Header: qwk.MessageHeader{Number: 10, Conference: 3, To: "ALL", From: "Bob Tester",
		Subject: "ping", Written: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)}, Text: routedBody}}
	if err := qwk.BuildQWKPacket(path, control, msgs); err != nil {
		t.Fatal(err)
	}
	q, err := store.OpenQueue(filepath.Join(t.TempDir(), "pending"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(path, openPacket(t, path), Options{Queue: q, From: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, app: a, w: testWidth, h: testHeight, queue: q}
	h.draw()
	h.enter() // General
	h.enter() // the ping
	return h
}

func TestSplitRouting(t *testing.T) {
	body, routing := splitRouting(routedBody)
	if body != "Ping!\nZweite Zeile\nDritte Zeile\n--- SomeTosser\n * Origin: Somewhere (2:301/1)" {
		t.Fatalf("body = %q", body)
	}
	if strings.Join(routing, "|") != "SEEN-BY: 301/1 100|PATH: 301/1 1" {
		t.Fatalf("routing = %q", routing)
	}
	if b, r := splitRouting("just text\n"); b != "just text" || r != nil {
		t.Fatalf("no routing: %q %q", b, r)
	}
}

func TestSeenByIsHiddenUntilAsked(t *testing.T) {
	h := routedHarness(t)
	if strings.Contains(h.text(), "SEEN-BY: 301") {
		t.Fatalf("SEEN-BY shown before asking:\n%s", h.text())
	}
	h.assertContains("S:SEEN-BY", "the status bar says it is there")
	h.typ('S')
	h.assertContains("SEEN-BY: 301/1 100", "S shows it")
	h.assertContains("PATH: 301/1 1", "PATH too, without its ^A")
	h.typ('S')
	if strings.Contains(h.text(), "SEEN-BY: 301") {
		t.Fatal("S did not hide it again")
	}
}

func TestReplyQuotesSeenByOnlyWhenAsked(t *testing.T) {
	h := routedHarness(t)
	h.typ('r')
	h.enter() // into the editor
	if strings.Contains(h.text(), "SEEN-BY: 301") {
		t.Fatalf("the plain quote carries SEEN-BY:\n%s", h.text())
	}
	h.key(tcell.KeyCtrlR)
	h.assertContains("BT> SEEN-BY: 301/1 100", "Ctrl-R quotes the routing")
	h.assertContains("BT> PATH: 301/1 1", "both lines")
	h.typeText("Pong.")
	h.key(tcell.KeyCtrlS)

	queued, _ := h.queue.List()
	if len(queued) != 1 {
		t.Fatalf("queued = %+v", queued)
	}
	body := queued[0].Body
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "SEEN-BY") || strings.HasPrefix(l, "PATH") {
			t.Fatalf("a bare routing line went into the reply: %q\n%s", l, body)
		}
	}
	if !strings.Contains(body, "BT> SEEN-BY: 301/1 100") || !strings.Contains(body, "Pong.") {
		t.Fatalf("body = %q", body)
	}
}

func TestCtrlYDeletesQuoteLines(t *testing.T) {
	h := routedHarness(t)
	h.typ('r')
	h.enter()
	before := strings.Count(h.text(), "BT> ")
	if strings.Contains(h.text(), "SomeTosser") || strings.Contains(h.text(), "Origin") {
		t.Fatalf("tearline or origin quoted:\n%s", h.text())
	}
	// The cursor starts one blank line below the quote; up to its
	// second line, then delete two lines.
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyCtrlY)
	h.key(tcell.KeyCtrlY)
	after := strings.Count(h.text(), "BT> ")
	if after != before-2 {
		t.Fatalf("quote lines %d -> %d, want two fewer", before, after)
	}
}
