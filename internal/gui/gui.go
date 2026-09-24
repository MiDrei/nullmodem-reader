package gui

import (
	"fmt"
	"image"
	"os"

	"github.com/hajimehoshi/ebiten/v2"

	"git.maik.ch/nullmodem/kit/ansi"
	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/app"
)

// Default window geometry: the classic 80x25 text screen at 2x, which
// lands close to the apparent size of a real VGA display on a modern
// panel.
const (
	defaultCols  = 80
	defaultRows  = 25
	defaultScale = 2
)

// window is the Ebitengine game driving the reader.
type window struct {
	a     *app.App
	atlas *atlas
	scale int

	// cols and rows are what the current window size works out to.
	cols, rows int
	// screen is the offscreen surface the grid is drawn into at 1:1
	// pixel scale, then blown up to the window. Drawing at 1:1 and
	// scaling once is what keeps the glyphs pixel-crisp: drawing
	// glyphs directly at 2x would resample each one.
	screen *ebiten.Image

	// caret blinks on a wall-clock division of the frame counter
	// rather than a timer, since Update runs at a fixed tick.
	ticks int
}

// Run opens the packet in a window and returns when the user quits.
func Run(path string, p *qwk.Packet, opts app.Options) error {
	a, err := app.New(path, p, opts)
	if err != nil {
		return err
	}
	at, err := loadAtlas()
	if err != nil {
		return err
	}

	// Suspend and Resume stay nil: there is no terminal to hand over.
	// A terminal editor launched from here would have nowhere to
	// draw, so the GUI expects a windowed one -- $VISUAL set to
	// something like "code -w", or on macOS "open -W -t".

	w := &window{a: a, atlas: at, scale: defaultScale}
	w.resizeTo(defaultCols*cellW*defaultScale, defaultRows*cellH*defaultScale)

	ebiten.SetWindowTitle("QWKReader")
	ebiten.SetWindowSize(defaultCols*cellW*defaultScale, defaultRows*cellH*defaultScale)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	// The reader is idle between keypresses; redrawing 60 times a
	// second to show nothing new would keep a laptop's fans on for a
	// text screen.
	ebiten.SetScreenClearedEveryFrame(false)

	runErr := ebiten.RunGame(w)
	if err := a.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "nmr: could not save read markers:", err)
	}
	// Quitting is how the reader ends, not a failure.
	if runErr != nil && runErr != ebiten.Termination {
		return fmt.Errorf("gui: %w", runErr)
	}
	return nil
}

// Layout tells Ebitengine the offscreen size for a given window size.
// Working at one logical pixel per screen pixel and scaling the whole
// surface at the end keeps the font crisp.
func (w *window) Layout(outsideWidth, outsideHeight int) (int, int) {
	w.resizeTo(outsideWidth, outsideHeight)
	return w.cols * cellW, w.rows * cellH
}

// resizeTo works out how many whole character cells fit, at the
// largest integer scale that still shows a usable number of columns.
//
// Integer scaling is not a nicety here: a fractional scale resamples
// an 8x16 bitmap glyph and the result is a blurred, unevenly-weighted
// mess -- exactly the thing a GUI over a terminal is supposed to fix.
func (w *window) resizeTo(pxW, pxH int) {
	scale := w.scale
	if scale < 1 {
		scale = 1
	}
	cols := pxW / (cellW * scale)
	rows := pxH / (cellH * scale)

	// Never go below a size the interface can lay out; the app draws
	// its own "window too small" notice below that.
	w.cols = max(cols, 20)
	w.rows = max(rows, 5)

	need := image.Pt(w.cols*cellW, w.rows*cellH)
	if w.screen == nil || w.screen.Bounds().Dx() != need.X || w.screen.Bounds().Dy() != need.Y {
		w.screen = ebiten.NewImage(need.X, need.Y)
	}
}

// Update handles input for one tick.
func (w *window) Update() error {
	w.ticks++
	for _, ev := range pollKeys() {
		w.a.HandleKey(ev)
	}
	if w.a.Quit() {
		return ebiten.Termination
	}
	return nil
}

// Draw paints the app's grid.
func (w *window) Draw(dst *ebiten.Image) {
	g := w.a.Render(w.cols, w.rows)
	w.paint(g)
	if x, y, on := w.a.Cursor(); on && w.ticks%60 < 40 {
		w.paintCaret(x, y)
	}

	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(w.scale), float64(w.scale))
	dst.DrawImage(w.screen, op)
}

// paint draws one grid into the offscreen surface: backgrounds first,
// then glyphs, so both passes batch.
func (w *window) paint(g ansi.Grid) {
	w.screen.Fill(palette[0])

	for y := 0; y < g.Height && y < w.rows; y++ {
		for x := 0; x < g.Width && x < w.cols; x++ {
			c := g.Cells[y*g.Width+x]
			if c.BG%8 == 0 {
				continue // already filled with black
			}
			bg := w.screen.SubImage(
				image.Rect(x*cellW, y*cellH, (x+1)*cellW, (y+1)*cellH),
			).(*ebiten.Image)
			bg.Fill(paletteColor(c.BG))
		}
	}

	for y := 0; y < g.Height && y < w.rows; y++ {
		for x := 0; x < g.Width && x < w.cols; x++ {
			c := g.Cells[y*g.Width+x]
			if c.Char == ' ' || c.Char == 0 {
				continue // nothing to draw, and by far the common case
			}
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Translate(float64(x*cellW), float64(y*cellH))
			fg := paletteColor(c.FG)
			op.ColorScale.ScaleWithColor(fg)
			w.screen.DrawImage(w.atlas.img.SubImage(w.atlas.rect(c.Char)).(*ebiten.Image), op)
		}
	}
}

// paintCaret draws the block cursor a DOS text screen would show.
func (w *window) paintCaret(x, y int) {
	if x < 0 || y < 0 || x >= w.cols || y >= w.rows {
		return
	}
	// The bottom two scanlines, which is where the VGA text cursor sat.
	caret := w.screen.SubImage(
		image.Rect(x*cellW, y*cellH+cellH-2, (x+1)*cellW, (y+1)*cellH),
	).(*ebiten.Image)
	caret.Fill(palette[7])
}
