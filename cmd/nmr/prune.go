package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/config"
	"git.maik.ch/nullmodem/reader/internal/store"
)

// prune deletes what sys.KeepDays says has been kept long enough:
// downloaded packets older than that whose every message is marked
// read, and sent reply packets older than that. Zero keeps everything.
//
// The newest packet always stays, read or not, so opening the reader
// never lands on an empty screen just because all the mail was read.
// Read markers come from disk; marks made in a session still running
// count from its next save -- until then a packet merely lives a
// little longer.
func prune(cfg config.Config, sys config.System, now time.Time) (removed int, err error) {
	if sys.KeepDays <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(sys.KeepDays) * 24 * time.Hour)
	d := systemDirs(cfg, sys)

	read, err := store.LoadReadState(d.readState)
	if err != nil {
		return 0, err
	}
	paths, err := downloadedPackets(d.down)
	if err != nil && err != errNoPacket {
		return 0, err
	}
	for i, path := range paths {
		if i == len(paths)-1 {
			break // the newest stays
		}
		if !olderThan(path, cutoff) || !allRead(path, read) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("removing %s: %w", path, err)
		}
		removed++
	}

	sent := filepath.Join(d.up, "sent")
	entries, err := os.ReadDir(sent)
	if err != nil {
		return removed, nil // nothing sent yet
	}
	for _, e := range entries {
		path := filepath.Join(sent, e.Name())
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".rep") || !olderThan(path, cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("removing %s: %w", path, err)
		}
		removed++
	}
	return removed, nil
}

func olderThan(path string, cutoff time.Time) bool {
	info, err := os.Stat(path)
	return err == nil && info.ModTime().Before(cutoff)
}

// allRead reports whether every message in the packet is marked read.
// A packet that does not open counts as unread: deleting it would take
// away the one copy someone might still want to look at.
func allRead(path string, read *store.ReadState) bool {
	p, err := qwk.OpenPacket(path)
	if err != nil {
		return false
	}
	defer p.Close()
	for _, m := range p.Messages {
		if !read.IsRead(m.Header.Conference, m.Header.Number) {
			return false
		}
	}
	return true
}
