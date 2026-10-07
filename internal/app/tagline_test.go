package app

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/midrei/nullmodem-reader/internal/store"
)

// taglineHarness is the netmail harness with two taglines on offer and
// a record of what got saved as the next default.
func taglineHarness(t *testing.T, choice string) (*harness, *[]string) {
	t.Helper()
	h := netmailHarness(t, false)
	var saved []string
	h.app.taglines = []string{"NO CARRIER", "Keep calm and Zmodem on."}
	h.app.taglineChoice = choice
	h.app.saveTaglineChoice = func(c string) { saved = append(saved, c) }
	return h, &saved
}

func TestTaglineChoiceGoesAboveTheTearlineAndIsRemembered(t *testing.T) {
	h, saved := taglineHarness(t, "")
	h.useFakeEditor("Mein Text")
	h.down()
	h.typ('e')
	h.assertContains("Tagline:", "the form offers a tagline")
	h.assertContains("(random)", "random is the default the first time")

	h.key(tcell.KeyTab) // to Subject
	h.typeText("Hallo")
	h.key(tcell.KeyTab) // to the tagline row
	h.key(tcell.KeyRight)
	h.assertContains("... NO CARRIER", "right arrow steps to the first tagline")
	h.enter()

	queued, _ := h.queue.List()
	if len(queued) != 1 {
		t.Fatalf("queued = %+v", queued)
	}
	body := queued[0].Body
	if !strings.Contains(body, "... NO CARRIER\n---") {
		t.Fatalf("body = %q, want the tagline right above the tearline", body)
	}
	if len(*saved) != 1 || (*saved)[0] != "NO CARRIER" {
		t.Fatalf("saved = %v, want the pick remembered", *saved)
	}
}

func TestNoTaglineLeavesTheBodyAlone(t *testing.T) {
	h, saved := taglineHarness(t, TaglineNone)
	h.useFakeEditor("Mein Text")
	h.down()
	h.typ('e')
	h.assertContains("(none)", "last time's choice is kept")
	h.key(tcell.KeyTab)
	h.typeText("Hallo")
	h.enter()

	queued, _ := h.queue.List()
	if strings.Contains(queued[0].Body, "... ") {
		t.Fatalf("body = %q, want no tagline", queued[0].Body)
	}
	if len(*saved) != 0 {
		t.Fatalf("saved = %v, nothing changed so nothing to save", *saved)
	}
}

func TestTaglineRowSwallowsLetters(t *testing.T) {
	h, _ := taglineHarness(t, TaglineNone)
	h.down()
	h.typ('e')
	h.key(tcell.KeyBacktab) // from To backwards onto the tagline row
	h.typ('Q')
	if h.app.Quit() {
		t.Fatal("Q on the tagline row quit the reader")
	}
	h.typ(' ')
	h.assertContains("(random)", "space steps on like the arrows")
}

func TestEditingAQueuedMessageOffersNoTagline(t *testing.T) {
	h, _ := taglineHarness(t, "")
	f := newEditForm(Conference{Number: 3, Name: "General"}, store.Reply{Conference: 3, To: "All", Subject: "x", Body: "y"})
	f.offerTaglines(h.app.taglines, "")
	if len(f.taglines) != 0 {
		t.Fatal("an edited message got a second tagline offered")
	}
}

func TestNetmailFormOrder(t *testing.T) {
	h, _ := taglineHarness(t, TaglineNone)
	h.typ('N')
	order := []string{"To:", "Address:", "empty for someone on this BBS", "Subject:", "Tagline:"}
	last := -1
	for _, want := range order {
		row := -1
		for y := 0; y < h.grid.Height; y++ {
			if strings.Contains(h.row(y), want) {
				row = y
				break
			}
		}
		if row <= last {
			t.Fatalf("%q on row %d, want it below the previous item (row %d):\n%s", want, row, last, h.text())
		}
		last = row
	}
}
