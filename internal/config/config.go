// Package config loads the reader's on-disk configuration: which BBS
// systems to exchange mail with, and where to keep the packets.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

// System is one BBS the reader exchanges QWK packets with.
type System struct {
	// ID is this system's short handle. It doubles as the environment
	// variable suffix for the password (see Password) and as the
	// subdirectory name under the data directory, so keep it to
	// letters, digits and underscores.
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	// URL is the base URL of the BBS's web API, e.g.
	// https://bbs.example.ch -- the QWK endpoints hang off /api/bbs.
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	// Password is deliberately optional here. Putting it in a config
	// file means it sits in cleartext on disk and in every backup, so
	// the reader prefers the NMR_PASSWORD_<ID> environment variable,
	// then the system keychain, and only falls back to this field when
	// neither has one -- see Password. The reader writes it only when
	// setup finds no keychain to store it in, and never logs it.
	PasswordInFile string `yaml:"password,omitempty"`
	// Poll is how often the scheduler checks this system for new mail.
	// Zero disables automatic polling; manual exchange still works.
	Poll time.Duration `yaml:"poll"`
	// KeepDays is how long a downloaded packet is kept once every
	// message in it has been read, and how long a sent reply packet is
	// kept at all. Zero keeps everything.
	KeepDays int `yaml:"keep_days,omitempty"`
}

// Password returns the system's password: the NMR_PASSWORD_<ID>
// environment variable first, then the system keychain (see
// StorePassword), then the config file. The second return value
// reports whether one was found at all -- an empty password is not a
// usable one, and the caller should say so rather than sending a
// blank login.
func (s System) Password() (string, bool) {
	if v, ok := os.LookupEnv(s.PasswordEnv()); ok && v != "" {
		return v, true
	}
	if v, err := keyring.Get(keyringService, s.keyringAccount()); err == nil && v != "" {
		return v, true
	}
	if s.PasswordInFile != "" {
		return s.PasswordInFile, true
	}
	return "", false
}

// PasswordEnv is the environment variable Password checks first.
func (s System) PasswordEnv() string { return "NMR_PASSWORD_" + strings.ToUpper(s.ID) }

// keyringService is the name the reader's entries carry in the
// system keychain (Windows Credential Manager, macOS Keychain, the
// Secret Service on Linux).
const keyringService = "NullModem Reader"

// keyringAccount keys the entry by system and login, so changing the
// username in the config doesn't silently reuse the old login's
// password.
func (s System) keyringAccount() string { return strings.ToUpper(s.ID) + "/" + s.Username }

// StorePassword puts the password into the system keychain. It fails
// where there is none (a Linux box without a Secret Service, say);
// the caller then decides whether the config file is an acceptable
// place instead.
func (s System) StorePassword(password string) error {
	if err := keyring.Set(keyringService, s.keyringAccount(), password); err != nil {
		return fmt.Errorf("config: storing the password in the system keychain: %w", err)
	}
	return nil
}

// Config is the whole configuration file.
type Config struct {
	// DataDir holds downloaded packets, outgoing replies and read
	// state. Empty means the platform default (see DefaultDataDir).
	DataDir string   `yaml:"data_dir,omitempty"`
	Systems []System `yaml:"systems"`
}

// System returns the configured system with the given ID.
func (c Config) System(id string) (System, bool) {
	for _, s := range c.Systems {
		if strings.EqualFold(s.ID, id) {
			return s, true
		}
	}
	return System{}, false
}

// DefaultPath is where Load looks when given no explicit path:
// $XDG_CONFIG_HOME/nmr/config.yaml, falling back to
// ~/.config/nmr/config.yaml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: locating config directory: %w", err)
	}
	return filepath.Join(dir, "nmr", "config.yaml"), nil
}

// DefaultDataDir is where packets and read state live when the config
// does not say otherwise.
func DefaultDataDir() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locating home directory: %w", err)
	}
	return filepath.Join(dir, ".nmr"), nil
}

// ErrNotFound is returned by Load when no config file exists, so a
// first run can tell "you have not configured anything yet" apart
// from "your config is broken" and offer to write a starter file.
var ErrNotFound = errors.New("config: no configuration file")

// Load reads the configuration from path, or from DefaultPath when
// path is empty, and fills in defaults. A missing file yields
// ErrNotFound rather than a zero Config, since silently proceeding
// with no systems configured only produces a confusing empty UI later.
func Load(path string) (Config, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%w at %s", ErrNotFound, path)
		}
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}
	if c.DataDir == "" {
		dir, err := DefaultDataDir()
		if err != nil {
			return Config{}, err
		}
		c.DataDir = dir
	}
	if err := c.validate(path); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate(path string) error {
	seen := map[string]bool{}
	for i, s := range c.Systems {
		switch {
		case s.ID == "":
			return fmt.Errorf("config: %s: system %d has no id", path, i+1)
		case s.URL == "":
			return fmt.Errorf("config: %s: system %q has no url", path, s.ID)
		case s.Username == "":
			return fmt.Errorf("config: %s: system %q has no username", path, s.ID)
		case s.KeepDays < 0:
			return fmt.Errorf("config: %s: system %q: keep_days cannot be negative", path, s.ID)
		}
		key := strings.ToUpper(s.ID)
		if seen[key] {
			return fmt.Errorf("config: %s: duplicate system id %q", path, s.ID)
		}
		seen[key] = true
	}
	return nil
}

// Save writes c to path (DefaultPath when empty), creating the
// directory. The file is 0600 since it may hold a password.
func Save(path string, c Config) error {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := c.validate(path); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append([]byte(header), data...), 0o600); err != nil {
		return fmt.Errorf("config: writing %s: %w", path, err)
	}
	return nil
}

// header opens a config file the reader wrote itself.
const header = `# NullModem Reader configuration, written by the setup screen.
# The password lives in the system keychain unless a password: line
# says otherwise; NMR_PASSWORD_<ID> in the environment overrides both.

`

// Example is a starter configuration, written on first run so the
// user has something to edit rather than a blank file.
const Example = `# NullModem Reader configuration.
#
# The password is best kept out of this file: the reader reads
# NMR_PASSWORD_<ID> from the environment first, then the system
# keychain (filled by the setup screen of "nmr gui"), and only falls
# back to a password: line here.

# data_dir: ~/.nmr

systems:
  - id: NULLMDM
    name: NullModem BBS
    url: https://bbs.example.ch
    username: your-login
    poll: 30m
    # Delete a packet once everything in it is read and it is older
    # than this many days; 0 keeps everything.
    keep_days: 30
`
