// Package xfer speaks NullModem BBS's web API: log in, pull the
// caller's QWK packet, push a .REP back, and read or change which
// message areas the packets cover.
//
// Every request goes over the same endpoints the BBS's own web portal
// uses, so a packet fetched here is byte-identical to one downloaded
// in a browser -- including the server-side read-state bookkeeping
// that a download commits.
package xfer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client is a logged-in (or about-to-be) connection to one BBS.
// It is safe to reuse across exchanges; the token is refreshed as
// needed. It is not safe for concurrent use.
type Client struct {
	BaseURL  string
	Username string
	// HTTP may be replaced to control timeouts, proxies or TLS. A nil
	// value means DefaultHTTPClient.
	HTTP *http.Client

	token     string
	tokenTill time.Time
}

// DefaultHTTPClient has a timeout generous enough for a large packet
// on a slow link but finite, so a hung server can't wedge an
// unattended scheduled exchange forever.
var DefaultHTTPClient = &http.Client{Timeout: 10 * time.Minute}

// New returns a client for the BBS at baseURL.
func New(baseURL, username string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Username: username}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return DefaultHTTPClient
}

// Login exchanges username/password for a bearer token.
//
// The password is used for this one request and never stored on the
// Client, so it cannot leak into a later error message or a dump of
// the struct; callers holding it are expected to be equally careful.
func (c *Client) Login(ctx context.Context, password string) error {
	body, err := json.Marshal(map[string]string{
		"username": c.Username,
		"password": password,
	})
	if err != nil {
		return fmt.Errorf("xfer: encoding login request: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/api/bbs/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("xfer: logging in to %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return err
	}

	var out struct {
		Token     string    `json:"token"`
		Username  string    `json:"username"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("xfer: decoding login response: %w", err)
	}
	if out.Token == "" {
		return fmt.Errorf("xfer: %s returned no token", c.BaseURL)
	}
	c.token, c.tokenTill = out.Token, out.ExpiresAt
	return nil
}

// LoggedIn reports whether the client holds a token that is still
// valid, with a minute of slack so an exchange that starts now does
// not expire mid-upload.
func (c *Client) LoggedIn() bool {
	return c.token != "" && (c.tokenTill.IsZero() || time.Now().Add(time.Minute).Before(c.tokenTill))
}

// Area is one message area as the BBS reports it. Selected reflects
// the server's own "an empty selection means everything" default, so
// a fresh account shows all areas selected rather than none.
type Area struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Selected    bool   `json:"selected"`
}

// Areas lists the message areas the caller may read, and which of
// them their QWK packets currently cover.
func (c *Client) Areas(ctx context.Context) ([]Area, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/bbs/qwk/areas", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var areas []Area
	if err := json.NewDecoder(resp.Body).Decode(&areas); err != nil {
		return nil, fmt.Errorf("xfer: decoding area list: %w", err)
	}
	return areas, nil
}

// SetAreas replaces the caller's QWK area selection. Passing every
// readable area's ID is equivalent to passing none: the server treats
// a full selection as "no explicit selection".
func (c *Client) SetAreas(ctx context.Context, ids []int64) error {
	if ids == nil {
		ids = []int64{}
	}
	body, err := json.Marshal(map[string][]int64{"area_ids": ids})
	if err != nil {
		return fmt.Errorf("xfer: encoding area selection: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPut, "/api/bbs/qwk/areas", bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expectStatus(resp, http.StatusNoContent)
}

// ErrNoNewMail reports that the BBS had nothing to send -- it answers
// 204 in that case specifically so a client can branch on it without
// unpacking an empty archive. It is an expected outcome of a
// scheduled exchange, not a failure.
var ErrNoNewMail = fmt.Errorf("xfer: no new mail")

// DownloadQWK fetches the caller's current packet into dir and
// returns the path it was written to. The filename comes from the
// server's Content-Disposition (conventionally <BBSID>.QWK), with a
// fallback so a server that omits the header still yields a usable
// name -- and it is stripped to its base, since a filename arriving
// over the network must never be able to steer the write out of dir.
//
// It returns ErrNoNewMail when the BBS answers 204. The download is
// written to a temporary file and renamed into place only once it is
// complete, so an interrupted transfer cannot leave a half-written
// packet looking like a whole one.
func (c *Client) DownloadQWK(ctx context.Context, dir string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/bbs/qwk/download", nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return "", ErrNoNewMail
	}
	if err := expectStatus(resp, http.StatusOK); err != nil {
		return "", err
	}

	name := packetFilename(resp.Header.Get("Content-Disposition"), c.Username)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("xfer: creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", fmt.Errorf("xfer: creating temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("xfer: downloading packet: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("xfer: writing packet: %w", err)
	}

	dest := filepath.Join(dir, name)
	if err := os.Rename(tmpName, dest); err != nil {
		return "", fmt.Errorf("xfer: storing packet as %s: %w", dest, err)
	}
	return dest, nil
}

// UploadREP posts a .REP reply packet. The server routes its messages
// into the right areas and answers 204.
func (c *Client) UploadREP(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("xfer: opening %s: %w", path, err)
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return fmt.Errorf("xfer: building upload: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return fmt.Errorf("xfer: reading %s: %w", path, err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("xfer: finishing upload: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, "/api/bbs/qwk/upload", &body, mw.FormDataContentType())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expectStatus(resp, http.StatusNoContent, http.StatusOK)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("xfer: no base URL configured")
	}
	if _, err := url.Parse(c.BaseURL); err != nil {
		return nil, fmt.Errorf("xfer: invalid base URL %q: %w", c.BaseURL, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("xfer: building %s %s: %w", method, path, err)
	}
	return req, nil
}

// do issues an authenticated request. It refuses rather than sending
// an anonymous request when there is no token, so a caller who forgot
// to log in gets a clear error instead of a puzzling 401 from the
// far end.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if c.token == "" {
		return nil, fmt.Errorf("xfer: %s %s: not logged in", method, path)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("xfer: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// expectStatus turns an unexpected status into an error carrying the
// server's own message. The API reports failures as {"error": "..."},
// which is far more useful to show the user than a bare status code.
func expectStatus(resp *http.Response, want ...int) error {
	for _, w := range want {
		if resp.StatusCode == w {
			return nil
		}
	}
	var payload struct {
		Error string `json:"error"`
	}
	// Bounded: an error body is a short JSON object, and a server
	// misbehaving badly enough to stream megabytes here should not
	// cost us memory on top of it.
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_ = json.Unmarshal(data, &payload)
	if payload.Error != "" {
		return fmt.Errorf("xfer: %s: %s (HTTP %d)", resp.Request.URL.Path, payload.Error, resp.StatusCode)
	}
	return fmt.Errorf("xfer: %s: unexpected HTTP %d", resp.Request.URL.Path, resp.StatusCode)
}

// packetFilename picks the name to save a downloaded packet under,
// from the server's Content-Disposition when it proposes something
// plausible and from the username otherwise.
//
// The proposed name is taken only when it is already a plain
// filename. Reducing a path to its base instead would be safe as far
// as the write goes -- "../../etc/passwd" becomes "passwd", still
// inside the download directory -- but a legitimate server never
// sends a path here at all, so one arriving is a sign something is
// wrong rather than something to quietly salvage. Falling back to a
// predictable name keeps the download somewhere the user can find it
// and keeps a hostile server from choosing names in our directory.
func packetFilename(contentDisposition, fallback string) string {
	if contentDisposition != "" {
		if _, params, err := mime.ParseMediaType(contentDisposition); err == nil {
			if name := strings.TrimSpace(params["filename"]); isPlainFilename(name) {
				return name
			}
		}
	}
	if fallback == "" {
		fallback = "packet"
	}
	return strings.ToUpper(fallback) + ".QWK"
}

// isPlainFilename reports whether name is a bare filename: no
// separators of either flavour (a Windows-style "..\\" must be
// rejected on Unix too, since the server's platform is not ours), no
// "." or ".." and not empty.
func isPlainFilename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}
