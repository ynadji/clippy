package plex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const plexTV = "https://plex.tv"

// NewClientID returns a random identifier for this installation.
func NewClientID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// PIN is a pending plex.tv link request.
type PIN struct {
	ID        int       `json:"id"`
	Code      string    `json:"code"`
	AuthToken string    `json:"authToken"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func tvRequest(ctx context.Context, method, path string, clientID, token string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, plexTV+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", Product)
	req.Header.Set("X-Plex-Version", Version)
	req.Header.Set("X-Plex-Client-Identifier", clientID)
	req.Header.Set("X-Plex-Platform", "CLI")
	req.Header.Set("X-Plex-Device-Name", "clippy")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("plex.tv: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// RequestPIN starts the plex.tv/link flow.
func RequestPIN(ctx context.Context, clientID string) (*PIN, error) {
	var pin PIN
	if err := tvRequest(ctx, http.MethodPost, "/api/v2/pins", clientID, "", url.Values{"strong": {"false"}}, &pin); err != nil {
		return nil, err
	}
	return &pin, nil
}

// WaitForPIN polls until the user has entered the code, returning the token.
func WaitForPIN(ctx context.Context, clientID string, pin *PIN) (string, error) {
	deadline := pin.ExpiresAt
	if deadline.IsZero() {
		deadline = time.Now().Add(15 * time.Minute)
	}
	for {
		var cur PIN
		if err := tvRequest(ctx, http.MethodGet, fmt.Sprintf("/api/v2/pins/%d", pin.ID), clientID, "", nil, &cur); err != nil {
			return "", err
		}
		if cur.AuthToken != "" {
			return cur.AuthToken, nil
		}
		if time.Now().After(deadline) {
			return "", errors.New("plex.tv: link code expired")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Resource is a server (or client) registered to the account.
type Resource struct {
	Name             string       `json:"name"`
	Provides         string       `json:"provides"`
	ClientIdentifier string       `json:"clientIdentifier"`
	AccessToken      string       `json:"accessToken"`
	Owned            bool         `json:"owned"`
	Connections      []Connection `json:"connections"`
}

// Connection is one way to reach a Resource.
type Connection struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

// IsServer reports whether the resource is a media server.
func (r *Resource) IsServer() bool {
	for _, p := range strings.Split(r.Provides, ",") {
		if p == "server" {
			return true
		}
	}
	return false
}

// Servers lists the media servers available to the account.
func Servers(ctx context.Context, clientID, token string) ([]Resource, error) {
	var all []Resource
	q := "/api/v2/resources?includeHttps=1&includeRelay=1&includeIPv6=0"
	if err := tvRequest(ctx, http.MethodGet, q, clientID, token, nil, &all); err != nil {
		return nil, err
	}
	var servers []Resource
	for _, r := range all {
		if r.IsServer() {
			servers = append(servers, r)
		}
	}
	return servers, nil
}

// Reachable tries the resource's connections (local first) and returns the
// first base URL that answers.
func Reachable(ctx context.Context, r *Resource, token, clientID string) (string, error) {
	conns := append([]Connection(nil), r.Connections...)
	rank := func(c Connection) int {
		n := 0
		if !c.Local {
			n += 2
		}
		if c.Relay {
			n += 4
		}
		if c.Protocol != "https" {
			n++
		}
		return n
	}
	sort.SliceStable(conns, func(i, j int) bool { return rank(conns[i]) < rank(conns[j]) })
	var last error
	for _, c := range conns {
		cl := New(c.URI, token, clientID)
		cl.HTTP.Timeout = 4 * time.Second
		if _, _, err := cl.Identity(ctx); err == nil {
			return c.URI, nil
		} else {
			last = err
		}
	}
	if last == nil {
		last = errors.New("no connections listed")
	}
	return "", fmt.Errorf("could not reach %q: %w", r.Name, last)
}
