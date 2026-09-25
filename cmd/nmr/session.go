package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/app"
	"git.maik.ch/nullmodem/reader/internal/config"
	"git.maik.ch/nullmodem/reader/internal/sched"
	"git.maik.ch/nullmodem/reader/internal/xfer"
)

// session is what the reader window (or full-screen terminal) needs to
// exchange mail and set itself up without a command line: the
// app.Options hooks, backed by the same configuration, exchange lock
// and directories as "nmr fetch".
//
// Every hook reads the configuration afresh, so a login saved by the
// setup screen takes effect on the very next exchange.
type session struct {
	configPath, systemID string
}

// defaultPoll is what setup writes for a new system: the interval
// "nmr daemon" uses, and the same as the starter config's.
const defaultPoll = 30 * time.Minute

func (s session) options() app.Options {
	opts := app.Options{Fetch: s.fetch, Latest: s.latest, Setup: s.setup}
	if _, sys, err := loadSystem(s.configPath, s.systemID); err == nil {
		opts.SetupDefaults = app.SetupInput{URL: sys.URL, Username: sys.Username}
	}
	return opts
}

// start builds the reader for "nmr gui" without a packet argument.
func (s session) start(opts app.Options) (*app.App, error) {
	cfg, sys, err := loadSystem(s.configPath, s.systemID)
	switch {
	case errors.Is(err, config.ErrNotFound):
		return app.NewHome(opts, app.HomeSetup, ""), nil
	case err != nil:
		return nil, err
	}

	path, err := newestPacket(systemDirs(cfg, sys).down)
	switch {
	case errors.Is(err, errNoPacket):
		return app.NewHome(opts, app.HomeFetch, ""), nil
	case err != nil:
		return nil, err
	}
	return openPacket(path, s, opts)
}

// fetch is one exchange, with login problems reported as
// app.ErrNeedsSetup so the reader offers the setup screen.
func (s session) fetch(ctx context.Context) (app.FetchResult, error) {
	cfg, sys, err := loadSystem(s.configPath, s.systemID)
	if errors.Is(err, config.ErrNotFound) {
		return app.FetchResult{}, fmt.Errorf("%w: no BBS is set up yet", app.ErrNeedsSetup)
	}
	if err != nil {
		return app.FetchResult{}, err
	}
	password, ok := sys.Password()
	if !ok {
		return app.FetchResult{}, fmt.Errorf("%w: no password is stored for %s -- enter it to continue", app.ErrNeedsSetup, sys.Name)
	}

	client := xfer.New(sys.URL, sys.Username)
	loginCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = client.Login(loginCtx, password)
	cancel()
	if errors.Is(err, xfer.ErrUnauthorized) {
		return app.FetchResult{}, fmt.Errorf("%w: the BBS rejected the login -- check username and password", app.ErrNeedsSetup)
	}
	if err != nil {
		return app.FetchResult{}, err
	}

	res, err := exchangeLocked(ctx, client, cfg, sys)
	if errors.Is(err, sched.ErrSkipped) {
		return app.FetchResult{}, errors.New("another exchange for this BBS is already running")
	}
	out := app.FetchResult{Sent: res.Sent, Received: res.Received, NoNewMail: res.NoNewMail()}
	if len(res.Unmapped) > 0 {
		out.Note = unmappedNote(res.Unmapped)
	}
	return out, err
}

// latest opens the newest downloaded packet with its queue and read
// markers.
func (s session) latest() (app.Loaded, error) {
	cfg, sys, err := loadSystem(s.configPath, s.systemID)
	if err != nil {
		return app.Loaded{}, err
	}
	path, err := newestPacket(systemDirs(cfg, sys).down)
	if err != nil {
		return app.Loaded{}, err
	}
	p, err := qwk.OpenPacket(path)
	if err != nil {
		return app.Loaded{}, err
	}
	// Same fallback as a packet opened at start-up: without a queue
	// or read markers the packet is still readable.
	q, read, from, _ := packetContext(s.configPath, s.systemID, p)
	return app.Loaded{Path: path, Packet: p, Queue: q, Read: read, From: from}, nil
}

