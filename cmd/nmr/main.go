// Command nmr is the QWK(E) offline reader: it exchanges packets with
// NullModem BBS over its web API and displays them, rendering ANSI art
// through the same ansi.Grid matrix the BBS itself uses.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/app"
	"git.maik.ch/nullmodem/reader/internal/config"
	"git.maik.ch/nullmodem/reader/internal/exchange"
	"git.maik.ch/nullmodem/reader/internal/gui"
	"git.maik.ch/nullmodem/reader/internal/sched"
	"git.maik.ch/nullmodem/reader/internal/store"
	"git.maik.ch/nullmodem/reader/internal/tui"
	"git.maik.ch/nullmodem/reader/internal/ui"
	"git.maik.ch/nullmodem/reader/internal/xfer"
)

const usage = `nmr -- QWK(E) offline reader

  nmr open    [packet.qwk]        read a packet in the terminal
  nmr gui     [packet.qwk]        read a packet in a window (CP437 bitmap font)
                                  without a packet: every downloaded one together,
                                  or first-run setup and fetching when there is none
  nmr daemon  [-s ID]             exchange on a schedule until stopped
  nmr outbox  [-s ID]             list the replies waiting to be sent
  nmr init                        write a starter configuration
  nmr fetch   [-s ID]             exchange mail: upload replies, download a packet
  nmr areas   [-s ID]             list the message areas your packets cover
  nmr list    <packet.qwk>        list the packet's conferences and messages
  nmr read    <packet.qwk> [-c N] [-m N]   display messages
  nmr screen  <packet.qwk> [file] display a screen from the packet (default: welcome)
  nmr version                     print the version

Common flags:
  -s ID       which configured system to talk to (default: the only one)
  -config P   configuration file (default: the platform config directory)
  -w N        render width in columns (default: the terminal's, else 80)
`

// version is stamped by scripts/release.sh (-ldflags "-X main.version=...").
var version = ""

// versionString is version, else the module version "go install" records,
// else "dev" for a plain local build.
func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nmr:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		if runtime.GOOS == "windows" {
			// Started by double-clicking nmr.exe: Ebitengine hides the
			// console window that would have shown the usage text, so
			// a printed usage would look like nothing happened at all.
			// Open the reader window instead, which walks a first-time
			// user through setup.
			return cmdOpen(true, nil)
		}
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "open":
		return cmdOpen(false, rest)
	case "gui":
		return cmdOpen(true, rest)
	case "daemon":
		return cmdDaemon(rest)
	case "outbox":
		return cmdOutbox(rest)
	case "init":
		return cmdInit(rest)
	case "fetch":
		return cmdFetch(rest)
	case "areas":
		return cmdAreas(rest)
	case "list":
		return cmdList(rest)
	case "read":
		return cmdRead(rest)
	case "screen":
		return cmdScreen(rest)
	case "version", "-v", "--version":
		fmt.Println("nmr " + versionString())
		return nil
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try \"nmr help\")", cmd)
	}
}

// ---------------------------------------------------------------- open

func cmdOpen(window bool, args []string) error {
	name := "open"
	if window {
		name = "gui"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	systemID := fs.String("s", "", "system id")
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return fmt.Errorf("usage: nmr %s [packet.qwk]", name)
	}

	sess := session{configPath: *configPath, systemID: *systemID}
	opts := sess.options()

	var a *app.App
	if len(positional) == 1 {
		a, err = openPacket(positional[0], sess, opts)
		if err != nil {
			return err
		}
	} else {
		// No packet named: show every packet downloaded so far as one,
		// so "nmr fetch && nmr open" is the whole daily routine and
		// mail left unread before a fetch is still there -- or, on a
		// first run, set up and fetch from inside the reader.
		a, err = sess.start(opts)
		if err != nil {
			if !window {
				return err
			}
			// A window may have been opened by double-click, with no
			// console to print to: show the problem in the window.
			a = app.NewHome(opts, app.HomeIdle, err.Error())
		}
	}
	if window {
		return gui.Run(a)
	}
	return tui.Run(a)
}

