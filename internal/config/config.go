// Package config loads and saves clippy's configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config is the on-disk configuration, stored as JSON.
type Config struct {
	URL        string `json:"url"`
	Token      string `json:"token"`
	ClientID   string `json:"client_id"`
	ServerName string `json:"server_name,omitempty"`
	FFmpeg     string `json:"ffmpeg,omitempty"`
	OutputDir  string `json:"output_dir,omitempty"`
	// PathMap rewrites file paths reported by Plex into paths valid on this
	// machine, e.g. {"/data/media": "/Volumes/media"}. Longest prefix wins.
	// When an entry matches and the file exists, clippy reads it directly
	// instead of streaming it from the server over HTTP.
	PathMap map[string]string `json:"path_map,omitempty"`
}

// Path returns the config file location, honouring CLIPPY_CONFIG and
// XDG_CONFIG_HOME.
func Path() string {
	if p := os.Getenv("CLIPPY_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "clippy", "config.json")
}

// Load reads the config file and applies environment overrides
// (CLIPPY_PLEX_URL, CLIPPY_PLEX_TOKEN). A missing file is not an error.
func Load() (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(Path())
	switch {
	case err == nil:
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", Path(), err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	if v := os.Getenv("CLIPPY_PLEX_URL"); v != "" {
		cfg.URL = v
	}
	if v := os.Getenv("CLIPPY_PLEX_TOKEN"); v != "" {
		cfg.Token = v
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return cfg, nil
}

// Save writes the config with owner-only permissions.
func Save(cfg *Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

// Ready reports whether the config has enough to talk to a server.
func (c *Config) Ready() error {
	if c.URL == "" || c.Token == "" {
		return fmt.Errorf("no Plex server configured: run `clippy login` or set CLIPPY_PLEX_URL and CLIPPY_PLEX_TOKEN")
	}
	return nil
}

// MapPath rewrites a server-side path using PathMap (longest prefix wins)
// and reports whether any entry matched.
func (c *Config) MapPath(p string) (string, bool) {
	if len(c.PathMap) == 0 {
		return p, false
	}
	keys := make([]string, 0, len(c.PathMap))
	for k := range c.PathMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if p == k || strings.HasPrefix(p, strings.TrimRight(k, "/")+"/") {
			return c.PathMap[k] + strings.TrimPrefix(p, strings.TrimRight(k, "/")), true
		}
	}
	return p, false
}

// Redacted returns a copy safe for printing.
func (c *Config) Redacted() *Config {
	out := *c
	if len(out.Token) > 4 {
		out.Token = out.Token[:4] + strings.Repeat("*", len(out.Token)-4)
	}
	return &out
}
