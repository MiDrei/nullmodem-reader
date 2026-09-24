package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newQueue(t *testing.T) *Queue {
	t.Helper()
	q, err := OpenQueue(filepath.Join(t.TempDir(), "up", "pending"))
	if err != nil {
		t.Fatalf("OpenQueue: %v", err)
	}
	return q
}

func TestAddAndListRoundTrip(t *testing.T) {
	q := newQueue(t)

	want := Reply{
		Conference: 3, ConferenceName: "Go Programming",
		To: "BOB", From: "ALICE", Subject: "Re: Grüße",
		Body: "Hallo Bob,\n\nschöne Grüße zurück.", RefNumber: 42,
	}
	stored, err := q.Add(want)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if stored.ID == "" {
		t.Fatal("Add returned no ID")
	}
	if stored.Written.IsZero() {
		t.Fatal("Add did not stamp the reply with a time")
	}

	list, err := q.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d replies, want 1", len(list))
	}
	got := list[0]
	if got.To != want.To || got.Subject != want.Subject || got.Body != want.Body ||
		got.Conference != want.Conference || got.RefNumber != want.RefNumber {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestQueueSurvivesReopening(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	q, err := OpenQueue(dir)
	if err != nil {
		t.Fatalf("OpenQueue: %v", err)
	}
	if _, err := q.Add(Reply{Conference: 1, Body: "written offline"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// The whole point of a queue: a reply written offline is still
	// there after the reader is closed and reopened.
	again, err := OpenQueue(dir)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	list, err := again.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Body != "written offline" {
		t.Fatalf("after reopening: %+v", list)
	}
}

func TestListIsChronological(t *testing.T) {
	q := newQueue(t)
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	for i, subject := range []string{"first", "second", "third"} {
		if _, err := q.Add(Reply{Subject: subject, Written: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatalf("Add %s: %v", subject, err)
		}
	}

	list, err := q.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var order []string
	for _, r := range list {
		order = append(order, r.Subject)
	}
	if strings.Join(order, ",") != "first,second,third" {
		t.Fatalf("order = %v, want the order they were written in", order)
	}
}

// Two replies written within the same second must not overwrite each
// other -- which is what a bare timestamp ID would do.
func TestSameSecondRepliesDoNotCollide(t *testing.T) {
	q := newQueue(t)
	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	for _, subject := range []string{"one", "two", "three"} {
		if _, err := q.Add(Reply{Subject: subject, Written: at}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	list, err := q.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List returned %d replies, want all 3", len(list))
	}
}

func TestUpdateReplacesAnExistingReply(t *testing.T) {
	q := newQueue(t)
	stored, err := q.Add(Reply{Subject: "draft", Body: "first try"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	stored.Body = "second try"
	if err := q.Update(stored); err != nil {
		t.Fatalf("Update: %v", err)
	}

	list, _ := q.List()
	if len(list) != 1 {
		t.Fatalf("Update created a second entry: %+v", list)
	}
	if list[0].Body != "second try" {
		t.Fatalf("Body = %q, want the edited text", list[0].Body)
	}
}

// An edit landing on a reply that was already sent must be reported,
// not turned into a new message.
func TestUpdateRefusesAnUnknownID(t *testing.T) {
	q := newQueue(t)
	err := q.Update(Reply{ID: "20260924-100000-abcdef", Body: "ghost"})
	if err == nil {
		t.Fatal("Update on an unknown ID should fail")
	}
	if list, _ := q.List(); len(list) != 0 {
		t.Fatalf("a failed Update still created %d entries", len(list))
	}
}

func TestRemoveAndRemoveAll(t *testing.T) {
	q := newQueue(t)
	var ids []string
	for _, s := range []string{"a", "b", "c"} {
		r, err := q.Add(Reply{Subject: s})
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		ids = append(ids, r.ID)
	}

	if err := q.Remove(ids[0]); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if n, _ := q.Len(); n != 2 {
		t.Fatalf("Len = %d, want 2", n)
	}

	// Removing something already gone is not a failure.
	if err := q.Remove(ids[0]); err != nil {
		t.Fatalf("removing an absent reply should succeed: %v", err)
	}

	if err := q.RemoveAll(ids); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if n, _ := q.Len(); n != 0 {
		t.Fatalf("Len = %d, want the queue emptied", n)
	}
}

// One corrupt file must not make the remaining replies unsendable.
func TestListSkipsCorruptEntries(t *testing.T) {
	q := newQueue(t)
	if _, err := q.Add(Reply{Subject: "good"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	bad := filepath.Join(q.Dir(), "20260924-999999-zzzzzz.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing the corrupt entry: %v", err)
	}

	list, err := q.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Subject != "good" {
		t.Fatalf("List = %+v, want just the readable reply", list)
	}
}

func TestListOnAnEmptyQueue(t *testing.T) {
	q := newQueue(t)
	list, err := q.List()
	if err != nil {
		t.Fatalf("List on an empty queue: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List = %+v, want none", list)
	}
}

// A pending reply is unsent private mail; it should not be
// world-readable.
func TestStoredRepliesAreNotWorldReadable(t *testing.T) {
	q := newQueue(t)
	stored, err := q.Add(Reply{Subject: "private"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	info, err := os.Stat(filepath.Join(q.Dir(), stored.ID+".json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("permissions = %v, want no access for group or others", info.Mode().Perm())
	}
}

// The ID is only second-resolution, so ordering by it alone leaves
// same-second replies in the random order of their filename suffixes.
func TestListOrdersSameSecondRepliesByWhenTheyWereWritten(t *testing.T) {
	q := newQueue(t)
	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	// Deliberately added out of order, within one second.
	for _, tc := range []struct {
		subject string
		offset  time.Duration
	}{
		{"third", 300 * time.Millisecond},
		{"first", 100 * time.Millisecond},
		{"second", 200 * time.Millisecond},
	} {
		if _, err := q.Add(Reply{Subject: tc.subject, Written: at.Add(tc.offset)}); err != nil {
			t.Fatalf("Add %s: %v", tc.subject, err)
		}
	}

	list, err := q.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var order []string
	for _, r := range list {
		order = append(order, r.Subject)
	}
	if strings.Join(order, ",") != "first,second,third" {
		t.Fatalf("order = %v, want them ordered by when they were written", order)
	}
}
