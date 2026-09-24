// Package store keeps what the reader owns between sessions: the
// replies written offline and still waiting to go out.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reply is one message written but not yet sent.
//
// Body and the header fields are UTF-8 here, not CP437. The
// conversion happens once, when the .REP is built: keeping the queue
// in UTF-8 means a reply can be listed, edited and re-shown without
// round-tripping through a lossy encoding each time, and it keeps the
// JSON on disk valid UTF-8 as the format requires.
type Reply struct {
	// ID is the queue entry's own identity, also its filename stem.
	// It leads with a timestamp so a plain sort is chronological.
	ID string `json:"id"`
	// Conference is the destination conference number in the packet
	// this reply answers.
	Conference     int       `json:"conference"`
	ConferenceName string    `json:"conference_name,omitempty"`
	To             string    `json:"to"`
	From           string    `json:"from"`
	Subject        string    `json:"subject"`
	Body           string    `json:"body"`
	Written        time.Time `json:"written"`
	// RefNumber is the message being replied to, 0 for a new thread.
	RefNumber int  `json:"ref_number,omitempty"`
	Private   bool `json:"private,omitempty"`
}

// Queue is the on-disk set of pending replies for one system.
//
// One file per reply rather than a single list: a half-written queue
// file would lose every pending reply at once, and one file per entry
// makes adding and removing atomic without locking.
type Queue struct{ dir string }

// OpenQueue returns the reply queue rooted at dir, creating it if it
// does not exist.
func OpenQueue(dir string) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: creating %s: %w", dir, err)
	}
	return &Queue{dir: dir}, nil
}

// Dir is where the queue keeps its files.
func (q *Queue) Dir() string { return q.dir }

// Add stores a reply, filling in ID and Written when the caller left
// them empty, and returns the stored form.
func (q *Queue) Add(r Reply) (Reply, error) {
	if r.Written.IsZero() {
		r.Written = time.Now()
	}
	if r.ID == "" {
		r.ID = newID(r.Written)
	}
	if err := q.write(r); err != nil {
		return Reply{}, err
	}
	return r, nil
}

// Update replaces an existing entry. It refuses an unknown ID rather
// than silently creating one: an edit that lands on a reply the user
// already sent should be reported, not turned into a second message.
func (q *Queue) Update(r Reply) error {
	if r.ID == "" {
		return errors.New("store: updating a reply with no id")
	}
	if _, err := os.Stat(q.path(r.ID)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("store: no pending reply %s", r.ID)
		}
		return fmt.Errorf("store: checking %s: %w", r.ID, err)
	}
	return q.write(r)
}

// List returns every pending reply, oldest first.
//
// The order comes from Written rather than from the ID, which is only
// second-resolution: two replies written within the same second would
// otherwise come back ordered by the random suffix that keeps their
// filenames apart, which is no order at all. The ID breaks a tie only
// when the timestamps are identical too.
//
// An unreadable or malformed entry is skipped rather than failing the
// whole listing: one corrupt file must not make the other pending
// replies unsendable.
func (q *Queue) List() ([]Reply, error) {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: reading %s: %w", q.dir, err)
	}

	var out []Reply
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(q.dir, e.Name()))
		if err != nil {
			continue
		}
		var r Reply
		if err := json.Unmarshal(data, &r); err != nil || r.ID == "" {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Written.Equal(out[j].Written) {
			return out[i].Written.Before(out[j].Written)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Len reports how many replies are waiting.
func (q *Queue) Len() (int, error) {
	list, err := q.List()
	return len(list), err
}

// Remove deletes one pending reply. Removing something already gone
// is not an error: the caller's intent -- that it no longer be
// pending -- is satisfied either way.
func (q *Queue) Remove(id string) error {
	if err := os.Remove(q.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: removing %s: %w", id, err)
	}
	return nil
}

// RemoveAll deletes the given replies, continuing past failures so a
// single stuck file does not leave the rest queued for a second send.
// It returns the first error it hit, after trying them all.
func (q *Queue) RemoveAll(ids []string) error {
	var firstErr error
	for _, id := range ids {
		if err := q.Remove(id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (q *Queue) path(id string) string { return filepath.Join(q.dir, id+".json") }

// write saves a reply atomically: a crash mid-write must not leave a
// truncated entry that List would then skip, silently losing a
// message the user believes is queued.
func (q *Queue) write(r Reply) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encoding reply %s: %w", r.ID, err)
	}
	tmp, err := os.CreateTemp(q.dir, ".reply-*")
	if err != nil {
		return fmt.Errorf("store: creating a temporary file in %s: %w", q.dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("store: writing reply %s: %w", r.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: writing reply %s: %w", r.ID, err)
	}
	// 0600: a pending reply is the user's unsent private mail.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("store: securing reply %s: %w", r.ID, err)
	}
	if err := os.Rename(tmpName, q.path(r.ID)); err != nil {
		return fmt.Errorf("store: storing reply %s: %w", r.ID, err)
	}
	return nil
}

// newID builds a sortable, filesystem-safe identity from a timestamp
// plus a short random suffix, so two replies written in the same
// second cannot collide.
func newID(t time.Time) string {
	return t.UTC().Format("20060102-150405") + "-" + randomSuffix()
}
