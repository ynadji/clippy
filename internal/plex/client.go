// Package plex is a minimal client for the parts of the Plex Media Server
// API that clippy needs: sessions, metadata and search.
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	Product = "clippy"
	Version = "0.1.0"
)

// Client talks to a single Plex Media Server.
type Client struct {
	URL      string
	Token    string
	ClientID string
	HTTP     *http.Client
}

// New returns a client for the server at base with the given token.
func New(base, token, clientID string) *Client {
	return &Client{
		URL:      strings.TrimRight(base, "/"),
		Token:    token,
		ClientID: clientID,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) headers(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", Product)
	req.Header.Set("X-Plex-Version", Version)
	if c.ClientID != "" {
		req.Header.Set("X-Plex-Client-Identifier", c.ClientID)
	}
	if c.Token != "" {
		req.Header.Set("X-Plex-Token", c.Token)
	}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.URL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	c.headers(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("plex: unauthorized (bad or expired token)")
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("plex: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		// Keep the raw response so a schema mismatch can be diagnosed.
		dump := filepath.Join(os.TempDir(), "clippy-response.json")
		_ = os.WriteFile(dump, body, 0o600)
		return fmt.Errorf("plex: decode %s: %w (raw response saved to %s)", path, err, dump)
	}
	return nil
}

// Download fetches a server path (such as a subtitle stream key) to a file.
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
		return fmt.Errorf("plex: GET %s: %s", path, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// FilePart returns the part that describes the file behind a session item.
// A session that is being transcoded describes the transcode, not the file,
// so the library record is fetched and matched by part id. The returned
// item is the one the part belongs to.
func (c *Client) FilePart(ctx context.Context, it *Item) (*Part, *Item, error) {
	p := it.SelectedPart()
	if p == nil {
		return nil, nil, fmt.Errorf("plex: %s has no media", it.Label())
	}
	if !p.IsTranscode() && it.Player == nil {
		return p, it, nil
	}
	full, err := c.Metadata(ctx, it.RatingKey)
	if err != nil {
		if !p.IsTranscode() {
			return p, it, nil // the session's own part will do
		}
		return nil, nil, err
	}
	fp := full.PartByID(p.ID)
	if fp == nil {
		for i := range full.Media {
			for j := range full.Media[i].Part {
				if full.Media[i].Part[j].Key == p.Key && p.Key != "" {
					fp = &full.Media[i].Part[j]
				}
			}
		}
	}
	if fp == nil {
		fp = full.SelectedPart()
	}
	if fp == nil || fp.Key == "" {
		return nil, nil, fmt.Errorf("plex: %s has no media file on the server", it.Label())
	}
	return fp, full, nil
}

// FileStream maps a stream of a session part onto the file's part by id, so
// that a transcoding session's selection resolves to a real stream index.
// Falls back to the stream itself when it is not in the file's part.
func FileStream(s *Stream, filePart *Part) *Stream {
	if s == nil {
		return nil
	}
	if filePart != nil {
		if fs := filePart.StreamByID(s.ID); fs != nil {
			return fs
		}
	}
	return s
}

// PartURL returns the URL from which ffmpeg can read the part's original file.
func (c *Client) PartURL(p *Part) string { return c.URL + p.Key }

// FFmpegHeaders returns the header block that authenticates ffmpeg's HTTP
// reads against this server.
func (c *Client) FFmpegHeaders() string { return "X-Plex-Token: " + c.Token + "\r\n" }

// ServerInfo describes the server answering at the client's URL.
type ServerInfo struct {
	Name              string `json:"friendlyName"`
	MachineIdentifier string `json:"machineIdentifier"`
	Version           string `json:"version"`
	Platform          string `json:"platform"`
}

// Identity returns the server's name and machine identifier.
func (c *Client) Identity(ctx context.Context) (name, id string, err error) {
	info, err := c.Info(ctx)
	if err != nil {
		return "", "", err
	}
	return info.Name, info.MachineIdentifier, nil
}

// Info returns the server's identity and version.
func (c *Client) Info(ctx context.Context) (*ServerInfo, error) {
	var out struct {
		MediaContainer ServerInfo `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/", nil, &out); err != nil {
		return nil, err
	}
	return &out.MediaContainer, nil
}

// Section is a library section.
type Section struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// Sections lists the library sections.
func (c *Client) Sections(ctx context.Context) ([]Section, error) {
	var out struct {
		MediaContainer struct {
			Directory []Section `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/library/sections", nil, &out); err != nil {
		return nil, err
	}
	return out.MediaContainer.Directory, nil
}

// SampleItem returns some video item from the library, for diagnostics.
func (c *Client) SampleItem(ctx context.Context) (*Item, error) {
	sections, err := c.Sections(ctx)
	if err != nil {
		return nil, err
	}
	for _, sec := range sections {
		typ := map[string]string{"movie": "1", "show": "4"}[sec.Type]
		if typ == "" {
			continue
		}
		q := url.Values{"type": {typ}, "X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {"1"}}
		var out container
		if err := c.get(ctx, "/library/sections/"+sec.Key+"/all", q, &out); err != nil {
			continue
		}
		for i := range out.MediaContainer.Metadata {
			if p := out.MediaContainer.Metadata[i].SelectedPart(); p != nil && p.Key != "" {
				return &out.MediaContainer.Metadata[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no video items found in the library")
}

// ReadRange fetches up to n bytes of a server path and reports how long it
// took, for measuring throughput.
func (c *Client) ReadRange(ctx context.Context, path string, n int64) (int64, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path, nil)
	if err != nil {
		return 0, 0, err
	}
	c.headers(req)
	req.Header.Del("Accept")
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, 0, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	got, err := io.Copy(io.Discard, io.LimitReader(resp.Body, n))
	return got, time.Since(start), err
}

// Sessions returns the currently playing sessions.
func (c *Client) Sessions(ctx context.Context) ([]Item, error) {
	var out container
	if err := c.get(ctx, "/status/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out.MediaContainer.Metadata, nil
}

// Metadata returns the full metadata for a rating key.
func (c *Client) Metadata(ctx context.Context, ratingKey string) (*Item, error) {
	var out container
	if err := c.get(ctx, "/library/metadata/"+url.PathEscape(ratingKey), nil, &out); err != nil {
		return nil, err
	}
	if len(out.MediaContainer.Metadata) == 0 {
		return nil, fmt.Errorf("plex: no item with key %s", ratingKey)
	}
	return &out.MediaContainer.Metadata[0], nil
}

// Search returns movies and episodes matching the query.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Item, error) {
	q := url.Values{"query": {query}, "limit": {fmt.Sprint(limit)}}
	var out container
	if err := c.get(ctx, "/hubs/search", q, &out); err != nil {
		return nil, err
	}
	var items []Item
	for _, h := range out.MediaContainer.Hub {
		for _, it := range h.Metadata {
			if it.IsVideo() {
				items = append(items, it)
			}
		}
	}
	return items, nil
}
