package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestPasswordPrefersEnvThenKeychainThenFile(t *testing.T) {
	keyring.MockInit()
	sys := System{ID: "nullmdm", Username: "alice", PasswordInFile: "from-file"}

	if pw, _ := sys.Password(); pw != "from-file" {
		t.Fatalf("file only: %q", pw)
	}
	if err := sys.StorePassword("from-keychain"); err != nil {
		t.Fatalf("StorePassword: %v", err)
	}
	if pw, _ := sys.Password(); pw != "from-keychain" {
		t.Fatalf("keychain over file: %q", pw)
	}
	t.Setenv("NMR_PASSWORD_NULLMDM", "from-env")
	if pw, _ := sys.Password(); pw != "from-env" {
		t.Fatalf("env over keychain: %q", pw)
	}

	// The keychain entry belongs to this login, not to the system.
	other := System{ID: "nullmdm", Username: "bob"}
	os.Unsetenv("NMR_PASSWORD_NULLMDM")
	if _, ok := other.Password(); ok {
		t.Fatal("another username must not get alice's password")
	}
}

func TestSaveRoundTripsAndKeepsThePasswordOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	cfg := Config{Systems: []System{{ID: "NULLMDM", Name: "NullModem BBS", URL: "https://bbs.example.ch", Username: "alice", Poll: 30 * time.Minute}}}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if regexp.MustCompile(`(?m)^\s*(- )?(password|data_dir):`).Match(data) {
		t.Fatalf("unexpected keys written:\n%s", data)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Systems) != 1 || got.Systems[0].Poll != 30*time.Minute || got.Systems[0].URL != "https://bbs.example.ch" {
		t.Fatalf("round trip = %+v\nfile:\n%s", got.Systems, data)
	}
}

func TestSaveRefusesAnInvalidConfig(t *testing.T) {
	if err := Save(filepath.Join(t.TempDir(), "c.yaml"), Config{Systems: []System{{ID: "X"}}}); err == nil {
		t.Fatal("a system without url/username must not be saved")
	}
}
