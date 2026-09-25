package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/store"
)

func TestInitials(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Bob Smith", "BS"},
		{"bob smith", "BS"},
		{"Bob Smith Jr", "BS"},
		{"SYSOP", "SY"},
		{"X", "X."},
		{"", "??"},
		{"   ", "??"},
		{"Jean-Luc Picard", "JL"}, // the hyphen splits like a space
	} {
		if got := Initials(tc.name); got != tc.want {
			t.Errorf("Initials(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestQuotePrefixesEveryLineWithTheSendersInitials(t *testing.T) {
	got := Quote("first line\nsecond line", "Bob Smith", 72)

	if !strings.HasPrefix(got, "Bob Smith wrote:") {
		t.Fatalf("quote = %q, want an attribution line first", got)
	}
	if !strings.Contains(got, "BS> first line") || !strings.Contains(got, "BS> second line") {
		t.Fatalf("quote = %q, want each line prefixed", got)
	}
}

func TestQuoteWrapsLongLinesToTheRemainingWidth(t *testing.T) {
	got := Quote(strings.Repeat("word ", 40), "Bob Smith", 40)

	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 40 {
			t.Fatalf("line %q exceeds the 40-column width", line)
		}
	}
}

// Re-wrapping an already-quoted line is what turns an old thread into
// a staircase.
func TestQuoteDoesNotRewrapAlreadyQuotedLines(t *testing.T) {
	original := "AB> something an earlier author already wrapped exactly here"
	got := Quote(original, "Bob Smith", 40)

	if !strings.Contains(got, "BS> "+original) {
		t.Fatalf("quote = %q, want the existing quote passed through whole", got)
	}
}

func TestQuoteKeepsBlankLinesAsBareQuoteMarkers(t *testing.T) {
	got := Quote("one\n\ntwo", "Bob Smith", 72)

	if !strings.Contains(got, "\nBS>\n") {
		t.Fatalf("quote = %q, want the blank line kept as a bare BS>", got)
	}
}

// Escape sequences inside a reply would repaint the reader's screen
// mid-message.
func TestQuoteDropsANSIArt(t *testing.T) {
	got := Quote("\x1b[1;31m\xdb\xdb\xdb", "SYSOP", 72)

	if strings.Contains(got, "\x1b") {
		t.Fatalf("quote = %q, must not carry escape sequences", got)
	}
	if !strings.Contains(got, "omitted") {
		t.Fatalf("quote = %q, want it to say the art was dropped", got)
	}
}

func TestReplySubject(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Hello", "Re: Hello"},
		{"Re: Hello", "Re: Hello"},
		{"RE: Hello", "RE: Hello"},
		{"re: Hello", "re: Hello"},
		{"", "Re:"},
		{"   ", "Re:"},
	} {
		if got := ReplySubject(tc.in); got != tc.want {
			t.Errorf("ReplySubject(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripTemplateRemovesCommentLinesAndTrailingBlanks(t *testing.T) {
	got := StripTemplate("; a hint\n\nreal text\n\n\n")

	if strings.Contains(got, "hint") {
		t.Fatalf("stripped = %q, want the comment line gone", got)
	}
	if !strings.HasSuffix(got, "real text") {
		t.Fatalf("stripped = %q, want trailing blank lines removed", got)
	}
}

// Text written below a quote should stay below it.
func TestStripTemplateKeepsLeadingBlankLines(t *testing.T) {
	got := StripTemplate("\n\nwritten below the quote")

	if !strings.HasPrefix(got, "\n\n") {
		t.Fatalf("stripped = %q, want the leading blank lines kept", got)
	}
}

func TestIsEmpty(t *testing.T) {
	if !IsEmpty("") {
		t.Error("an empty body should count as empty")
	}
	if !IsEmpty("\n  \n\t\n") {
		t.Error("whitespace only should count as empty")
	}
	if !IsEmpty(Tearline) {
		t.Error("the tearline alone should count as empty -- the user wrote nothing")
	}
	if IsEmpty("hi\n" + Tearline) {
		t.Error("a body with real text is not empty")
	}
}

func TestTemplateCarriesTheQuoteAndTearline(t *testing.T) {
	got := Template("BS> quoted text")

	if !strings.Contains(got, "BS> quoted text") {
		t.Fatalf("template = %q, want the quote", got)
	}
	if !strings.Contains(got, Tearline) {
		t.Fatalf("template = %q, want the tearline", got)
	}
	if StripTemplate(got) == got {
		t.Fatal("the template's hint line should be something StripTemplate removes")
	}
}

func TestEditorCommandPrefersVisualAndSplitsArguments(t *testing.T) {
	t.Setenv("VISUAL", "code -w")
	t.Setenv("EDITOR", "vim")

	name, args := EditorCommand()
	if name != "code" || len(args) != 1 || args[0] != "-w" {
		t.Fatalf("EditorCommand = %q %v, want VISUAL split into command and flags", name, args)
	}

	t.Setenv("VISUAL", "")
	if name, _ := EditorCommand(); name != "vim" {
		t.Fatalf("EditorCommand = %q, want the EDITOR fallback", name)
	}
}

// ------------------------------------------------------- REP building

func TestBuildREPProducesAPacketTheParserAccepts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "NULLMDM.REP")
	written := time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)

	unmapped, err := BuildREP(path, "NULLMDM", []store.Reply{
		{Conference: 3, To: "BOB", From: "ALICE", Subject: "Re: Grüße",
			Body: "Schöne Grüße zurück.", Written: written, RefNumber: 42},
		{Conference: 0, To: "SYSOP", From: "ALICE", Subject: "Privat",
			Body: "vertraulich", Written: written, Private: true},
	})
	if err != nil {
		t.Fatalf("BuildREP: %v", err)
	}
	if len(unmapped) != 0 {
		t.Fatalf("unmapped = %q, want none: German text is entirely in CP437", unmapped)
	}

	got, err := qwk.ParseReplyPacket(path, "NULLMDM")
	if err != nil {
		t.Fatalf("ParseReplyPacket: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Header.Number != 3 {
		t.Fatalf("Header.Number = %d, want the destination conference", got[0].Header.Number)
	}
	if got[0].Header.RefNumber != 42 {
		t.Fatalf("RefNumber = %d, want 42", got[0].Header.RefNumber)
	}
	if got[1].Header.Status != '*' {
		t.Fatalf("Status = %q, want '*' for the private reply", got[1].Header.Status)
	}
}

func TestBuildREPEncodesBodiesAsCP437(t *testing.T) {
	path := filepath.Join(t.TempDir(), "NULLMDM.REP")
	if _, err := BuildREP(path, "NULLMDM", []store.Reply{
		{Conference: 1, Body: "Grüße", Subject: "Test"},
	}); err != nil {
		t.Fatalf("BuildREP: %v", err)
	}

	got, err := qwk.ParseReplyPacket(path, "NULLMDM")
	if err != nil {
		t.Fatalf("ParseReplyPacket: %v", err)
	}
	// 0x81 is ü and 0xE1 is ß in CP437 -- the packet must hold the
	// single bytes, not the two-byte UTF-8 sequences.
	if got[0].Text != "Gr\x81\xe1e" {
		t.Fatalf("Text = %q, want CP437 bytes", got[0].Text)
	}
}

// The caller is the only one who can decide what to do about
// characters that will not survive the trip.
func TestBuildREPReportsCharactersItCouldNotCarry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "NULLMDM.REP")
	unmapped, err := BuildREP(path, "NULLMDM", []store.Reply{
		{Conference: 1, Body: "ein 中 im Text", Subject: "und ein 日 im Betreff"},
	})
	if err != nil {
		t.Fatalf("BuildREP: %v", err)
	}

	if len(unmapped) != 2 {
		t.Fatalf("unmapped = %q, want both the body's and the subject's unmappable runes", unmapped)
	}
}

