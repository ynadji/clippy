//go:build libav

package libav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ynadji/clippy/internal/clip"
)

// fixture builds a 30s HEVC/AAC MKV with an SRT track reading "SUB N"
// during second N, using the ffmpeg binary (test-only dependency).
func fixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg binary needed to build the fixture")
	}
	dir := t.TempDir()
	var srt strings.Builder
	for i := 0; i < 30; i++ {
		srt.WriteString(strconv.Itoa(i+1) + "\n")
		srt.WriteString("00" + clip.Clock(time.Duration(i) * time.Second)[1:] + ",000 --> 00" + clip.Clock(time.Duration(i+1) * time.Second)[1:] + ",000\n")
		srt.WriteString("SUB " + strconv.Itoa(i) + "\n\n")
	}
	srtPath := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srtPath, []byte(srt.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "fixture.mkv")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=30:duration=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=30",
		"-i", srtPath, "-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx265", "-x265-params", "log-level=none", "-preset", "ultrafast", "-g", "300",
		"-c:a", "aac", "-c:s", "srt", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, b)
	}
	return out
}

func probe(t *testing.T, file string) (dur float64, streams string) {
	t.Helper()
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_name,width,height,sample_rate,channels", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if d, err := strconv.ParseFloat(strings.TrimSpace(line), 64); err == nil {
			dur = d
		}
	}
	return dur, s
}

// meanVolume returns the mean audio level in dB via the ffmpeg binary.
func meanVolume(t *testing.T, file string) float64 {
	t.Helper()
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-i", file, "-map", "0:a:0", "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "mean_volume:"); i >= 0 {
			f, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(line[i+len("mean_volume:"):], " dB")), 64)
			return f
		}
	}
	return -999
}

func TestEngineLocal(t *testing.T) {
	in := fixture(t)
	dir := t.TempDir()
	r := clip.Request{Input: in, Output: filepath.Join(dir, "local.mp4"),
		Start: 12500 * time.Millisecond, Duration: 4 * time.Second,
		AudioIndex: -1, MaxHeight: 180, Subtitle: &clip.Subtitle{Index: 2, Codec: "srt"}}
	var e Engine
	if err := e.Preflight(&r); err != nil {
		t.Fatal(err)
	}
	var last time.Duration
	if err := e.Run(context.Background(), &r, os.Stderr, func(done, total time.Duration) { last = done }); err != nil {
		t.Fatal(err)
	}
	dur, streams := probe(t, r.Output)
	t.Logf("output: %.3fs\n%s", dur, streams)
	if dur < 3.9 || dur > 4.2 {
		t.Errorf("duration = %v, want ~4s", dur)
	}
	if !strings.Contains(streams, "h264,320,180") {
		t.Errorf("video not scaled to 320x180: %s", streams)
	}
	if !strings.Contains(streams, "aac") {
		t.Errorf("no aac audio: %s", streams)
	}
	if last < 3900*time.Millisecond {
		t.Errorf("progress ended at %v", last)
	}
	if v := meanVolume(t, r.Output); v < -40 {
		t.Errorf("audio is silent (mean %.1f dB); the 440 Hz tone should be there", v)
	}
	if keep := os.Getenv("CLIPPY_KEEP"); keep != "" {
		exec.Command("cp", r.Output, keep).Run()
	}
}

func TestEngineRemote(t *testing.T) {
	in := fixture(t)
	dir := t.TempDir()
	var sawToken bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Plex-Token") == "secret" {
			sawToken = true
		}
		http.ServeFile(w, req, in)
	}))
	defer srv.Close()
	r := clip.Request{Input: srv.URL + "/library/parts/1/2/file.mkv", Headers: "X-Plex-Token: secret\r\n",
		Output: filepath.Join(dir, "remote.mp4"), Start: 5 * time.Second, Duration: 3 * time.Second, AudioIndex: 1}
	var e Engine
	if err := e.Run(context.Background(), &r, os.Stderr, nil); err != nil {
		t.Fatal(err)
	}
	if !sawToken {
		t.Error("server never saw the token header")
	}
	if dur, _ := probe(t, r.Output); dur < 2.9 || dur > 3.2 {
		t.Errorf("duration = %v, want ~3s", dur)
	}
	if v := meanVolume(t, r.Output); v < -40 {
		t.Errorf("audio is silent (mean %.1f dB)", v)
	}
}

func TestThumbnail(t *testing.T) {
	in := fixture(t)
	jpeg, err := Thumbnail(context.Background(), in, "", 15200*time.Millisecond, 320, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(jpeg) < 1000 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 {
		t.Fatalf("not a JPEG (%d bytes)", len(jpeg))
	}
	if keep := os.Getenv("CLIPPY_KEEP"); keep != "" {
		os.WriteFile(keep+".jpg", jpeg, 0o644)
	}
	t.Logf("thumbnail: %d bytes", len(jpeg))
}

// Two thumbnails and a clip on the same remote file should share one block
// cache: the upstream sees the file fetched once, not three times.
func TestSharedCache(t *testing.T) {
	in := fixture(t)
	dir := t.TempDir()
	var served int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cw := &countingWriter{ResponseWriter: w}
		http.ServeFile(cw, req, in)
		mu.Lock()
		served += cw.n
		mu.Unlock()
	}))
	defer srv.Close()
	url, hdr := srv.URL+"/library/parts/9/9/file.mkv", "X-Plex-Token: t\r\n"
	st, _ := os.Stat(in)

	var wg sync.WaitGroup
	for _, at := range []time.Duration{5 * time.Second, 8 * time.Second} {
		wg.Add(1)
		go func(at time.Duration) {
			defer wg.Done()
			if _, err := Thumbnail(context.Background(), url, hdr, at, 320, "auto"); err != nil {
				t.Error(err)
			}
		}(at)
	}
	wg.Wait()
	r := clip.Request{Input: url, Headers: hdr, Output: filepath.Join(dir, "c.mp4"), Start: 5 * time.Second, Duration: 3 * time.Second, AudioIndex: -1}
	if err := (Engine{}).Run(context.Background(), &r, nil, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// The fixture is smaller than one block, so a shared cache fetches it
	// about once (plus the 1-byte size probe). Separate caches would be 3x.
	if served > st.Size()+st.Size()/2 {
		t.Errorf("upstream served %d bytes for a %d byte file; cache is not shared", served, st.Size())
	}
}

type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}
