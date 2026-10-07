package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midrei/nullmodem-kit/qwk"
	"github.com/midrei/nullmodem-reader/internal/store"
	"github.com/midrei/nullmodem-reader/internal/xfer"
)

// fakeClient stands in for a BBS. uploadErr and downloadErr make it
// fail the way a bad connection would.
type fakeClient struct {
	uploadErr   error
	downloadErr error
	// packet is what a download produces; empty means no new mail.
	packet []qwk.PackedMessage

	uploaded     []string
	uploads      int
	uploadResult xfer.UploadResult
}

func (c *fakeClient) UploadREP(_ context.Context, path string) (xfer.UploadResult, error) {
	c.uploads++
	if c.uploadErr != nil {
		return xfer.UploadResult{}, c.uploadErr
	}
	c.uploaded = append(c.uploaded, path)
	return c.uploadResult, nil
}

func (c *fakeClient) DownloadQWK(_ context.Context, dir string) (string, error) {
	if c.downloadErr != nil {
		return "", c.downloadErr
	}
	if len(c.packet) == 0 {
		return "", xfer.ErrNoNewMail
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "TEST.QWK")
	err := qwk.BuildQWKPacket(path, qwk.ControlInfo{
		BBSName: "Test", BBSID: "TEST",
		Conferences: []qwk.ConferenceInfo{{Number: 0, Name: "Personal"}},
	}, c.packet)
	return path, err
}

func setup(t *testing.T, replies ...store.Reply) (*store.Queue, Dirs) {
	t.Helper()
	base := t.TempDir()
	q, err := store.OpenQueue(filepath.Join(base, "up", "pending"))
	if err != nil {
		t.Fatalf("OpenQueue: %v", err)
	}
	for _, r := range replies {
		if _, err := q.Add(r); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	return q, Dirs{Down: filepath.Join(base, "down"), Up: filepath.Join(base, "up")}
}

func TestRunSendsThenFetches(t *testing.T) {
	q, dirs := setup(t, store.Reply{Conference: 3, To: "BOB", Subject: "Re: hi", Body: "text"})
	c := &fakeClient{packet: []qwk.PackedMessage{
		{Header: qwk.MessageHeader{Number: 1, Conference: 0}, Text: "one"},
		{Header: qwk.MessageHeader{Number: 2, Conference: 0}, Text: "two"},
	}}

	res, err := Run(context.Background(), c, q, "TEST", dirs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("Sent = %d, want 1", res.Sent)
	}
	if res.Received != 2 {
		t.Fatalf("Received = %d, want 2", res.Received)
	}
	if res.NoNewMail() {
		t.Fatal("NoNewMail should be false when a packet arrived")
	}
	if n, _ := q.Len(); n != 0 {
		t.Fatalf("queue holds %d replies, want it cleared", n)
	}
}

// A download commits read state on the server, so the upload has to
// happen first -- otherwise a failed download leaves the replies
// queued behind mail the server already considers delivered.
func TestRunDoesNotDownloadWhenTheUploadFailed(t *testing.T) {
	q, dirs := setup(t, store.Reply{Conference: 3, Subject: "queued", Body: "text"})
	c := &fakeClient{
		uploadErr: errors.New("connection reset"),
		packet:    []qwk.PackedMessage{{Header: qwk.MessageHeader{Number: 1}, Text: "x"}},
	}

	res, err := Run(context.Background(), c, q, "TEST", dirs)
	if err == nil {
		t.Fatal("Run should report the failed upload")
	}
	if res.PacketPath != "" {
		t.Fatal("Run downloaded although the upload failed")
	}
	if n, _ := q.Len(); n != 1 {
		t.Fatalf("queue holds %d replies, want the message still waiting", n)
	}
}

func TestRunReportsNoNewMail(t *testing.T) {
	q, dirs := setup(t)
	res, err := Run(context.Background(), &fakeClient{}, q, "TEST", dirs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.NoNewMail() {
		t.Fatal("NoNewMail should be true when the board had nothing")
	}
	if res.Sent != 0 || res.Received != 0 {
		t.Fatalf("result = %+v, want nothing moved", res)
	}
}

func TestRunSkipsTheUploadWhenNothingIsQueued(t *testing.T) {
	q, dirs := setup(t)
	c := &fakeClient{}
	if _, err := Run(context.Background(), c, q, "TEST", dirs); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.uploads != 0 {
		t.Fatalf("uploaded %d times, want none -- an empty queue is not worth a request", c.uploads)
	}
}

func TestRunReportsCharactersItCouldNotCarry(t *testing.T) {
	q, dirs := setup(t, store.Reply{Conference: 1, Subject: "x", Body: "ein 中 im Text"})
	res, err := Run(context.Background(), &fakeClient{}, q, "TEST", dirs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Unmapped) != 1 || res.Unmapped[0] != '中' {
		t.Fatalf("Unmapped = %q, want the one rune CP437 has no room for", res.Unmapped)
	}
}

func TestRunArchivesTheSentPacket(t *testing.T) {
	q, dirs := setup(t, store.Reply{Conference: 1, Subject: "x", Body: "text"})
	if _, err := Run(context.Background(), &fakeClient{}, q, "TEST", dirs); err != nil {
		t.Fatalf("Run: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dirs.Up, "sent"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("sent directory: %v, %d entries -- want the packet that went out kept", err, len(entries))
	}
	if _, err := os.Stat(filepath.Join(dirs.Up, "TEST.REP")); !os.IsNotExist(err) {
		t.Fatal("the sent packet is still in the outgoing directory, where a later run could resend it")
	}
}

// ------------------------------------------------------------- lock

func TestLockExcludesASecondExchange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exchange.lock")

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire = %v, want ErrLocked", err)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	again.Release()
}

func TestReleasingTwiceIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exchange.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// A machine that lost power mid-exchange must not stay locked out
// forever.
func TestStaleLockIsTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exchange.lock")

	old, _ := json.Marshal(lockInfo{PID: 999999, Started: time.Now().Add(-StaleAfter - time.Minute)})
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatalf("writing the stale lock: %v", err)
	}

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire over a stale lock: %v", err)
	}
	defer l.Release()

	info, err := read(path)
	if err != nil {
		t.Fatalf("reading the new lock: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Fatalf("lock records pid %d, want this process", info.PID)
	}
}

func TestFreshLockIsNotTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exchange.lock")
	fresh, _ := json.Marshal(lockInfo{PID: 999999, Started: time.Now().Add(-time.Minute)})
	if err := os.WriteFile(path, fresh, 0o644); err != nil {
		t.Fatalf("writing the lock: %v", err)
	}

	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("Acquire = %v, want ErrLocked for a lock held a minute ago", err)
	}
}

// An unreadable lock file must not block exchanges forever either.
func TestUnparseableLockFallsBackToItsAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exchange.lock")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("writing the lock: %v", err)
	}
	old := time.Now().Add(-StaleAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("aging the lock: %v", err)
	}

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire over an old unparseable lock: %v", err)
	}
	l.Release()
}

func TestLockErrorSaysWhoHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exchange.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()

	_, err = Acquire(path)
	if err == nil {
		t.Fatal("second Acquire should fail")
	}
	if got := err.Error(); !strings.Contains(got, "pid") {
		t.Fatalf("error = %q, want it to name the holder so someone can act on it", got)
	}
}

func TestRefusedRepliesStayHeldAndAreNotSentAgain(t *testing.T) {
	q, dirs := setup(t,
		store.Reply{Conference: 3, To: "All", Subject: "fine", Body: "text"},
		store.Reply{Conference: 0, To: "nobody", Subject: "lost", Body: "text"},
	)
	c := &fakeClient{uploadResult: xfer.UploadResult{Posted: 1,
		Rejected: []xfer.Rejected{{Index: 1, Reason: `unknown recipient "nobody"`}}}}

	res, err := Run(context.Background(), c, q, "TEST", dirs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Sent != 1 || len(res.Rejected) != 1 || res.Rejected[0].Subject != "lost" {
		t.Fatalf("result = %+v, want 1 sent and the netmail refused", res)
	}
	left, _ := q.List()
	if len(left) != 1 || left[0].Subject != "lost" || !left[0].Held() || !strings.Contains(left[0].Error, "unknown recipient") {
		t.Fatalf("queue = %+v, want only the refused one, held with the reason", left)
	}

	// Held: the next exchange does not upload it again.
	if _, err := Run(context.Background(), c, q, "TEST", dirs); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if c.uploads != 1 {
		t.Fatalf("uploads = %d, want 1 -- a held message went out again", c.uploads)
	}

	// Edited (the error cleared), it goes out.
	left[0].To, left[0].Error = "bob", ""
	if err := q.Update(left[0]); err != nil {
		t.Fatal(err)
	}
	c.uploadResult = xfer.UploadResult{Sent: 1}
	res, err = Run(context.Background(), c, q, "TEST", dirs)
	if err != nil || res.Sent != 1 || c.uploads != 2 {
		t.Fatalf("after editing: %+v, %v, uploads %d", res, err, c.uploads)
	}
	if n, _ := q.Len(); n != 0 {
		t.Fatalf("queue holds %d, want it empty", n)
	}
}
