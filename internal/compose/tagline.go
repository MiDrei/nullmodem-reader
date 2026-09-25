package compose

import (
	"bufio"
	"os"
	"strings"
)

// DefaultTaglines ship with the reader, in the spirit of the tagline
// files Blue Wave and OLX users swapped. All plain ASCII, so they
// survive the trip to CP437 on any board.
var DefaultTaglines = []string{
	"Modems don't die, they just lose carrier.",
	"NO CARRIER",
	"CONNECT 33600/ARQ/V34/LAPM/V42BIS",
	"+++ATH0 -- oops, wrong window.",
	"ATDT the past. Busy signal. Redial.",
	"Real sysops don't sleep, they poll.",
	"My other computer is a BBS.",
	"Offline reading: because the phone bill said so.",
	"QWK: reading mail at 1993 prices.",
	"FidoNet: the internet before the internet.",
	"Echomail is just netmail with an audience.",
	"Keep calm and Zmodem on.",
	"Press any key to continue. No, not THAT key!",
	"I'd rather be drawing ANSI.",
	"ESC[2J -- and the screen was without form, and void.",
	"This message was composed offline. The typos are live.",
	"All work and no play makes a dull BBS.",
	"Offline ist das neue Online.",
	"Frueher war mehr Baud.",
	"Wer zuletzt pollt, liest am laengsten.",
}

// taglineMark opens a tagline, by the old readers' convention: three
// dots and a space, on its own line above the tearline.
const taglineMark = "... "

// maxTagline keeps a tagline on one 79-column line with its mark.
const maxTagline = 79 - len(taglineMark)

// WithTagline puts tagline into body just above the tearline, or at
// the end when the user removed the tearline. An empty tagline leaves
// body as it is.
func WithTagline(body, tagline string) string {
	tagline = strings.TrimSpace(tagline)
	if tagline == "" {
		return body
	}
	line := taglineMark + tagline
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if isTearline(strings.TrimSpace(lines[i])) {
			out := append([]string{}, lines[:i]...)
			out = append(out, line)
			return strings.Join(append(out, lines[i:]...), "\n")
		}
	}
	return strings.TrimRight(body, "\n") + "\n" + line
}

// LoadTaglines is the defaults followed by the user's own, one per
// line in path; blank lines and lines starting with '#' are skipped,
// and a tagline too long for one line is dropped rather than wrapped.
// A missing file is no error: most people never write one.
func LoadTaglines(path string) []string {
	out := append([]string{}, DefaultTaglines...)
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	seen := map[string]bool{}
	for _, t := range out {
		seen[t] = true
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		t := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf"))
		if t == "" || strings.HasPrefix(t, "#") || len([]rune(t)) > maxTagline || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
