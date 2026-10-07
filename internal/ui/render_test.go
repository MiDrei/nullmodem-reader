package ui

import (
	"strings"
	"testing"

	"github.com/midrei/nullmodem-kit/ansi"
)

func TestGridToTerminalMapsCP437BytesToUnicode(t *testing.T) {
	// 0xC9/0xCD/0xBB are the double-line box corners and horizontal
	// rule that virtually every BBS screen is framed with.
	g := ansi.ParseGrid("\xc9\xcd\xbb", 80)
	got := GridToTerminal(g, TerminalOptions{TrimTrailingBlanks: true})

	if !strings.Contains(got, "╔═╗") {
		t.Fatalf("render = %q, want the box-drawing glyphs ╔═╗", got)
	}
	if strings.ContainsRune(got, 0xC9) {
		t.Fatal("render still carries raw CP437 bytes, which a UTF-8 terminal shows as mojibake")
	}
}

func TestGridToTerminalEmitsBrightForegroundAsBoldForm(t *testing.T) {
	g := ansi.ParseGrid("\x1b[1;36mX", 80)
	got := GridToTerminal(g, TerminalOptions{TrimTrailingBlanks: true})

	if !strings.Contains(got, "\x1b[0;1;36m") {
		t.Fatalf("render = %q, want a bright-cyan SGR run in the bold form", got)
	}
}

func TestGridToTerminalOmitsBlackBackground(t *testing.T) {
	g := ansi.ParseGrid("\x1b[37;40mX", 80)
	got := GridToTerminal(g, TerminalOptions{TrimTrailingBlanks: true})

	if strings.Contains(got, ";40m") {
		t.Fatalf("render = %q, should not paint the default black background", got)
	}
}

func TestGridToTerminalEmitsNonBlackBackground(t *testing.T) {
	g := ansi.ParseGrid("\x1b[37;44mX", 80)
	got := GridToTerminal(g, TerminalOptions{TrimTrailingBlanks: true})

	if !strings.Contains(got, ";44m") {
		t.Fatalf("render = %q, want the blue background painted", got)
	}
}

// A space on a colored background is a drawn block -- trimming it
// away would eat the right-hand edge of most BBS art.
func TestGridToTerminalKeepsColoredSpacesWhenTrimming(t *testing.T) {
	g := ansi.ParseGrid("\x1b[44m   ", 80)
	got := GridToTerminal(g, TerminalOptions{TrimTrailingBlanks: true})

	if !strings.Contains(got, ";44m") || !strings.Contains(got, "   ") {
		t.Fatalf("render = %q, want the three blue blocks kept", got)
	}
}

func TestGridToTerminalTrimsPlainTrailingSpaces(t *testing.T) {
	g := ansi.ParseGrid("hi", 80)
	got := GridToTerminal(g, TerminalOptions{TrimTrailingBlanks: true})

	if strings.Contains(got, "hi ") {
		t.Fatalf("render = %q, want the row to stop after the text", got)
	}
}

func TestGridToTerminalResetsAtEveryRowEnd(t *testing.T) {
	g := ansi.ParseGrid("\x1b[44mA\r\n\x1b[41mB", 80)
	got := GridToTerminal(g, TerminalOptions{TrimTrailingBlanks: true})

	first, _, found := strings.Cut(got, "\n")
	if !found {
		t.Fatalf("render = %q, want two rows", got)
	}
	if !strings.HasSuffix(first, Reset) {
		t.Fatalf("first row = %q, want it reset before the newline so the color does not bleed", first)
	}
}

// Cursor-addressed art does not arrive in drawing order. Going
// through the Grid is what puts it back where it belongs.
func TestRenderScreenResolvesCursorAddressing(t *testing.T) {
	raw := []byte("\x1b[2J\x1b[3;5HX")
	got := RenderScreen(raw, 80)

	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("render = %q, want at least 3 rows", got)
	}
	if !strings.Contains(lines[2], "X") {
		t.Fatalf("row 3 = %q, want the X the cursor was moved to", lines[2])
	}
	if strings.Contains(lines[0], "X") {
		t.Fatal("X landed on row 1 -- the cursor move was not honored")
	}
}

func TestRenderMessageBodyDecodesCP437ProseLineByLine(t *testing.T) {
	// 0x81 is ü and 0xE1 is ß in CP437; the newline must stay a
	// newline rather than becoming CP437's ◙ glyph.
	got := RenderMessageBody("Gr\x81\xe1e\nzweite Zeile", 80)

	if !strings.Contains(got, "Grüße") {
		t.Fatalf("render = %q, want the CP437 umlauts decoded", got)
	}
	if !strings.Contains(got, "\nzweite Zeile") {
		t.Fatalf("render = %q, want the line break preserved", got)
	}
	if strings.ContainsRune(got, '◙') {
		t.Fatal("a line break was decoded as CP437's ◙ glyph")
	}
}

func TestRenderMessageBodyWrapsLongProse(t *testing.T) {
	long := strings.Repeat("word ", 30)
	got := RenderMessageBody(long, 40)

	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 40 {
			t.Fatalf("line %q exceeds the 40-column width", line)
		}
	}
	if !strings.Contains(got, "\n") {
		t.Fatal("long prose was not wrapped at all")
	}
}

func TestRenderMessageBodyRoutesANSIThroughTheGrid(t *testing.T) {
	got := RenderMessageBody("\x1b[1;31m\xdb\xdb\xdb", 80)

	if !strings.Contains(got, "███") {
		t.Fatalf("render = %q, want the CP437 block glyphs", got)
	}
	if !strings.Contains(got, ";31m") {
		t.Fatalf("render = %q, want the red SGR run preserved", got)
	}
}

func TestRenderMessageBodyLeavesASCIIArtUnwrapped(t *testing.T) {
	// Aligned-column ASCII art. ansi.HasAlignedSpacing needs three
	// lines that each carry an internal run of three or more spaces,
	// which is what tells hand-aligned art apart from prose that
	// merely happens to be double-spaced once.
	art := "+---+   +---+   +---+\n|  A|   |  B|   |  C|\n+---+   +---+   +---+"
	got := RenderMessageBody(art, 10)

	if got != art {
		t.Fatalf("render = %q, want the art untouched at %q", got, art)
	}
}

// Header fields carry CP437 just as bodies do. This is easy to miss
// because most of them are pure ASCII -- until someone posts from a
// German-language board.
func TestDecodeFieldDecodesCP437HeaderText(t *testing.T) {
	if got := DecodeField("Gr\x81\xe1e aus Z\x81rich"); got != "Grüße aus Zürich" {
		t.Fatalf("DecodeField = %q, want %q", got, "Grüße aus Zürich")
	}
}

func TestDecodeFieldLeavesASCIIAlone(t *testing.T) {
	if got := DecodeField("Re: Hello, World!"); got != "Re: Hello, World!" {
		t.Fatalf("DecodeField = %q, want it unchanged", got)
	}
}