// openPacket opens a packet named on the command line.
func openPacket(path string, sess session, opts app.Options) (*app.App, error) {
	p, err := qwk.OpenPacket(path)
	if err != nil {
		return nil, err
	}
	defer p.Close()

	// A data directory that cannot be opened costs replies and
	// remembered read markers, not the read itself: someone who just
	// wants to catch up on their mail should not be blocked by a
	// writable-directory problem.
	q, read, from, ctxErr := packetContext(sess.configPath, sess.systemID, p)
	if ctxErr != nil {
		fmt.Fprintln(os.Stderr, "nmr: replies and read markers are disabled:", ctxErr)
	}
	opts.Queue, opts.Read, opts.From = q, read, from
	return app.New(path, p, opts)
}

// errNoPacket means nothing has been downloaded yet.
var errNoPacket = errors.New("no packets downloaded yet")

// downloadedPackets lists the packets in dir, oldest first by
// modification time -- the order they were fetched in.
func downloadedPackets(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errNoPacket
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	type packet struct {
		path string
		mod  time.Time
	}
	var found []packet
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".qwk") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		found = append(found, packet{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	if len(found) == 0 {
		return nil, errNoPacket
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].mod.Before(found[j].mod) })
	paths := make([]string, len(found))
	for i, f := range found {
		paths[i] = f.path
	}
	return paths, nil
}

// -------------------------------------------------------------- daemon

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	systemID := fs.String("s", "", "only this system")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	systems := cfg.Systems
	if *systemID != "" {
		sys, ok := cfg.System(*systemID)
		if !ok {
			return fmt.Errorf("no system with id %q", *systemID)
		}
		systems = []config.System{sys}
	}

	var jobs []sched.Job
	for _, sys := range systems {
		if sys.Poll <= 0 {
			// Zero means "manual only", which is a setting, not an
			// oversight -- say so rather than silently skipping it.
			fmt.Fprintf(os.Stderr, "nmr: %s has no poll interval, skipping\n", sys.ID)
			continue
		}
		if _, ok := sys.Password(); !ok {
			return noPasswordError(sys)
		}

		// One client per system, reused across exchanges: it holds the
		// bearer token, so reusing it means one login per token
		// lifetime rather than one per poll.
		client := xfer.New(sys.URL, sys.Username)
		sys := sys
		jobs = append(jobs, sched.Job{
			ID:       sys.ID,
			Interval: sys.Poll,
			Exchange: func(ctx context.Context) (sched.Outcome, error) {
				return exchangeOnce(ctx, client, cfg, sys)
			},
		})
	}
	if len(jobs) == 0 {
		return errors.New("no system has a poll interval configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events := make(chan sched.Event, 32)
	go logEvents(events)

	for _, j := range jobs {
		fmt.Printf("Polling %s every %s.\n", j.ID, j.Interval)
	}
	fmt.Println("Press Ctrl-C to stop.")

	err = (&sched.Scheduler{Jobs: jobs, Events: events}).Run(ctx)
	close(events)
	return err
}

// logEvents prints what the scheduler did, one line each.
func logEvents(events <-chan sched.Event) {
	for e := range events {
		when := e.Time.Local().Format("15:04:05")
		next := ""
		if !e.Next.IsZero() {
			next = ", next " + e.Next.Local().Format("15:04:05")
		}
		switch e.Kind {
		case sched.Polling:
			// Deliberately not printed: one line per attempt plus one
			// per outcome would double the log to say nothing new.
		case sched.NoNewMail:
			fmt.Printf("%s  %-10s no new mail%s\n", when, e.System, next)
		case sched.Exchanged:
			fmt.Printf("%s  %-10s sent %d, received %d%s\n", when, e.System, e.Sent, e.Received, next)
		case sched.Skipped:
			fmt.Printf("%s  %-10s skipped, another exchange is running%s\n", when, e.System, next)
		case sched.Failed:
			fmt.Printf("%s  %-10s failed: %v%s\n", when, e.System, e.Err, next)
		}
	}
}

// -------------------------------------------------------------- outbox

func cmdOutbox(args []string) error {
	fs := flag.NewFlagSet("outbox", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	systemID := fs.String("s", "", "system id")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, sys, err := loadSystem(*configPath, *systemID)
	if err != nil {
		return err
	}
	q, err := store.OpenQueue(systemDirs(cfg, sys).pending)
	if err != nil {
		return err
	}
	pending, err := q.List()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Println("Nothing waiting to be sent.")
		return nil
	}

	for _, r := range pending {
		name := r.ConferenceName
		if name == "" {
			name = fmt.Sprintf("conference %d", r.Conference)
		}
		mark := " "
		if r.Private {
			mark = "*"
		}
		fmt.Printf("%s %s  %-24s  %-16s %s\n",
			mark, r.Written.Local().Format("2006-01-02 15:04"), name, r.To, r.Subject)
	}
	fmt.Printf("\n%d message(s) waiting; \"nmr fetch\" sends them.\n", len(pending))
	return nil
}

// ---------------------------------------------------------------- init

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	path := fs.String("config", "", "configuration file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := *path
	if target == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		target = p
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists -- edit it rather than overwriting your settings", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(target), err)
	}
	// 0600: this file may end up holding a password.
	if err := os.WriteFile(target, []byte(config.Example), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", target, err)
	}
	fmt.Printf("Wrote %s -- edit it, then run \"nmr fetch\".\n", target)
	return nil
}

