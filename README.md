# NullModem Reader

Nativer Cross-Platform QWK(E)-Offline-Reader mit automatischem Paketaustausch
gegen NullModem BBS. ANSI-Art wird über dieselbe Grid-Matrix dargestellt, die
die BBS selbst benutzt.

## Abhängigkeit

Der Reader hängt an
[dem Kit](https://git.maik.ch/nullmodem/kit) — dem gemeinsamen Unterbau mit
NullModem BBS: `ansi` (Grid-Matrix, CP437, SGR, Layout, Templates), `qwk`
(QWK/QWKE-Formatschicht), `zmodem`. Server und Reader müssen sich über
Dateiformat und Bildschirmdarstellung einig sein; zwei Kopien desselben Codes
driften ab dem ersten Bugfix auseinander, und beim QWK-Format merkt man das
erst, wenn jemandem Post verlorengeht.

Das Kit ist öffentlich, liegt aber auf git.maik.ch statt bei einem der
großen Hoster. Damit Go es direkt dort holt statt über den öffentlichen
Go-Proxy und die Checksum-Datenbank, einmal pro Maschine:

```
go env -w GOPRIVATE=git.maik.ch
```

`go.sum` legt die Prüfsumme trotzdem fest.

### Pakete im Reader

| Paket | Rolle |
|---|---|
| `internal/app` | **Die gesamte Oberfläche.** Kennt weder Terminal noch Fenster: Tasten rein, `ansi.Grid` raus |
| `internal/tui` | Terminal-Frontend — malt die Grid in tcell-Zellen |
| `internal/gui` | Fenster-Frontend — malt dieselbe Grid mit einem CP437-Bitmapfont |
| `internal/exchange` | Ein Mailaustausch: senden, dann holen — plus die Sperre dagegen, dass zwei gleichzeitig laufen |
| `internal/sched` | Unbeaufsichtigtes Pollen: Intervall, Jitter, Backoff |
| `internal/xfer`, `store`, `compose`, `ui`, `config` | Transport, Warteschlange und Lesezeiger, Verfassen, Grid-Aufbereitung, Konfiguration |

## Die Matrix

`ansi.Grid` ist die einzige Repräsentation von Bildschirminhalt; alles andere
ist ein Blitter darüber:

| Blitter | Ziel |
|---|---|
| `ansi.Grid.Encode` (aus der BBS) | Terminal, das CP437 nativ spricht (SyncTERM, PuTTY über Telnet) |
| `ansi.ToHTML` (aus der BBS) | Web-Vorschau |
| `ui.GridToTerminal` | modernes UTF-8-Terminal, zeilenweise Ausgabe |
| `tui.present` | tcell-Vollbild, Zelle für Zelle |
| `gui.paint` | Fenster, CP437-Bitmapfont über `Cell.Char` |
| `gui.Rasterize` | PNG — dasselbe Bild wie die GUI, nur ohne Fenster |

Nicht nur die Kunst geht diesen Weg, sondern **die ganze Oberfläche**: Listen,
Kopfzeilen, Statusleiste und Eingabeformular werden in eine Grid gezeichnet.
Ein Frontend ist damit ein Blitter und eine Eingabequelle, keine zweite Kopie
der Oberfläche — deshalb können Terminal und Fenster nicht auseinanderdriften.

Der UTF-8-Blitter ist nötig, weil rohe CP437-Bytes in einem UTF-8-Terminal als
Mojibake ankommen. Der Weg über die Matrix löst zusätzlich cursor-adressierte
Art auf, die nicht in Zeichenreihenfolge gezeichnet wird.

## Stand

**Fertig und getestet**

- QWK/QWKE-Formatschicht in *beide* Richtungen — die BBS konnte nur schreiben,
  der Reader braucht auch die Leseseite: `ReadControlDAT`, `OpenPacket`,
  `ParseQWKEKludges`, `ReadToReaderEXT`, `BuildReplyPacket`
- Transport gegen die BBS-API: Login, Download (204 = keine neue Post),
  Upload, Area-Auswahl
- Darstellung: ANSI-Art, CP437-Prosa, ASCII-Art-Erkennung, Header-Felder
- **TUI** (tcell): Konferenzliste → Nachrichtenliste → Nachricht, Welcome-Screen,
  Scrollen, Hilfe-Overlay, Resize. Gegen tcells Simulations-Screen getestet,
  nicht nur von Hand angesehen.
- **Antworten verfassen**: `r` antwortet (Empfänger, Betreff und Zitat vorbelegt),
  `e` schreibt neu. Der Nachrichtentext geht an `$VISUAL`/`$EDITOR` — deine
  Tastenbelegung, dein Undo, deine Rechtschreibprüfung. Entwürfe landen in einer
  Warteschlange, die einen Neustart übersteht; `nmr fetch` baut daraus ein `.REP`
  und sendet es.
- **Lesezeiger**: Ungelesenes ist mit `•` markiert, die Konferenzliste zeigt
  `3/12 msg`, eine Konferenz öffnet bei der ersten ungelesenen Nachricht,
  `m` markiert alles gelesen. Bleibt über Sitzungen und Pakete hinweg erhalten.
- **GUI** (Ebitengine): eigenes Fenster mit eingebettetem CP437-8×16-Font,
  ganzzahlige Skalierung, DOS-Palette. Dieselbe Oberfläche wie im Terminal,
  nur pixelgenau — Blockgrafik kachelt nahtlos.
- **Windows-Kosmetik**: Programmsymbol (von `tools/mkicon` aus dem
  CP437-Font gezeichnet) und Versionsangaben in `nmr.exe`, dasselbe Symbol
  als Fenstersymbol; gedrückt gehaltene Pfeil-, Bild- und Löschtasten
  wiederholen sich im Fenster
- **Einrichtung und Austausch im Reader**: Einrichtungsdialog beim ersten
  Start, `f` tauscht Post im Hintergrund aus, Passwort im Schlüsselbund des
  Systems — siehe *Erster Start ohne Kommandozeile*
- **Scheduler**: `nmr daemon` tauscht nach Zeitplan aus — pro System eigenes
  Intervall, Jitter gegen gleichzeitige Zugriffe, exponentielles Backoff bei
  Ausfällen (gedeckelt bei einer Stunde), Dateisperre gegen Doppelläufe.
- `nmr`-Kommandozeile: `open`, `gui`, `init`, `fetch`, `daemon`, `outbox`, `areas`, `list`, `read`, `screen`
- Baut für linux, darwin und windows, je amd64 und arm64 — alles ohne cgo,
  also von einer einzigen Maschine aus (siehe *Releases*)

**Offen**

- Einfügen aus der Zwischenablage (Strg+V) im Fenster -- Ebitengine hat
  keinen Zugriff darauf, Adresse und Passwort müssen getippt werden.
- `bbskit/zmodem` liegt ungenutzt bereit, falls die serielle Strecke doch
  einmal gebraucht wird.

## Installieren

Fertige Builds liegen unter
[Releases](https://git.maik.ch/nullmodem/reader/releases): ein Archiv pro
Plattform mit `nmr` (bzw. `nmr.exe`), dieser README und der Font-Lizenz, dazu
`SHA256SUMS` zum Prüfen (`sha256sum -c SHA256SUMS`). `nmr version` zeigt,
welche Version läuft.

- **Linux**: braucht glibc (nicht Alpine/musl) — Ebitengine lädt X11 zur
  Laufzeit, ohne cgo. Für `nmr gui` außerdem X11 bzw. XWayland und OpenGL;
  der Terminal-Modus braucht beides nicht.
- **macOS**: die Binaries sind nicht signiert. Nach dem Download einmal
  `xattr -d com.apple.quarantine nmr`, sonst blockiert Gatekeeper den Start.
- **Windows**: Doppelklick auf `nmr.exe` öffnet direkt das Fenster. Beim
  ersten Start fragt es nach BBS-Adresse, Benutzername und Passwort, prüft
  die Anmeldung und holt gleich die erste Post. Aus einer Eingabeaufforderung
  heraus funktionieren weiterhin alle Befehle unten.

  Beim ersten Start warnt Windows SmartScreen („Der Computer wurde durch
  Windows geschützt“), weil `nmr.exe` nicht signiert und neu ist. Entweder
  dort „Weitere Informationen“ → „Trotzdem ausführen“, oder vor dem
  Entpacken das ZIP rechtsklicken → Eigenschaften → „Zulassen“ anhaken.
  Nur beim ersten Start nötig.

### Erster Start ohne Kommandozeile

`nmr gui` (unter Windows auch der Doppelklick) und `nmr open` brauchen kein
Paket als Argument: Ist noch nichts eingerichtet, erscheint der
Einrichtungsdialog; ist eingerichtet, aber noch nichts heruntergeladen, holt
der Reader sofort Post; sonst öffnet er das neueste Paket.

Im Reader tauscht `f` Post aus — Antworten senden, neue Post holen, auf das
neue Paket wechseln — ohne dass das Fenster hängt. `s` öffnet die Einrichtung
erneut, etwa nach einem Passwortwechsel. Lehnt die BBS die Anmeldung ab, führt
der Reader von selbst dorthin.

Das Passwort landet im Passwortspeicher des Systems (Windows-
Anmeldeinformationsverwaltung, macOS-Schlüsselbund, Secret Service unter
Linux). Wo es keinen gibt, schreibt der Dialog es in die Konfigurationsdatei
und sagt das. `NMR_PASSWORD_<ID>` in der Umgebung hat weiterhin Vorrang.

Oder aus dem Quelltext: `go install git.maik.ch/nullmodem/reader/cmd/nmr@latest`
(mit `GOPRIVATE` wie oben).

## Releases

```
DRY_RUN=1 scripts/release.sh v0.2.0   # testen und nach dist/ bauen, sonst nichts
GITEA_TOKEN=… scripts/release.sh v0.2.0
```

Das Skript prüft, dass `main` sauber und mit `origin` gleichauf ist und
`go.mod` kein `replace` enthält, lässt `go vet` und die Tests laufen, baut
alle sechs Ziele mit `GOWORK=off` — also gegen die Kit-Version aus `go.mod`,
nicht gegen einen lokalen Checkout — und packt sie samt `SHA256SUMS`. Erst
danach setzt es den Tag, pusht ihn und legt das Release mit den Archiven auf
git.maik.ch an. `GITEA_TOKEN` ist ein persönlicher Token mit
`write:repository` (Einstellungen → Anwendungen).

Braucht der Reader eine neue Kit-Version, kommt die zuerst — siehe
*Releases* in der README des Kits.

## Ausprobieren

```
go run ./tools/mkfixture testdata/SAMPLE.QWK
go run ./cmd/nmr open testdata/SAMPLE.QWK     # im Terminal
go run ./cmd/nmr gui  testdata/SAMPLE.QWK     # im eigenen Fenster
```

Tasten: `↑↓`/`jk` bewegen, `Enter` öffnen, `Esc`/`q` zurück, `w` Welcome-Screen,
`n`/`p` nächste/vorige Nachricht, `space` blättern, `r` antworten, `e` neu
schreiben, `m` Konferenz als gelesen markieren, `o` Warteschlange, `f` Post
austauschen, `s` Einrichtung, `?` Hilfe, `Q` beenden.

Ohne Vollbild:

```
go run ./cmd/nmr list   testdata/SAMPLE.QWK
go run ./cmd/nmr screen testdata/SAMPLE.QWK
go run ./cmd/nmr read   testdata/SAMPLE.QWK
```

Gegen eine echte BBS:

```
go run ./cmd/nmr gui           # Einrichtungsdialog, dann die erste Post
```

oder ganz ohne Fenster:

```
go run ./cmd/nmr init          # schreibt eine Beispielkonfiguration
export NMR_PASSWORD_NULLMDM=…  # Passwort nicht in die Datei
go run ./cmd/nmr fetch
go run ./cmd/nmr open            # das zuletzt geholte Paket
```

Automatisch im Hintergrund, mit `poll:` je System in der Konfiguration:

```
go run ./cmd/nmr daemon
```

Der Daemon tauscht sofort beim Start aus und dann nach Intervall. `nmr fetch`
von Hand ist parallel dazu sicher: beide nehmen dieselbe Sperre, der zweite
meldet das und tut nichts.

## Funde unterwegs

`withQWKEKludges` in NullModem BBS schrieb `TO:`/`FROM:`/`SUBJ:` in den
Nachrichtentext. Das sind aber die Beschriftungen der *Header-Felder* im
Beispiel der QWKE-1.02-Spezifikation; die Kludges selbst heißen `To:`, `From:`,
`Subject:`. Kein spezifikationstreuer Reader (MultiMail, NoCarrierMail) hätte
die alten Zeilen erkannt — er hätte die auf 25 Byte gekürzten Header-Felder
angezeigt und die Kludge-Zeilen obendrein als Text.

In dem Kit ist das korrigiert. `ParseQWKEKludges` akzeptiert beim *Lesen*
weiterhin beide Schreibweisen, damit Pakete älterer NullModem-Versionen
lesbar bleiben.

Zweitens ersetzte `EncodeCP437` alles Unbekannte kommentarlos durch `?`. Eine
Mac-Tastatur liefert typografische Anführungszeichen, Gedankenstriche und
Auslassungspunkte von selbst — die wurden damit zu Fragezeichen, ohne dass
jemand es merkte. Jetzt transliteriert die Funktion sie nach ASCII, und
`EncodeCP437Report` nennt, was wirklich nicht übertragbar war.

## Schriftart

Die GUI bettet einen CP437-8×16-Bitmapfont ein, erzeugt aus
[Spleen](https://github.com/fcambus/spleen) (BSD-2-Clause, Copyright Frederic
Cambus — Lizenztext in `reader/assets/font/LICENSE.spleen`). `tools/mkfont`
wandelt Spleens CP437-BDF in das flache VGA-ROM-Format um, damit
nachvollziehbar bleibt, woher die 4096 Bytes stammen:

```
go run ./tools/mkfont spleen-8x16-ibm-437.bdf assets/font/cp437-8x16.bin
```

Ein Outline-Font käme hier nicht in Frage: Blockgrafik muss auf jeder
Skalierungsstufe nahtlos kacheln, und das kann nur ein Bitmapfont mit
ganzzahliger Vergrößerung zusagen.
