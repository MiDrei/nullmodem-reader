// Package assets holds the binary resources the reader ships with.
package assets

import _ "embed"

// CP437Font is a 4096-byte VGA-style bitmap font: 256 glyphs of 16
// rows, one byte per row, bit 7 leftmost. Index a glyph's first row
// at codepoint*16.
//
// It was produced from Spleen's cp437-indexed 8x16 BDF by
// tools/mkfont; Spleen is BSD-2-Clause and its licence travels with
// the font in LICENSE.spleen. A CP437 bitmap font is what makes a GUI
// worth having over the terminal: the block and line-drawing glyphs
// tile without seams at every scale, which is exactly what a font
// rendered from outlines cannot promise.
//
//go:embed font/cp437-8x16.bin
var CP437Font []byte