// --------------------------------------------------------------- fetch

func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	systemID := fs.String("s", "", "system id")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, sys, err := loadSystem(*configPath, *systemID)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := login(ctx, sys)
	if err != nil {
		return err
	}
	out, err := exchangeOnce(ctx, client, cfg, sys)
	if err != nil {
		if errors.Is(err, sched.ErrSkipped) {
			return fmt.Errorf("another exchange for %s is already running", sys.ID)
		}
		return err
	}

	switch {
	case out.Sent > 0 && out.NoNewMail:
		fmt.Printf("%s: sent %d message(s); no new mail.\n", sys.Name, out.Sent)
	case out.NoNewMail:
		fmt.Printf("%s: no new mail.\n", sys.Name)
	case out.Sent > 0:
		fmt.Printf("%s: sent %d, received %d message(s).\n", sys.Name, out.Sent, out.Received)
	default:
		fmt.Printf("%s: received %d message(s).\n", sys.Name, out.Received)
	}
	return nil
}

// exchangeOnce performs one exchange under the system's lock,
// refreshing the login when the token has run out.
//
// The lock is what makes it safe to leave "nmr daemon" running while
// also fetching by hand: two exchanges at once would upload the same
// queued replies twice and race each other's read-state updates on
// the server.
func exchangeOnce(ctx context.Context, client *xfer.Client, cfg config.Config, sys config.System) (sched.Outcome, error) {
	res, err := exchangeLocked(ctx, client, cfg, sys)
	if len(res.Unmapped) > 0 {
		// Worth saying, not worth refusing to send over: the message
		// is still readable, and the user can see what was lost.
		fmt.Fprintln(os.Stderr, "nmr:", unmappedNote(res.Unmapped))
	}
	return sched.Outcome{Sent: res.Sent, Received: res.Received, NoNewMail: res.NoNewMail()}, err
}

func unmappedNote(unmapped []rune) string {
	return fmt.Sprintf("%d character(s) have no CP437 equivalent and were sent as '?': %s",
		len(unmapped), string(unmapped))
}

