package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ConferenceState records what has been read in one conference.
//
// The shape is a high-water mark plus a set of stragglers above it,
// rather than a plain set of every message number ever read. A busy
// echo carries tens of thousands of messages over a few years, and
// the plain set would grow without bound in a file rewritten on every
// keypress. Reading normally runs oldest-first, so the mark absorbs
// almost everything and Read stays short.
type ConferenceState struct {
	// LastRead is the highest message number below which everything
	// counts as read.
	LastRead int `json:"last_read"`
	// Read holds individually-read messages above LastRead, sorted.
	// It shrinks whenever the numbers just above the mark turn out to
	// be contiguous (see fold).
	Read []int `json:"read,omitempty"`
}

// ReadState is what one system's messages have been read, across all
// the packets downloaded from it.
//
// Local state is needed even though the BBS keeps its own: the server
// marks a message read when it builds the packet, which says nothing
// about whether anyone has actually looked at it. Without this,
// reopening yesterday's packet would present it all as new.
type ReadState struct {
	path        string
	Conferences map[int]*ConferenceState `json:"conferences"`
	dirty       bool
}

// LoadReadState reads the state file at path. A missing file yields
// empty state rather than an error -- the first run is the normal
// case, not a failure.
//
// A corrupt file also yields empty state: read markers are a
// convenience, and losing them means re-reading some messages, which
// is far better than refusing to open the packet at all.
func LoadReadState(path string) (*ReadState, error) {
	s := &ReadState{path: path, Conferences: map[int]*ConferenceState{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("store: reading %s: %w", path, err)
	}
	var onDisk struct {
		Conferences map[int]*ConferenceState `json:"conferences"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return s, nil
	}
	if onDisk.Conferences != nil {
		s.Conferences = onDisk.Conferences
	}
	return s, nil
}

// IsRead reports whether message num in conference conf has been read.
//
// A number of zero or less is never read. QWK numbers start at 1, so
// one of those means the header field was blank or unparseable --
// and with an empty high-water mark the plain "num <= LastRead" test
// would otherwise call every such message read, quietly hiding
// exactly the messages something already went wrong with.
func (s *ReadState) IsRead(conf, num int) bool {
	if num <= 0 {
		return false
	}
	c := s.Conferences[conf]
	if c == nil {
		return false
	}
	if num <= c.LastRead {
		return true
	}
	i := sort.SearchInts(c.Read, num)
	return i < len(c.Read) && c.Read[i] == num
}

// MarkRead records one message as read.
func (s *ReadState) MarkRead(conf, num int) {
	if num <= 0 || s.IsRead(conf, num) {
		return
	}
	c := s.Conferences[conf]
	if c == nil {
		c = &ConferenceState{}
		s.Conferences[conf] = c
	}
	i := sort.SearchInts(c.Read, num)
	c.Read = append(c.Read, 0)
	copy(c.Read[i+1:], c.Read[i:])
	c.Read[i] = num
	c.fold()
	s.dirty = true
}

// MarkAllRead records every given message number as read, which is
// what "mark this conference read" does.
func (s *ReadState) MarkAllRead(conf int, numbers []int) {
	for _, n := range numbers {
		s.MarkRead(conf, n)
	}
}

// UnreadCount counts how many of the given message numbers are still
// unread.
func (s *ReadState) UnreadCount(conf int, numbers []int) int {
	n := 0
	for _, num := range numbers {
		if !s.IsRead(conf, num) {
			n++
		}
	}
	return n
}

// fold absorbs the run of numbers immediately above LastRead into the
// mark, which is what keeps Read from growing as a conference is read
// through in order.
func (c *ConferenceState) fold() {
	i := 0
	for i < len(c.Read) && c.Read[i] == c.LastRead+1 {
		c.LastRead = c.Read[i]
		i++
	}
	if i > 0 {
		c.Read = append(c.Read[:0], c.Read[i:]...)
	}
}

// Save writes the state, atomically, and only when something changed.
//
// Skipping an unchanged save matters because this is called on the
// way out of every message: rewriting the file each time would put a
// disk write behind a keypress for no reason.
func (s *ReadState) Save() error {
	if !s.dirty || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("store: creating %s: %w", filepath.Dir(s.path), err)
	}

	data, err := json.MarshalIndent(struct {
		Conferences map[int]*ConferenceState `json:"conferences"`
	}{s.Conferences}, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encoding read state: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".readstate-*")
	if err != nil {
		return fmt.Errorf("store: creating a temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("store: writing read state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: writing read state: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("store: securing read state: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("store: storing read state: %w", err)
	}
	s.dirty = false
	return nil
}

// NewReadState returns state with nowhere to save to, for a caller
// that wants the bookkeeping without the persistence -- a packet
// opened from a read-only location, or a test.
func NewReadState() *ReadState {
	return &ReadState{Conferences: map[int]*ConferenceState{}}
}
