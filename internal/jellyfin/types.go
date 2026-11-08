package jellyfin

import (
	"path"
	"strings"
	"time"

	"github.com/ynadji/clippy/internal/names"
)

// Jellyfin reports time in ticks of 100 ns.
const ticksPerMillisecond = 10_000

// Ticks is a duration in Jellyfin's 100 ns ticks.
type Ticks int64

// Duration converts ticks to a time.Duration.
func (t Ticks) Duration() time.Duration { return time.Duration(t) * 100 * time.Nanosecond }

// Milliseconds converts ticks to whole milliseconds.
func (t Ticks) Milliseconds() int64 { return int64(t) / ticksPerMillisecond }

// PublicInfo is what an unauthenticated GET /System/Info/Public returns.
type PublicInfo struct {
	ServerName   string `json:"ServerName"`
	Version      string `json:"Version"`
	ProductName  string `json:"ProductName"` // "Jellyfin Server"; Emby leaves it empty
	ID           string `json:"Id"`
	LocalAddress string `json:"LocalAddress"`
}

// AuthResult is a successful sign-in.
type AuthResult struct {
	AccessToken string `json:"AccessToken"`
	ServerID    string `json:"ServerId"`
	User        User   `json:"User"`
}

// User is a Jellyfin account on the server.
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// Session is one connected client, with what it is playing if anything.
type Session struct {
	ID             string    `json:"Id"`
	UserID         string    `json:"UserId"`
	UserName       string    `json:"UserName"`
	Client         string    `json:"Client"`
	DeviceName     string    `json:"DeviceName"`
	DeviceID       string    `json:"DeviceId"`
	NowPlayingItem *Item     `json:"NowPlayingItem"`
	PlayState      PlayState `json:"PlayState"`
}

// PlayState is the player's position and track choices.
type PlayState struct {
	PositionTicks       Ticks  `json:"PositionTicks"`
	IsPaused            bool   `json:"IsPaused"`
	MediaSourceID       string `json:"MediaSourceId"`
	AudioStreamIndex    *int   `json:"AudioStreamIndex"`
	SubtitleStreamIndex *int   `json:"SubtitleStreamIndex"`
	PlayMethod          string `json:"PlayMethod"` // DirectPlay, DirectStream, Transcode
}

// State names the playback state the way Plex does: playing or paused.
func (s *Session) State() string {
	if s.PlayState.IsPaused {
		return "paused"
	}
	return "playing"
}

// Item is a library item. Movies and episodes carry their media sources.
type Item struct {
	ID                string        `json:"Id"`
	Name              string        `json:"Name"`
	Type              string        `json:"Type"` // Movie, Episode, ...
	SeriesName        string        `json:"SeriesName"`
	ParentIndexNumber int           `json:"ParentIndexNumber"` // season
	IndexNumber       int           `json:"IndexNumber"`       // episode
	ProductionYear    int           `json:"ProductionYear"`
	RunTimeTicks      Ticks         `json:"RunTimeTicks"`
	Path              string        `json:"Path"`
	Container         string        `json:"Container"`
	MediaSources      []MediaSource `json:"MediaSources"`
	MediaStreams      []MediaStream `json:"MediaStreams"`
}

// MediaSource is one file (or version) of an item.
type MediaSource struct {
	ID                         string        `json:"Id"`
	Name                       string        `json:"Name"`
	Path                       string        `json:"Path"`
	Container                  string        `json:"Container"`
	RunTimeTicks               Ticks         `json:"RunTimeTicks"`
	MediaStreams               []MediaStream `json:"MediaStreams"`
	DefaultAudioStreamIndex    *int          `json:"DefaultAudioStreamIndex"`
	DefaultSubtitleStreamIndex *int          `json:"DefaultSubtitleStreamIndex"`
}

// MediaStream is one stream of a media source. Index is the stream's
// position in the file for embedded streams; external subtitle files get
// indexes after the embedded streams.
type MediaStream struct {
	Index                int    `json:"Index"`
	Type                 string `json:"Type"` // Video, Audio, Subtitle, ...
	Codec                string `json:"Codec"`
	Language             string `json:"Language"`
	DisplayTitle         string `json:"DisplayTitle"`
	Title                string `json:"Title"`
	Path                 string `json:"Path"`
	IsDefault            bool   `json:"IsDefault"`
	IsForced             bool   `json:"IsForced"`
	IsExternal           bool   `json:"IsExternal"`
	IsTextSubtitleStream bool   `json:"IsTextSubtitleStream"`
	Width                int    `json:"Width"`
	Height               int    `json:"Height"`
}

// Stream types.
const (
	StreamVideo    = "Video"
	StreamAudio    = "Audio"
	StreamSubtitle = "Subtitle"
)

// IsVideo reports whether the item is a movie or episode.
func (it *Item) IsVideo() bool { return it.Type == "Movie" || it.Type == "Episode" }

// Label is a human-readable name like "Show S01E02 – Title" or "Title (2020)".
func (it *Item) Label() string {
	if it.Type == "Episode" {
		return names.Episode(it.SeriesName, it.ParentIndexNumber, it.IndexNumber, it.Name)
	}
	return names.Movie(it.Name, it.ProductionYear)
}

// FileStem is a filesystem-safe base name for clips of this item.
func (it *Item) FileStem() string {
	if it.Type == "Episode" {
		return names.EpisodeStem(it.SeriesName, it.ParentIndexNumber, it.IndexNumber, it.Name)
	}
	return names.Sanitize(it.Name)
}

// Source returns the media source with the given id, the only source, or
// the first one. Nil when the item has none.
func (it *Item) Source(id string) *MediaSource {
	for i := range it.MediaSources {
		if it.MediaSources[i].ID == id {
			return &it.MediaSources[i]
		}
	}
	if len(it.MediaSources) > 0 {
		return &it.MediaSources[0]
	}
	return nil
}

// Streams returns the source's streams of one type, in file order.
func (s *MediaSource) Streams(typ string) []MediaStream {
	var out []MediaStream
	for _, st := range s.MediaStreams {
		if st.Type == typ {
			out = append(out, st)
		}
	}
	return out
}

// Stream returns the stream with the given index, or nil.
func (s *MediaSource) Stream(index int) *MediaStream {
	for i := range s.MediaStreams {
		if s.MediaStreams[i].Index == index {
			return &s.MediaStreams[i]
		}
	}
	return nil
}

// FileName is the base name of the source's file.
func (s *MediaSource) FileName() string {
	if s.Path == "" {
		return s.Name
	}
	return path.Base(strings.ReplaceAll(s.Path, "\\", "/"))
}

// Describe is a one-line summary of a stream for listings.
func (st *MediaStream) Describe() string {
	parts := []string{strings.ToLower(st.Type), st.Codec}
	if st.Language != "" {
		parts = append(parts, st.Language)
	}
	if st.DisplayTitle != "" {
		parts = append(parts, st.DisplayTitle)
	}
	if st.IsExternal {
		parts = append(parts, "external")
	}
	return strings.Join(parts, " ")
}