// exchangeLocked is exchangeOnce without the reporting, for callers
// that show the outcome their own way (the reader window).
func exchangeLocked(ctx context.Context, client *xfer.Client, cfg config.Config, sys config.System) (exchange.Result, error) {
	d := systemDirs(cfg, sys)

	lock, err := exchange.Acquire(d.lock)
	if err != nil {
		if errors.Is(err, exchange.ErrLocked) {
			return exchange.Result{}, fmt.Errorf("%w: %v", sched.ErrSkipped, err)
		}
		return exchange.Result{}, err
	}
	defer lock.Release()

	if !client.LoggedIn() {
		password, ok := sys.Password()
		if !ok {
			return exchange.Result{}, noPasswordError(sys)
		}
		if err := client.Login(ctx, password); err != nil {
			return exchange.Result{}, err
		}
	}

	q, err := store.OpenQueue(d.pending)
	if err != nil {
		return exchange.Result{}, err
	}
	return exchange.Run(ctx, client, q, sys.ID, exchange.Dirs{Down: d.down, Up: d.up})
}

func noPasswordError(sys config.System) error {
	return fmt.Errorf("no password for %s -- run \"nmr gui\" and press s to store one, or set %s", sys.ID, sys.PasswordEnv())
}

// --------------------------------------------------------------- areas

func cmdAreas(args []string) error {
	fs := flag.NewFlagSet("areas", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	systemID := fs.String("s", "", "system id")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, sys, err := loadSystem(*configPath, *systemID)
	if err != nil {
		return err
	}
	client, err := login(context.Background(), sys)
	if err != nil {
		return err
	}

	areas, err := client.Areas(context.Background())
	if err != nil {
		return err
	}
	for _, a := range areas {
		mark := " "
		if a.Selected {
			mark = "*"
		}
		fmt.Printf("%s %5d  %-28s %s\n", mark, a.ID, a.Name, a.Description)
	}
	fmt.Printf("\n%d area(s); * = included in your packets\n", len(areas))
	return nil
}

// ---------------------------------------------------------------- list

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: nmr list <packet.qwk>")
	}

	p, err := qwk.OpenPacket(positional[0])
	if err != nil {
		return err
	}
	defer p.Close()

	fmt.Printf("%s", ui.DecodeField(p.Control.BBSName))
	if p.Control.City != "" {
		fmt.Printf(" (%s)", ui.DecodeField(p.Control.City))
	}
	fmt.Println()
	if !p.Control.PacketTime.IsZero() {
		fmt.Printf("Packet from %s for %s\n", p.Control.PacketTime.Format("2006-01-02 15:04"), p.Control.CallerName)
	}
	if p.QWKE {
		fmt.Print("QWKE packet")
		if p.Ext.Alias != "" {
			fmt.Printf(" (alias %s)", p.Ext.Alias)
		}
		fmt.Println()
	}
	fmt.Println()

	counts := map[int]int{}
	for _, m := range p.Messages {
		counts[m.Header.Conference]++
	}
	nums := make([]int, 0, len(counts))
	for n := range counts {
		nums = append(nums, n)
	}
	sort.Ints(nums)

	for _, n := range nums {
		name := fmt.Sprintf("conference %d", n)
		if c, ok := p.Conference(n); ok {
			name = ui.DecodeField(c.Name)
		}
		flags := ""
		if area, ok := p.Ext.Area(n); ok && area.IsNetmail() {
			flags = "  [netmail]"
		}
		fmt.Printf("%5d  %-40s %4d msg%s\n", n, name, counts[n], flags)
	}
	fmt.Printf("\n%d message(s) total\n", len(p.Messages))
	return nil
}

// ---------------------------------------------------------------- read

func cmdRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	conf := fs.Int("c", -1, "only this conference number")
	msg := fs.Int("m", 0, "only this message number")
	width := fs.Int("w", 0, "render width in columns")
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: nmr read <packet.qwk> [-c conference] [-m message]")
	}

	p, err := qwk.OpenPacket(positional[0])
	if err != nil {
		return err
	}
	defer p.Close()

	cols := renderWidth(*width)
	shown := 0
	for _, m := range p.Messages {
		if *conf >= 0 && m.Header.Conference != *conf {
			continue
		}
		if *msg > 0 && m.Header.Number != *msg {
			continue
		}
		printMessage(p, m, cols)
		shown++
	}
	if shown == 0 {
		return errors.New("no message matched")
	}
	return nil
}

