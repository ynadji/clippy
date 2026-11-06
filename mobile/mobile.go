//go:build libav

// Package mobile is the API the Banana Clip iOS app calls, built into an
// xcframework with gomobile bind. It is a thin, gomobile-friendly layer
// (basic types, []byte, pointers to structs, listener interfaces) over the
// Plex client and the libav clip engine.
package mobile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ynadji/clippy/internal/clip"
	"github.com/ynadji/clippy/internal/clip/libav"
	"github.com/ynadji/clippy/internal/plex"
)

// Version of the core library.
const Version = "0.1.0"

// NewClientID returns a random identifier the app should persist and send
// with every call.
func NewClientID() string { return plex.NewClientID() }

// ---------------------------------------------------------------------------
// Sign in

// PIN is a pending plex.tv/link request.
type PIN struct {
	ID   int
	Code string
}

// RequestPIN starts the plex.tv/link flow.
func RequestPIN(clientID string) (*PIN, error) {
	p, err := plex.RequestPIN(context.Background(), clientID)
	if err != nil {
		return nil, err
	}
	return &PIN{ID: p.ID, Code: p.Code}, nil
}

// WaitForPIN blocks until the user enters the code (returning the account
// token) or timeoutSeconds elapse. Call it off the main thread.
func WaitForPIN(clientID string, id int, timeoutSeconds int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	return plex.WaitForPIN(ctx, clientID, &plex.PIN{ID: id})
}

// Server is a Plex Media Server the account can reach.
type Server struct {
	Name  string
	URL   string // best reachable connection, empty if none answered
	Token string // token to use with this server
	Owned bool
	Local bool
}

// ServerList is a gomobile-friendly slice of servers.
type ServerList struct{ items []*Server }

func (l *ServerList) Len() int          { return len(l.items) }
func (l *ServerList) Get(i int) *Server { return l.items[i] }

