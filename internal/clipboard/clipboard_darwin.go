package clipboard

import "os/exec"

func read() (string, error) {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return "", ErrUnavailable
	}
	return string(out), nil
}
