// Package clipboard reads text from the system clipboard, for pasting
// into the reader's window.
//
// Ebitengine has no clipboard access, and the usual Go packages for it
// need cgo on Linux and macOS -- which the reader's builds do without.
// So each system is asked the way that works there without cgo: the
// Win32 API on Windows, pbpaste on macOS, and whichever of wl-paste,
// xclip and xsel is installed elsewhere.
package clipboard

import "errors"

// ErrUnavailable means there is no way to read the clipboard here.
var ErrUnavailable = errors.New("clipboard: no way to read the clipboard on this system")

// Read returns the clipboard's text, "" when it holds none.
func Read() (string, error) { return read() }
