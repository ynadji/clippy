package jellyfin

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCandidates(t *testing.T) {
	cases := map[string][]string{
		"":                         nil,
		"https://jf.example.com/":  {"https://jf.example.com"},
		"http://192.168.1.20:8096": {"http://192.168.1.20:8096"},
		"192.168.1.20:8096":        {"http://192.168.1.20:8096", "https://192.168.1.20:8096"},
		"jf.example.com":           {"https://jf.example.com", "http://jf.example.com:8096", "http://jf.example.com"},
		"  jf.example.com  ":       {"https://jf.example.com", "http://jf.example.com:8096", "http://jf.example.com"},
	}
	for in, want := range cases {
		if got := Candidates(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPaths(t *testing.T) {
	if got, want := MediaPath("abc", "src1"), "/Videos/abc/stream?mediaSourceId=src1&static=true"; got != want {
		t.Errorf("MediaPath = %q, want %q", got, want)
	}
	ass := &MediaStream{Index: 5, Codec: "ass"}
	if got, want := SubtitlePath("abc", "src1", ass), "/Videos/abc/src1/Subtitles/5/Stream.ass"; got != want {
		t.Errorf("SubtitlePath(ass) = %q, want %q", got, want)
	}
	srt := &MediaStream{Index: 6, Codec: "subrip"}
	if got, want := SubtitlePath("abc", "src1", srt), "/Videos/abc/src1/Subtitles/6/Stream.srt"; got != want {
		t.Errorf("SubtitlePath(subrip) = %q, want %q", got, want)
	}
}

func TestSessionDecode(t *testing.T) {
	raw := `[{"Id":"s1","UserName":"demo","Client":"Jellyfin Web","DeviceName":"Firefox",
	  "PlayState":{"PositionTicks":1234560000,"IsPaused":true,"MediaSourceId":"m1","AudioStreamIndex":1,"SubtitleStreamIndex":-1,"PlayMethod":"DirectPlay"},
	  "NowPlayingItem":{"Id":"i1","Name":"Pilot","Type":"Episode","SeriesName":"Show","ParentIndexNumber":1,"IndexNumber":2,"RunTimeTicks":36000000000}},
	 {"Id":"s2","UserName":"idle"}]`
	var ss []Session
	if err := json.Unmarshal([]byte(raw), &ss); err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[1].NowPlayingItem != nil {
		t.Fatalf("decoded %+v", ss)
	}
	s := ss[0]
	if s.State() != "paused" || s.PlayState.PositionTicks.Milliseconds() != 123456 {
		t.Errorf("state %s pos %d", s.State(), s.PlayState.PositionTicks.Milliseconds())
	}
	if *s.PlayState.AudioStreamIndex != 1 || *s.PlayState.SubtitleStreamIndex != -1 {
		t.Errorf("stream choices %+v", s.PlayState)
	}
	it := s.NowPlayingItem
	if it.Label() != "Show S01E02 – Pilot" || it.FileStem() != "Show_S01E02_Pilot" {
		t.Errorf("label %q stem %q", it.Label(), it.FileStem())
	}
	if it.RunTimeTicks.Duration().Seconds() != 3600 {
		t.Errorf("runtime %v", it.RunTimeTicks.Duration())
	}
}

func TestSourceLookup(t *testing.T) {
	it := &Item{MediaSources: []MediaSource{
		{ID: "a", Path: `C:\media\Movie (2020)\Movie.mkv`, MediaStreams: []MediaStream{{Index: 0, Type: StreamVideo}, {Index: 1, Type: StreamAudio}, {Index: 2, Type: StreamSubtitle}}},
		{ID: "b", Path: "/media/other.mkv"},
	}}
	if it.Source("b").ID != "b" || it.Source("zzz").ID != "a" || it.Source("").ID != "a" {
		t.Error("Source lookup")
	}
	a := it.Source("a")
	if a.FileName() != "Movie.mkv" || it.Source("b").FileName() != "other.mkv" {
		t.Errorf("file names %q %q", a.FileName(), it.Source("b").FileName())
	}
	if len(a.Streams(StreamAudio)) != 1 || a.Stream(2).Type != StreamSubtitle || a.Stream(9) != nil {
		t.Error("stream lookup")
	}
	if (&Item{}).Source("") != nil {
		t.Error("empty item should have no source")
	}
}
