package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midrei/nullmodem-kit/qwk"
)

func writePacket(t *testing.T, name string, confs []qwk.ConferenceInfo, msgs []qwk.PackedMessage) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	control := qwk.ControlInfo{BBSName: "NullModem BBS", BBSID: "NULLMODE", CallerName: "ALICE",
		PacketTime: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC), Conferences: confs}
	if err := qwk.BuildQWKPacket(path, control, msgs); err != nil {
		t.Fatalf("BuildQWKPacket: %v", err)
	}
	return path
}

func echo(num, conf int, subject string, day int) qwk.PackedMessage {
	return qwk.PackedMessage{
		Header: qwk.MessageHeader{Number: num, Conference: conf, To: "ALL", From: "BOB", Subject: subject,
			Written: time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC)},
		Text: "body of " + subject,
	}
}

func sourcesOf(t *testing.T, paths ...string) []Source {
	t.Helper()
	var out []Source
	for _, p := range paths {
		pk, err := qwk.OpenPacket(p)
		if err != nil {
			t.Fatalf("OpenPacket: %v", err)
		}
		out = append(out, Source{Path: p, Packet: pk})
	}
	return out
}

func TestMergeShowsEveryPacketOnceOldestFirst(t *testing.T) {
	older := writePacket(t, "A.QWK",
		[]qwk.ConferenceInfo{{Number: 3, Name: "Old Name"}},
		[]qwk.PackedMessage{echo(101, 3, "first", 20), echo(102, 3, "second", 21)})
	newer := writePacket(t, "B.QWK",
		[]qwk.ConferenceInfo{{Number: 3, Name: "General"}, {Number: 7, Name: "Art"}},
		[]qwk.PackedMessage{echo(102, 3, "second", 21), echo(105, 3, "third", 22), echo(106, 7, "art", 22)})

	m := mergeModel(sourcesOf(t, older, newer))

	if len(m.conferences) != 2 {
		t.Fatalf("conferences = %+v", m.conferences)
	}
	general := m.conferences[0]
	if general.Name != "General" {
		t.Fatalf("name = %q, want the newest packet's", general.Name)
	}
	var subjects []string
	for _, msg := range general.Messages {
		subjects = append(subjects, msg.Subject)
	}
	if strings.Join(subjects, ",") != "first,second,third" {
		t.Fatalf("messages = %v, want first,second,third (the duplicate once)", subjects)
	}
	if m.path != newer {
		t.Fatalf("path = %q, want the newest packet", m.path)
	}
}

func TestMergeKeepsDifferentMessagesThatShareANumber(t *testing.T) {
	// Packets from before stable numbering restarted at 1: two
	// different messages with the same number must both survive.
	a := writePacket(t, "A.QWK", []qwk.ConferenceInfo{{Number: 3, Name: "General"}},
		[]qwk.PackedMessage{echo(1, 3, "from packet A", 20)})
	b := writePacket(t, "B.QWK", []qwk.ConferenceInfo{{Number: 3, Name: "General"}},
		[]qwk.PackedMessage{echo(1, 3, "from packet B", 21)})

	m := mergeModel(sourcesOf(t, a, b))
	if n := len(m.conferences[0].Messages); n != 2 {
		t.Fatalf("messages = %d, want both", n)
	}
}

func TestUnreadMailFromTheOldPacketStaysAfterAFetch(t *testing.T) {
	older := writePacket(t, "A.QWK", []qwk.ConferenceInfo{{Number: 3, Name: "General"}},
		[]qwk.PackedMessage{echo(101, 3, "still unread", 20)})
	newer := writePacket(t, "B.QWK", []qwk.ConferenceInfo{{Number: 3, Name: "General"}},
		[]qwk.PackedMessage{echo(105, 3, "just arrived", 22)})

	a, err := NewMerged(sourcesOf(t, older), Options{})
	if err != nil {
		t.Fatalf("NewMerged: %v", err)
	}
	a.latest = func() (Loaded, error) { return Loaded{Sources: sourcesOf(t, older, newer)}, nil }
	a.fetch = fetchReturning(FetchResult{Received: 1})
	h := &harness{t: t, app: a, w: testWidth, h: testHeight}
	h.draw()

	h.typ('f')
	h.settleBackground()
	h.assertContains("2/2 msg", "both messages, both unread, in one conference")
	h.enter()
	h.assertContains("still unread", "the old packet's message is still listed")
	h.assertContains("just arrived", "next to the new one")
}

func fetchReturning(res FetchResult) func(context.Context) (FetchResult, error) {
	return func(context.Context) (FetchResult, error) { return res, nil }
}
