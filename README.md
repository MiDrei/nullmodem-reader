# NullModem Reader

**English** · [Deutsch](README.de.md)

A native, cross-platform QWK(E) offline reader with automatic packet
exchange against NullModem BBS. ANSI art is rendered through the same
grid the BBS itself uses.

## Dependency

The reader builds on [the kit](https://github.com/midrei/nullmodem-kit),
the foundation it shares with NullModem BBS: `ansi` (grid, CP437, SGR,
layout, templates), `qwk` (the QWK/QWKE format layer) and `zmodem`.
Server and reader have to agree on file format and screen rendering; two
copies of the same code drift apart from the first bug fix on, and with
QWK you only notice when someone's mail goes missing.

### Packages in the reader

| Package | Role |
|---|---|
| `internal/app` | **The whole interface.** Knows neither terminal nor window: keys in, `ansi.Grid` out |
| `internal/tui` | Terminal front end -- paints the grid into tcell cells |
| `internal/gui` | Window front end -- paints the same grid with a CP437 bitmap font |
| `internal/exchange` | One mail exchange: send, then fetch -- plus the lock that keeps two from running at once |
| `internal/sched` | Unattended polling: interval, jitter, backoff |
| `internal/xfer`, `store`, `compose`, `ui`, `config` | Transport, queue and read pointers, composing, grid preparation, configuration |

## The grid

`ansi.Grid` is the only representation of screen content; everything
else is a blitter over it:

| Blitter | Target |
|---|---|
| `ansi.Grid.Encode` (from the BBS) | a terminal that speaks CP437 natively (SyncTERM, PuTTY over Telnet) |
| `ansi.ToHTML` (from the BBS) | web preview |
| `ui.GridToTerminal` | a modern UTF-8 terminal, line by line |
| `tui.present` | tcell full screen, cell by cell |
| `gui.paint` | a window, CP437 bitmap font via `Cell.Char` |
| `gui.Rasterize` | PNG -- the same picture as the GUI, without a window |

Not only the art takes this path but **the whole interface**: lists,
headers, the status bar and the input form are drawn into a grid. A
front end is thus a blitter and an input source, not a second copy of
the interface -- which is why terminal and window can't drift apart.

The UTF-8 blitter is needed because raw CP437 bytes arrive as mojibake
in a UTF-8 terminal. Going through the grid also resolves
cursor-addressed art that isn't drawn in character order.

## Status

**Done and tested**

- The QWK/QWKE format layer in *both* directions -- the BBS only needed
  to write, the reader needs the reading side too: `ReadControlDAT`,
  `OpenPacket`, `ParseQWKEKludges`, `ReadToReaderEXT`, `BuildReplyPacket`
- Transport against the BBS API: login, download (204 = no new mail),
  upload, area selection
- Rendering: ANSI art, CP437 prose, ASCII-art detection, header fields
- **TUI** (tcell): conference list → message list → message, welcome
  screen, scrolling, help overlay, resize. Tested against tcell's
  simulation screen, not just looked at by hand.
- **Writing replies**: `r` replies (recipient, subject and quote filled
  in), `e` writes a new message. You write the text in the built-in
  editor (see *Writing*); set `$VISUAL`/`$EDITOR` to get your own instead
  -- key bindings, undo, spell checking. Drafts go into a queue that
  survives a restart; `nmr fetch` builds a `.REP` from it and sends it.
- **Read pointers**: unread messages are marked `•`, the conference list
  shows `3/12 msg`, a conference opens at the first unread message, `m`
  marks everything read. Kept across sessions and packets.
- **GUI** (Ebitengine): its own window with an embedded CP437 8×16 font,
  integer scaling, DOS palette. The same interface as in the terminal,
  pixel-exact -- block graphics tile seamlessly.
- **Windows touches**: a program icon (drawn by `tools/mkicon` from the
  CP437 font) and version details in `nmr.exe`, the same icon for the
  window; held arrow, page and delete keys repeat in the window
- **Setup and exchange inside the reader**: a setup dialog on first
  start, `f` exchanges mail in the background, the password in the
  system's keychain -- see *First start without a command line*
- **Scheduler**: `nmr daemon` exchanges on a schedule -- an interval per
  system, jitter against simultaneous access, exponential backoff on
  failures (capped at one hour), a file lock against double runs.
- The `nmr` command line: `open`, `gui`, `init`, `fetch`, `daemon`,
  `outbox`, `areas`, `list`, `read`, `screen`
- Builds for linux, darwin and windows, amd64 and arm64 each -- all
  without cgo, so from a single machine (see *Releases*)

**Open**

- The kit's `zmodem` is ready but unused, in case a serial link is ever
  needed.

## Installing

Ready builds are under
[Releases](https://github.com/midrei/nullmodem-reader/releases): one
archive per platform with `nmr` (or `nmr.exe`), this README, the license
and the font license, plus `SHA256SUMS` to check them
(`sha256sum -c SHA256SUMS`). `nmr version` shows which version runs.

- **Linux**: needs glibc (not Alpine/musl) -- Ebitengine loads X11 at
  run time, without cgo. `nmr gui` also needs X11 or XWayland and
  OpenGL; the terminal mode needs neither.
- **macOS**: the binaries aren't signed. After downloading, run
  `xattr -d com.apple.quarantine nmr` once, or Gatekeeper blocks it.
- **Windows**: double-clicking `nmr.exe` opens the window. On first
  start it asks for the BBS address, user name and password, checks the
  login and fetches the first mail right away. From a command prompt,
  all the commands below work as well.

  On first start Windows SmartScreen warns ("Windows protected your
  PC") because `nmr.exe` is unsigned and new. Either choose "More info"
  → "Run anyway", or before unpacking right-click the ZIP → Properties →
  tick "Unblock". Only needed once.

### First start without a command line

`nmr gui` (on Windows also the double-click) and `nmr open` need no
packet as argument: with nothing set up, the setup dialog appears; set
up but nothing downloaded yet, the reader fetches mail at once;
otherwise it shows all downloaded packets together as one view. What
was unread before an exchange stays that way until it's read -- the BBS
delivers a message only once.

### Writing

Enter in the form opens the built-in editor in the same window: arrows,
Home/End, Page Up/Down; paragraphs wrap at the window's edge and at 79
columns when saved. In a reply the quote is already there (without the
original's tearline and origin), the cursor below it. `Ctrl+Y` deletes
the whole line -- held down, one after the other, so a quote is
trimmed quickly. `Ctrl+S` puts the message in the queue, `Esc` asks
before discarding. The reader adds the tagline and tearline itself.

An echomail's SEEN-BY and PATH lines (NullModem BBS includes them from
0.26.2) are hidden; `S` in the message shows them. In a reply, `Ctrl+R`
inserts them as a quote (`BT> SEEN-BY: …`) -- quoted, because a bare
`SEEN-BY:` line in the text would be taken for real routing by any
tosser.

Paste works everywhere with `Ctrl+V` (`Cmd+V` on a Mac), multi-line in
the editor, the first line in fields. In the window the reader reads
the clipboard itself -- directly on Windows, via `pbpaste` on a Mac, and
via `wl-paste`, `xclip` or `xsel` on Linux, whichever is installed. In a
terminal, the terminal's own paste works too.

With `VISUAL` or `EDITOR` set, the reader uses that editor instead of
the built-in one, as before.

### Netmail

`N` writes a new netmail from anywhere; the netmail conference
"Personal" is in the list even without mail (NullModem BBS marks it in
the packet since 0.26.0). The form asks for a name and an address: an
empty address means someone on this BBS, otherwise an FTN address like
`2:301/1.5` -- the reader then sends `Name@2:301/1.5`. Netmail from
another system shows its sender in the same form, so a reply finds its
way back.

If the BBS refuses a message (unknown recipient, no write access, …),
it stays in the queue with the reason (`o`, marked `!`) and isn't sent
again: `e` opens it for correction, after which it goes out with the
next exchange; `d` discards it.

### Taglines

New messages and replies get a tagline if you like, as `... text`
right above the tearline (`--- NullModem Reader/nmr <version>`). The
"Tagline" row in the form chooses with ←/→: none, random (space draws
another) or a specific one. The choice is the default for the next
message.

About 20 taglines are built in. Your own go in `taglines.txt` next to
the configuration file (`%APPDATA%\nmr\` on Windows), one per line,
`#` starts a comment; more than 75 characters don't fit on a line and
are skipped.

Cleanup is set in the setup dialog under "Keep days" (`keep_days` in
the configuration, per BBS): a packet in which everything is read is
deleted after that many days, and so are sent reply packets; the newest
packet always stays. 0 keeps everything -- as does a configuration
without the entry. A new setup suggests 30 days. Cleanup runs at start
and after every exchange, also with `nmr fetch` and `nmr daemon`. To
change something in the dialog, the password field may stay empty when
one is already saved.

Read pointers are tied to the message number. NullModem BBS uses the
database ID for it from version 0.25.1, the same in every packet;
before that every packet started at 1, so new mail could appear read.
Read pointers from that time are dropped once on first start.

Inside the reader, `f` exchanges mail -- sends replies, fetches new
mail, switches to the new packet -- without freezing the window. `s`
opens the setup again, say after a password change. If the BBS refuses
the login, the reader takes you there by itself.

The password goes into the system's password store (Windows Credential
Manager, macOS Keychain, Secret Service on Linux). Where there is none,
the dialog writes it into the configuration file and says so.
`NMR_PASSWORD_<ID>` in the environment still takes precedence.

Or from source: `go install github.com/midrei/nullmodem-reader/cmd/nmr@latest`.

## Releases

```
DRY_RUN=1 scripts/release.sh v0.2.0   # test and build into dist/, nothing else
GITHUB_TOKEN=… scripts/release.sh v0.2.0    # REMOTE=github if origin isn't GitHub
```

The script checks that `main` is clean and in sync with `origin` and
that `go.mod` has no `replace`, runs `go vet` and the tests, builds all
six targets with `GOWORK=off` -- that is, against the kit version in
`go.mod`, not a local checkout -- and packs them with `SHA256SUMS`. Only
then does it set the tag, push it and create the release with the
archives on GitHub. `GITHUB_TOKEN` is a personal access token allowed
to write the repository's contents (fine-grained: *Contents*
read/write).

If the reader needs a new kit version, that comes first -- see
*Releases* in the kit's README.

## Trying it out

```
go run ./tools/mkfixture testdata/SAMPLE.QWK
go run ./cmd/nmr open testdata/SAMPLE.QWK     # in the terminal
go run ./cmd/nmr gui  testdata/SAMPLE.QWK     # in its own window
```

Keys: `↑↓`/`jk` move, `Enter` opens, `Esc`/`q` back, `w` welcome
screen, `n`/`p` next/previous message (after a conference's last one
back to the list, on the next one with unread mail), `space` pages and
at the end moves on to the next message, `r` reply, `e` new message,
`N` new netmail, `m` mark conference read, `o` queue, `f` exchange
mail, `s` setup, `?` help, `Q` quit.

Without full screen:

```
go run ./cmd/nmr list   testdata/SAMPLE.QWK
go run ./cmd/nmr screen testdata/SAMPLE.QWK
go run ./cmd/nmr read   testdata/SAMPLE.QWK
```

Against a real BBS:

```
go run ./cmd/nmr gui           # setup dialog, then the first mail
```

or with no window at all:

```
go run ./cmd/nmr init          # writes an example configuration
export NMR_PASSWORD_NULLMDM=…  # the password not in the file
go run ./cmd/nmr fetch
go run ./cmd/nmr open            # all fetched packets together
```

Automatically in the background, with `poll:` per system in the
configuration:

```
go run ./cmd/nmr daemon
```

The daemon exchanges right at start and then at its interval. Running
`nmr fetch` by hand alongside is safe: both take the same lock, the
second says so and does nothing.

## Found along the way

`withQWKEKludges` in NullModem BBS wrote `TO:`/`FROM:`/`SUBJ:` into the
message text. Those, however, are the labels of the *header fields* in
the QWKE 1.02 specification's example; the kludges themselves are
`To:`, `From:`, `Subject:`. No reader true to the specification
(MultiMail, NoCarrierMail) would have recognised the old lines -- it
would have shown the header fields cut to 25 bytes, and the kludge
lines as text on top.

That's fixed in the kit. `ParseQWKEKludges` still accepts both spellings
when *reading*, so packets from older NullModem versions stay readable.

Second, `EncodeCP437` silently replaced everything unknown with `?`. A
Mac keyboard produces typographic quotes, dashes and ellipses by itself
-- they became question marks without anyone noticing. Now the
function transliterates them to ASCII, and `EncodeCP437Report` names
what really couldn't be carried over.

## Font

The GUI embeds a CP437 8×16 bitmap font made from
[Spleen](https://github.com/fcambus/spleen) (BSD-2-Clause, copyright
Frederic Cambus -- license text in `assets/font/LICENSE.spleen`).
`tools/mkfont` turns Spleen's CP437 BDF into the flat VGA ROM format, so
it stays traceable where the 4096 bytes come from:

```
go run ./tools/mkfont spleen-8x16-ibm-437.bdf assets/font/cp437-8x16.bin
```

An outline font wouldn't do here: block graphics must tile seamlessly
at every scale, and only a bitmap font with integer scaling can promise
that.

## License

MIT -- see [LICENSE](LICENSE). The embedded Spleen font is under
BSD-2-Clause (`assets/font/LICENSE.spleen`); both licenses are in every
release archive.
