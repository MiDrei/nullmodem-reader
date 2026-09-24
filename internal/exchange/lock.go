package exchange

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked reports that another exchange for the same system is
// already running.
var ErrLocked = errors.New("exchange: another exchange is already running")

// StaleAfter is how old a lock has to be before it is treated as left
// behind by a crashed process.
//
// Checking whether the recorded process is still alive would be more
// precise, but doing that portably is a thicket -- and a PID can be
// reused, so it is not even reliably more correct. An age well beyond
// any plausible exchange is simpler and fails in the safe direction:
// the worst case is waiting half an hour after a crash before the
// scheduler resumes, instead of two processes uploading the same
// replies twice.
const StaleAfter = 30 * time.Minute

// lockInfo is what a lock file records, purely so a human who finds
// one can tell what put it there.
type lockInfo struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Host    string    `json:"host,omitempty"`
}

// Lock is a held exchange lock.
type Lock struct{ path string }

// Acquire takes the exchange lock for a system, or returns ErrLocked.
//
// Two exchanges at once would upload the same queued replies twice
// and race each other's read-state updates on the server, so this is
// not advisory politeness -- it is what keeps an unattended scheduler
// safe to run while someone is also reading their mail.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("exchange: creating %s: %w", filepath.Dir(path), err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			host, _ := os.Hostname()
			enc, _ := json.Marshal(lockInfo{PID: os.Getpid(), Started: time.Now(), Host: host})
			f.Write(enc)
			f.Close()
			return &Lock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("exchange: taking the lock at %s: %w", path, err)
		}
		// Someone holds it -- unless they died holding it.
		if attempt == 0 && stale(path) {
			os.Remove(path)
			continue
		}
		return nil, fmt.Errorf("%w (%s)", ErrLocked, describe(path))
	}
	return nil, fmt.Errorf("%w (%s)", ErrLocked, describe(path))
}

// Release drops the lock. Releasing twice is not an error: the caller
// deferring it should not have to track whether it already ran.
func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	err := os.Remove(l.path)
	l.path = ""
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("exchange: releasing the lock: %w", err)
	}
	return nil
}

// stale reports whether a lock file is old enough to have been left
// behind. It reads the recorded start time rather than the file's
// mtime, since the two can disagree after a filesystem is restored
// from a backup or copied between machines.
func stale(path string) bool {
	info, err := read(path)
	if err != nil {
		// A lock file that cannot be read or parsed is not something
		// to wait behind indefinitely: fall back to its mtime.
		st, statErr := os.Stat(path)
		return statErr == nil && time.Since(st.ModTime()) > StaleAfter
	}
	return time.Since(info.Started) > StaleAfter
}

func read(path string) (lockInfo, error) {
	var info lockInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	if info.Started.IsZero() {
		return info, errors.New("no start time")
	}
	return info, nil
}

// describe says who holds a lock, for an error message someone has to
// act on.
func describe(path string) string {
	info, err := read(path)
	if err != nil {
		return "held by an unknown process"
	}
	where := ""
	if info.Host != "" {
		where = " on " + info.Host
	}
	return fmt.Sprintf("held by pid %d%s since %s", info.PID, where, info.Started.Local().Format("15:04:05"))
}
