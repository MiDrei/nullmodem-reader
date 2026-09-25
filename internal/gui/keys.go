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

// repeatingKeys fire again while held, like a keyboard's own
// autorepeat: scrolling through a long message or deleting a word by
// holding the key. Enter, Escape and Tab deliberately do not -- a held
// Enter would open, send or confirm one thing after another.
var repeatingKeys = map[ebiten.Key]bool{
	ebiten.KeyUp:        true,
	ebiten.KeyDown:      true,
	ebiten.KeyLeft:      true,
	ebiten.KeyRight:     true,
	ebiten.KeyPageUp:    true,
	ebiten.KeyPageDown:  true,
	ebiten.KeyBackspace: true,
	ebiten.KeyDelete:    true,
}

// Autorepeat timing in ticks (Ebitengine runs Update 60 times a
// second): a first repeat after about a third of a second, then about
// 20 per second -- close to the Windows and macOS defaults.
const (
	repeatDelay    = 20
	repeatInterval = 3
)

// fires reports whether a key held for d ticks should produce an event
// on this tick. d is inpututil.KeyPressDuration: 1 on the tick the key
// went down, 0 when it is up.
func fires(d int, repeats bool) bool {
	switch {
	case d == 1:
		return true
	case !repeats || d <= repeatDelay:
		return false
	default:
		return (d-repeatDelay)%repeatInterval == 0
	}
}

// controlKeys are the Ctrl combinations the compose form's text
// fields use.
var controlKeys = map[ebiten.Key]tcell.Key{
	ebiten.KeyA: tcell.KeyCtrlA,
	ebiten.KeyE: tcell.KeyCtrlE,
	ebiten.KeyU: tcell.KeyCtrlU,
	ebiten.KeyD: tcell.KeyCtrlD,
	ebiten.KeyC: tcell.KeyCtrlC,
	ebiten.KeyS: tcell.KeyCtrlS,
	ebiten.KeyV: tcell.KeyCtrlV,
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
		if fires(inpututil.KeyPressDuration(k), repeatingKeys[k]) {
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