// setup checks a login against the BBS and saves it: the address and
// username in the config file, the password in the system keychain.
// Nothing is written unless the BBS accepted the login.
func (s session) setup(ctx context.Context, in app.SetupInput) (string, error) {
	url := normalizeURL(in.URL)
	client := xfer.New(url, in.Username)

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	name, err := client.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("could not reach a NullModem BBS at %s: %v", url, err)
	}
	if err := client.Login(ctx, in.Password); err != nil {
		if errors.Is(err, xfer.ErrUnauthorized) {
			return "", errors.New("the BBS rejected that username or password")
		}
		return "", fmt.Errorf("logging in to %s: %v", url, err)
	}

	cfg, err := config.Load(s.configPath)
	switch {
	case errors.Is(err, config.ErrNotFound):
		cfg = config.Config{}
	case err != nil:
		return "", err
	}
	// Load filled in the default data directory; writing it back
	// would pin a path the user never chose.
	if def, err := config.DefaultDataDir(); err == nil && cfg.DataDir == def {
		cfg.DataDir = ""
	}

	idx, err := s.systemIndex(cfg, url)
	if err != nil {
		return "", err
	}
	var sys config.System
	if idx >= 0 {
		sys = cfg.Systems[idx]
	} else {
		sys = config.System{ID: bbsID(name), Poll: defaultPoll}
	}
	if name != "" {
		sys.Name = name
	}
	sys.URL, sys.Username = url, in.Username

	var note string
	if err := sys.StorePassword(in.Password); err != nil {
		// No keychain on this machine (a Linux box without a Secret
		// Service, typically). A working reader beats a secure one
		// that cannot log in, but the user should know where it went.
		sys.PasswordInFile = in.Password
		note = "No system password store is available, so the password was saved in the config file."
	} else {
		sys.PasswordInFile = ""
	}
	if _, set := lookupEnv(sys.PasswordEnv()); set {
		note = strings.TrimSpace(note + " " + sys.PasswordEnv() + " is set in the environment and overrides it.")
	}

	if idx >= 0 {
		cfg.Systems[idx] = sys
	} else {
		cfg.Systems = append(cfg.Systems, sys)
	}
	if err := config.Save(s.configPath, cfg); err != nil {
		return "", err
	}
	return note, nil
}

// systemIndex picks the configured system setup should update: the one
// named with -s, the only one, or the one at the same address. -1
// means add a new one.
func (s session) systemIndex(cfg config.Config, url string) (int, error) {
	for i, sys := range cfg.Systems {
		if s.systemID != "" && strings.EqualFold(sys.ID, s.systemID) {
			return i, nil
		}
	}
	if s.systemID == "" && len(cfg.Systems) == 1 {
		return 0, nil
	}
	for i, sys := range cfg.Systems {
		if strings.EqualFold(strings.TrimRight(sys.URL, "/"), url) {
			return i, nil
		}
	}
	if len(cfg.Systems) > 1 {
		return -1, errors.New("several BBSes are configured -- start with -s ID to pick the one to change")
	}
	return -1, nil
}

// normalizeURL accepts what people type into an address bar:
// "bbs.example.ch" means https://bbs.example.ch.
func normalizeURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return u
}

// bbsID derives a system ID from the BBS's name the way the BBS derives
// its QWK packet ID (internal/qwkdoor.BBSID in NullModem BBS): letters
// and digits, upper case, at most 8. Matching it means a packet opened
// by hand lands in the same directories as a fetched one.
func bbsID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	id := b.String()
	if id == "" {
		id = "BBS"
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

// lookupEnv is os.LookupEnv, treating an empty value as unset.
func lookupEnv(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	return v, ok && v != ""
}
