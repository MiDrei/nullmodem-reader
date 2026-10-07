// Command mkicon draws the reader's application icon from its own
// CP437 font: "NM" in bright white on DOS blue, with a yellow cursor
// block -- a DOS prompt waiting for input.
//
// Drawn rather than hand-painted so it stays in the same pixels as the
// interface itself, and reproducible like the font:
//
//	go run ./tools/mkicon assets/icon/nmr.png
//
// The Windows build turns this 256x256 PNG into the .exe's icon (see
// scripts/release.sh), and the GUI sets it as its window icon.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"github.com/midrei/nullmodem-reader/assets"
)

const (
	size  = 256
	scale = 11 // one font pixel becomes 11x11
)

// DOS palette entries used.
var (
	blue   = color.NRGBA{0x00, 0x00, 0xAA, 0xFF}
	white  = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	yellow = color.NRGBA{0xFF, 0xFF, 0x55, 0xFF}
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkicon out.png")
		os.Exit(2)
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	// Blue square with rounded corners, transparent outside them.
	const radius = 36
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if insideRounded(x, y, radius) {
				img.Set(x, y, blue)
			}
		}
	}

	// "NM" and the cursor under it, centred on the glyphs' ink rather
	// than their 8x16 cells: the font leaves blank rows above and below
	// every letter, which would push the picture off centre.
	text := []byte("NM")
	top, bottom, left, right := inkBounds(text)
	inkW := (right - left + 1) * scale
	inkH := (bottom - top + 1) * scale
	gap, cursorH := 2*scale, 2*scale
	x0 := (size-inkW)/2 - left*scale
	y0 := (size-(inkH+gap+cursorH))/2 - top*scale
	for i, ch := range text {
		drawGlyph(img, ch, x0+i*8*scale, y0, white)
	}

	// The cursor: a bar under the text, as the VGA text cursor sat in
	// the bottom scanlines of its cell.
	cursorY := y0 + (bottom+1)*scale + gap
	for y := cursorY; y < cursorY+cursorH; y++ {
		for x := x0 + left*scale; x < x0+(right+1)*scale; x++ {
			img.Set(x, y, yellow)
		}
	}

	f, err := os.Create(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func drawGlyph(img *image.NRGBA, ch byte, x0, y0 int, c color.Color) {
	rows := assets.CP437Font[int(ch)*16 : int(ch)*16+16]
	for gy, bits := range rows {
		for gx := 0; gx < 8; gx++ {
			if bits&(0x80>>gx) == 0 {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set(x0+gx*scale+dx, y0+gy*scale+dy, c)
				}
			}
		}
	}
}

// inkBounds is the rows and columns, in font pixels across the whole
// string laid out cell after cell, that any glyph actually sets.
func inkBounds(text []byte) (top, bottom, left, right int) {
	top, left = 16, 8*len(text)
	for i, ch := range text {
		rows := assets.CP437Font[int(ch)*16 : int(ch)*16+16]
		for y, bits := range rows {
			for x := 0; x < 8; x++ {
				if bits&(0x80>>x) == 0 {
					continue
				}
				top, bottom = min(top, y), max(bottom, y)
				left, right = min(left, i*8+x), max(right, i*8+x)
			}
		}
	}
	return top, bottom, left, right
}

func insideRounded(x, y, r int) bool {
	cx, cy := x, y
	switch {
	case x < r:
		cx = r
	case x >= size-r:
		cx = size - r - 1
	}
	switch {
	case y < r:
		cy = r
	case y >= size-r:
		cy = size - r - 1
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}