func TestBuildREPTransliteratesTypographicPunctuation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "NULLMDM.REP")
	unmapped, err := BuildREP(path, "NULLMDM", []store.Reply{
		{Conference: 1, Body: "er sagte “ja” — endlich…"},
	})
	if err != nil {
		t.Fatalf("BuildREP: %v", err)
	}
	if len(unmapped) != 0 {
		t.Fatalf("unmapped = %q, want smart quotes and dashes transliterated rather than lost", unmapped)
	}

	got, _ := qwk.ParseReplyPacket(path, "NULLMDM")
	if got[0].Text != `er sagte "ja" -- endlich...` {
		t.Fatalf("Text = %q, want ASCII punctuation", got[0].Text)
	}
}

// Pressing reply and writing nothing must not queue the quote on its
// own: that is echo noise, and it would be the reader's doing rather
// than the user's.
func TestIsEmptyIgnoresQuoteAttributionAndTearline(t *testing.T) {
	quoteOnly := StripTemplate(Template(Quote("original text\nsecond line", "Bob Smith", 72)))

	if !IsEmpty(quoteOnly) {
		t.Fatalf("a draft holding only the quote should count as empty:\n%q", quoteOnly)
	}
	if IsEmpty(quoteOnly + "\nund hier meine Antwort") {
		t.Fatal("a draft with the user's own line is not empty")
	}
}

