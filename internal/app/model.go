package app

import (
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

// newModel arranges an opened packet for display: messages grouped by
// conference in packet order, conferences sorted by number, names
// taken from CONTROL.DAT where it has them.
func newModel(path string, p *qwk.Packet) model {
	m := model{
		path:       path,
		bbsName:    ui.DecodeField(p.Control.BBSName),
		packetTime: p.Control.PacketTime,
		caller:     ui.DecodeField(p.Control.CallerName),
		qwke:       p.QWKE,
	}

	byNumber := map[int]*Conference{}
	var order []int
	for _, pm := range p.Messages {
		n := pm.Header.Conference
		conf, ok := byNumber[n]
		if !ok {
			conf = &Conference{Number: n, Name: conferenceName(p, n)}
			if area, ok := p.Ext.Area(n); ok && area.IsNetmail() {
				conf.Netmail = true
			}
			byNumber[n] = conf
			order = append(order, n)
		}
		conf.Messages = append(conf.Messages, newMessage(pm))
	}

	sort.Ints(order)
	for _, n := range order {
		m.conferences = append(m.conferences, *byNumber[n])
	}

	m.welcome, m.welcomeName = findWelcome(p)
	return m
}

func newMessage(pm qwk.PackedMessage) Message {
	k, body := qwk.ParseQWKEKludges(pm.Text)
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
	}
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
