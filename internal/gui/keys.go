package gui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// specialKeys maps the keys that are not text onto tcell's vocabulary.
//
// tcell's key constants are the shared language between the app and
// its frontends. Inventing a third enum here would mean maintaining
// two translation tables instead of one, for no gain: the app already
// has to name keys somehow, and tcell's names are as good as any.
var specialKeys = map[ebiten.Key]tcell.Key{
	ebiten.KeyUp:          tcell.KeyUp,
	ebiten.KeyDown:        tcell.KeyDown,
	ebiten.KeyLeft:        tcell.KeyLeft,
	ebiten.KeyRight:       tcell.KeyRight,
	ebiten.KeyEnter:       tcell.KeyEnter,
	ebiten.KeyNumpadEnter: tcell.KeyEnter,
	ebiten.KeyEscape:      tcell.KeyEscape,
	ebiten.KeyTab:         tcell.KeyTab,
	ebiten.KeyBackspace:   tcell.KeyBackspace2,
	ebiten.KeyDelete:      tcell.KeyDelete,
	ebiten.KeyHome:        tcell.KeyHome,
	ebiten.KeyEnd:         tcell.KeyEnd,
	ebiten.KeyPageUp:      tcell.KeyPgUp,
	ebiten.KeyPageDown:    tcell.KeyPgDn,
}

// controlKeys are the Ctrl combinations the compose form's text
// fields use.
var controlKeys = map[ebiten.Key]tcell.Key{
	ebiten.KeyA: tcell.KeyCtrlA,
	ebiten.KeyE: tcell.KeyCtrlE,
	ebiten.KeyU: tcell.KeyCtrlU,
	ebiten.KeyD: tcell.KeyCtrlD,
	ebiten.KeyC: tcell.KeyCtrlC,
}

// pollKeys turns this tick's input into key events.
//
// Text and control keys come from two different places on purpose.
// AppendInputChars is the platform's own text input: it already
// applies the keyboard layout, dead keys and modifiers, so a German
// keyboard produces "ü" without this code knowing anything about
// layouts. Arrow keys and Ctrl combinations produce no characters at
// all, so those are read as raw key presses.
func pollKeys() []*tcell.EventKey {
	var out []*tcell.EventKey

	shift := ebiten.IsKeyPressed(ebiten.KeyShift)
	ctrl := ebiten.IsKeyPressed(ebiten.KeyControl) || ebiten.IsKeyPressed(ebiten.KeyMeta)

	if ctrl {
		for k, tk := range controlKeys {
			if inpututil.IsKeyJustPressed(k) {
				out = append(out, tcell.NewEventKey(tk, 0, tcell.ModCtrl))
			}
		}
		// With Ctrl held, the platform still reports characters for
		// some combinations; swallowing them here keeps Ctrl-U from
		// also typing a "u" into the field it just cleared.
		return out
	}

	for k, tk := range specialKeys {
		if inpututil.IsKeyJustPressed(k) {
			mod := tcell.ModNone
			if shift && tk == tcell.KeyTab {
				// Shift-Tab is its own key in tcell, not a modifier.
				out = append(out, tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone))
				continue
			}
			out = append(out, tcell.NewEventKey(tk, 0, mod))
		}
	}

	for _, r := range ebiten.AppendInputChars(nil) {
		// Control characters arrive through specialKeys above; letting
		// them through here too would deliver Enter twice.
		if r < ' ' {
			continue
		}
		out = append(out, tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	return out
}
