package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestProxyRanges(t *testing.T) {
	data := make([]byte, 10<<20+123)
	rand.Read(data)
	dir := t.TempDir()
	file := filepath.Join(dir, "f.bin")
	os.WriteFile(file, data, 0o644)

	var upstreamReqs int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "t" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		upstreamReqs++
		http.ServeFile(w, r, file)
	}))
	defer origin.Close()

	p, err := Start(context.Background(), origin.URL+"/library/parts/1/2/file.mp4", http.Header{"X-Plex-Token": {"t"}}, WithBlockSize(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Size() != int64(len(data)) {
		t.Fatalf("size = %d", p.Size())
	}

	get := func(rng string) ([]byte, int) {
		req, _ := http.NewRequest("GET", p.URL(), nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b, resp.StatusCode
	}
	// Many small seeks within the same region, the way mp4 demuxing behaves.
	for i := 0; i < 200; i++ {
		off := int64(3<<20) + int64(i*7919)%(1<<20)
		b, code := get("bytes=" + itoa(off) + "-" + itoa(off+4095))
		if code != http.StatusPartialContent || !bytes.Equal(b, data[off:off+4096]) {
			t.Fatalf("range at %d: code %d, %d bytes", off, code, len(b))
		}
	}
	if upstreamReqs > 4 { // 1 probe + at most 2 blocks (region straddles a boundary)
		t.Errorf("upstream requests = %d, want <= 4", upstreamReqs)
	}
	// Tail and open-ended ranges.
	if b, _ := get("bytes=" + itoa(int64(len(data)-100)) + "-"); !bytes.Equal(b, data[len(data)-100:]) {
		t.Errorf("tail range wrong (%d bytes)", len(b))
	}
	// Wrong token path is refused.
	resp, _ := http.Get("http://" + p.listener.Addr().String() + "/nope/f.mp4")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bad path: %d", resp.StatusCode)
	}
}

func itoa(n int64) string { return string(fmtInt(n)) }
func fmtInt(n int64) []byte {
	if n == 0 {
		return []byte("0")
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return b
}
