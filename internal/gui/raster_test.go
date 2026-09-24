package gui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gdamore/tcell/v2"

	"git.maik.ch/nullmodem/kit/ansi"
	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/assets"
	"git.maik.ch/nullmodem/reader/internal/app"
	"git.maik.ch/nullmodem/reader/internal/ui"
)

func TestEmbeddedFontHasEveryCodePoint(t *testing.T) {
	if len(assets.CP437Font) != glyphCount*cellH {
		t.Fatalf("font is %d bytes, want %d", len(assets.CP437Font), glyphCount*cellH)
	}
	// Space must be blank and the full block must be solid; if those
	// two are right the indexing is right.
	for row := 0; row < cellH; row++ {
		if got := glyphRow(assets.CP437Font, ' ', row); got != 0x00 {
			t.Fatalf("space row %d = %#x, want blank", row, got)
		}
		if got := glyphRow(assets.CP437Font, 0xDB, row); got != 0xFF {
			t.Fatalf("full block row %d = %#x, want solid -- blocks must tile without seams", row, got)
		}
	}
}

func TestRasterizePaintsForegroundAndBackground(t *testing.T) {
	g := ansi.NewGrid(1, 1)
	g.Cells[0] = ansi.Cell{Char: 0xDB, FG: 9, BG: 1} // solid block, bright red on red

	img, err := Rasterize(g, assets.CP437Font, 1)
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	if got := img.Bounds().Size(); got.X != cellW || got.Y != cellH {
		t.Fatalf("image is %v, want one %dx%d cell", got, cellW, cellH)
	}
	// Every pixel of a full block is foreground.
	if got := img.RGBAAt(0, 0); got != palette[9] {
		t.Fatalf("pixel = %v, want the bright red foreground %v", got, palette[9])
	}

	g.Cells[0] = ansi.Cell{Char: ' ', FG: 9, BG: 1}
	img, _ = Rasterize(g, assets.CP437Font, 1)
	if got := img.RGBAAt(0, 0); got != palette[1] {
		t.Fatalf("pixel = %v, want the red background %v", got, palette[1])
	}
}

func TestRasterizeScalesByWholePixels(t *testing.T) {
	g := ansi.NewGrid(2, 1)
	img, err := Rasterize(g, assets.CP437Font, 3)
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	if got := img.Bounds().Size(); got.X != 2*cellW*3 || got.Y != cellH*3 {
		t.Fatalf("image is %v, want the grid at 3x", got)
	}
}

func TestRasterizeRejectsAFontOfTheWrongSize(t *testing.T) {
	if _, err := Rasterize(ansi.NewGrid(1, 1), []byte{1, 2, 3}, 1); err == nil {
		t.Fatal("Rasterize should refuse a font that is not 256 glyphs of 16 rows")
	}
}

// Writes a picture of real ANSI art rendered through the GUI's own
// font, so the result can be looked at rather than only asserted on.
// NMR_RASTER_OUT names where it goes.
func TestRasterizeWelcomeArt(t *testing.T) {
	out := os.Getenv("NMR_RASTER_OUT")
	if out == "" {
		t.Skip("set NMR_RASTER_OUT to write a sample image")
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "SAMPLE.ANS"))
	if err != nil {
		t.Skipf("no sample art: %v", err)
	}

	img, err := Rasterize(ui.ScreenGrid(raw, 80), assets.CP437Font, 2)
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("creating %s: %v", out, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	t.Logf("wrote %s (%v)", out, img.Bounds().Size())
}

// Renders the reader's own interface through the GUI's font, which is
// the closest thing to a screenshot that can be produced without a
// window. NMR_RASTER_OUT names where it goes.
func TestRasterizeReaderScreen(t *testing.T) {
	out := os.Getenv("NMR_RASTER_OUT")
	if out == "" {
		t.Skip("set NMR_RASTER_OUT to write a sample image")
	}

	p, err := qwk.OpenPacket(filepath.Join("..", "..", "testdata", "SAMPLE.QWK"))
	if err != nil {
		t.Skipf("no sample packet: %v", err)
	}
	defer p.Close()

	a, err := app.New("SAMPLE.QWK", p, app.Options{From: "Alice Example"})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	// Into a conference and onto the first message.
	for _, ev := range []*tcell.EventKey{
		tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
	} {
		a.HandleKey(ev)
	}

	img, err := Rasterize(a.Render(defaultCols, defaultRows), assets.CP437Font, 2)
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("creating %s: %v", out, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	t.Logf("wrote %s (%v)", out, img.Bounds().Size())
}

// A glyph placed one cell off in the atlas would draw the wrong
// character everywhere, consistently enough that it might pass for
// an odd font.
func TestAtlasPlacesEveryGlyphWhereTheDrawPathLooksForIt(t *testing.T) {
	img, err := atlasImage(assets.CP437Font)
	if err != nil {
		t.Fatalf("atlasImage: %v", err)
	}
	if got := img.Bounds().Size(); got.X != atlasCols*cellW || got.Y != (glyphCount/atlasCols)*cellH {
		t.Fatalf("atlas is %v, want the whole code page", got)
	}

	for cp := 0; cp < glyphCount; cp++ {
		r := glyphRect(byte(cp))
		for row := 0; row < cellH; row++ {
			want := glyphRow(assets.CP437Font, byte(cp), row)
			var got byte
			for col := 0; col < cellW; col++ {
				if _, _, _, a := img.At(r.Min.X+col, r.Min.Y+row).RGBA(); a != 0 {
					got |= 1 << (7 - col)
				}
			}
			if got != want {
				t.Fatalf("glyph %#x row %d: atlas has %08b, font has %08b", cp, row, got, want)
			}
		}
	}
}
