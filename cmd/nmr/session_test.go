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

	"github.com/zalando/go-keyring"

	"git.maik.ch/nullmodem/reader/internal/app"
	"git.maik.ch/nullmodem/reader/internal/config"
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
