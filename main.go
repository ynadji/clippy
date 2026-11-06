// Command clippy cuts short clips out of media in your Plex library.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ynadji/clippy/internal/clip"
	"github.com/ynadji/clippy/internal/config"
	"github.com/ynadji/clippy/internal/plex"
	"github.com/ynadji/clippy/internal/proxy"
)

const usage = `clippy – clip moments from your Plex library

Usage:
  clippy login                 Link to your Plex account and pick a server
  clippy now                   Show what is currently playing
  clippy search <query>        Find movies and episodes
  clippy clip [flags]          Cut a clip (see clippy clip -h)
  clippy config                Show the current configuration
  clippy doctor                Check connectivity, throughput and ffmpeg

Examples:
  clippy clip --last 20s                       # last 20s of what's playing
  clippy clip --before 10s --after 5s --subs   # around the current position, with subs
  clippy clip --session 2 --start 12:34 --end 12:51
  clippy clip --query "sintel" --start 0:05:03 --duration 15
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "login":
		err = cmdLogin(ctx, os.Args[2:])
	case "now", "sessions":
		err = cmdNow(ctx, os.Args[2:])
	case "search":
		err = cmdSearch(ctx, os.Args[2:])
	case "clip":
		err = cmdClip(ctx, os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "doctor":
		err = cmdDoctor(ctx, os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func client(cfg *config.Config) (*plex.Client, error) {
	if err := cfg.Ready(); err != nil {
		return nil, err
	}
	return plex.New(cfg.URL, cfg.Token, cfg.ClientID), nil
}

func cmdConfig(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Println("config file:", config.Path())
	r := cfg.Redacted()
	fmt.Println("url:        ", r.URL)
	fmt.Println("token:      ", r.Token)
	fmt.Println("server:     ", r.ServerName)
	fmt.Println("ffmpeg:     ", orDefault(r.FFmpeg, "ffmpeg (from PATH)"))
	fmt.Println("output_dir: ", orDefault(r.OutputDir, "current directory"))
	if len(r.PathMap) > 0 {
		fmt.Println("path_map:")
		for k, v := range r.PathMap {
			fmt.Printf("  %s -> %s\n", k, v)
		}
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func cmdLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "", "server name to use when the account has several")
	url := fs.String("url", "", "skip discovery and use this server URL")
	fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.ClientID == "" {
		cfg.ClientID = plex.NewClientID()
	}

	pin, err := plex.RequestPIN(ctx, cfg.ClientID)
	if err != nil {
		return err
	}
	fmt.Printf("Open https://plex.tv/link and enter the code: %s\n", pin.Code)
	fmt.Println("Waiting for you to link... (Ctrl-C to abort)")
	token, err := plex.WaitForPIN(ctx, cfg.ClientID, pin)
	if err != nil {
		return err
	}
	cfg.Token = token
	fmt.Println("Linked.")

	if *url != "" {
		cfg.URL = strings.TrimRight(*url, "/")
		name, _, err := plex.New(cfg.URL, cfg.Token, cfg.ClientID).Identity(ctx)
		if err != nil {
			return fmt.Errorf("server at %s: %w", cfg.URL, err)
		}
		cfg.ServerName = name
	} else {
		servers, err := plex.Servers(ctx, cfg.ClientID, token)
		if err != nil {
			return err
		}
		if len(servers) == 0 {
			return errors.New("no Plex servers found on this account; rerun with --url http://host:32400")
		}
		var chosen *plex.Resource
		if *server != "" {
			for i := range servers {
				if strings.EqualFold(servers[i].Name, *server) {
					chosen = &servers[i]
				}
			}
			if chosen == nil {
				return fmt.Errorf("no server named %q", *server)
			}
		} else if len(servers) == 1 {
			chosen = &servers[0]
		} else {
			fmt.Println("Several servers found; rerun with --server <name>:")
			for _, s := range servers {
				fmt.Printf("  %s (owned=%v)\n", s.Name, s.Owned)
			}
			return errors.New("multiple servers")
		}
		fmt.Printf("Connecting to %s...\n", chosen.Name)
		if chosen.AccessToken != "" {
			cfg.Token = chosen.AccessToken
		}
		uri, err := plex.Reachable(ctx, chosen, cfg.Token, cfg.ClientID)
		if err != nil {
			return err
		}
		cfg.URL = uri
		cfg.ServerName = chosen.Name
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Saved %s (server %q at %s)\n", config.Path(), cfg.ServerName, cfg.URL)
	fmt.Println("If media paths differ on this machine, add a \"path_map\" to the config.")
	return nil
}

func cmdNow(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cl, err := client(cfg)
	if err != nil {
		return err
	}
	sessions, err := cl.Sessions(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("Nothing is playing.")
		return nil
	}
	printSessions(ctx, cl, sessions)
	return nil
}

func printSessions(ctx context.Context, cl *plex.Client, sessions []plex.Item) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tUSER\tPLAYER\tTITLE\tPOSITION\tVIA\tSTATE\tKEY")
	for i, s := range sessions {
		user, player, state := "", "", ""
		if s.User != nil {
			user = s.User.Title
		}
		if s.Player != nil {
			player = s.Player.Title
			if s.Player.Product != "" {
				player += " (" + s.Player.Product + ")"
			}
			state = s.Player.State
		}
		pos, via := cl.LivePosition(ctx, &s)
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s / %s\t%s\t%s\t%s\n",
			i+1, user, player, s.Label(), clip.Clock(pos), clip.Clock(s.Length()), via, state, s.RatingKey)
	}
	w.Flush()
}

func cmdSearch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	limit := fs.Int("limit", 10, "max results per type")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("usage: clippy search <query>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cl, err := client(cfg)
	if err != nil {
		return err
	}
	items, err := cl.Search(ctx, strings.Join(fs.Args(), " "), *limit)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("No matches.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tTYPE\tTITLE\tLENGTH")
	for _, it := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", it.RatingKey, it.Type, it.Label(), clip.Clock(it.Length()))
	}
	return w.Flush()
}

type clipFlags struct {
	session, user, key, query          string
	start, end, duration, last, at     string
	before, after                      string
	output                             string
	copyMode, subs, dryRun, listStream bool
	remote, direct                     bool
	subStream, audioStream             int
	maxHeight, crf                     int
	preset, hwaccel, engine            string
}

func cmdClip(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("clip", flag.ExitOnError)
	var f clipFlags
	fs.StringVar(&f.session, "session", "", "session number from `clippy now` (default: the only active session)")
	fs.StringVar(&f.user, "user", "", "pick the session belonging to this Plex user")
	fs.StringVar(&f.key, "key", "", "clip a library item by rating key (from `clippy search`)")
	fs.StringVar(&f.query, "query", "", "clip the first search match for this title")
	fs.StringVar(&f.start, "start", "", "clip start (12:34, 90, 1m30s)")
	fs.StringVar(&f.end, "end", "", "clip end")
	fs.StringVar(&f.duration, "duration", "", "clip length, instead of --end")
	fs.StringVar(&f.duration, "d", "", "shorthand for --duration")
	fs.StringVar(&f.last, "last", "", "clip the N seconds ending at the current position (e.g. 20s)")
	fs.StringVar(&f.before, "before", "", "seconds before the current position to start")
	fs.StringVar(&f.after, "after", "", "seconds after the current position to end")
	fs.StringVar(&f.at, "at", "", "override the current position used by --last/--before/--after")
	fs.StringVar(&f.output, "o", "", "output file (default: <title>_<start>-<end>.mp4 in output_dir)")
	fs.BoolVar(&f.copyMode, "copy", false, "stream copy without re-encoding (fast, but cuts land on keyframes)")
	fs.BoolVar(&f.subs, "subs", false, "burn in subtitles (the stream selected in Plex, else the first)")
	fs.IntVar(&f.subStream, "sub-stream", -1, "burn in this subtitle stream (absolute index; see --streams)")
	fs.IntVar(&f.audioStream, "audio", -1, "use this audio stream (absolute index; default: the one selected in Plex)")
	fs.BoolVar(&f.listStream, "streams", false, "list the item's streams and exit")
	fs.IntVar(&f.maxHeight, "max-height", 1080, "downscale video taller than this when re-encoding (0 = never)")
	fs.IntVar(&f.crf, "crf", 20, "x264 quality, lower is better")
	fs.StringVar(&f.preset, "preset", "veryfast", "x264 preset")
	fs.StringVar(&f.hwaccel, "hwaccel", "auto", "hardware decoder: auto, none, videotoolbox, cuda, vaapi, ...")
	fs.BoolVar(&f.remote, "remote", false, "stream from the server even if a path_map entry matches")
	fs.BoolVar(&f.direct, "direct", false, "let ffmpeg read from the server directly instead of through the local block cache")
	fs.StringVar(&f.engine, "engine", "exec", "clip engine: exec (ffmpeg binary) or libav (linked FFmpeg libraries, if built in)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the ffmpeg command instead of running it")
	fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cl, err := client(cfg)
	if err != nil {
		return err
	}

	item, anchor, err := resolveItem(ctx, cl, &f)
	if err != nil {
		return err
	}
	sessionPart := item.SelectedPart()
	part, fileItem, err := cl.FilePart(ctx, item)
	if err != nil {
		return err
	}
	item = fileItem
	if f.listStream {
		printStreams(item, part)
		return nil
	}

	start, dur, err := resolveRange(&f, anchor, item.Length())
	if err != nil {
		return err
	}

	// Read the file from the server over HTTP unless the config maps it to
	// a path that exists here.
	input, headers := cl.PartURL(part), cl.FFmpegHeaders()
	if local, ok := cfg.MapPath(part.File); ok && !f.remote {
		if fileExists(local) {
			input, headers = local, ""
		} else {
			fmt.Fprintf(os.Stderr, "note: path_map gave %s but it does not exist; streaming from server instead\n", local)
		}
	}

	req := clip.Request{
		Input: input, Headers: headers, Start: start, Duration: dur,
		Copy: f.copyMode, AudioIndex: -1,
		MaxHeight: f.maxHeight, CRF: f.crf, Preset: f.preset, FFmpeg: cfg.FFmpeg, HWAccel: f.hwaccel,
	}
	if m := item.MediaFor(part); m != nil {
		req.VideoCodec, req.AudioCodec = m.VideoCodec, m.AudioCodec
	}
	if f.audioStream >= 0 {
		req.AudioIndex = f.audioStream
	} else if s := plex.FileStream(sessionPart.ActiveStream(plex.StreamAudio), part); s != nil && s.Index >= 0 {
		req.AudioIndex = int(s.Index)
	} else if s := part.SelectedStream(plex.StreamAudio); s != nil {
		req.AudioIndex = int(s.Index)
	}
	if f.subs || f.subStream >= 0 {
		sub, err := pickSubtitle(ctx, cl, cfg, part, f.subStream)
		if err != nil {
			return err
		}
		req.Subtitle = sub
	}

	req.Output = f.output
	if req.Output == "" {
		name := fmt.Sprintf("%s_%s-%s.mp4", item.FileStem(), clip.Stamp(start), clip.Stamp(start+dur))
		req.Output = filepath.Join(cfg.OutputDir, name)
	}

	engine := selectEngine(f.engine)
	if err := engine.Preflight(&req); err != nil && !f.dryRun {
		return err
	}
	// ffmpeg seeks relentlessly in some containers, and each seek against a
	// remote server is a new request. Serve the file from a local block
	// cache instead so those seeks are cheap.
	var px *proxy.Proxy
	if req.Headers != "" && !f.direct && !f.dryRun {
		px, err = proxy.Start(ctx, req.Input, http.Header{"X-Plex-Token": {cl.Token}})
		if err != nil {
			return fmt.Errorf("start local cache for %s: %w", req.Input, err)
		}
		defer px.Close()
		req.Input, req.Headers = px.URL(), ""
	}
	fmt.Fprintf(os.Stderr, "%s  %s → %s (%s)\n", item.Label(), clip.Clock(start), clip.Clock(start+dur), dur.Round(100*time.Millisecond))
	if f.dryRun {
		steps, err := engine.Describe(&req)
		if err != nil {
			return err
		}
		fmt.Printf("# engine: %s\n", engine.Name())
		for _, s := range steps {
			fmt.Println(s)
		}
		return nil
	}
	if dir := filepath.Dir(req.Output); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	t0 := time.Now()
	if err := engine.Run(ctx, &req, os.Stderr, nil); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr)
	if px != nil {
		fmt.Fprintf(os.Stderr, "fetched %.1f MB from the server in %d requests; %s total\n", float64(px.Bytes)/1e6, px.Fetches, time.Since(t0).Round(100*time.Millisecond))
	}
	fmt.Println(req.Output)
	return nil
}

// resolveItem picks the item to clip and, for sessions, its current position.
func resolveItem(ctx context.Context, cl *plex.Client, f *clipFlags) (*plex.Item, time.Duration, error) {
	switch {
	case f.key != "":
		it, err := cl.Metadata(ctx, f.key)
		return it, -1, err
	case f.query != "":
		items, err := cl.Search(ctx, f.query, 5)
		if err != nil {
			return nil, 0, err
		}
		if len(items) == 0 {
			return nil, 0, fmt.Errorf("no results for %q", f.query)
		}
		it, err := cl.Metadata(ctx, items[0].RatingKey)
		if err != nil {
			return nil, 0, err
		}
		if len(items) > 1 {
			fmt.Fprintf(os.Stderr, "using first match: %s (key %s)\n", it.Label(), it.RatingKey)
		}
		return it, -1, nil
	}
	sessions, err := cl.Sessions(ctx)
	if err != nil {
		return nil, 0, err
	}
	var vids []plex.Item
	for _, s := range sessions {
		if s.IsVideo() {
			vids = append(vids, s)
		}
	}
	var pick *plex.Item
	switch {
	case f.session != "":
		var n int
		if _, err := fmt.Sscanf(f.session, "%d", &n); err != nil || n < 1 || n > len(vids) {
			return nil, 0, fmt.Errorf("--session must be between 1 and %d", len(vids))
		}
		pick = &vids[n-1]
	case f.user != "":
		for i := range vids {
			if vids[i].User != nil && strings.EqualFold(vids[i].User.Title, f.user) {
				pick = &vids[i]
			}
		}
		if pick == nil {
			return nil, 0, fmt.Errorf("no active session for user %q", f.user)
		}
	case len(vids) == 1:
		pick = &vids[0]
	case len(vids) == 0:
		return nil, 0, errors.New("nothing is playing; use --key, --query, or start playback")
	default:
		printSessions(ctx, cl, vids)
		return nil, 0, errors.New("several sessions are active; pick one with --session N or --user NAME")
	}
	// Session metadata already carries file and stream info, including the
	// streams the player has selected, so use it as-is.
	pos, via := cl.LivePosition(ctx, pick)
	if via == "server" {
		who := "the player"
		if pick.Player != nil {
			who = fmt.Sprintf("%s (%s)", pick.Player.Title, orDefault(pick.Player.Product, "unknown app"))
		}
		fmt.Fprintf(os.Stderr, "note: %s did not answer a Companion poll, so the position (%s) is the server's last progress report and may lag; pass --at to override\n", who, clip.Clock(pos))
	}
	return pick, pos, nil
}

// resolveRange turns the flags into a start and duration, clamped to the item.
func resolveRange(f *clipFlags, anchor, length time.Duration) (time.Duration, time.Duration, error) {
	if f.at != "" {
		a, err := clip.ParseTime(f.at)
		if err != nil {
			return 0, 0, err
		}
		anchor = a
	}
	needAnchor := func(flag string) error {
		if anchor < 0 {
			return fmt.Errorf("--%s needs a playing session; with --key or --query use --start, or pass --at", flag)
		}
		return nil
	}
	var start, dur time.Duration
	switch {
	case f.start != "":
		s, err := clip.ParseTime(f.start)
		if err != nil {
			return 0, 0, err
		}
		start = s
		switch {
		case f.end != "":
			e, err := clip.ParseTime(f.end)
			if err != nil {
				return 0, 0, err
			}
			if e <= s {
				return 0, 0, errors.New("--end must be after --start")
			}
			dur = e - s
		case f.duration != "":
			d, err := clip.ParseTime(f.duration)
			if err != nil {
				return 0, 0, err
			}
			dur = d
		default:
			return 0, 0, errors.New("--start needs --end or --duration")
		}
	case f.last != "":
		if err := needAnchor("last"); err != nil {
			return 0, 0, err
		}
		d, err := clip.ParseTime(f.last)
		if err != nil {
			return 0, 0, err
		}
		start, dur = anchor-d, d
	case f.before != "" || f.after != "":
		if err := needAnchor("before/--after"); err != nil {
			return 0, 0, err
		}
		var b, a time.Duration
		var err error
		if f.before != "" {
			if b, err = clip.ParseTime(f.before); err != nil {
				return 0, 0, err
			}
		}
		if f.after != "" {
			if a, err = clip.ParseTime(f.after); err != nil {
				return 0, 0, err
			}
		}
		start, dur = anchor-b, b+a
	default:
		return 0, 0, errors.New("say what to clip: --last 20s, --before/--after, or --start with --end/--duration")
	}
	if start < 0 {
		dur += start
		start = 0
	}
	if length > 0 && start+dur > length {
		dur = length - start
	}
	if dur <= 0 {
		return 0, 0, errors.New("clip range is empty")
	}
	return start, dur, nil
}

func pickSubtitle(ctx context.Context, cl *plex.Client, cfg *config.Config, part *plex.Part, want int) (*clip.Subtitle, error) {
	subs := part.Streams(plex.StreamSubtitle)
	if len(subs) == 0 {
		return nil, errors.New("this item has no subtitle streams")
	}
	var s *plex.Stream
	if want >= 0 {
		for i := range subs {
			if int(subs[i].Index) == want || int(subs[i].ID) == want {
				s = &subs[i]
			}
		}
		if s == nil {
			return nil, fmt.Errorf("no subtitle stream with index %d (use --streams to list them)", want)
		}
	} else if sel := part.SelectedStream(plex.StreamSubtitle); sel != nil {
		s = sel
	} else {
		s = &subs[0]
	}
	fmt.Fprintf(os.Stderr, "subtitles: %s\n", s.Describe())
	out := &clip.Subtitle{Index: int(s.Index), Codec: s.Codec}
	if s.Key != "" { // external file: use it directly if we can see it, else download
		if s.File != "" {
			if p, ok := cfg.MapPath(s.File); ok && fileExists(p) {
				out.File = p
				return out, nil
			}
		}
		dir, err := os.MkdirTemp("", "clippy-sub-")
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(dir, "external."+orDefault(s.Codec, "srt"))
		if err := cl.Download(ctx, s.Key, dest); err != nil {
			return nil, fmt.Errorf("download subtitles: %w", err)
		}
		out.File = dest
	}
	return out, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func printStreams(item *plex.Item, part *plex.Part) {
	fmt.Printf("%s\n%s\n\n", item.Label(), part.File)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "INDEX\tTYPE\tDESCRIPTION\tSELECTED")
	for _, s := range part.Stream {
		typ := map[int]string{plex.StreamVideo: "video", plex.StreamAudio: "audio", plex.StreamSubtitle: "subtitle"}[int(s.StreamType)]
		sel := ""
		if s.Selected {
			sel = "*"
		}
		idx := fmt.Sprint(s.Index)
		if s.Key != "" {
			idx = fmt.Sprintf("id:%d", s.ID)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", idx, typ, s.Describe(), sel)
	}
	w.Flush()
}

func cmdDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	sample := fs.Int64("mb", 16, "megabytes to read for the throughput test")
	fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cl, err := client(cfg)
	if err != nil {
		return err
	}

	fmt.Println("server")
	t0 := time.Now()
	info, err := cl.Info(ctx)
	if err != nil {
		return fmt.Errorf("  %s: %w", cfg.URL, err)
	}
	fmt.Printf("  url:      %s (%s)\n", cfg.URL, time.Since(t0).Round(time.Millisecond))
	fmt.Printf("  name:     %s, Plex Media Server %s on %s\n", info.Name, info.Version, info.Platform)

	if servers, err := plex.Servers(ctx, cfg.ClientID, cfg.Token); err == nil {
		for _, srv := range servers {
			if srv.ClientIdentifier != info.MachineIdentifier {
				continue
			}
			fmt.Println("\nconnections known to plex.tv for this server")
			for _, c := range srv.Connections {
				kind := "remote"
				if c.Local {
					kind = "local"
				}
				if c.Relay {
					kind = "relay (slow, ~2 Mbps)"
				}
				probe := plex.New(c.URI, cfg.Token, cfg.ClientID)
				probe.HTTP.Timeout = 4 * time.Second
				t := time.Now()
				_, _, perr := probe.Identity(ctx)
				status := "ok " + time.Since(t).Round(time.Millisecond).String()
				if perr != nil {
					status = "unreachable"
				}
				mark := " "
				if strings.TrimRight(c.URI, "/") == cfg.URL {
					mark = "*"
				}
				fmt.Printf("  %s %-60s %-24s %s\n", mark, c.URI, kind, status)
			}
			fmt.Println("  (* = in use; to switch, edit \"url\" in the config or rerun `clippy login --url ...`)")
		}
	} else {
		fmt.Printf("\nplex.tv: %v\n", err)
	}

	fmt.Println("\nthroughput")
	var part *plex.Part
	var label string
	if sessions, err := cl.Sessions(ctx); err == nil {
		for i := range sessions {
			if p := sessions[i].SelectedPart(); p != nil && p.Key != "" {
				part, label = p, sessions[i].Label()
				break
			}
		}
	}
	if part == nil {
		if it, err := cl.SampleItem(ctx); err == nil {
			part, label = it.SelectedPart(), it.Label()
		}
	}
	if part == nil {
		fmt.Println("  no media item available to read")
	} else {
		n := *sample << 20
		got, took, err := cl.ReadRange(ctx, part.Key, n)
		if err != nil {
			fmt.Printf("  read failed: %v\n", err)
		} else {
			mbps := float64(got) * 8 / took.Seconds() / 1e6
			fmt.Printf("  read %.1f MB of %q in %s: %.1f Mbit/s\n", float64(got)/1e6, label, took.Round(time.Millisecond), mbps)
			switch {
			case mbps < 5:
				fmt.Println("  this is far too slow for clipping; ffmpeg will crawl. Use a local connection, or run clippy on the server with a path_map.")
			case mbps < 40:
				fmt.Println("  workable, but clips of high-bitrate sources will be network-bound")
			default:
				fmt.Println("  good")
			}
		}
	}

	fmt.Println("\nffmpeg")
	bin := orDefault(cfg.FFmpeg, "ffmpeg")
	if path, err := exec.LookPath(bin); err != nil {
		fmt.Printf("  %s: not found\n", bin)
	} else {
		ver, _ := exec.Command(path, "-version").Output()
		first, _, _ := strings.Cut(string(ver), "\n")
		fmt.Printf("  %s\n  %s\n", path, first)
		fmt.Printf("  subtitles filter (libass): %v\n", clip.HasFilter(path, "subtitles"))
		if hw, err := exec.Command(path, "-hide_banner", "-hwaccels").Output(); err == nil {
			fmt.Printf("  hwaccels: %s\n", strings.Join(strings.Fields(strings.TrimPrefix(string(hw), "Hardware acceleration methods:")), " "))
		}
	}
	return nil
}