func printMessage(p *qwk.Packet, m qwk.PackedMessage, cols int) {
	// QWKE kludges carry the untruncated To/From/Subject; the header's
	// own 25-byte fields are only a fallback.
	k, body := qwk.ParseQWKEKludges(m.Text)
	// Header fields and kludge values are CP437 just like the body is.
	to := ui.DecodeField(firstNonEmpty(k.To, m.Header.To))
	from := ui.DecodeField(firstNonEmpty(k.From, m.Header.From))
	subject := ui.DecodeField(firstNonEmpty(k.Subject, m.Header.Subject))

	area := fmt.Sprintf("conference %d", m.Header.Conference)
	if c, ok := p.Conference(m.Header.Conference); ok {
		area = ui.DecodeField(c.Name)
	}

	rule := strings.Repeat("─", cols)
	fmt.Printf("\x1b[0;1;36m%s\x1b[0m\n", rule)
	fmt.Printf("\x1b[0;1;37mMsg #%d\x1b[0m in \x1b[0;1;32m%s\x1b[0m\n", m.Header.Number, area)
	fmt.Printf("From: %s\nTo:   %s\nSubj: %s\n", from, to, subject)
	if !m.Header.Written.IsZero() {
		fmt.Printf("Date: %s\n", m.Header.Written.Format("2006-01-02 15:04"))
	}
	fmt.Printf("\x1b[0;1;36m%s\x1b[0m\n", rule)
	fmt.Println(ui.RenderMessageBody(body, cols))
	fmt.Println()
}

// -------------------------------------------------------------- screen

func cmdScreen(args []string) error {
	fs := flag.NewFlagSet("screen", flag.ContinueOnError)
	width := fs.Int("w", 0, "render width in columns")
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return errors.New("usage: nmr screen <packet.qwk> [file]")
	}

	p, err := qwk.OpenPacket(positional[0])
	if err != nil {
		return err
	}
	defer p.Close()

	name := ""
	if len(positional) > 1 {
		name = positional[1]
	}
	data, name, err := findScreen(p, name)
	if err != nil {
		return err
	}
	// Art is drawn for 80 columns; rendering it at the terminal's own
	// width would reflow and break it. The parse width stays 80 and a
	// narrow window simply gets a wrapped picture from the terminal.
	fmt.Println(ui.RenderScreen(data, screenWidth(*width)))
	fmt.Fprintf(os.Stderr, "(%s)\n", name)
	return nil
}

// findScreen locates a screen inside the packet. With no name it
// tries CONTROL.DAT's welcome entry and then the conventional
// filenames, each with the extensions doors actually ship -- a packet
// names "WELCOME" in CONTROL.DAT but stores "WELCOME.ANS".
func findScreen(p *qwk.Packet, name string) ([]byte, string, error) {
	if name != "" {
		data, err := p.ReadFile(name)
		return data, name, err
	}

	var candidates []string
	for _, base := range []string{p.Control.WelcomeFile, "WELCOME", "HELLO"} {
		if base == "" {
			continue
		}
		candidates = append(candidates, base, base+".ANS", base+".TXT",
			strings.TrimSuffix(base, filepath.Ext(base)))
	}
	for _, c := range candidates {
		if data, err := p.ReadFile(c); err == nil {
			return data, c, nil
		}
	}
	return nil, "", fmt.Errorf("no welcome screen in the packet (it holds: %s)", strings.Join(p.Names, ", "))
}

// -------------------------------------------------------------- shared

