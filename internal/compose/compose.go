// Package compose turns what the user wrote into something sendable:
// quoting an original, seeding the editor, and building the .REP
// packet from the pending queue.
package compose

import (
	"strings"
	"unicode"

	"git.maik.ch/nullmodem/kit/ansi"
	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/store"
)

// Tearline identifies the software that wrote a message, by the
// FidoNet/QWK convention of three dashes and a space. Other readers
// on the same echo recognise it and keep it out of quoted text.
const Tearline = "--- QWKReader/nmr"

// Initials reduces a name to the two-letter prefix QWK quoting uses:
// the first letter of each of the first two words, or the first two
// letters when there is only one word.
//
// The convention matters for interoperability rather than looks: the
// other readers on an echo detect quoted lines by this shape, and
// use that to colour them differently and to keep them out of a
// re-quote.
func Initials(name string) string {
	fields := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	switch {
	case len(fields) == 0:
		return "??"
	case len(fields) == 1:
		r := []rune(fields[0])
		if len(r) == 1 {
			return strings.ToUpper(string(r[0])) + "."
		}
		return strings.ToUpper(string(r[0:2]))
	default:
		return strings.ToUpper(string([]rune(fields[0])[0:1]) + string([]rune(fields[1])[0:1]))
	}
}

// Quote prepares the original for inclusion in a reply: an
// attribution line, then every line prefixed with the sender's
// initials.
//
// Lines already carrying a quote prefix are passed through with the
// new prefix added but are not re-wrapped -- their author already
// chose where they break, and rewrapping a quote of a quote is what
// turns an old thread into a staircase. Fresh text is wrapped to
// what is left of width after the prefix.
//
// ANSI art is dropped rather than quoted: escape sequences inside a
// reply would repaint the reader's screen mid-message, and a quoted
// picture is meaningless anyway.
func Quote(body, from string, width int) string {
	if ansi.HasEscapeCodes(body) {
		return "[ANSI art omitted from the quote]\n"
	}

	prefix := Initials(from) + "> "
	budget := width - len(prefix)
	if budget < 20 {
		budget = 20
	}

	var out []string
	out = append(out, from+" wrote:", "")
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, " \r")
		switch {
		case line == "":
			out = append(out, strings.TrimRight(prefix, " "))
		case isQuoted(line):
			out = append(out, prefix+line)
		default:
			for _, w := range wrap(line, budget) {
				out = append(out, prefix+w)
			}
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// isQuoted reports whether a line already carries a QWK quote prefix
// -- up to a few letters, then '>'.
func isQuoted(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	for i, r := range trimmed {
		if r == '>' {
			return i > 0 && i <= 4
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' {
			return false
		}
	}
	return false
}

// wrap breaks one line at spaces to fit width, hard-breaking a word
// that cannot fit on a line of its own.
func wrap(line string, width int) []string {
	r := []rune(line)
	var out []string
	for len(r) > width {
		brk := -1
		for i := width; i > 0; i-- {
			if r[i] == ' ' {
				brk = i
				break
			}
		}
		if brk <= 0 {
			brk = width
		}
		out = append(out, strings.TrimRight(string(r[:brk]), " "))
		r = []rune(strings.TrimLeft(string(r[brk:]), " "))
	}
	return append(out, string(r))
}

// ReplySubject prefixes a subject with "Re:" unless it already
// carries one. Stacking "Re: Re: Re:" eats the 25-byte header field
// that the subject has to fit into in the first place.
func ReplySubject(subject string) string {
	s := strings.TrimSpace(subject)
	if s == "" {
		return "Re:"
	}
	if len(s) >= 3 && strings.EqualFold(s[:3], "re:") {
		return s
	}
	return "Re: " + s
}

// Draft is the editor buffer handed to the user, and what comes back
// from it.
type Draft struct {
	To      string
	Subject string
	Body    string
}

// BuildREP writes a .REP packet holding every given reply and
// reports which characters could not be carried into CP437.
//
// The unmapped runes are returned rather than logged because the
// caller is the only one who can decide what to do about them: a
// scheduled exchange should mention them and carry on, while someone
// sitting in front of the reader may want to go back and rewrite the
// sentence.
func BuildREP(path, bbsID string, replies []store.Reply) (unmapped []rune, err error) {
	seen := map[rune]bool{}
	out := make([]qwk.Reply, len(replies))
	for i, r := range replies {
		body, lost := ansi.EncodeCP437Report(r.Body)
		to, lostTo := ansi.EncodeCP437Report(r.To)
		from, lostFrom := ansi.EncodeCP437Report(r.From)
		subject, lostSubj := ansi.EncodeCP437Report(r.Subject)
		for _, group := range [][]rune{lost, lostTo, lostFrom, lostSubj} {
			for _, ru := range group {
				if !seen[ru] {
					seen[ru] = true
					unmapped = append(unmapped, ru)
				}
			}
		}

		out[i] = qwk.Reply{
			Conference: r.Conference,
			To:         string(to),
			From:       string(from),
			Subject:    string(subject),
			Written:    r.Written,
			Text:       string(body),
			RefNumber:  r.RefNumber,
			Private:    r.Private,
		}
	}

	if err := qwk.BuildReplyPacket(path, bbsID, out); err != nil {
		return unmapped, err
	}
	return unmapped, nil
}
