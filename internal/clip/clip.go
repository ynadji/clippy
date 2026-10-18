// Package clip builds and runs ffmpeg commands that cut a segment out of a
// media file.
package clip

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Subtitle describes a subtitle source to burn into the clip. Exactly one of
// Index (an absolute stream index in the input file) or File (an external
// subtitle file) is used.
type Subtitle struct {
	Index int    // absolute stream index; ignored when File is set
	File  string // external subtitle file
	Codec string // as reported by Plex, for sanity checks
}

// Request describes one clip.
type Request struct {
	Input    string
	Headers  string // extra HTTP headers for Input when it is a URL ("Name: value\r\n")
	Start    time.Duration
	Duration time.Duration
	Output   string

	Copy       bool      // stream copy instead of re-encoding (keyframe accurate only)
	VideoCodec string    // source video codec, used for mp4 tagging in copy mode
	AudioCodec string    // source audio codec, used in copy mode
	AudioIndex int       // absolute audio stream index, or -1 for the first audio stream
	Subtitle   *Subtitle // nil for none
	MaxHeight  int       // downscale to this height when re-encoding; 0 keeps source
	CRF        int       // x264 quality when re-encoding (lower is better)
	Preset     string    // x264 preset
	FFmpeg     string    // ffmpeg binary; defaults to "ffmpeg" on PATH
	HWAccel    string    // hardware decoder: "auto" (default), "none", or an ffmpeg -hwaccel name
}

// textSubtitleCodecs are the codecs the subtitles filter can render.
var textSubtitleCodecs = map[string]bool{
	"srt": true, "subrip": true, "ass": true, "ssa": true, "mov_text": true,
	"webvtt": true, "vtt": true, "text": true, "microdvd": true, "subviewer": true,
}

// TextSubtitle reports whether a Plex subtitle codec can be burned in.
func TextSubtitle(codec string) bool { return textSubtitleCodecs[strings.ToLower(codec)] }

func (r *Request) ffmpeg() string {
	if r.FFmpeg != "" {
		return r.FFmpeg
	}
	return "ffmpeg"
}

// HasFilter reports whether the ffmpeg binary provides the named filter.
// Homebrew's slimmed-down ffmpeg, for example, lacks "subtitles" (libass).
func HasFilter(ffmpeg, name string) bool {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	out, err := exec.Command(ffmpeg, "-hide_banner", "-filters").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

// Preflight checks that the configured ffmpeg can carry out the request.
func (r *Request) Preflight() error {
	if _, err := exec.LookPath(r.ffmpeg()); err != nil {
		return fmt.Errorf("%s not found: install ffmpeg or set the ffmpeg path in the config", r.ffmpeg())
	}
	if r.Subtitle != nil && !HasFilter(r.ffmpeg(), "subtitles") {
		return fmt.Errorf("%s was built without libass, so it cannot burn in subtitles; point the ffmpeg config key at a full build (for example Plex's own \"Plex Transcoder\")", r.ffmpeg())
	}
	return nil
}

// Step is one unit of work in a Plan: either an external command or a
// function (described by Desc) run in-process.
type Step struct {
	Cmd  []string
	Fn   func() error
	Desc string
}

// String renders the step for display, masking header values (which may
// carry credentials).
func (s Step) String() string {
	if s.Fn != nil {
		return "# " + s.Desc
	}
	cmd := append([]string(nil), s.Cmd...)
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] == "-headers" {
			name, _, _ := strings.Cut(cmd[i+1], ":")
			cmd[i+1] = strings.TrimSpace(name) + ": ***"
		}
	}
	return Shell(cmd)
}

// Plan is the sequence of steps that produces the clip.
type Plan struct {
	Steps   []Step
	TempDir string
}

