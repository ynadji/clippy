//go:build libav

package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ynadji/clippy/internal/jellyfin"
)

// TestJellyfinLive runs against a real server when JELLYFIN_URL, JELLYFIN_USER
// and JELLYFIN_PASSWORD are set. It signs in, reports a playback session
// for the first movie as a second client, and checks that NowPlaying sees
// it, Frame renders, and StartClip produces a file with the chosen audio
// and subtitle tracks.
func TestJellyfinLive(t *testing.T) {
	url, user, pass := os.Getenv("JELLYFIN_URL"), os.Getenv("JELLYFIN_USER"), os.Getenv("JELLYFIN_PASSWORD")
	if url == "" || user == "" {
		t.Skip("JELLYFIN_URL, JELLYFIN_USER and JELLYFIN_PASSWORD not set")
	}
	srv, err := JellyfinSignIn(url, user, pass, "clippy-test-"+NewClientID()[:8])
	if err != nil {
		t.Fatal(err)
	}
	if srv.Kind != KindJellyfin || srv.Token == "" || srv.UserID == "" {
		t.Fatalf("server %+v", srv)
	}
	t.Logf("signed in to %q at %s", srv.Name, srv.URL)

	// Pick a movie and pretend to play it from a different device.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	player := jellyfin.New(srv.URL, "", "", "clippy-test-player")
	player.Client, player.Device = "clippy test player", "test"
	if _, err := player.Login(ctx, user, pass); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []jellyfin.Item `json:"Items"`
	}
	want := os.Getenv("JELLYFIN_ITEM")
	if err := playerGet(ctx, player, "/Users/"+player.UserID+"/Items?IncludeItemTypes=Movie&Recursive=true&SortBy=SortName", &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Fatal("no movies on the server")
	}
	movie := list.Items[0]
	for _, it := range list.Items {
		if want != "" && strings.Contains(it.Name, want) {
			movie = it
		}
	}
	report(ctx, t, player, "/Sessions/Playing", movie.ID, 30*time.Second)
	defer report(ctx, t, player, "/Sessions/Playing/Stopped", movie.ID, 30*time.Second)

	sessions, err := NowPlaying(srv)
	if err != nil {
		t.Fatal(err)
	}
	var s *Session
	for i := 0; i < sessions.Len(); i++ {
		if c := sessions.Get(i); c.RatingKey == movie.ID && c.Player == player.Device {
			s = c
		}
	}
	if s == nil {
		t.Fatalf("session for %q not in NowPlaying (%d sessions)", movie.Name, sessions.Len())
	}
	t.Logf("session: %s pos=%dms dur=%dms part=%s audio=%d(%s) sub=%d video=%dx%d %s",
		s.Label, s.PositionMs, s.DurationMs, s.PartKey, s.AudioIndex, s.AudioLang, s.SubIndex, s.VideoWidth, s.VideoHeight, s.VideoCodec)
	if s.PositionMs != 30_000 || s.PartKey == "" || s.VideoWidth == 0 {
		t.Errorf("session fields: %+v", *s)
	}
	subs := s.Subtitles()
	for i := 0; i < subs.Len(); i++ {
		sub := subs.Get(i)
		t.Logf("  subtitle %d: idx=%d key=%q %s %s ext=%v burn=%v sel=%v", i, sub.Index, sub.Key, sub.Codec, sub.Title, sub.External, sub.Burnable, sub.Selected)
	}

	img, err := Frame(srv, s.PartKey, 10_000, 320)
	if err != nil {
		t.Fatal("frame:", err)
	}
	if !bytes.HasPrefix(img, []byte{0xff, 0xd8}) {
		t.Errorf("frame is not a JPEG (%d bytes)", len(img))
	}

	// Clip two seconds with the playing audio track (or JELLYFIN_AUDIO, a
	// 1-based position) and, if there is one, the last burnable subtitle.
	audio := s.AudioIndex
	if v, err := strconv.Atoi(os.Getenv("JELLYFIN_AUDIO")); err == nil {
		audio = v
	}
	start := int64(10_000)
	if v, err := strconv.Atoi(os.Getenv("JELLYFIN_START")); err == nil {
		start = int64(v) * 1000
	}
	req := &ClipRequest{Server: srv, PartKey: s.PartKey, StartMs: start, DurationMs: 2_000,
		Output: filepath.Join(t.TempDir(), "clip.mp4"), MaxHeight: 360, AudioIndex: audio, SubIndex: -1}
	for i := 0; i < subs.Len(); i++ {
		if sub := subs.Get(i); sub.Burnable {
			if sub.External {
				req.SubKey, req.SubIndex = sub.Key, -1
			} else {
				req.SubIndex, req.SubKey = sub.Index, ""
			}
		}
	}
	t.Logf("clip: %s sub=%d key=%q", Describe(req), req.SubIndex, req.SubKey)
	done := make(chan string, 1)
	job, err := StartClip(req, listenerFunc(func(e string) { done <- e }))
	if err != nil {
		t.Fatal("start clip:", err)
	}
	defer job.Cancel()
	select {
	case e := <-done:
		if e != "" {
			t.Fatal("clip:", e)
		}
	case <-time.After(3 * time.Minute):
		t.Fatal("clip timed out")
	}
	fi, err := os.Stat(req.Output)
	if err != nil || fi.Size() < 10_000 {
		t.Fatalf("output: %v %v", fi, err)
	}
	t.Logf("clip written: %d bytes", fi.Size())
	if keep := os.Getenv("JELLYFIN_KEEP"); keep != "" {
		data, _ := os.ReadFile(req.Output)
		if err := os.WriteFile(keep, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type listenerFunc func(err string)

func (f listenerFunc) OnProgress(done, total int64) {}
func (f listenerFunc) OnDone(err string)            { f(err) }

func playerGet(ctx context.Context, c *jellyfin.Client, path string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path, nil)
	req.Header.Set("Authorization", c.Authorization())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func report(ctx context.Context, t *testing.T, c *jellyfin.Client, path, itemID string, pos time.Duration) {
	body, _ := json.Marshal(map[string]any{
		"ItemId": itemID, "MediaSourceId": itemID, "PositionTicks": pos.Nanoseconds() / 100,
		"IsPaused": false, "PlayMethod": "DirectPlay", "CanSeek": true,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", c.Authorization())
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s: %s", path, resp.Status)
	}
}
