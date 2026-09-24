package xfer

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
)

// fakeBBS mimics the endpoints NullModem BBS exposes, closely enough
// that a client working against it works against the real thing:
// same paths, same JSON field names, same status-code conventions
// (204 for "no new mail" and for a successful area update).
type fakeBBS struct {
	token       string
	packet      []byte
	areas       []Area
	lastAreaIDs []int64
	uploaded    []byte
	uploadName  string
}

func (f *fakeBBS) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/bbs/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.Username != "alice" || req.Password != "correct horse" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid username or password"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token": f.token, "username": "alice", "security_level": 10,
		})
	})

	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+f.token {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"invalid or expired token"}`)
				return
			}
			next(w, r)
		}
	}

	mux.HandleFunc("GET /api/bbs/qwk/areas", authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.areas)
	}))
	mux.HandleFunc("PUT /api/bbs/qwk/areas", authed(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AreaIDs []int64 `json:"area_ids"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.lastAreaIDs = req.AreaIDs
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/bbs/qwk/download", authed(func(w http.ResponseWriter, r *http.Request) {
		if len(f.packet) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="NULLMDM.QWK"`)
		w.Write(f.packet)
	}))
	mux.HandleFunc("POST /api/bbs/qwk/upload", authed(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, `{"error":"malformed"}`, http.StatusBadRequest)
			return
		}
		part, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, `{"error":"missing file upload"}`, http.StatusBadRequest)
			return
		}
		defer part.Close()
		f.uploaded, _ = io.ReadAll(part)
		f.uploadName = hdr.Filename
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}

func newTestClient(t *testing.T, f *fakeBBS) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "alice")
	c.HTTP = srv.Client()
	return c, srv
}

func TestLoginStoresTokenAndAuthenticatesLaterCalls(t *testing.T) {
	f := &fakeBBS{token: "tok-123", areas: []Area{{ID: 1, Name: "General", Selected: true}}}
	c, _ := newTestClient(t, f)

	if c.LoggedIn() {
		t.Fatal("LoggedIn should be false before logging in")
	}
	if err := c.Login(context.Background(), "correct horse"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !c.LoggedIn() {
		t.Fatal("LoggedIn should be true after a successful login")
	}

	areas, err := c.Areas(context.Background())
	if err != nil {
		t.Fatalf("Areas: %v", err)
	}
	if len(areas) != 1 || areas[0].Name != "General" || !areas[0].Selected {
		t.Fatalf("Areas = %+v", areas)
	}
}

func TestLoginSurfacesTheServersOwnErrorMessage(t *testing.T) {
	c, _ := newTestClient(t, &fakeBBS{token: "tok-123"})

	err := c.Login(context.Background(), "wrong")
	if err == nil {
		t.Fatal("Login with a bad password should fail")
	}
	if !strings.Contains(err.Error(), "invalid username or password") {
		t.Fatalf("error = %v, want the server's own message so the user knows what went wrong", err)
	}
}

func TestRequestsBeforeLoginFailLocallyRatherThanAnonymously(t *testing.T) {
	c, _ := newTestClient(t, &fakeBBS{token: "tok-123"})

	if _, err := c.Areas(context.Background()); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("error = %v, want a clear \"not logged in\" rather than a puzzling 401", err)
	}
}

func TestDownloadQWKWritesPacketUnderTheServersFilename(t *testing.T) {
	f := &fakeBBS{token: "tok-123", packet: []byte("PK\x03\x04 pretend this is a packet")}
	c, _ := newTestClient(t, f)
	if err := c.Login(context.Background(), "correct horse"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "down")
	path, err := c.DownloadQWK(context.Background(), dir)
	if err != nil {
		t.Fatalf("DownloadQWK: %v", err)
	}
	if filepath.Base(path) != "NULLMDM.QWK" {
		t.Fatalf("saved as %q, want NULLMDM.QWK from the Content-Disposition header", filepath.Base(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading downloaded packet: %v", err)
	}
	if string(got) != string(f.packet) {
		t.Fatalf("downloaded %q, want %q", got, f.packet)
	}

	// The temporary file used during the transfer must be gone.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("download directory holds %d entries, want just the packet", len(entries))
	}
}

func TestDownloadQWKReportsNoNewMailOn204(t *testing.T) {
	c, _ := newTestClient(t, &fakeBBS{token: "tok-123"}) // no packet
	if err := c.Login(context.Background(), "correct horse"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	_, err := c.DownloadQWK(context.Background(), t.TempDir())
	if !errors.Is(err, ErrNoNewMail) {
		t.Fatalf("err = %v, want ErrNoNewMail so a scheduled run can treat it as routine", err)
	}
}

func TestUploadREPSendsTheFileAsMultipartFieldFile(t *testing.T) {
	f := &fakeBBS{token: "tok-123"}
	c, _ := newTestClient(t, f)
	if err := c.Login(context.Background(), "correct horse"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	path := filepath.Join(t.TempDir(), "NULLMDM.REP")
	want := []byte("PK\x03\x04 pretend this is a reply packet")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if err := c.UploadREP(context.Background(), path); err != nil {
		t.Fatalf("UploadREP: %v", err)
	}
	if string(f.uploaded) != string(want) {
		t.Fatalf("server received %q, want %q", f.uploaded, want)
	}
	if f.uploadName != "NULLMDM.REP" {
		t.Fatalf("server saw filename %q, want NULLMDM.REP", f.uploadName)
	}
}

func TestSetAreasSendsAreaIDs(t *testing.T) {
	f := &fakeBBS{token: "tok-123"}
	c, _ := newTestClient(t, f)
	if err := c.Login(context.Background(), "correct horse"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if err := c.SetAreas(context.Background(), []int64{3, 7}); err != nil {
		t.Fatalf("SetAreas: %v", err)
	}
	if len(f.lastAreaIDs) != 2 || f.lastAreaIDs[0] != 3 || f.lastAreaIDs[1] != 7 {
		t.Fatalf("server received %v, want [3 7]", f.lastAreaIDs)
	}
}

// A filename arriving over the network must not be able to steer the
// write out of the download directory.
func TestPacketFilenameRefusesTraversal(t *testing.T) {
	for _, disposition := range []string{
		`attachment; filename="../../etc/passwd"`,
		`attachment; filename=".."`,
		`attachment; filename="."`,
		`attachment; filename=""`,
	} {
		got := packetFilename(disposition, "alice")
		if got != "ALICE.QWK" {
			t.Fatalf("packetFilename(%q) = %q, want the safe fallback", disposition, got)
		}
	}
}

func TestPacketFilenameAcceptsAnOrdinaryName(t *testing.T) {
	if got := packetFilename(`attachment; filename="NULLMDM.QWK"`, "alice"); got != "NULLMDM.QWK" {
		t.Fatalf("packetFilename = %q, want NULLMDM.QWK", got)
	}
}
