// Package exchange performs one mail exchange with a BBS: send what
// is queued, then fetch what is waiting.
//
// It exists as its own package because two callers need exactly the
// same behaviour -- "nmr fetch" and the scheduler -- and an exchange
// that differs between a manual run and an unattended one is a
// problem nobody would find until mail went missing.
package exchange

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midrei/nullmodem-kit/qwk"
	"github.com/midrei/nullmodem-reader/internal/compose"
	"github.com/midrei/nullmodem-reader/internal/store"
	"github.com/midrei/nullmodem-reader/internal/xfer"
)

// Dirs are the per-system directories an exchange works in.
type Dirs struct {
	// Down receives downloaded .QWK packets.
	Down string
	// Up holds the .REP packets sent, under a "sent" subdirectory.
	Up string
}

// Result reports what one exchange did.
type Result struct {
	// Sent is how many queued messages went out.
	Sent int
	// Unmapped are characters the queued messages could not carry
	// into CP437; they were sent as '?'.
	Unmapped []rune
	// PacketPath is where the downloaded packet landed, empty when
	// there was nothing to fetch.
	PacketPath string
	// Received is how many messages that packet holds.
	Received int
	// Rejected are queued messages the BBS refused; they stay in the
	// queue, held with the reason (see store.Reply.Error).
	Rejected []store.Reply
}

// NoNewMail reports whether the BBS had nothing to send.
func (r Result) NoNewMail() bool { return r.PacketPath == "" }

// Client is the part of xfer.Client an exchange uses, named as an
// interface so a test can stand in for a BBS without a server.
type Client interface {
	UploadREP(ctx context.Context, path string) (xfer.UploadResult, error)
	DownloadQWK(ctx context.Context, dir string) (string, error)
}

// Run sends the queue and fetches a packet.
//
// Sending comes first. A download commits read state on the server,
// so posting what is already written before asking for more keeps the
// two sides in step when the download then fails -- the opposite
// order would leave the replies queued behind a packet the server
// already considers delivered.
func Run(ctx context.Context, c Client, q *store.Queue, bbsID string, dirs Dirs) (Result, error) {
	var res Result

	sent, rejected, unmapped, err := send(ctx, c, q, bbsID, dirs.Up)
	res.Sent, res.Rejected, res.Unmapped = sent, rejected, unmapped
	if err != nil {
		return res, err
	}

	path, err := c.DownloadQWK(ctx, dirs.Down)
	switch {
	case errors.Is(err, xfer.ErrNoNewMail):
		return res, nil
	case err != nil:
		return res, err
	}
	res.PacketPath = path

	// Opening the packet is not just bookkeeping: a packet that
	// arrived corrupt should be reported now, while the user is
	// looking, rather than when they next try to read it.
	p, err := qwk.OpenPacket(path)
	if err != nil {
		return res, fmt.Errorf("exchange: the packet downloaded but could not be opened: %w", err)
	}
	defer p.Close()
	res.Received = len(p.Messages)

	return res, nil
}

// send builds one .REP from the queue, uploads it, and only then
// clears those entries.
//
// The order matters: clearing first would lose the messages if the
// upload failed, and a failed upload is the normal outcome of a bad
// connection. Entries are removed by the IDs that actually went into
// the packet, so a reply written while the upload was running stays
// queued for next time rather than being dropped unsent.
func send(ctx context.Context, c Client, q *store.Queue, bbsID, upDir string) (int, []store.Reply, []rune, error) {
	if q == nil {
		return 0, nil, nil, nil
	}
	all, err := q.List()
	if err != nil {
		return 0, nil, nil, err
	}
	// Held messages were refused before and would only be refused
	// again: they wait for the user to fix or discard them.
	var pending []store.Reply
	for _, r := range all {
		if !r.Held() {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return 0, nil, nil, nil
	}

	if err := os.MkdirAll(upDir, 0o755); err != nil {
		return 0, nil, nil, fmt.Errorf("exchange: creating %s: %w", upDir, err)
	}
	path := filepath.Join(upDir, strings.ToUpper(bbsID)+".REP")

	unmapped, err := compose.BuildREP(path, bbsID, pending)
	if err != nil {
		return 0, nil, unmapped, err
	}
	up, err := c.UploadREP(ctx, path)
	if err != nil {
		return 0, nil, unmapped, fmt.Errorf("exchange: uploading %d queued message(s): %w", len(pending), err)
	}

	// The BBS names refused messages by their position in the packet,
	// which is their position in pending.
	reasons := map[int]string{}
	for _, r := range up.Rejected {
		if r.Index >= 0 && r.Index < len(pending) {
			reasons[r.Index] = r.Reason
		}
	}
	var ids []string
	var rejected []store.Reply
	for i, r := range pending {
		reason, refused := reasons[i]
		if !refused {
			ids = append(ids, r.ID)
			continue
		}
		r.Error = reason
		if err := q.Update(r); err != nil {
			return len(ids), rejected, unmapped, fmt.Errorf("exchange: keeping a refused message: %w", err)
		}
		rejected = append(rejected, r)
	}
	if err := q.RemoveAll(ids); err != nil {
		return len(ids), rejected, unmapped, fmt.Errorf("exchange: the messages were sent but the queue could not be cleared: %w", err)
	}

	// Keep the packet that actually went out: if the board later
	// claims never to have received something, this is the evidence.
	if sentDir := filepath.Join(upDir, "sent"); os.MkdirAll(sentDir, 0o755) == nil {
		os.Rename(path, filepath.Join(sentDir, time.Now().UTC().Format("20060102-150405")+".REP"))
	}

	return len(ids), rejected, unmapped, nil
}