func TestTearlineCarriesTheVersionAndAnyTearlineCountsAsEmpty(t *testing.T) {
	defer SetVersion("")
	SetVersion("v0.5.3")
	if Tearline != "--- NullModem Reader/nmr v0.5.3" {
		t.Fatalf("Tearline = %q", Tearline)
	}
	if !strings.Contains(Template(""), "--- NullModem Reader/nmr v0.5.3") {
		t.Fatal("the draft does not carry the versioned tearline")
	}
	for _, old := range []string{"--- QWKReader/nmr", "--- NullModem Reader/nmr v0.5.2", "---"} {
		if !IsEmpty(old) {
			t.Errorf("IsEmpty(%q) = false, want an older tearline to count as furniture", old)
		}
	}
}

func TestWithTaglineGoesAboveTheTearline(t *testing.T) {
	got := WithTagline("Hallo\n\n--- NullModem Reader/nmr v1", "NO CARRIER")
	if got != "Hallo\n\n... NO CARRIER\n--- NullModem Reader/nmr v1" {
		t.Fatalf("got %q", got)
	}
	if got := WithTagline("Hallo\n", "NO CARRIER"); got != "Hallo\n... NO CARRIER" {
		t.Fatalf("without a tearline: %q", got)
	}
	if got := WithTagline("Hallo", ""); got != "Hallo" {
		t.Fatalf("no tagline: %q", got)
	}
}

func TestLoadTaglinesAddsTheUsersOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taglines.txt")
	os.WriteFile(path, []byte("# mine\nMein eigener Spruch\n\nNO CARRIER\n"+strings.Repeat("x", 90)+"\n"), 0o600)
	got := LoadTaglines(path)
	if len(got) != len(DefaultTaglines)+1 || got[len(got)-1] != "Mein eigener Spruch" {
		t.Fatalf("got %d taglines, last %q; want the defaults plus one (comment, blank, duplicate and overlong skipped)", len(got), got[len(got)-1])
	}
	if n := len(LoadTaglines(filepath.Join(t.TempDir(), "missing.txt"))); n != len(DefaultTaglines) {
		t.Fatalf("missing file: %d taglines", n)
	}
	for _, tl := range DefaultTaglines {
		if len(tl) > maxTagline {
			t.Errorf("default tagline too long: %q", tl)
		}
		for _, r := range tl {
			if r > 127 {
				t.Errorf("default tagline not ASCII: %q", tl)
				break
			}
		}
	}
}
