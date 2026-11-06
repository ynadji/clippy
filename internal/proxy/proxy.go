// Package proxy serves a remote HTTP file to ffmpeg from localhost, fetching
// it from the origin in large blocks and caching them. Demuxers such as mp4
// seek constantly between small audio and video chunks; against a remote
// server every seek is a new request, which makes even a short clip crawl.
// Against this proxy those seeks are served from memory.
package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache is a block cache over one remote file, usable as an io.ReaderAt.
// This is what the libav engine reads through in-process; Proxy wraps it in
// an HTTP server for the ffmpeg binary.
type Cache struct {
	upstream  string
	headers   http.Header
	client    *http.Client
	blockSize int64
	maxBlocks int

	size int64
	name string

	mu     sync.Mutex
	blocks map[int64][]byte
	order  []int64 // insertion order, for eviction

	// Stats
	Fetches int
	Bytes   int64
}

// Proxy serves a Cache to ffmpeg over localhost HTTP.
type Proxy struct {
	*Cache
	listener net.Listener
	server   *http.Server
	token    string
}

// Option configures a Cache.
type Option func(*Cache)

// WithBlockSize sets the fetch granularity (default 4 MiB).
func WithBlockSize(n int64) Option { return func(p *Cache) { p.blockSize = n } }

// WithMaxBlocks caps the cache (default 128 blocks, 512 MiB at 4 MiB each).
func WithMaxBlocks(n int) Option { return func(p *Cache) { p.maxBlocks = n } }

// NewCache probes the upstream file for its size and returns a cache over it.
func NewCache(ctx context.Context, upstream string, headers http.Header, opts ...Option) (*Cache, error) {
	p := &Cache{
		upstream:  upstream,
		headers:   headers,
		client:    &http.Client{Timeout: 5 * time.Minute},
		blockSize: 4 << 20,
		maxBlocks: 128,
		blocks:    map[int64][]byte{},
	}
	for _, o := range opts {
		o(p)
	}
	p.name = path.Base(strings.SplitN(upstream, "?", 2)[0])
	if p.name == "" || p.name == "." || p.name == "/" {
		p.name = "media"
	}
	size, err := p.probeSize(ctx)
	if err != nil {
		return nil, err
	}
	p.size = size
	return p, nil
}

// Stats returns how many upstream requests were made and bytes fetched.
func (p *Cache) Stats() (fetches int, bytes int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Fetches, p.Bytes
}

// Name is the file's base name, useful for format probing.
func (p *Cache) Name() string { return p.name }

// Size is the upstream file size.
func (p *Cache) Size() int64 { return p.size }

// Start probes the upstream file for its size and begins serving it on a
// random localhost port. Call Close when done.
func Start(ctx context.Context, upstream string, headers http.Header, opts ...Option) (*Proxy, error) {
	c, err := NewCache(ctx, upstream, headers, opts...)
	if err != nil {
		return nil, err
	}
	p := &Proxy{Cache: c}
	tok := make([]byte, 8)
	_, _ = rand.Read(tok)
	p.token = hex.EncodeToString(tok)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.listener = ln
	p.server = &http.Server{Handler: p}
	go p.server.Serve(ln)
	return p, nil
}

// URL is the localhost address ffmpeg should read from.
func (p *Proxy) URL() string {
	return fmt.Sprintf("http://%s/%s/%s", p.listener.Addr(), p.token, p.name)
}

// Close stops the server.
func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

func (p *Cache) newRequest(ctx context.Context, from, to int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.upstream, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range p.headers {
		req.Header[k] = v
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	return req, nil
}

// probeSize asks for the first byte and reads the total from Content-Range.
func (p *Cache) probeSize(ctx context.Context) (int64, error) {
	req, err := p.newRequest(ctx, 0, 0)
	if err != nil {
		return 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.Header.Get("Content-Range")
		_, total, ok := strings.Cut(cr, "/")
		if !ok {
			return 0, fmt.Errorf("proxy: bad Content-Range %q", cr)
		}
		n, err := strconv.ParseInt(total, 10, 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("proxy: bad Content-Range %q", cr)
		}
		return n, nil
	case http.StatusOK:
		if resp.ContentLength > 0 {
			return resp.ContentLength, nil
		}
		return 0, errors.New("proxy: server does not support range requests")
	case http.StatusUnauthorized, http.StatusForbidden:
		return 0, fmt.Errorf("proxy: %s (bad token?)", resp.Status)
	default:
		return 0, fmt.Errorf("proxy: %s", resp.Status)
	}
}

// block returns the cached block starting at off, fetching it if needed.
// Called with p.mu held.
func (p *Cache) block(ctx context.Context, off int64) ([]byte, error) {
	if b, ok := p.blocks[off]; ok {
		return b, nil
	}
	end := off + p.blockSize - 1
	if end >= p.size {
		end = p.size - 1
	}
	req, err := p.newRequest(ctx, off, end)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("proxy: range %d-%d: %s", off, end, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, end-off+1))
	if err != nil {
		return nil, err
	}
	p.Fetches++
	p.Bytes += int64(len(b))
	for len(p.order) >= p.maxBlocks {
		delete(p.blocks, p.order[0])
		p.order = p.order[1:]
	}
	p.blocks[off] = b
	p.order = append(p.order, off)
	return b, nil
}

// ReadAt implements io.ReaderAt over the block cache.
func (p *Cache) ReadAt(buf []byte, off int64) (int, error) {
	if off >= p.size {
		return 0, io.EOF
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for n < len(buf) && off < p.size {
		start := off - off%p.blockSize
		b, err := p.block(context.Background(), start)
		if err != nil {
			return n, err
		}
		c := copy(buf[n:], b[off-start:])
		n += c
		off += int64(c)
	}
	if n < len(buf) {
		return n, io.EOF
	}
	return n, nil
}

// ServeHTTP answers ffmpeg's (range) requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/"+p.token+"/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, p.name, time.Time{}, io.NewSectionReader(p, 0, p.size))
}
