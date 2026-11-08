//go:build libav

package mobile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ynadji/clippy/internal/clip"
	"github.com/ynadji/clippy/internal/jellyfin"
)

func newJellyfin(url, token, userID, clientID string) *jellyfin.Client {
	cl := jellyfin.New(url, token, userID, clientID)
	cl.Client, cl.Device = "Banana Clip", "iPhone"
	return cl
}

// JellyfinSignIn checks the address, signs in with a username and
// password, and returns the server to persist. The address may be a full
// URL or a bare host; see jellyfin.Candidates for what is tried. Emby
// servers work the same way.
func JellyfinSignIn(address, username, password, clientID string) (*Server, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, info, err := jellyfin.Probe(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("could not find a server at %q: %w", address, err)
	}
	cl := newJellyfin(base, "", "", clientID)
	auth, err := cl.Login(ctx, username, password)
	if err != nil {
		return nil, err
	}
	name := info.ServerName
	if name == "" {
		name = base
	}
	return &Server{
		Kind: KindJellyfin, Name: name, URL: base, Token: auth.AccessToken,
		UserID: auth.User.ID, ClientID: clientID, Owned: true,
	}, nil
}

type jellyfinBackend struct{ cl *jellyfin.Client }

func (b jellyfinBackend) headers() string { return b.cl.FFmpegHeaders() }
func (b jellyfinBackend) download(ctx context.Context, path, dest string) error {
	return b.cl.Download(ctx, path, dest)
}

func (b jellyfinBackend) nowPlaying(ctx context.Context) ([]*Session, error) {
	sessions, err := b.cl.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Session
	for i := range sessions {
		ss := &sessions[i]
		if ss.NowPlayingItem == nil || !ss.NowPlayingItem.IsVideo() {
			continue
		}
		// The session's item is a summary; the library record has every
		// stream of every source.
		it, err := b.cl.Item(ctx, ss.NowPlayingItem.ID)
		if err != nil {
			it = ss.NowPlayingItem
		}
		s := jellyfinSession(it, ss)
		out = append(out, s)
	}
	return out, nil
}

func (b jellyfinBackend) item(ctx context.Context, key string) (*Session, error) {
	it, err := b.cl.Item(ctx, key)
	if err != nil {
		return nil, err
	}
	if !it.IsVideo() {
		return nil, errors.New("not a movie or episode")
	}
	return jellyfinSession(it, nil), nil
}

// jellyfinSession maps an item (and the session playing it, if any) onto
// the app's Session.
func jellyfinSession(it *jellyfin.Item, ss *jellyfin.Session) *Session {
	s := &Session{
		RatingKey: it.ID, Label: it.Label(), FileStem: it.FileStem(),
		DurationMs: it.RunTimeTicks.Milliseconds(), AudioIndex: -1, SubIndex: -1,
	}
	var ps *jellyfin.PlayState
	if ss != nil {
		ps = &ss.PlayState
		s.User, s.Player, s.Product, s.State = ss.UserName, ss.DeviceName, ss.Client, ss.State()
		s.PositionMs, s.PositionVia = ps.PositionTicks.Milliseconds(), "server"
	}
	sourceID := ""
	if ps != nil {
		sourceID = ps.MediaSourceID
	}
	src := it.Source(sourceID)
	if src == nil {
		return s
	}
	if src.RunTimeTicks > 0 {
		s.DurationMs = src.RunTimeTicks.Milliseconds()
	}
	s.PartKey, s.FileName = jellyfin.MediaPath(it.ID, src.ID), src.FileName()
	if v := src.Streams(jellyfin.StreamVideo); len(v) > 0 {
		s.VideoCodec, s.VideoWidth, s.VideoHeight = v[0].Codec, v[0].Width, v[0].Height
		s.VideoClass = clip.ResolutionClass(s.VideoWidth, s.VideoHeight)
	}
	// Jellyfin numbers streams its own way (external subtitles first, then
	// the file's streams), so tracks are handed to the engine by their
	// position among streams of their type, which survives the renumbering.
	position := func(st *jellyfin.MediaStream) int {
		n := 0
		for _, o := range src.Streams(st.Type) {
			if !o.IsExternal {
				n++
			}
			if o.Index == st.Index {
				return n
			}
		}
		return 0
	}
	// Audio: what the player is on, else the source's default, else the
	// first track.
	audio := src.Streams(jellyfin.StreamAudio)
	var a *jellyfin.MediaStream
	if ps != nil && ps.AudioStreamIndex != nil {
		a = src.Stream(*ps.AudioStreamIndex)
	}
	if a == nil && src.DefaultAudioStreamIndex != nil {
		a = src.Stream(*src.DefaultAudioStreamIndex)
	}
	if a == nil && len(audio) > 0 {
		a = &audio[0]
	}
	if a != nil && a.Type == jellyfin.StreamAudio {
		s.AudioIndex, s.AudioLang = position(a), a.Language
	}
	// Subtitles: every track; the one the player shows is selected. When
	// nothing is playing, the source's default stands in.
	selected := -1
	if ps != nil && ps.SubtitleStreamIndex != nil {
		selected = *ps.SubtitleStreamIndex
	} else if ps == nil && src.DefaultSubtitleStreamIndex != nil {
		selected = *src.DefaultSubtitleStreamIndex
	}
	for _, st := range src.Streams(jellyfin.StreamSubtitle) {
		st := st
		sub := &Subtitle{
			Index: position(&st), Codec: st.Codec, Title: st.DisplayTitle, Lang: st.Language,
			External: st.IsExternal, Forced: st.IsForced, Selected: st.Index == selected,
			Burnable: st.IsTextSubtitleStream || clip.TextSubtitle(st.Codec),
		}
		if sub.Title == "" {
			sub.Title = st.Title
		}
		if sub.Title == "" {
			sub.Title = st.Codec
		}
		if sub.External {
			sub.Key = jellyfin.SubtitlePath(it.ID, src.ID, &st)
			sub.Index = -1
		}
		if sub.Selected {
			s.SubIndex, s.SubCodec, s.SubExternal = sub.Index, sub.Codec, sub.External
		}
		s.subs = append(s.subs, sub)
	}
	return s
}
