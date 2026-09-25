package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.maik.ch/nullmodem/kit/qwk"
	"git.maik.ch/nullmodem/reader/internal/ui"
)

// Message is one message prepared for display: header fields already
// decoded from CP437 and already resolved against any QWKE kludge
// lines, with those lines stripped off the body.
//
// That resolution happens once here rather than in each view, because
// getting it wrong is invisible until someone's name is long: the
// header's own fields are hard-truncated at 25 bytes and would simply
// show a shortened name with no hint anything was lost.
type Message struct {
	Number            int
	To, From, Subject string
	Written           time.Time
	// Body is CP437 with "\n" line endings, kludges removed, ready
	// for ui.MessageGrid.
	Body    string
	Private bool
	// Routing is the echomail's SEEN-BY/PATH block, split off Body:
	// hidden when reading, shown on request, quotable into a reply.
	// CP437 lines, any leading ^A removed.
	Routing []string
}

// Conference is one conference in the packet with its messages, in
// packet order.
type Conference struct {
	Number   int
	Name     string
	Netmail  bool
	Messages []Message
}

// Numbers lists the conference's message numbers, for the read-state
// bookkeeping that works in those terms.
func (c Conference) Numbers() []int {
	out := make([]int, len(c.Messages))
	for i, m := range c.Messages {
		out[i] = m.Number
	}
	return out
}

// model is the whole opened packet, arranged the way the frontend
// navigates it.
type model struct {
	path        string
	bbsName     string
	packetTime  time.Time
	caller      string
	qwke        bool
	conferences []Conference
	// welcome is the packet's welcome screen as raw CP437/ANSI bytes,
	// nil when it carries none.
	welcome     []byte
	welcomeName string
}

// title is what the frontend shows in its header bar.
func (m model) title() string {
	if m.bbsName != "" {
		return m.bbsName
	}
	return filepath.Base(m.path)
}

func (m model) messageCount() int {
	n := 0
	for _, c := range m.conferences {
		n += len(c.Messages)
	}
	return n
}

// Source is one downloaded packet the reader shows.
type Source struct {
	Path   string
	Packet *qwk.Packet
}

// newModel arranges one opened packet for display.
func newModel(path string, p *qwk.Packet) model {
	return mergeModel([]Source{{Path: path, Packet: p}})
}

// mergeModel arranges packets for display as one: messages grouped by
// conference, conferences sorted by number, names taken from
// CONTROL.DAT where it has them. sources run oldest first, so within a
// conference messages keep the order they arrived in, and the newest
// packet has the last word on the header, conference names and the
// welcome screen.
//
// Showing every downloaded packet rather than just the newest is what
// keeps unread mail from disappearing: the BBS counts a message as
// delivered once it is in a packet, so a message not read before the
// next fetch would otherwise only be reachable by opening its old
// packet by hand. A message that turns up in two packets -- the same
// file downloaded twice, say -- is shown once.
func mergeModel(sources []Source) model {
	var m model
	byNumber := map[int]*Conference{}
	var order []int
	seen := map[string]bool{}

	for _, src := range sources {
		p := src.Packet
		m.path = src.Path
		m.bbsName = ui.DecodeField(p.Control.BBSName)
		m.packetTime = p.Control.PacketTime
		m.caller = ui.DecodeField(p.Control.CallerName)
		m.qwke = p.QWKE

		for _, pm := range p.Messages {
			n := pm.Header.Conference
			conf, ok := byNumber[n]
			if !ok {
				conf = &Conference{Number: n, Name: conferenceName(p, n)}
				byNumber[n] = conf
				order = append(order, n)
			} else if c, named := p.Conference(n); named && strings.TrimSpace(c.Name) != "" {
				conf.Name = ui.DecodeField(c.Name)
			}
			if area, ok := p.Ext.Area(n); ok && area.IsNetmail() {
				conf.Netmail = true
			}

			msg := newMessage(pm)
			key := fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%d", n, msg.Number, msg.From, msg.Subject, msg.Written.Unix())
			if seen[key] {
				continue
			}
			seen[key] = true
			conf.Messages = append(conf.Messages, msg)
		}

		if welcome, name := findWelcome(p); welcome != nil {
			m.welcome, m.welcomeName = welcome, name
		}

		// A netmail conference is listed even without mail in it: it
		// is where a new netmail is written, and a packet with nothing
		// personal in it still says where that is (TOREADER.EXT's
		// AREA ... N).
		for _, area := range p.Ext.Areas {
			if !area.IsNetmail() {
				continue
			}
			if conf, ok := byNumber[area.Number]; ok {
				conf.Netmail = true
				continue
			}
			byNumber[area.Number] = &Conference{Number: area.Number, Name: conferenceName(p, area.Number), Netmail: true}
			order = append(order, area.Number)
		}
	}

	sort.Ints(order)
	for _, n := range order {
		m.conferences = append(m.conferences, *byNumber[n])
	}
	return m
}

func newMessage(pm qwk.PackedMessage) Message {
	k, body := qwk.ParseQWKEKludges(pm.Text)
	body, routing := splitRouting(body)
	return Message{
		Number:  pm.Header.Number,
		To:      ui.DecodeField(firstNonEmpty(k.To, pm.Header.To)),
		From:    ui.DecodeField(firstNonEmpty(k.From, pm.Header.From)),
		Subject: ui.DecodeField(firstNonEmpty(k.Subject, pm.Header.Subject)),
		Written: pm.Header.Written,
		Body:    body,
		// '*' unread private, '+' read private -- the two private
		// states in the format's status byte.
		Private: pm.Header.Status == '*' || pm.Header.Status == '+',
		Routing: routing,
	}
}

// splitRouting takes the SEEN-BY/PATH block off the end of an echomail
// body -- the lines tossers read and people do not, which NullModem
// BBS leaves in its packets. PATH often carries a leading ^A; it is
// dropped here. The tearline and origin line above the block stay
// with the body, where every reader shows them.
func splitRouting(body string) (string, []string) {
	lines := strings.Split(body, "\n")
	end := len(lines)
	var routing []string
	for end > 0 {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[end-1]), "\x01"))
		upper := strings.ToUpper(line)
		switch {
		case line == "":
		case strings.HasPrefix(upper, "SEEN-BY:"), strings.HasPrefix(upper, "PATH:"):
			routing = append([]string{line}, routing...)
		default:
			return strings.Join(lines[:end], "\n"), routing
		}
		end--
	}
	return strings.Join(lines[:end], "\n"), routing
}

func conferenceName(p *qwk.Packet, n int) string {
	if c, ok := p.Conference(n); ok && strings.TrimSpace(c.Name) != "" {
		return ui.DecodeField(c.Name)
	}
	if n == 0 {
		// Conference 0 is personal/netmail by QWK convention, so it
		// is worth naming even when CONTROL.DAT stayed silent.
		return "Personal"
	}
	return "Conference " + strconv.Itoa(n)
}

// findWelcome locates the packet's welcome screen. CONTROL.DAT names
// it without an extension ("WELCOME") while the archive stores it
// with one ("WELCOME.ANS"), so each candidate is tried both ways.
func findWelcome(p *qwk.Packet) ([]byte, string) {
	var candidates []string
	for _, base := range []string{p.Control.WelcomeFile, "WELCOME", "HELLO"} {
		if base == "" {
			continue
		}
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		candidates = append(candidates, base, base+".ANS", base+".TXT", stem)
	}
	for _, c := range candidates {
		if data, err := p.ReadFile(c); err == nil && len(data) > 0 {
			return data, c
		}
	}
	return nil, ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
