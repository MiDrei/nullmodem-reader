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
	// the reader prefers the NMR_PASSWORD_<ID> environment variable
	// and only falls back to this field when that is unset -- see
	// Password. It is never written back out by the reader and never
	// logged.
	PasswordInFile string `yaml:"password,omitempty"`
	// Poll is how often the scheduler checks this system for new mail.
	// Zero disables automatic polling; manual exchange still works.
	Poll time.Duration `yaml:"poll"`
}

// Password returns the system's password, preferring the
// NMR_PASSWORD_<ID> environment variable over the config file. The
// second return value reports whether one was found at all -- an
// empty password is not a usable one, and the caller should say so
// rather than sending a blank login.
func (s System) Password() (string, bool) {
	if v, ok := os.LookupEnv("NMR_PASSWORD_" + strings.ToUpper(s.ID)); ok && v != "" {
		return v, true
	}
	if s.PasswordInFile != "" {
		return s.PasswordInFile, true
	}
	return "", false
}

// Config is the whole configuration file.
type Config struct {
	// DataDir holds downloaded packets, outgoing replies and read
	// state. Empty means the platform default (see DefaultDataDir).
	DataDir string   `yaml:"data_dir"`
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
		}
		key := strings.ToUpper(s.ID)
		if seen[key] {
			return fmt.Errorf("config: %s: duplicate system id %q", path, s.ID)
		}
		seen[key] = true
	}
	return nil
}

// Example is a starter configuration, written on first run so the
// user has something to edit rather than a blank file.
const Example = `# QWKReader configuration.
#
# The password is best kept out of this file: the reader reads
# NMR_PASSWORD_<ID> from the environment first and only falls back to
# a password: line here.

# data_dir: ~/.nmr

systems:
  - id: NULLMDM
    name: NullModem BBS
    url: https://bbs.example.ch
    username: your-login
    poll: 30m
`
