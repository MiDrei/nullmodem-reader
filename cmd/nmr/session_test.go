package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/zalando/go-keyring"

	"git.maik.ch/nullmodem/kit/ansi"
	"git.maik.ch/nullmodem/kit/qwk"

	"git.maik.ch/nullmodem/reader/internal/app"
	"git.maik.ch/nullmodem/reader/internal/config"
	"git.maik.ch/nullmodem/reader/internal/store"
)

// fakeBBS answers the three calls setup and a first fetch make.
func fakeBBS(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bbs/info", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"name":"NullModem BBS"}`)
	})
	mux.HandleFunc("POST /api/bbs/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Username != "alice" || req.Password != "s3cret!" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid username or password"}`)
			return
		}
		io.WriteString(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("GET /api/bbs/qwk/download", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newSession(t *testing.T) (session, string) {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("HOME", dir) // default data directory
	path := filepath.Join(dir, "nmr", "config.yaml")
	return session{configPath: path}, path
}

func TestSetupSavesConfigAndKeepsThePasswordInTheKeychain(t *testing.T) {
	srv := fakeBBS(t)
	s, path := newSession(t)

	note, err := s.setup(context.Background(), app.SetupInput{URL: srv.URL + "/", Username: "alice", Password: "s3cret!"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if note != "" {
		t.Fatalf("note = %q, want none with a working keychain", note)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if strings.Contains(string(data), "s3cret!") {
		t.Fatalf("password in the config file:\n%s", data)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sys := cfg.Systems[0]
	if sys.ID != "NULLMODE" || sys.Name != "NullModem BBS" || sys.URL != srv.URL || sys.Username != "alice" {
		t.Fatalf("system = %+v", sys)
	}
	if pw, ok := sys.Password(); !ok || pw != "s3cret!" {
		t.Fatalf("password from the keychain = %q, %v", pw, ok)
	}

	// The saved login works for an exchange straight away.
	res, err := s.fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch after setup: %v", err)
	}
	if !res.NoNewMail {
		t.Fatalf("fetch = %+v, want no new mail from the fake", res)
	}
}

func TestSetupWithAWrongPasswordWritesNothing(t *testing.T) {
	srv := fakeBBS(t)
	s, path := newSession(t)

	_, err := s.setup(context.Background(), app.SetupInput{URL: srv.URL, Username: "alice", Password: "nope"})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v, want a rejected login", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("config written despite the failed login: %v", statErr)
	}
}

func TestSetupReachingSomethingElseSaysSo(t *testing.T) {
	s, _ := newSession(t)
	_, err := s.setup(context.Background(), app.SetupInput{URL: "http://127.0.0.1:1", Username: "alice", Password: "x"})
	if err == nil || !strings.Contains(err.Error(), "could not reach") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupUpdatesTheExistingSystem(t *testing.T) {
	srv := fakeBBS(t)
	s, path := newSession(t)
	if err := config.Save(path, config.Config{Systems: []config.System{{ID: "HOME", Name: "Old", URL: "https://old.example", Username: "old", PasswordInFile: "stale"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := s.setup(context.Background(), app.SetupInput{URL: srv.URL, Username: "alice", Password: "s3cret!"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg, _ := config.Load(path)
	if len(cfg.Systems) != 1 {
		t.Fatalf("systems = %+v, want the one updated in place", cfg.Systems)
	}
	sys := cfg.Systems[0]
	if sys.ID != "HOME" || sys.URL != srv.URL || sys.Username != "alice" || sys.PasswordInFile != "" {
		t.Fatalf("system = %+v (ID kept, the rest updated, stale file password dropped)", sys)
	}
}

func TestFetchWithoutConfigOrPasswordAsksForSetup(t *testing.T) {
	s, path := newSession(t)
	if _, err := s.fetch(context.Background()); !errors.Is(err, app.ErrNeedsSetup) {
		t.Fatalf("no config: err = %v, want ErrNeedsSetup", err)
	}

	srv := fakeBBS(t)
	if err := config.Save(path, config.Config{Systems: []config.System{{ID: "NULLMDM", URL: srv.URL, Username: "alice"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.fetch(context.Background()); !errors.Is(err, app.ErrNeedsSetup) {
		t.Fatalf("no password: err = %v, want ErrNeedsSetup", err)
	}

	t.Setenv("NMR_PASSWORD_NULLMDM", "wrong")
	if _, err := s.fetch(context.Background()); !errors.Is(err, app.ErrNeedsSetup) {
		t.Fatalf("wrong password: err = %v, want ErrNeedsSetup", err)
	}
}

func TestNormalizeURLAndBBSID(t *testing.T) {
	for in, want := range map[string]string{
		"bbs.example.ch":          "https://bbs.example.ch",
		" http://apollo:8090/ ":   "http://apollo:8090",
		"https://bbs.example.ch/": "https://bbs.example.ch",
	} {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"NullModem BBS": "NULLMODE", "": "BBS", "a-b c": "ABC"} {
		if got := bbsID(in); got != want {
			t.Errorf("bbsID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStartShowsEveryDownloadedPacketOldestFirst(t *testing.T) {
	s, path := newSession(t)
	if err := config.Save(path, config.Config{Systems: []config.System{{ID: "NULLMODE", URL: "http://bbs.invalid", Username: "alice"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, sys, err := loadSystem(path, "")
	if err != nil {
		t.Fatalf("loadSystem: %v", err)
	}
	down := systemDirs(cfg, sys).down
	if err := os.MkdirAll(down, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, subject string, num int, mod time.Time) {
		t.Helper()
		p := filepath.Join(down, name)
		control := qwk.ControlInfo{BBSName: "NullModem BBS", BBSID: "NULLMODE", CallerName: "ALICE",
			Conferences: []qwk.ConferenceInfo{{Number: 3, Name: "General"}}}
		msgs := []qwk.PackedMessage{{Header: qwk.MessageHeader{Number: num, Conference: 3, To: "ALL", From: "BOB",
			Subject: subject, Written: mod}, Text: "x"}}
		if err := qwk.BuildQWKPacket(p, control, msgs); err != nil {
			t.Fatalf("BuildQWKPacket: %v", err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	// Named against their age, so the order can only come from mtime.
	write("Z.QWK", "older mail", 101, time.Now().Add(-48*time.Hour))
	write("A.QWK", "newer mail", 105, time.Now().Add(-time.Hour))

	paths, err := downloadedPackets(down)
	if err != nil || len(paths) != 2 || filepath.Base(paths[0]) != "Z.QWK" {
		t.Fatalf("downloadedPackets = %v, %v; want Z.QWK (older) first", paths, err)
	}

	a, err := s.start(s.options())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	a.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	screen := gridText(a.Render(80, 24))
	older, newer := strings.Index(screen, "older mail"), strings.Index(screen, "newer mail")
	if older < 0 || newer < 0 || older > newer {
		t.Fatalf("want both messages, older first:\n%s", screen)
	}
}

func gridText(g ansi.Grid) string {
	var b strings.Builder
	for y := 0; y < g.Height; y++ {
		for x := 0; x < g.Width; x++ {
			b.WriteRune(ansi.Rune(g.Cells[y*g.Width+x].Char))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestPruneDeletesOnlyOldFullyReadPacketsAndKeepsTheNewest(t *testing.T) {
	_, path := newSession(t)
	if err := config.Save(path, config.Config{Systems: []config.System{{ID: "NULLMODE", URL: "http://bbs.invalid", Username: "alice", KeepDays: 30}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, sys, err := loadSystem(path, "")
	if err != nil {
		t.Fatalf("loadSystem: %v", err)
	}
	d := systemDirs(cfg, sys)
	sent := filepath.Join(d.up, "sent")
	for _, dir := range []string{d.down, sent} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	age := func(p string, days int) {
		t.Helper()
		when := now.Add(-time.Duration(days) * 24 * time.Hour)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	packet := func(name string, num, days int) string {
		t.Helper()
		p := filepath.Join(d.down, name)
		control := qwk.ControlInfo{BBSName: "NullModem BBS", BBSID: "NULLMODE",
			Conferences: []qwk.ConferenceInfo{{Number: 3, Name: "General"}}}
		msgs := []qwk.PackedMessage{{Header: qwk.MessageHeader{Number: num, Conference: 3, To: "ALL", From: "BOB", Subject: name}, Text: "x"}}
		if err := qwk.BuildQWKPacket(p, control, msgs); err != nil {
			t.Fatalf("BuildQWKPacket: %v", err)
		}
		age(p, days)
		return p
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	oldRead := packet("A.QWK", 101, 40)    // past keep_days, read: goes
	oldUnread := packet("B.QWK", 102, 39)  // past keep_days, unread: stays
	newestRead := packet("C.QWK", 103, 35) // past keep_days, read, but the newest: stays
	read, _ := store.LoadReadState(d.readState)
	read.MarkRead(3, 101)
	read.MarkRead(3, 103)
	if err := read.Save(); err != nil {
		t.Fatal(err)
	}
	oldREP, newREP := filepath.Join(sent, "old.REP"), filepath.Join(sent, "new.REP")
	os.WriteFile(oldREP, []byte("x"), 0o600)
	os.WriteFile(newREP, []byte("x"), 0o600)
	age(oldREP, 45)

	removed, err := prune(cfg, sys, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if exists(oldRead) || !exists(oldUnread) || !exists(newestRead) {
		t.Fatalf("after prune: A=%v B=%v C=%v, want A gone, B and C kept", exists(oldRead), exists(oldUnread), exists(newestRead))
	}
	if exists(oldREP) || !exists(newREP) {
		t.Fatal("sent replies: want only the old one removed")
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (one packet, one reply)", removed)
	}

	// Once a newer packet arrives, C is no longer the newest and goes;
	// the newcomer is recent and stays whatever its read state.
	fresh := packet("D.QWK", 104, 1)
	if _, err := prune(cfg, sys, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if exists(newestRead) || !exists(fresh) || !exists(oldUnread) {
		t.Fatalf("second prune: C=%v D=%v B=%v, want C gone, D and B kept", exists(newestRead), exists(fresh), exists(oldUnread))
	}

	// keep_days 0 keeps everything.
	sys.KeepDays = 0
	if n, _ := prune(cfg, sys, now.Add(1000*24*time.Hour)); n != 0 {
		t.Fatalf("keep_days 0 removed %d", n)
	}
}

func TestSetupWithoutAPasswordKeepsTheStoredOneAndSavesKeepDays(t *testing.T) {
	srv := fakeBBS(t)
	s, path := newSession(t)
	if _, err := s.setup(context.Background(), app.SetupInput{URL: srv.URL, Username: "alice", Password: "s3cret!", KeepDays: 30}); err != nil {
		t.Fatalf("first setup: %v", err)
	}
	if _, err := s.setup(context.Background(), app.SetupInput{URL: srv.URL, Username: "alice", KeepDays: 7}); err != nil {
		t.Fatalf("setup without a password: %v", err)
	}
	cfg, _ := config.Load(path)
	sys := cfg.Systems[0]
	if sys.KeepDays != 7 {
		t.Fatalf("keep days = %d, want 7", sys.KeepDays)
	}
	if pw, _ := sys.Password(); pw != "s3cret!" {
		t.Fatalf("stored password = %q, want it kept", pw)
	}
	if d := s.options().SetupDefaults; !d.HavePassword || d.KeepDays != 7 {
		t.Fatalf("defaults = %+v", d)
	}
}

func TestSetupWithoutAnyPasswordAsksForOne(t *testing.T) {
	srv := fakeBBS(t)
	s, _ := newSession(t)
	if _, err := s.setup(context.Background(), app.SetupInput{URL: srv.URL, Username: "alice"}); err == nil || !strings.Contains(err.Error(), "enter your password") {
		t.Fatalf("err = %v", err)
	}
}

func TestReplyIDComesFromTheNewestPacket(t *testing.T) {
	dir := t.TempDir()
	if got := replyID(dir, "LOCALID"); got != "LOCALID" {
		t.Fatalf("no packets: %q, want the fallback", got)
	}
	control := qwk.ControlInfo{BBSName: "Maiks Place BBS", BBSID: "MAIKSPLA", Conferences: []qwk.ConferenceInfo{{Number: 0, Name: "Personal"}}}
	msgs := []qwk.PackedMessage{{Header: qwk.MessageHeader{Number: 1, Conference: 0, To: "x", From: "y", Subject: "z"}, Text: "t"}}
	if err := qwk.BuildQWKPacket(filepath.Join(dir, "MAIKSPLA.QWK"), control, msgs); err != nil {
		t.Fatal(err)
	}
	if got := replyID(dir, "LOCALID"); got != "MAIKSPLA" {
		t.Fatalf("replyID = %q, want the packet's MAIKSPLA", got)
	}
}

func TestTaglinesComeFromNextToTheConfigAndTheChoiceSticks(t *testing.T) {
	s, path := newSession(t)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "taglines.txt"), []byte("Mein eigener Spruch\n"), 0o600)

	opts := s.options()
	if last := opts.Taglines[len(opts.Taglines)-1]; last != "Mein eigener Spruch" {
		t.Fatalf("last tagline = %q, want the user's own after the defaults", last)
	}
	if opts.TaglineChoice != "" {
		t.Fatalf("choice = %q before anything was picked", opts.TaglineChoice)
	}
	opts.SaveTaglineChoice("Mein eigener Spruch")
	if got := s.options().TaglineChoice; got != "Mein eigener Spruch" {
		t.Fatalf("choice after saving = %q", got)
	}
}
