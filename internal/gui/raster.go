package gui

import (
	"fmt"
	"image"
	"image/color"

	"git.maik.ch/nullmodem/kit/ansi"
)

// glyphRow returns one scanline of a glyph: eight pixels as bits,
// bit 7 leftmost. Both the GPU atlas and the software rasteriser go
// through here, so there is one definition of the font's layout
// rather than two that could disagree.
func glyphRow(font []byte, cp byte, row int) byte {
	i := int(cp)*cellH + row
	if row < 0 || row >= cellH || i >= len(font) {
		return 0
	}
	return font[i]
}

// Rasterize draws a grid into an image with the CP437 font, at an
// integer pixel scale.
//
// It exists apart from the Ebitengine path for two reasons. It is
// what makes the GUI's output testable at all -- a window cannot be
// asserted on, but an image can -- and it gives the reader a way to
// save exactly what is on screen, which for ANSI art is a thing
// people actually want to do.
//
// scale below 1 is treated as 1: a zero-sized image helps nobody.
func Rasterize(g ansi.Grid, font []byte, scale int) (*image.RGBA, error) {
	if len(font) != glyphCount*cellH {
		return nil, fmt.Errorf("gui: font is %d bytes, want %d", len(font), glyphCount*cellH)
	}
	if scale < 1 {
		scale = 1
	}

	img := image.NewRGBA(image.Rect(0, 0, g.Width*cellW*scale, g.Height*cellH*scale))
	for y := 0; y < g.Height; y++ {
		for x := 0; x < g.Width; x++ {
			c := g.Cells[y*g.Width+x]
			fg, bg := paletteColor(c.FG), paletteColor(c.BG)
			for row := 0; row < cellH; row++ {
				bits := glyphRow(font, c.Char, row)
				for col := 0; col < cellW; col++ {
					px := bg
					if bits&(1<<(7-col)) != 0 {
						px = fg
					}
					setBlock(img, (x*cellW+col)*scale, (y*cellH+row)*scale, scale, px)
				}
			}
		}
	}
	return img, nil
}

// setBlock paints one logical pixel as a scale-by-scale square.
func setBlock(img *image.RGBA, x, y, scale int, c color.RGBA) {
	for dy := 0; dy < scale; dy++ {
		for dx := 0; dx < scale; dx++ {
			img.SetRGBA(x+dx, y+dy, c)
		}
	}
}
