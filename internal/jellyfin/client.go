// Package jellyfin is a minimal client for the parts of the Jellyfin API
// that clippy needs: sign-in, sessions, items and media files. Emby's API
// is the same for all of them, so the client also talks to Emby servers.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	Product = "clippy"
	Version = "0.1.0"
)

// Client talks to a single Jellyfin (or Emby) server.
type Client struct {
	URL      string
	Token    string // access token from Login; empty before sign-in
	UserID   string // the signed-in user, needed for item lookups
	ClientID string // stable per-installation device id
	Device   string // device name shown in the server's dashboard
	Client   string // client name shown in the server's dashboard
	HTTP     *http.Client
}

// New returns a client for the server at base. Token and UserID come from
// Login; pass them back in for later calls.
func New(base, token, userID, clientID string) *Client {
	return &Client{
		URL:      strings.TrimRight(base, "/"),
		Token:    token,
		UserID:   userID,
		ClientID: clientID,
		Device:   Product,
		Client:   Product,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// authorization is the MediaBrowser scheme both Jellyfin and Emby accept.
// Authorization is the Authorization header value for this client.
func (c *Client) Authorization() string {
	q := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, "") + `"` }
	h := fmt.Sprintf("MediaBrowser Client=%s, Device=%s, DeviceId=%s, Version=%s",
		q(c.Client), q(c.Device), q(c.ClientID), q(Version))
	if c.Token != "" {
		h += ", Token=" + q(c.Token)
	}
	return h
}

func (c *Client) headers(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", c.Authorization())
	// Emby only reads the legacy header.
	req.Header.Set("X-Emby-Authorization", c.Authorization())
}

// FFmpegHeaders is the header block to hand to ffmpeg/libav for media URLs.
func (c *Client) FFmpegHeaders() string {
	return "Authorization: " + c.Authorization() + "\r\nX-Emby-Authorization: " + c.Authorization() + "\r\n"
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.URL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	c.headers(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("jellyfin: unauthorized (bad or expired token)")
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("jellyfin: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("jellyfin: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, q, nil, out)
}

// PublicInfo asks the server who it is. It needs no token, so it is the way
// to check an address the user typed.
func (c *Client) PublicInfo(ctx context.Context) (*PublicInfo, error) {
	var info PublicInfo
	if err := c.get(ctx, "/System/Info/Public", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Login signs in with a username and password and stores the resulting
// token and user id on the client.
func (c *Client) Login(ctx context.Context, username, password string) (*AuthResult, error) {
	var res AuthResult
	body := map[string]string{"Username": username, "Pw": password}
	if err := c.do(ctx, http.MethodPost, "/Users/AuthenticateByName", nil, body, &res); err != nil {
		if strings.Contains(err.Error(), "unauthorized") {
			return nil, errors.New("jellyfin: wrong username or password")
		}
		return nil, err
	}
	if res.AccessToken == "" {
		return nil, errors.New("jellyfin: sign-in returned no token")
	}
	c.Token, c.UserID = res.AccessToken, res.User.ID
	return &res, nil
}

// Sessions lists the server's active sessions. Ones with nothing playing
// are included; callers filter on NowPlayingItem.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	if err := c.get(ctx, "/Sessions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Item fetches a library item with its media sources and streams.
func (c *Client) Item(ctx context.Context, id string) (*Item, error) {
	var it Item
	// The per-user route works on every Jellyfin and Emby version; the
	// newer /Items/{id}?userId= does not exist on Emby or old Jellyfin.
	p := "/Items/" + url.PathEscape(id)
	if c.UserID != "" {
		p = "/Users/" + url.PathEscape(c.UserID) + p
	}
	if err := c.get(ctx, p, nil, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// MediaPath is the server path that serves a media source's file as is,
// with range requests. Append it to the client URL.
func MediaPath(itemID, sourceID string) string {
	q := url.Values{"static": {"true"}}
	if sourceID != "" {
		q.Set("mediaSourceId", sourceID)
	}
	return "/Videos/" + url.PathEscape(itemID) + "/stream?" + q.Encode()
}

// SubtitlePath is the server path of an external subtitle stream, in the
// format the codec implies (ass stays ass, everything else becomes srt).
func SubtitlePath(itemID, sourceID string, st *MediaStream) string {
	format := "srt"
	switch strings.ToLower(st.Codec) {
	case "ass", "ssa":
		format = "ass"
	case "webvtt", "vtt":
		format = "vtt"
	}
	return fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/Stream.%s", url.PathEscape(itemID), url.PathEscape(sourceID), st.Index, format)
}

// Download fetches a server path to a file.
func (c *Client) Download(ctx context.Context, path, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path, nil)
	if err != nil {
		return err
	}
	c.headers(req)
	req.Header.Del("Accept")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("jellyfin: GET %s: %s", path, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// Candidates expands an address the user typed into base URLs to try, in
// order. A full URL is used as is; a bare host is tried over https on the
// default port (how servers behind a reverse proxy are reached), then over
// http on Jellyfin's own port, then plain http.
func Candidates(addr string) []string {
	a := strings.TrimRight(strings.TrimSpace(addr), "/")
	if a == "" {
		return nil
	}
	if strings.Contains(a, "://") {
		return []string{a}
	}
	if strings.Contains(a, ":") {
		return []string{"http://" + a, "https://" + a}
	}
	return []string{"https://" + a, "http://" + a + ":8096", "http://" + a}
}

// Probe finds the first candidate base URL for addr that answers as a
// Jellyfin or Emby server, and returns it with the server's public info.
func Probe(ctx context.Context, addr string) (string, *PublicInfo, error) {
	var last error
	for _, base := range Candidates(addr) {
		c := New(base, "", "", "")
		c.HTTP.Timeout = 6 * time.Second
		info, err := c.PublicInfo(ctx)
		if err == nil && info.ID != "" {
			return base, info, nil
		}
		if err == nil {
			err = errors.New("not a Jellyfin or Emby server")
		}
		last = fmt.Errorf("%s: %w", base, err)
	}
	if last == nil {
		last = errors.New("no server address")
	}
	return "", nil, last
}
