package compose

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

// EditorCommand returns the editor to run and its arguments, from
// $VISUAL, then $EDITOR, then a platform default.
//
// Handing the body to the user's own editor rather than building one
// into the reader is the deliberate choice here. A message is prose:
// people want the keybindings, undo, spell-checking and wrapping they
// already have, and a hand-rolled tcell text widget would be a poor
// imitation of all of it -- for exactly the part of the reader where
// mistakes are most annoying, since the result gets posted publicly.
// Classic offline readers did the same for the same reason.
//
// The variable is split on spaces so "code -w" or "emacsclient -c"
// work. That does mean an editor whose path contains a space must be
// quoted by wrapping it in a shell script instead; treating the whole
// value as one path would break the far more common case of an
// editor with flags.
func EditorCommand() (name string, args []string) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			fields := strings.Fields(v)
			return fields[0], fields[1:]
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad", nil
	}
	return "vi", nil
}

// Edit opens initial in the user's editor and returns what they
// saved.
//
// The editor inherits the real terminal, so a full-screen caller has
// to release it first -- see the TUI's own reply flow, which suspends
// tcell around this call.
//
// The temporary file keeps a .txt suffix so editors that pick a mode
// from the extension treat it as prose rather than guessing.
func Edit(initial string) (string, error) {
	dir, err := os.MkdirTemp("", "nmr-compose-*")
	if err != nil {
		return "", fmt.Errorf("compose: creating a working directory: %w", err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "message.txt")
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		return "", fmt.Errorf("compose: writing the draft: %w", err)
	}

	name, args := EditorCommand()
	cmd := exec.Command(name, append(args, path)...)
	// A terminal editor needs the terminal; a windowed one does not.
	// Only hand the standard streams on when they are one: a reader
	// started by double-click on Windows has no console any more
	// (Ebitengine frees it), and passing its dead handles on makes
	// Windows refuse to start the editor at all ("The request is not
	// supported"). Left nil, the editor gets the null device.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	started := time.Now()
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("compose: running %s: %w", name, err)
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("compose: reading the draft back: %w", err)
	}
	if runtime.GOOS == "windows" && string(edited) == initial && time.Since(started) < returnedAtOnce {
		return "", ErrReturnedAtOnce
	}
	return string(edited), nil
}

// returnedAtOnce is quicker than anyone writes a message.
const returnedAtOnce = 2 * time.Second

// ErrReturnedAtOnce means the editor came back before anyone could
// have written anything. Windows 11's Notepad does that when a Notepad
// window is already open: it opens the draft as a tab there and the
// process that was started exits straight away. Treating that as "the
// user wrote nothing" would throw the draft away under their hands.
var ErrReturnedAtOnce = errors.New("the editor closed right away -- if the draft opened in a Notepad window that was already open, close all Notepad windows and press Enter again")

// Template is the buffer the editor opens on: a hint line the user
// deletes, the quoted original if there is one, and the tearline.
//
// The instruction line is a comment the user is expected to remove,
// and StripTemplate removes it for them if they do not. Leaving it
// in the sent message would be worse than a blank draft.
const templateHint = "; Write your reply below. Lines starting with ';' are removed."

// Template seeds an editor buffer for a reply to quoted, or for a new
// message when quoted is empty.
func Template(quoted string) string {
	var b strings.Builder
	b.WriteString(templateHint)
	b.WriteString("\n\n")
	if quoted != "" {
		b.WriteString(quoted)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(Tearline)
	b.WriteString("\n")
	return b.String()
}

// EditTemplate is the buffer for editing a message that is already
// written: the same hint, then the text as it stands.
func EditTemplate(body string) string {
	return templateHint + "\n\n" + body + "\n"
}

// StripTemplate cleans an edited buffer: comment lines out, trailing
// blank lines off.
//
// Leading blank lines are kept. A message that opens with a blank
// line usually means the user wrote below the quote on purpose, and
// silently pulling their text up against the header would change how
// it reads.
func StripTemplate(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ";") {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t\r"))
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// IsEmpty reports whether a stripped draft holds nothing the user
// actually wrote.
//
// Quoted lines and the attribution above them do not count, nor does
// the tearline: all three were put there by the reader, not typed.
// Without that, pressing reply and then closing the editor without
// writing anything would queue the quote on its own -- which is the
// exact echo noise that gets people asked to stop, and it would be
// the reader's doing rather than theirs.
//
// Treating a line ending in "wrote:" as furniture is a heuristic, but
// a safe one: a message whose entire content is "Bob wrote:" has
// nothing in it either way.
func IsEmpty(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", isTearline(line):
			continue
		case isQuoted(line):
			continue
		case strings.HasSuffix(line, "wrote:"):
			continue
		}
		return false
	}
	return true
}