func loadSystem(configPath, id string) (config.Config, config.System, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return cfg, config.System{}, fmt.Errorf("%w -- run \"nmr init\" to create one", err)
		}
		return cfg, config.System{}, err
	}
	switch {
	case len(cfg.Systems) == 0:
		return cfg, config.System{}, errors.New("no systems configured")
	case id != "":
		sys, ok := cfg.System(id)
		if !ok {
			return cfg, config.System{}, fmt.Errorf("no system with id %q", id)
		}
		return cfg, sys, nil
	case len(cfg.Systems) == 1:
		return cfg, cfg.Systems[0], nil
	default:
		ids := make([]string, len(cfg.Systems))
		for i, s := range cfg.Systems {
			ids[i] = s.ID
		}
		return cfg, config.System{}, fmt.Errorf("several systems configured (%s) -- pick one with -s", strings.Join(ids, ", "))
	}
}

func login(ctx context.Context, sys config.System) (*xfer.Client, error) {
	password, ok := sys.Password()
	if !ok {
		return nil, noPasswordError(sys)
	}
	client := xfer.New(sys.URL, sys.Username)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := client.Login(ctx, password); err != nil {
		return nil, err
	}
	return client, nil
}

type dirs struct{ down, up, pending, readState, lock string }

func systemDirs(cfg config.Config, sys config.System) dirs {
	return dirsFor(cfg.DataDir, sys.ID)
}

func dirsFor(dataDir, id string) dirs {
	base := filepath.Join(dataDir, strings.ToUpper(id))
	up := filepath.Join(base, "up")
	return dirs{
		down:      filepath.Join(base, "down"),
		up:        up,
		pending:   filepath.Join(up, "pending"),
		readState: filepath.Join(base, "readstate.json"),
		lock:      filepath.Join(base, "exchange.lock"),
	}
}

// packetContext locates everything a packet needs beyond its own
// contents: where replies queue, where read markers live, and the
// name to send under.
//
// It prefers the configured system, so a packet opened by hand ends
// up in the same queue "nmr fetch" will send and shares its read
// markers. Without a usable configuration it falls back to the
// packet's own BBS ID under the default data directory: someone
// handed a .QWK file should still be able to read and reply, even
// before they have configured anything.
func packetContext(configPath, systemID string, p *qwk.Packet) (*store.Queue, *store.ReadState, string, error) {
	dataDir, id, from := "", "", ui.DecodeField(p.Control.CallerName)

	if cfg, sys, err := loadSystem(configPath, systemID); err == nil {
		dataDir, id = cfg.DataDir, sys.ID
		if from == "" {
			from = sys.Username
		}
	} else {
		dataDir, err = config.DefaultDataDir()
		if err != nil {
			return nil, nil, from, err
		}
		id = p.Control.BBSID
		if id == "" {
			return nil, nil, from, errors.New("the packet names no BBS ID, so there is nowhere to keep replies or read markers")
		}
	}

	d := dirsFor(dataDir, id)
	q, err := store.OpenQueue(d.pending)
	if err != nil {
		return nil, nil, from, err
	}
	read, err := store.LoadReadState(d.readState)
	if err != nil {
		return q, nil, from, err
	}
	return q, read, from, nil
}

// renderWidth is the column count for prose: the terminal's own
// width, so a message fills the window the user actually has.
func renderWidth(flagValue int) int {
	if flagValue > 0 {
		return flagValue
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	return 80
}

// screenWidth is the column count for art, which is a different
// question from prose: BBS art is drawn for a fixed 80-column canvas
// and parsing it at anything else reflows it into nonsense. Only an
// explicit flag overrides that.
func screenWidth(flagValue int) int {
	if flagValue > 0 {
		return flagValue
	}
	return 80
}

// parseFlexible parses flags that may sit on either side of the
// positional arguments, returning the positionals in order.
//
// Go's flag package stops at the first non-flag argument, so a plain
// fs.Parse turns "read packet.qwk -w 76" into three positionals and
// silently ignores the width. Requiring flags-first would be the
// other option, but every other command-line tool the user reaches
// for accepts both orders, and being the exception is a papercut on
// every single invocation.
func parseFlexible(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
