// Command mkfixture writes a sample .QWK packet: a CP437/ANSI welcome
// screen, prose with umlauts, ANSI art in a message body, and a
// subject long enough to need QWKE kludge lines. It exists so the
// reader can be exercised end to end without a live BBS.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midrei/nullmodem-kit/qwk"
)

// welcome is drawn the way real BBS art is: double-line box glyphs in
// CP437, bright colors, and a cursor jump that only lands correctly
// once the bytes have been through the Grid.
const welcome = "\x1b[2J\x1b[H" +
	"\x1b[0;1;36m\xc9" + "\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd" + "\xbb\r\n" +
	"\x1b[0;1;36m\xba\x1b[0;1;33m      N U L L M O D E M   B B S       \x1b[0;1;36m\xba\r\n" +
	"\x1b[0;1;36m\xba\x1b[0;36m      \xb0\xb1\xb2\xdb\x1b[0;1;37m QWK Offline Mail \x1b[0;36m\xdb\xb2\xb1\xb0      \x1b[0;1;36m\xba\r\n" +
	"\x1b[0;1;36m\xc8" + "\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd" + "\xbc\r\n" +
	"\x1b[6;10H\x1b[0;1;35mcursor-addressed, drawn out of order\x1b[0m\r\n"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkfixture <out.qwk>")
		os.Exit(1)
	}
	path := os.Args[1]

	// A fresh checkout has no testdata directory: it holds nothing but
	// generated files, so git does not track it. Creating it here is
	// what makes the README's first command work on a clone.
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "mkfixture:", err)
			os.Exit(1)
		}
	}

	longSubject := "A subject line deliberately longer than the classic 25-byte field"
	control := qwk.ControlInfo{
		BBSName:     "NullModem BBS",
		City:        "Zurich, CH",
		SysopName:   "Maik",
		BBSID:       "NULLMDM",
		PacketTime:  time.Now(),
		CallerName:  "ALICE",
		Username:    "alice",
		WelcomeFile: "WELCOME.ANS",
		Conferences: []qwk.ConferenceInfo{
			{Number: 0, Name: "Personal"},
			{Number: 3, Name: "Go Programming"},
			{Number: 7, Name: "ANSI Art"},
		},
	}

	messages := []qwk.PackedMessage{
		{Header: qwk.MessageHeader{Number: 1, Conference: 3, To: "ALICE", From: "BOB",
			Subject: "Gr\x81\xe1e aus Z\x81rich", Written: time.Now().Add(-2 * time.Hour)},
			// CP437 prose: 0x81 = ü, 0xE1 = ß.
			Text: "Hallo Alice,\n\ndas hier ist gew\x94hnlicher Flie\xe1text mit Umlauten:\n" +
				"\x84\x94\x81 \x8e\x99\x9a \xe1 -- und eine Zeile, die absichtlich weit \x81ber achtzig Spalten hinausgeht, damit der Umbruch sichtbar wird.\n\n" +
				"Gru\xe1,\nBob"},
		{Header: qwk.MessageHeader{Number: 2, Conference: 7, To: "ALL", From: "SYSOP",
			Subject: "ANSI im Nachrichtentext", Written: time.Now().Add(-time.Hour)},
			Text: "\x1b[0;1;31m\xdb\xdb\xdb\x1b[0;1;33m\xdb\xdb\xdb\x1b[0;1;32m\xdb\xdb\xdb\x1b[0;1;36m\xdb\xdb\xdb\x1b[0m\n" +
				"\x1b[0;35m\xb0\xb1\xb2\x1b[0;1;35m\xdb\x1b[0;35m\xb2\xb1\xb0\x1b[0m  Farbverlauf aus CP437-Blockzeichen\n"},
		{Header: qwk.MessageHeader{Number: 3, Conference: 0, To: "ALICE",
			From:    "A Sender Whose Name Also Exceeds The Field",
			Subject: longSubject, Written: time.Now()},
			Text: withKludges("ALICE", "A Sender Whose Name Also Exceeds The Field", longSubject,
				"Diese Nachricht tr\x84gt QWKE-Kludges, weil Betreff und Absender\n"+
					"l\x84nger sind als die 25 Byte im Header.")},
	}

	if err := qwk.BuildQWKPacket(path, control, messages); err != nil {
		fmt.Fprintln(os.Stderr, "mkfixture:", err)
		os.Exit(1)
	}
	if err := addFile(path, "WELCOME.ANS", []byte(welcome)); err != nil {
		fmt.Fprintln(os.Stderr, "mkfixture:", err)
		os.Exit(1)
	}
	// The same art standalone, which is what the GUI's rasteriser test
	// renders when asked for a sample image. Writing both here keeps
	// one command enough to recreate every fixture.
	art := strings.TrimSuffix(path, filepath.Ext(path)) + ".ANS"
	if err := os.WriteFile(art, []byte(welcome), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "mkfixture:", err)
		os.Exit(1)
	}
	fmt.Println(path)
	fmt.Println(art)
}

// withKludges mirrors what a QWKE-aware door writes into a body whose
// header fields had to be truncated. The reader's own writer does this
// automatically; here it is spelled out because BuildQWKPacket takes
// already-composed message text.
func withKludges(to, from, subject, text string) string {
	return "To: " + to + "\nFrom: " + from + "\nSubject: " + subject + "\n\n" + text
}

// addFile appends one entry to an existing zip by rewriting it --
// archive/zip cannot append in place.
func addFile(path, name string, data []byte) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	var out []struct {
		name string
		data []byte
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			zr.Close()
			return err
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			zr.Close()
			return err
		}
		out = append(out, struct {
			name string
			data []byte
		}{f.Name, buf})
	}
	zr.Close()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, e := range out {
		w, err := zw.Create(e.name)
		if err != nil {
			return err
		}
		if _, err := w.Write(e.data); err != nil {
			return err
		}
	}
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return zw.Close()
}