// Build returns the commands for the request. If a temporary directory is
// needed (for subtitle extraction) it is created and recorded in the plan;
// the caller should remove it when done.
func (r *Request) Build() (*Plan, error) {
	if r.Input == "" || r.Output == "" {
		return nil, fmt.Errorf("input and output are required")
	}
	if r.Duration <= 0 {
		return nil, fmt.Errorf("clip duration must be positive")
	}
	if r.Copy && r.Subtitle != nil {
		return nil, fmt.Errorf("cannot burn subtitles with --copy (re-encoding is required)")
	}
	plan := &Plan{}
	ss, t := Seconds(r.Start), Seconds(r.Duration)

	var subFile string
	if r.Subtitle != nil {
		if r.Subtitle.Codec != "" && !TextSubtitle(r.Subtitle.Codec) {
			return nil, fmt.Errorf("subtitle codec %q is bitmap-based and cannot be burned in; pick a text subtitle stream", r.Subtitle.Codec)
		}
		dir, err := os.MkdirTemp("", "clippy-")
		if err != nil {
			return nil, err
		}
		plan.TempDir = dir
		rawFile := filepath.Join(dir, "raw.ass")
		subFile = filepath.Join(dir, "subs.ass")
		// Extract the subtitles from the seek point onward with their original
		// (absolute) timestamps, then shift and trim them in-process so they
		// line up with the clip's zero-based timeline. Doing the shift
		// ourselves avoids ffmpeg emitting malformed negative times for cues
		// that straddle the cut.
		extract := []string{r.ffmpeg(), "-hide_banner", "-loglevel", "error", "-y", "-ss", ss, "-copyts"}
		if r.Subtitle.File != "" {
			extract = append(extract, "-i", r.Subtitle.File, "-map", "0:0")
		} else {
			extract = append(extract, r.inputArgs()...)
			extract = append(extract, "-map", fmt.Sprintf("0:%d", r.Subtitle.Index))
		}
		extract = append(extract, "-c:s", "ass", rawFile)
		plan.Steps = append(plan.Steps,
			Step{Cmd: extract},
			Step{Desc: fmt.Sprintf("shift subtitles by -%s and trim to %s", Seconds(r.Start), Seconds(r.Duration)),
				Fn: func() error { return shiftASS(rawFile, subFile, r.Start, r.Duration) }})
	}

	args := []string{r.ffmpeg(), "-hide_banner", "-loglevel", "error", "-stats", "-y"}
	if hw := r.hwaccel(); hw != "" && !r.Copy {
		args = append(args, "-hwaccel", hw)
	}
	args = append(args, "-ss", ss, "-t", t)
	args = append(args, r.inputArgs()...)
	args = append(args, "-map", "0:v:0")
	if r.AudioIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", r.AudioIndex))
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1")

	isMP4 := strings.EqualFold(filepath.Ext(r.Output), ".mp4") || strings.EqualFold(filepath.Ext(r.Output), ".m4v")
	if r.Copy {
		args = append(args, "-c:v", "copy", "-avoid_negative_ts", "make_zero")
		if isMP4 && strings.EqualFold(r.VideoCodec, "hevc") {
			args = append(args, "-tag:v", "hvc1")
		}
		if isMP4 && !mp4Audio(r.AudioCodec) {
			args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
		} else {
			args = append(args, "-c:a", "copy")
		}
	} else {
		var filters []string
		if r.MaxHeight > 0 {
			filters = append(filters, fmt.Sprintf("scale=-2:'min(%d,ih)'", r.MaxHeight))
		}
		if subFile != "" {
			filters = append(filters, "subtitles="+filterEscape(subFile))
		}
		if len(filters) > 0 {
			args = append(args, "-vf", strings.Join(filters, ","))
		}
		crf := r.CRF
		if crf <= 0 {
			crf = 20
		}
		preset := r.Preset
		if preset == "" {
			preset = "veryfast"
		}
		args = append(args,
			"-c:v", "libx264", "-preset", preset, "-crf", fmt.Sprint(crf), "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "160k", "-ac", "2")
	}
	if isMP4 {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, r.Output)
	plan.Steps = append(plan.Steps, Step{Cmd: args})
	return plan, nil
}

// hwaccel resolves the decoder choice for this platform.
func (r *Request) hwaccel() string {
	switch r.HWAccel {
	case "", "auto":
		if runtime.GOOS == "darwin" {
			return "videotoolbox"
		}
		return ""
	case "none", "off":
		return ""
	}
	return r.HWAccel
}

// inputArgs returns the "-i" clause, with headers when the input is a URL.
func (r *Request) inputArgs() []string {
	var args []string
	if r.Headers != "" && (strings.HasPrefix(r.Input, "http://") || strings.HasPrefix(r.Input, "https://")) {
		args = append(args, "-headers", r.Headers)
	}
	return append(args, "-i", r.Input)
}

func mp4Audio(codec string) bool {
	switch strings.ToLower(codec) {
	case "aac", "mp3", "ac3", "eac3", "alac", "opus", "flac":
		return true
	}
	return false
}

// filterEscape quotes a path for use inside an ffmpeg filter graph.
func filterEscape(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, `:`, `\:`, `[`, `\[`, `]`, `\]`, `,`, `\,`, `;`, `\;`)
	return "'" + r.Replace(p) + "'"
}

// Shell renders a command for display.
func Shell(cmd []string) string {
	parts := make([]string, len(cmd))
	for i, c := range cmd {
		if c == "" || strings.ContainsAny(c, " '\"$\\;&|<>()[]*?") {
			parts[i] = "'" + strings.ReplaceAll(c, "'", `'\''`) + "'"
		} else {
			parts[i] = c
		}
	}
	return strings.Join(parts, " ")
}

// Run executes the plan, streaming ffmpeg's progress to stderr.
func (p *Plan) Run(ctx context.Context, stderr io.Writer) error {
	if p.TempDir != "" {
		defer os.RemoveAll(p.TempDir)
	}
	for _, step := range p.Steps {
		if step.Fn != nil {
			if err := step.Fn(); err != nil {
				return fmt.Errorf("%s: %w", step.Desc, err)
			}
			continue
		}
		cmd := exec.CommandContext(ctx, step.Cmd[0], step.Cmd[1:]...)
		cmd.Stdout = io.Discard
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			if _, lookErr := exec.LookPath(step.Cmd[0]); lookErr != nil {
				return fmt.Errorf("%s not found: install ffmpeg or set ffmpeg path in config", step.Cmd[0])
			}
			return fmt.Errorf("%s failed: %w", filepath.Base(step.Cmd[0]), err)
		}
	}
	return nil
}

// Cleanup removes temporary files without running the plan.
func (p *Plan) Cleanup() {
	if p.TempDir != "" {
		os.RemoveAll(p.TempDir)
	}
}
