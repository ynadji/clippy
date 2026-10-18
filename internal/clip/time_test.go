package clip

import (
	"os"
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	cases := map[string]time.Duration{
		"90":       90 * time.Second,
		"90.5":     90*time.Second + 500*time.Millisecond,
		"12:34":    12*time.Minute + 34*time.Second,
		"1:02:03":  time.Hour + 2*time.Minute + 3*time.Second,
		"12:34.5":  12*time.Minute + 34*time.Second + 500*time.Millisecond,
		"20s":      20 * time.Second,
		"1m30s":    90 * time.Second,
		" 0:00:05": 5 * time.Second,
	}
	for in, want := range cases {
		got, err := ParseTime(in)
		if err != nil || got != want {
			t.Errorf("ParseTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-5", "1:2:3:4", "-1m"} {
		if _, err := ParseTime(bad); err == nil {
			t.Errorf("ParseTime(%q) should fail", bad)
		}
	}
}

func TestStampAndClock(t *testing.T) {
	d := time.Hour + 2*time.Minute + 3*time.Second
	if got := Stamp(d); got != "1h02m03s" {
		t.Errorf("Stamp = %q", got)
	}
	if got := Stamp(754 * time.Second); got != "12m34s" {
		t.Errorf("Stamp = %q", got)
	}
	if got := Clock(d); got != "1:02:03" {
		t.Errorf("Clock = %q", got)
	}
}

func TestBuildCopyAndEncode(t *testing.T) {
	r := Request{Input: "in.mkv", Output: "out.mp4", Start: 10 * time.Second, Duration: 5 * time.Second,
		Copy: true, VideoCodec: "hevc", AudioCodec: "dts", AudioIndex: 2}
	p, err := r.Build()
	if err != nil {
		t.Fatal(err)
	}
	s := p.Steps[0].String()
	for _, want := range []string{"-ss 10.000", "-t 5.000", "-map 0:2", "-tag:v hvc1", "-c:a aac", "-c:v copy"} {
		if !contains(s, want) {
			t.Errorf("copy plan missing %q: %s", want, s)
		}
	}
	r.Copy = false
	r.MaxHeight = 720
	r.Subtitle = &Subtitle{Index: 3, Codec: "srt"}
	p, err = r.Build()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if len(p.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(p.Steps))
	}
	if !contains(p.Steps[0].String(), "-map 0:3") || p.Steps[1].Fn == nil || !contains(p.Steps[2].String(), "subtitles=") {
		t.Errorf("encode plan wrong: %s\n%s\n%s", p.Steps[0], p.Steps[1], p.Steps[2])
	}
	r.Subtitle.Codec = "pgs"
	if _, err := r.Build(); err == nil {
		t.Error("bitmap subtitles should be rejected")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestShiftASS(t *testing.T) {
	dir := t.TempDir()
	in, out := dir+"/in.ass", dir+"/out.ass"
	src := "[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:10.00,0:00:11.00,Default,,0,0,0,,before\n" +
		"Dialogue: 0,0:00:12.00,0:00:13.00,Default,,0,0,0,,straddle, with comma\n" +
		"Dialogue: 0,0:00:14.00,0:00:15.00,Default,,0,0,0,,inside\n" +
		"Dialogue: 0,0:00:16.00,0:00:18.00,Default,,0,0,0,,tail\n" +
		"Dialogue: 0,0:00:20.00,0:00:21.00,Default,,0,0,0,,after\n"
	if err := os.WriteFile(in, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := shiftASS(in, out, 12500*time.Millisecond, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	want := "[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:00.00,0:00:00.50,Default,,0,0,0,,straddle, with comma\n" +
		"Dialogue: 0,0:00:01.50,0:00:02.50,Default,,0,0,0,,inside\n" +
		"Dialogue: 0,0:00:03.50,0:00:04.00,Default,,0,0,0,,tail\n"
	if string(got) != want {
		t.Errorf("shiftASS output:\n%s\nwant:\n%s", got, want)
	}
}
