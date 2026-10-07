// Package tui runs the reader in a terminal.
//
// It is one of two frontends over internal/app, and it is
// deliberately thin: the app composes the entire interface into an
// ansi.Grid, so all this does is paint that grid into tcell cells and
// feed keypresses back. Nothing about the interface itself lives
// here, which is what keeps the terminal and the GUI from drifting
// apart.
package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/midrei/nullmodem-kit/ansi"
	"github.com/midrei/nullmodem-reader/internal/app"
	"github.com/midrei/nullmodem-reader/internal/clipboard"
)

// dosColors is ansi.DOSPalette resolved to tcell colors once at
// startup.
//
// The exact RGB values are used rather than tcell.PaletteColor's
// indices into the terminal's own 16 colors. Those follow whatever
// theme the user has set -- Solarized, Gruvbox, a light scheme -- and
// BBS art is composed against the VGA palette specifically: shading
// built from ░▒▓ relies on the real spacing between DOS grey levels,
// and a re-themed "bright yellow" that is actually orange changes
// what the picture is.
var dosColors = func() [16]tcell.Color {
	var out [16]tcell.Color
	for i, hex := range ansi.DOSPalette {
		v, err := strconv.ParseInt(strings.TrimPrefix(hex, "#"), 16, 64)
		if err != nil {
			out[i] = tcell.ColorWhite
			continue
		}
		out[i] = tcell.NewRGBColor(int32(v>>16&0xFF), int32(v>>8&0xFF), int32(v&0xFF))
	}
	return out
}()

// Run shows the reader full-screen in the terminal and returns when
// the user quits.
func Run(a *app.App) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("tui: opening the terminal: %w", err)
	}
	if err := screen.Init(); err != nil {
		return fmt.Errorf("tui: initialising the terminal: %w", err)
	}
	// Restoring the terminal must survive a panic in drawing code:
	// leaving the user in a raw-mode terminal with no echo is a far
	// worse failure than the crash itself.
	defer screen.Fini()

	// The app hands the terminal to an external editor through these.
	a.Suspend = screen.Suspend
	a.Resume = func() error {
		if err := screen.Resume(); err != nil {
			return err
		}
		screen.Sync()
		return nil
	}

	// PollEvent blocks until input arrives; an interrupt event makes
	// the loop render the outcome of background work right away.
	a.Wake = func() { _ = screen.PostEvent(tcell.NewEventInterrupt(nil)) }

	defer func() {
		if err := a.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "nmr: could not save read markers:", err)
		}
	}()

	// With bracketed paste, a terminal's paste arrives marked as such
	// instead of as typing -- a pasted line break would otherwise act
	// as Enter and submit a form half-filled.
	screen.EnablePaste()
	var pasting bool
	var pasted strings.Builder

	for !a.Quit() {
		w, h := screen.Size()
		present(screen, a.Render(w, h))
		if x, y, on := a.Cursor(); on {
			screen.ShowCursor(x, y)
		} else {
			screen.HideCursor()
		}
		screen.Show()

		switch ev := screen.PollEvent().(type) {
		case *tcell.EventResize:
			screen.Sync()
		case *tcell.EventPaste:
			if ev.Start() {
				pasting = true
				pasted.Reset()
			} else if pasting {
				pasting = false
				a.HandlePaste(pasted.String())
			}
		case *tcell.EventKey:
			switch {
			case pasting && ev.Key() == tcell.KeyRune:
				pasted.WriteRune(ev.Rune())
			case pasting && (ev.Key() == tcell.KeyEnter || ev.Key() == tcell.KeyCtrlJ):
				pasted.WriteByte('\n')
			case pasting && ev.Key() == tcell.KeyTab:
				pasted.WriteByte('\t')
			case pasting:
			case ev.Key() == tcell.KeyCtrlV:
				// Ctrl-V itself, for terminals whose own paste key is
				// something else: read the system clipboard directly.
				if text, err := clipboard.Read(); err == nil {
					a.HandlePaste(text)
				} else {
					a.Flash("Cannot read the clipboard -- use your terminal's own paste.")
				}
			default:
				a.HandleKey(ev)
			}
		}
	}
	return nil
}

// present paints a grid into the terminal.
func present(s tcell.Screen, g ansi.Grid) {
	s.Clear()
	for y := 0; y < g.Height; y++ {
		for x := 0; x < g.Width; x++ {
			c := g.Cells[y*g.Width+x]
			s.SetContent(x, y, ansi.Rune(c.Char), nil, style(c.FG, c.BG))
		}
	}
}

// style is the tcell style for one cell's colour pair.
//
// A black background is left at the terminal's default rather than
// painted, so a terminal whose own background is not pure black shows
// through instead of getting a black rectangle stamped over it -- the
// same choice ansi.ToHTML makes for the web preview. The default
// foreground gets the same treatment, which is what lets ordinary
// body text still follow the user's colour scheme while the art and
// the chrome keep their exact DOS colours.
func style(fg, bg int) tcell.Style {
	st := tcell.StyleDefault
	if fg != 7 || bg != 0 {
		st = st.Foreground(dosColors[clampColor(fg)])
	}
	if bg%8 != 0 {
		st = st.Background(dosColors[clampColor(bg)])
	}
	return st
}

// clampColor keeps an out-of-range index from panicking the whole
// frontend. ansi.Cell's background can briefly hold 8-15 after a
// reverse-video run with a bright foreground, and a malformed screen
// could in principle produce worse; a wrong colour is a far better
// outcome than a crash mid-packet.
func clampColor(c int) int {
	if c < 0 || c > 15 {
		return 7
	}
	return c
}
