package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newReadState(t *testing.T) (*ReadState, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "readstate.json")
	s, err := LoadReadState(path)
	if err != nil {
		t.Fatalf("LoadReadState: %v", err)
	}
	return s, path
}

func TestMarkReadAndIsRead(t *testing.T) {
	s, _ := newReadState(t)

	if s.IsRead(3, 1) {
		t.Fatal("nothing should be read before anything is marked")
	}
	s.MarkRead(3, 1)
	if !s.IsRead(3, 1) {
		t.Fatal("the message was just marked read")
	}
	if s.IsRead(3, 2) {
		t.Fatal("marking one message must not mark its neighbour")
	}
	if s.IsRead(7, 1) {
		t.Fatal("marking a message must not reach into another conference")
	}
}

func TestReadingInOrderFoldsIntoTheHighWaterMark(t *testing.T) {
	s, _ := newReadState(t)
	for n := 1; n <= 500; n++ {
		s.MarkRead(3, n)
	}

	c := s.Conferences[3]
	if c.LastRead != 500 {
		t.Fatalf("LastRead = %d, want 500", c.LastRead)
	}
	// This is the point of the shape: reading a conference through
	// leaves nothing behind to grow.
	if len(c.Read) != 0 {
		t.Fatalf("Read holds %d stragglers, want none after reading in order", len(c.Read))
	}
	for n := 1; n <= 500; n++ {
		if !s.IsRead(3, n) {
			t.Fatalf("message %d should be read", n)
		}
	}
}

func TestReadingOutOfOrderKeepsStragglersUntilTheGapCloses(t *testing.T) {
	s, _ := newReadState(t)
	s.MarkRead(3, 5)
	s.MarkRead(3, 6)

	c := s.Conferences[3]
	if c.LastRead != 0 || len(c.Read) != 2 {
		t.Fatalf("state = %+v, want the two stragglers held above a zero mark", c)
	}
	if s.IsRead(3, 1) {
		t.Fatal("message 1 was skipped, not read")
	}

	// Closing the gap folds everything into the mark at once.
	for n := 1; n <= 4; n++ {
		s.MarkRead(3, n)
	}
	if c.LastRead != 6 || len(c.Read) != 0 {
		t.Fatalf("state = %+v, want everything folded into the mark once the gap closed", c)
	}
}

func TestMarkReadIsIdempotentAndIgnoresNonsense(t *testing.T) {
	s, _ := newReadState(t)
	s.MarkRead(3, 4)
	s.MarkRead(3, 4)
	s.MarkRead(3, 4)

	if got := len(s.Conferences[3].Read); got != 1 {
		t.Fatalf("Read holds %d entries, want the one message recorded once", got)
	}

	s.MarkRead(3, 0)
	s.MarkRead(3, -1)
	if s.IsRead(3, 0) || s.IsRead(3, -1) {
		t.Fatal("a message number of zero or less is not a message")
	}
}

func TestUnreadCountAndMarkAllRead(t *testing.T) {
	s, _ := newReadState(t)
	numbers := []int{1, 2, 3, 4, 5}

	if got := s.UnreadCount(3, numbers); got != 5 {
		t.Fatalf("UnreadCount = %d, want 5", got)
	}
	s.MarkRead(3, 2)
	if got := s.UnreadCount(3, numbers); got != 4 {
		t.Fatalf("UnreadCount = %d, want 4", got)
	}

	s.MarkAllRead(3, numbers)
	if got := s.UnreadCount(3, numbers); got != 0 {
		t.Fatalf("UnreadCount = %d, want none left", got)
	}
}

func TestStateSurvivesSaveAndLoad(t *testing.T) {
	s, path := newReadState(t)
	s.MarkRead(3, 1)
	s.MarkRead(3, 2)
	s.MarkRead(7, 42)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := LoadReadState(path)
	if err != nil {
		t.Fatalf("LoadReadState: %v", err)
	}
	if !again.IsRead(3, 1) || !again.IsRead(3, 2) || !again.IsRead(7, 42) {
		t.Fatalf("state did not survive a reload: %+v", again.Conferences)
	}
	if again.IsRead(3, 3) || again.IsRead(7, 1) {
		t.Fatal("the reload invented read markers")
	}
}

// Writing on every keypress for no change would put a disk write
// behind moving the cursor.
func TestSaveSkipsWritingWhenNothingChanged(t *testing.T) {
	s, path := newReadState(t)

	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Save wrote a file although nothing was ever marked")
	}

	s.MarkRead(3, 1)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after a real change: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("permissions = %v, want no access for group or others", info.Mode().Perm())
	}
}

// Read markers are a convenience: losing them means re-reading some
// messages, which beats refusing to open the packet.
func TestCorruptStateFileYieldsEmptyStateRatherThanAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readstate.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing the corrupt file: %v", err)
	}

	s, err := LoadReadState(path)
	if err != nil {
		t.Fatalf("LoadReadState on a corrupt file: %v", err)
	}
	if s.IsRead(3, 1) {
		t.Fatal("a corrupt file should leave nothing marked read")
	}
	s.MarkRead(3, 1)
	if err := s.Save(); err != nil {
		t.Fatalf("Save over a corrupt file: %v", err)
	}
}

func TestLoadingAMissingFileIsNotAnError(t *testing.T) {
	s, err := LoadReadState(filepath.Join(t.TempDir(), "nothing", "readstate.json"))
	if err != nil {
		t.Fatalf("LoadReadState on a missing file: %v", err)
	}
	if len(s.Conferences) != 0 {
		t.Fatalf("Conferences = %+v, want empty", s.Conferences)
	}
}

func TestMarkersFromBeforeStableNumberingAreDiscardedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readstate.json")
	legacy := `{"conferences":{"3":{"last_read":5}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := LoadReadState(path)
	if err != nil {
		t.Fatalf("LoadReadState: %v", err)
	}
	if s.IsRead(3, 1) {
		t.Fatal("a legacy marker survived: message 1 in conference 3 counts as read")
	}
	// The reset is written back even though nothing was marked since.
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"numbering": 2`) {
		t.Fatalf("saved file does not record the numbering:\n%s", data)
	}

	// From now on markers are kept.
	s.MarkRead(3, 4711)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, _ := LoadReadState(path)
	if !again.IsRead(3, 4711) {
		t.Fatal("a marker saved under the new numbering was dropped")
	}
}
