package app

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// builtinEditor makes sure no VISUAL/EDITOR from the environment
// sends a test to an external editor.
func builtinEditor(t *testing.T) {
	t.Helper()
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
}

func TestBuiltinEditorWritesAndQueuesWithTearline(t *testing.T) {
	builtinEditor(t)
	h, _ := taglineHarness(t, TaglineNone)
	h.down()
	h.typ('e')
	h.key(tcell.KeyTab)
	h.typeText("Hallo")
	h.enter()
	h.assertContains("Writing to All in General", "Enter opens the built-in editor")

	h.typeText("Erste Zeile")
	h.enter()
	h.typeText("Zweite Zeile")
	h.key(tcell.KeyCtrlS)

	queued, _ := h.queue.List()
	if len(queued) != 1 {
		t.Fatalf("queued = %+v", queued)
	}
	body := queued[0].Body
	if !strings.HasPrefix(body, "Erste Zeile\nZweite Zeile\n\n--- NullModem Reader/nmr") {
		t.Fatalf("body = %q, want the text and the tearline", body)
	}
	h.assertContains("Queued for General", "saving queues it")
}

func TestBuiltinEditorReplyStartsBelowTheQuote(t *testing.T) {
	builtinEditor(t)
	h := newHarness(t)
	h.down()
	h.enter()
	h.enter()
	h.typ('r')
	h.enter()
	h.assertContains("BO> Zeile 50", "the quote is in the editor, scrolled to its end where the cursor is")
	h.typeText("Danke!")
	h.key(tcell.KeyCtrlS)

	queued, _ := h.queue.List()
	if len(queued) != 1 || !strings.Contains(queued[0].Body, "BO> Zeile 01") || !strings.Contains(queued[0].Body, "\n\nDanke!") {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestBuiltinEditorEscapeAsksBeforeDiscarding(t *testing.T) {
	builtinEditor(t)
	h, _ := taglineHarness(t, TaglineNone)
	h.down()
	h.typ('e')
	h.enter()
	h.typeText("Entwurf")
	h.esc()
	h.assertContains("Esc again: discard", "the first Escape asks")
	h.typ('x') // keeps writing -- and the x is typed
	h.esc()
	h.esc()
	if n, _ := h.queue.Len(); n != 0 {
		t.Fatal("a discarded draft was queued")
	}
	h.assertContains("Message discarded.", "and says so")
}

func TestBuiltinEditorEmptyMessageIsNotQueued(t *testing.T) {
	builtinEditor(t)
	h, _ := taglineHarness(t, TaglineNone)
	h.down()
	h.typ('e')
	h.enter()
	h.key(tcell.KeyCtrlS)
	if n, _ := h.queue.Len(); n != 0 {
		t.Fatal("an empty message was queued")
	}
}

func TestEditorCursorMovesOverWrappedRows(t *testing.T) {
	v := newEditorView(&composeForm{}, "All", "")
	v.width = 10
	v.insertText("aaaa bbbb cccc dddd")
	if row, x := v.visualRow(); row != 1 || x != 9 {
		t.Fatalf("after typing: row %d x %d, want the second screen row", row, x)
	}
	v.wantX = 2
	v.moveVertical(-1)
	if v.row != 0 || v.col != 2 {
		t.Fatalf("up: line %d col %d, want col 2 on the first screen row", v.row, v.col)
	}
	v.moveVertical(1)
	if v.col != 12 {
		t.Fatalf("down: col %d, want 12 (x 2 on the second row)", v.col)
	}
}

func TestPasteIntoEditorAndField(t *testing.T) {
	builtinEditor(t)
	h, _ := taglineHarness(t, TaglineNone)
	h.down()
	h.typ('e')
	h.app.HandlePaste("Hallo\r\nWelt") // into the To field: first line only
	h.draw()
	h.assertContains("AllHallo", "a field takes the first line at the cursor")

	h.key(tcell.KeyTab)
	h.enter()
	h.app.HandlePaste("Zeile 1\r\nZeile 2\tmit Tab")
	h.key(tcell.KeyCtrlS)
	queued, _ := h.queue.List()
	if len(queued) != 1 || !strings.HasPrefix(queued[0].Body, "Zeile 1\nZeile 2    mit Tab") {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestPasteIntoSetupReplacesTheSelectedValue(t *testing.T) {
	f := &fakeSession{t: t, packetPath: buildPacket(t)}
	opts := f.options()
	opts.SetupDefaults = SetupInput{URL: "https://old.example", Username: "alice"}
	h := &harness{t: t, app: NewHome(opts, HomeSetup, ""), w: testWidth, h: testHeight}
	h.draw()
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp) // the address, selected
	h.app.HandlePaste("  https://bbs.maik.ch\n")
	h.draw()
	h.assertContains("https://bbs.maik.ch", "the pasted address replaced the old one")
	if strings.Contains(h.text(), "old.example") {
		t.Fatal("paste appended instead of replacing the selection")
	}
}