// DiscoverServers lists the account's servers and probes each one's
// connections, local first.
func DiscoverServers(clientID, token string) (*ServerList, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	servers, err := plex.Servers(ctx, clientID, token)
	if err != nil {
		return nil, err
	}
	out := &ServerList{}
	for i := range servers {
		s := &servers[i]
		tok := token
		if s.AccessToken != "" {
			tok = s.AccessToken
		}
		srv := &Server{Name: s.Name, Token: tok, Owned: s.Owned}
		if uri, err := plex.Reachable(ctx, s, tok, clientID); err == nil {
			srv.URL = uri
			for _, c := range s.Connections {
				if c.URI == uri {
					srv.Local = c.Local
				}
			}
		}
		out.items = append(out.items, srv)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Now playing

// Session is something playing on the server, with the file details the
// clip engine needs.
type Session struct {
	RatingKey   string
	Label       string // "Show S01E02 – Title" or "Movie (Year)"
	FileStem    string // safe base name for clip files
	User        string
	Player      string
	Product     string
	State       string // playing, paused, buffering
	PositionMs  int64
	PositionVia string // "player" (live, via Companion) or "server" (last report)
	DurationMs  int64
	PartKey     string // path on the server that serves the file
	FileName    string
	VideoCodec  string
	AudioIndex  int // selected audio stream (absolute index), -1 if unknown
	SubIndex    int // selected subtitle stream (absolute index), -1 if none
	SubCodec    string
	SubExternal bool   // selected subtitle is a sidecar file, not embedded
	AudioLang   string // language code of the selected audio stream, if known

	subs []*Subtitle
}

// Subtitle is one subtitle track of an item.
type Subtitle struct {
	Index    int    // absolute stream index for embedded tracks, -1 for sidecar files
	Key      string // server path of a sidecar subtitle file, empty for embedded tracks
	Codec    string
	Title    string // display title, such as "English (ASS)"
	Lang     string // language code, such as "eng"
	External bool
	Forced   bool
	Selected bool // Plex has this track selected for the account
	Burnable bool // text-based, so it can be rendered into the picture
}

// SubtitleList is a gomobile-friendly slice of subtitle tracks.
type SubtitleList struct{ items []*Subtitle }

func (l *SubtitleList) Len() int            { return len(l.items) }
func (l *SubtitleList) Get(i int) *Subtitle { return l.items[i] }

// Subtitles lists the item's subtitle tracks.
func (s *Session) Subtitles() *SubtitleList { return &SubtitleList{items: s.subs} }

func subtitleFrom(st *plex.Stream) *Subtitle {
	sub := &Subtitle{
		Index: int(st.Index), Key: st.Key, Codec: st.Codec, Title: st.DisplayTitle,
		Lang: st.LanguageCode, External: st.Key != "", Forced: bool(st.Forced),
		Selected: bool(st.Selected), Burnable: clip.TextSubtitle(st.Codec),
	}
	if sub.External {
		sub.Index = -1
	}
	if sub.Title == "" {
		sub.Title = st.Language
	}
	if sub.Title == "" {
		sub.Title = st.Codec
	}
	return sub
}

// SessionList is a gomobile-friendly slice of sessions.
type SessionList struct{ items []*Session }

func (l *SessionList) Len() int           { return len(l.items) }
func (l *SessionList) Get(i int) *Session { return l.items[i] }

func sessionFrom(ctx context.Context, cl *plex.Client, it *plex.Item) *Session {
	s := &Session{
		RatingKey: it.RatingKey, Label: it.Label(), FileStem: it.FileStem(),
		DurationMs: int64(it.Duration), AudioIndex: -1, SubIndex: -1,
	}
	if it.User != nil {
		s.User = it.User.Title
	}
	if it.Player != nil {
		s.Player, s.Product, s.State = it.Player.Title, it.Player.Product, it.Player.State
	}
	pos, via := cl.LivePosition(ctx, it)
	s.PositionMs, s.PositionVia = pos.Milliseconds(), via
	if p := it.SelectedPart(); p != nil {
		s.PartKey, s.FileName = p.Key, p.File
		if m := it.MediaFor(p); m != nil {
			s.VideoCodec = m.VideoCodec
		}
		if a := p.SelectedStream(plex.StreamAudio); a != nil {
			s.AudioIndex = int(a.Index)
			s.AudioLang = a.LanguageCode
		}
		if sub := p.SelectedStream(plex.StreamSubtitle); sub != nil {
			s.SubIndex, s.SubCodec, s.SubExternal = int(sub.Index), sub.Codec, sub.Key != ""
		}
		streams := p.Streams(plex.StreamSubtitle)
		// A session lists only some streams and some players (Plex for iOS)
		// do not mark the subtitle selection there. The library metadata has
		// every track, with the account's selection.
		if it.Player != nil {
			if full, err := cl.Metadata(ctx, it.RatingKey); err == nil {
				if fp := partByKey(full, p.Key); fp != nil {
					streams = fp.Streams(plex.StreamSubtitle)
				}
			}
		}
		sessionSelected := s.SubIndex
		for i := range streams {
			sub := subtitleFrom(&streams[i])
			if sessionSelected >= 0 && !sub.External {
				sub.Selected = sub.Index == sessionSelected // the player's choice wins when it reports one
			}
			s.subs = append(s.subs, sub)
		}
	}
	return s
}

// partByKey finds the part with the given key, or the selected part.
func partByKey(it *plex.Item, key string) *plex.Part {
	for i := range it.Media {
		for j := range it.Media[i].Part {
			if it.Media[i].Part[j].Key == key {
				return &it.Media[i].Part[j]
			}
		}
	}
	return it.SelectedPart()
}

// NowPlaying lists the video sessions on the server with live positions.
func NowPlaying(serverURL, token, clientID string) (*SessionList, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := plex.New(serverURL, token, clientID)
	items, err := cl.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	out := &SessionList{}
	for i := range items {
		if items[i].IsVideo() {
			out.items = append(out.items, sessionFrom(ctx, cl, &items[i]))
		}
	}
	return out, nil
}

// Item fetches a library item by rating key as a Session with no player
// information, for clipping things that are not currently playing.
func Item(serverURL, token, clientID, ratingKey string) (*Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := plex.New(serverURL, token, clientID)
	it, err := cl.Metadata(ctx, ratingKey)
	if err != nil {
		return nil, err
	}
	s := sessionFrom(ctx, cl, it)
	s.PositionMs, s.PositionVia = -1, ""
	return s, nil
}

// ---------------------------------------------------------------------------
// Frames and clips

func headersFor(token string) string { return "X-Plex-Token: " + token + "\r\n" }

// Frame returns a JPEG of the video at atMs, no wider than maxWidth.
func Frame(serverURL, token, partKey string, atMs int64, maxWidth int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return libav.Thumbnail(ctx, serverURL+partKey, headersFor(token), time.Duration(atMs)*time.Millisecond, maxWidth, "auto")
}

// ClipRequest describes a clip to produce.
type ClipRequest struct {
	ServerURL  string
	Token      string
	PartKey    string
	StartMs    int64
	DurationMs int64
	Output     string // destination .mp4 path
	MaxHeight  int    // 0 keeps the source height
	AudioIndex int    // absolute stream index, -1 for default
	SubIndex   int    // absolute subtitle stream index to burn in, -1 for none
	SubKey     string // server path of a sidecar subtitle file to burn in instead
}

// ProgressListener receives clip progress on a background goroutine.
type ProgressListener interface {
	OnProgress(doneMs, totalMs int64)
	OnDone(err string)
}

// ClipJob is a running clip; Cancel stops it.
type ClipJob struct {
	cancel context.CancelFunc
	once   sync.Once
}

// Cancel aborts the job; OnDone is still called.
func (j *ClipJob) Cancel() { j.once.Do(j.cancel) }

// StartClip begins producing the clip in the background and reports through
// the listener. OnDone receives an empty string on success.
func StartClip(req *ClipRequest, listener ProgressListener) (*ClipJob, error) {
	if req == nil || listener == nil {
		return nil, errors.New("request and listener are required")
	}
	if req.DurationMs <= 0 {
		return nil, errors.New("duration must be positive")
	}
	r := &clip.Request{
		Input:      req.ServerURL + req.PartKey,
		Headers:    headersFor(req.Token),
		Start:      time.Duration(req.StartMs) * time.Millisecond,
		Duration:   time.Duration(req.DurationMs) * time.Millisecond,
		Output:     req.Output,
		MaxHeight:  req.MaxHeight,
		AudioIndex: req.AudioIndex,
		HWAccel:    "auto",
	}
	var subDir string
	switch {
	case req.SubKey != "":
		// Sidecar file: fetch it next to the output, burn it in, remove it.
		dir, err := os.MkdirTemp("", "bananaclip-sub-")
		if err != nil {
			return nil, err
		}
		subDir = dir
		ext := filepath.Ext(strings.SplitN(req.SubKey, "?", 2)[0])
		if ext == "" || len(ext) > 5 {
			ext = ".srt"
		}
		file := filepath.Join(dir, "subs"+ext)
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = plex.New(req.ServerURL, req.Token, "").Download(dctx, req.SubKey, file)
		dcancel()
		if err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("download subtitles: %w", err)
		}
		r.Subtitle = &clip.Subtitle{File: file, Index: -1}
	case req.SubIndex >= 0:
		r.Subtitle = &clip.Subtitle{Index: req.SubIndex}
	}
	var eng libav.Engine
	if err := eng.Preflight(r); err != nil {
		if subDir != "" {
			os.RemoveAll(subDir)
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &ClipJob{cancel: cancel}
	go func() {
		defer cancel()
		if subDir != "" {
			defer os.RemoveAll(subDir)
		}
		err := eng.Run(ctx, r, nil, func(done, total time.Duration) {
			listener.OnProgress(done.Milliseconds(), total.Milliseconds())
		})
		if err != nil {
			listener.OnDone(err.Error())
			return
		}
		listener.OnDone("")
	}()
	return job, nil
}

// Describe returns a one-line summary of what a request would do, for logs.
func Describe(req *ClipRequest) string {
	return fmt.Sprintf("%s @%s +%s → %s", req.PartKey, clip.Clock(time.Duration(req.StartMs)*time.Millisecond),
		time.Duration(req.DurationMs)*time.Millisecond, req.Output)
}
