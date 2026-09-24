// Command mkfont converts a CP437-indexed BDF bitmap font into the
// flat 4096-byte form the GUI embeds: 256 glyphs, 16 rows each, one
// byte per row, bit 7 leftmost.
//
// That layout is the classic VGA font ROM format. A glyph lookup is
// then a slice index rather than a parse, which matters when every
// frame blits a couple of thousand cells.
//
// The font shipped in assets/ was produced from Spleen's
// cp437/spleen-8x16-ibm-437.bdf (BSD-2-Clause, see LICENSE.spleen).
// This tool exists so that provenance is reproducible rather than a
// blob someone has to take on faith:
//
//	go run ./tools/mkfont spleen-8x16-ibm-437.bdf assets/font/cp437-8x16.bin
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	glyphs    = 256
	glyphRows = 16
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkfont <input.bdf> <output.bin>")
		os.Exit(1)
	}
	data, err := convert(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkfont:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "mkfont:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d bytes\n", os.Args[2], len(data))
}

func convert(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make([]byte, glyphs*glyphRows)
	seen := make([]bool, glyphs)

	sc := bufio.NewScanner(f)
	encoding, row := -1, 0
	inBitmap := false

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "ENCODING "):
			encoding, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "ENCODING")))
		case line == "BITMAP":
			inBitmap, row = true, 0
		case line == "ENDCHAR":
			if inBitmap && encoding >= 0 && encoding < glyphs {
				seen[encoding] = true
			}
			inBitmap, encoding = false, -1
		case inBitmap:
			if encoding < 0 || encoding >= glyphs || row >= glyphRows {
				continue
			}
			// One byte per row for an 8-pixel-wide font; a wider font
			// would need masking, which this deliberately does not do
			// rather than silently producing half a glyph.
			if len(line) != 2 {
				return nil, fmt.Errorf("glyph %d row %d: expected 2 hex digits, got %q", encoding, row, line)
			}
			v, err := strconv.ParseUint(line, 16, 8)
			if err != nil {
				return nil, fmt.Errorf("glyph %d row %d: %w", encoding, row, err)
			}
			out[encoding*glyphRows+row] = byte(v)
			row++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	var missing int
	for _, ok := range seen {
		if !ok {
			missing++
		}
	}
	if missing > 0 {
		return nil, fmt.Errorf("%d of %d code points have no glyph -- this is not a complete CP437 font", missing, glyphs)
	}
	return out, nil
}
