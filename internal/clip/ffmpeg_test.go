package clip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildFixture makes a 30s HEVC/AAC MKV with an SRT track that shows "SUB N"
// during second N.
func ffmpegBin() string {
	if v := os.Getenv("CLIPPY_FFMPEG"); v != "" {
		return v
	}
	return "ffmpeg"
}

func buildFixture(t *testing.T, dir string) string {
	t.Helper()
	var srt strings.Builder
	for i := 0; i < 30; i++ {
		srt.WriteString(strconv.Itoa(i+1) + "\n")
		srt.WriteString(Clock(time.Duration(i)*time.Second) + ",000 --> " + Clock(time.Duration(i+1)*time.Second) + ",000\n")
		srt.WriteString("SUB " + strconv.Itoa(i) + "\n\n")
	}
	srtPath := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srtPath, []byte(strings.ReplaceAll(srt.String(), "0:", "00:")), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "fixture.mkv")
	cmd := exec.Command(ffmpegBin(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=30",
		"-i", srtPath, "-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx265", "-x265-params", "log-level=none", "-preset", "ultrafast", "-g", "300",
		"-c:a", "aac", "-c:s", "srt", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, b)
	}
	return out
}

func probeDuration(t *testing.T, file string) float64 {
	t.Helper()
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFFmpegPipeline(t *testing.T) {
	if _, err := exec.LookPath(ffmpegBin()); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if !HasFilter(ffmpegBin(), "subtitles") {
		t.Skip("ffmpeg lacks the subtitles filter (no libass); set CLIPPY_FFMPEG to a full build")
	}
	dir := t.TempDir()
	in := buildFixture(t, dir)
	ctx := context.Background()

	// Re-encode with burned-in subtitles and a downscale.
	r := Request{Input: in, Output: filepath.Join(dir, "sub.mp4"), Start: 12500 * time.Millisecond, Duration: 4 * time.Second,
		AudioIndex: 1, Subtitle: &Subtitle{Index: 2, Codec: "srt"}, MaxHeight: 90, CRF: 30, FFmpeg: ffmpegBin()}
	p, err := r.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Run(ctx, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if d := probeDuration(t, r.Output); d < 3.9 || d > 4.2 {
		t.Errorf("encoded clip duration = %v, want ~4s", d)
	}

	// Same clip read over HTTP with a token header, as from a Plex server.
	var sawToken bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Plex-Token") == "secret" {
			sawToken = true
		}
		http.ServeFile(w, req, in) // supports Range requests
	}))
	defer srv.Close()
	r = Request{Input: srv.URL + "/library/parts/1/2/file.mkv", Headers: "X-Plex-Token: secret\r\n",
		Output: filepath.Join(dir, "remote.mp4"), Start: 12500 * time.Millisecond, Duration: 4 * time.Second,
		AudioIndex: 1, Subtitle: &Subtitle{Index: 2, Codec: "srt"}, MaxHeight: 90, CRF: 30, FFmpeg: ffmpegBin()}
	p, err = r.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range p.Steps {
		if strings.Contains(st.String(), "secret") {
			t.Errorf("token leaked into displayed command: %s", st)
		}
	}
	if err := p.Run(ctx, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if !sawToken {
		t.Error("server never received the token header")
	}
	if d := probeDuration(t, r.Output); d < 3.9 || d > 4.2 {
		t.Errorf("remote clip duration = %v, want ~4s", d)
	}

	// Stream copy to mp4 from HEVC; cut should land on the keyframe at 0s.
	r = Request{Input: in, Output: filepath.Join(dir, "copy.mp4"), Start: 5 * time.Second, Duration: 3 * time.Second,
		Copy: true, VideoCodec: "hevc", AudioCodec: "aac", AudioIndex: -1, FFmpeg: ffmpegBin()}
	p, err = r.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Run(ctx, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if d := probeDuration(t, r.Output); d <= 0 {
		t.Errorf("copied clip duration = %v", d)
	}
}
