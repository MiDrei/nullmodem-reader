//go:build !windows && !darwin

package clipboard

import (
	"os"
	"os/exec"
)

// readers are tried in order; Wayland's first when a Wayland session
// is running, since xclip there only sees XWayland's clipboard.
var readers = [][]string{
	{"wl-paste", "--no-newline"},
	{"xclip", "-selection", "clipboard", "-out"},
	{"xsel", "--clipboard", "--output"},
}

func read() (string, error) {
	for _, r := range readers {
		if r[0] == "wl-paste" && os.Getenv("WAYLAND_DISPLAY") == "" {
			continue
		}
		if _, err := exec.LookPath(r[0]); err != nil {
			continue
		}
		out, err := exec.Command(r[0], r[1:]...).Output()
		if err != nil {
			// wl-paste exits non-zero on an empty clipboard.
			return "", nil
		}
		return string(out), nil
	}
	return "", ErrUnavailable
}
