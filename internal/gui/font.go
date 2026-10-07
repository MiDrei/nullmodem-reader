// Package gui runs the reader in its own window.
//
// It is the second frontend over internal/app and, like the terminal
// one, it holds no part of the interface itself: the app composes
// everything into an ansi.Grid and this draws that grid with a CP437
// bitmap font. What it adds over the terminal is fidelity -- real VGA
// glyphs at a real cell size, so block and line-drawing characters
// tile without seams and art looks the way it did on the machine it
// was drawn on, whatever font the user's terminal happens to use.
package gui

import (
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/midrei/nullmodem-kit/ansi"
	"github.com/midrei/nullmodem-reader/assets"
)

// Cell dimensions of the embedded font.
const (
	cellW = 8
	cellH = 16
)

const glyphCount = 256

// atlas holds every glyph in one image, laid out 16 by 16.
//
// One image rather than 256 keeps the whole screen in a single
// batched draw call: Ebitengine batches consecutive draws that share
// a source image, so a 80x25 screen costs two passes (backgrounds,
// then glyphs) instead of four thousand.
//
// Glyph pixels are white and everything else transparent, so a cell's
// foreground colour is applied at draw time with a colour scale
// rather than baked into 16 copies of the atlas.
type atlas struct {
	img *ebiten.Image
}

// atlasCols is how many glyphs sit in one row of the atlas. Sixteen
// gives the familiar square code-page layout, which makes the image
// readable if anyone ever dumps it.
const atlasCols = 16

// atlasImage builds the atlas pixels. It is kept apart from newAtlas
// so the layout can be tested without a graphics context: a glyph
// placed one cell off would draw the wrong character everywhere, and
// that is not something a window makes obvious.
func atlasImage(font []byte) (*image.RGBA, error) {
	if len(font) != glyphCount*cellH {
		return nil, fmt.Errorf("gui: font is %d bytes, want %d", len(font), glyphCount*cellH)
	}

	rows := glyphCount / atlasCols
	src := image.NewRGBA(image.Rect(0, 0, atlasCols*cellW, rows*cellH))
	for cp := 0; cp < glyphCount; cp++ {
		r := glyphRect(byte(cp))
		for row := 0; row < cellH; row++ {
			bits := glyphRow(font, byte(cp), row)
			for col := 0; col < cellW; col++ {
				if bits&(1<<(7-col)) != 0 {
					src.SetRGBA(r.Min.X+col, r.Min.Y+row, color.RGBA{R: 255, G: 255, B: 255, A: 255})
				}
			}
		}
	}
	return src, nil
}

func newAtlas(font []byte) (*atlas, error) {
	src, err := atlasImage(font)
	if err != nil {
		return nil, err
	}
	return &atlas{img: ebiten.NewImageFromImage(src)}, nil
}

// glyphRect is where one glyph sits in the atlas. Both the builder
// and the draw path call it, so they cannot disagree about the
// layout.
func glyphRect(cp byte) image.Rectangle {
	x, y := (int(cp)%atlasCols)*cellW, (int(cp)/atlasCols)*cellH
	return image.Rect(x, y, x+cellW, y+cellH)
}

// rect is the source rectangle of one glyph in the atlas.
func (a *atlas) rect(cp byte) image.Rectangle { return glyphRect(cp) }

// palette is ansi.DOSPalette resolved to colours once at startup.
//
// A GUI has no terminal theme to inherit, so every colour comes from
// the VGA palette -- which is the point: art was composed against
// these exact values, and the spacing between the DOS greys is what
// makes ░▒▓ read as a gradient.
var palette = func() [16]color.RGBA {
	var out [16]color.RGBA
	for i, hex := range ansi.DOSPalette {
		v, err := strconv.ParseInt(strings.TrimPrefix(hex, "#"), 16, 64)
		if err != nil {
			out[i] = color.RGBA{R: 170, G: 170, B: 170, A: 255}
			continue
		}
		out[i] = color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
	}
	return out
}()

func paletteColor(i int) color.RGBA {
	if i < 0 || i > 15 {
		return palette[7]
	}
	return palette[i]
}

// loadAtlas builds the atlas from the embedded font.
func loadAtlas() (*atlas, error) { return newAtlas(assets.CP437Font) }
