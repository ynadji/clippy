package plex

import (
	"fmt"
	"strings"
	"time"
)

type container struct {
	MediaContainer struct {
		Size     Int    `json:"size"`
		Metadata []Item `json:"Metadata"`
		Hub      []Hub  `json:"Hub"`
	} `json:"MediaContainer"`
}

// Hub is one group of search results.
type Hub struct {
	Type     string `json:"type"`
	Metadata []Item `json:"Metadata"`
}

// Stream types as reported by Plex.
const (
	StreamVideo    = 1
	StreamAudio    = 2
	StreamSubtitle = 3
)

// Item is a movie or episode, optionally with session info attached.
type Item struct {
	RatingKey        string  `json:"ratingKey"`
	Type             string  `json:"type"`
	Title            string  `json:"title"`
	GrandparentTitle string  `json:"grandparentTitle"`
	ParentTitle      string  `json:"parentTitle"`
	ParentIndex      Int     `json:"parentIndex"`
	Index            Int     `json:"index"`
	Year             Int     `json:"year"`
	ViewOffset       Int     `json:"viewOffset"` // ms
	Duration         Int     `json:"duration"`   // ms
	Media            []Media `json:"Media"`
	User             *User   `json:"User,omitempty"`
	Player           *Player `json:"Player,omitempty"`
}

// Media is one version (file set) of an item.
type Media struct {
	ID         Int    `json:"id"`
	VideoCodec string `json:"videoCodec"`
	AudioCodec string `json:"audioCodec"`
	Width      Int    `json:"width"`
	Height     Int    `json:"height"`
	// VideoResolution is Plex's own label: "480", "720", "1080", "4k", "sd".
	VideoResolution string `json:"videoResolution"`
	Container       string `json:"container"`
	Selected        Bool   `json:"selected"`
	Part            []Part `json:"Part"`
}

// Part is one file of a Media.
type Part struct {
	ID       Int      `json:"id"`
	Key      string   `json:"key"`
	File     string   `json:"file"`
	Duration Int      `json:"duration"`
	Selected Bool     `json:"selected"`
	Stream   []Stream `json:"Stream"`
}

// Stream is a video, audio or subtitle stream within a Part.
type Stream struct {
	ID           Int    `json:"id"`
	StreamType   Int    `json:"streamType"`
	Codec        string `json:"codec"`
	Index        Int    `json:"index"` // absolute stream index in the container; -1 for external
	Selected     Bool   `json:"selected"`
	Language     string `json:"language"`
	LanguageCode string `json:"languageCode"`
	Title        string `json:"title"`
	DisplayTitle string `json:"displayTitle"`
	Key          string `json:"key"`  // set for external (sidecar) subtitles
	File         string `json:"file"` // set for external subtitles on some server versions
	Forced       Bool   `json:"forced"`
}

// User is the account a session belongs to.
type User struct {
	ID    Int    `json:"id"`
	Title string `json:"title"`
}

// Player is the device a session is playing on.
type Player struct {
	Title             string `json:"title"`
	Product           string `json:"product"`
	State             string `json:"state"`
	MachineIdentifier string `json:"machineIdentifier"`
}

// IsVideo reports whether the item is a movie or episode.
func (it *Item) IsVideo() bool { return it.Type == "movie" || it.Type == "episode" }

// Label is a human-readable name like "Show S01E02 – Title" or "Title (2020)".
func (it *Item) Label() string {
	if it.Type == "episode" {
		return fmt.Sprintf("%s S%02dE%02d – %s", it.GrandparentTitle, it.ParentIndex, it.Index, it.Title)
	}
	if it.Year > 0 {
		return fmt.Sprintf("%s (%d)", it.Title, it.Year)
	}
	return it.Title
}

// FileStem is a filesystem-safe base name for clips of this item, such as
// "Show_S02E01_Episode_Title" or "Movie_Title".
func (it *Item) FileStem() string {
	var s string
	if it.Type == "episode" {
		s = fmt.Sprintf("%s S%02dE%02d %s", it.GrandparentTitle, it.ParentIndex, it.Index, it.Title)
	} else {
		s = it.Title
	}
	return sanitize(s)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		case r == ' ', r == '_', r == ';', r == ':', r == ',', r == '/':
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_.")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		out = "clip"
	}
	return out
}

// Offset is the current playback position (sessions only).
func (it *Item) Offset() time.Duration { return time.Duration(it.ViewOffset) * time.Millisecond }

// Length is the item's total duration.
func (it *Item) Length() time.Duration { return time.Duration(it.Duration) * time.Millisecond }

// SelectedPart returns the part Plex marked selected, or the first one.
func (it *Item) SelectedPart() *Part {
	var first *Part
	for i := range it.Media {
		for j := range it.Media[i].Part {
			p := &it.Media[i].Part[j]
			if first == nil {
				first = p
			}
			if bool(p.Selected) || bool(it.Media[i].Selected) {
				return p
			}
		}
	}
	return first
}

// SelectedMedia returns the media containing part p.
func (it *Item) MediaFor(p *Part) *Media {
	for i := range it.Media {
		for j := range it.Media[i].Part {
			if &it.Media[i].Part[j] == p {
				return &it.Media[i]
			}
		}
	}
	return nil
}

// Streams returns the part's streams of the given type.
func (p *Part) Streams(typ int) []Stream {
	var out []Stream
	for _, s := range p.Stream {
		if int(s.StreamType) == typ {
			out = append(out, s)
		}
	}
	return out
}

// SelectedStream returns the selected stream of the given type, or nil.
func (p *Part) SelectedStream(typ int) *Stream {
	for i := range p.Stream {
		if int(p.Stream[i].StreamType) == typ && bool(p.Stream[i].Selected) {
			return &p.Stream[i]
		}
	}
	return nil
}

// PartByID finds the part with the given Plex part id.
func (it *Item) PartByID(id Int) *Part {
	if id == 0 {
		return nil
	}
	for i := range it.Media {
		for j := range it.Media[i].Part {
			if it.Media[i].Part[j].ID == id {
				return &it.Media[i].Part[j]
			}
		}
	}
	return nil
}

// StreamByID finds the stream with the given Plex stream id.
func (p *Part) StreamByID(id Int) *Stream {
	if id == 0 {
		return nil
	}
	for i := range p.Stream {
		if p.Stream[i].ID == id {
			return &p.Stream[i]
		}
	}
	return nil
}

// IsTranscode reports whether a session part describes what the server is
// sending a player rather than the file: no key, and streams without index
// numbers. Such a part's streams still carry the ids of the file's streams.
func (p *Part) IsTranscode() bool { return p.Key == "" }

// ActiveStream returns the stream of the given type that is in use: the one
// marked selected, or failing that the only one listed. Sessions from some
// players (Plex for iOS) list just the audio track being played, without
// marking it.
func (p *Part) ActiveStream(typ int) *Stream {
	if s := p.SelectedStream(typ); s != nil {
		return s
	}
	var only *Stream
	for i := range p.Stream {
		if int(p.Stream[i].StreamType) == typ {
			if only != nil {
				return nil
			}
			only = &p.Stream[i]
		}
	}
	return only
}

// Describe returns a short label for a stream.
func (s *Stream) Describe() string {
	parts := []string{}
	if s.DisplayTitle != "" {
		parts = append(parts, s.DisplayTitle)
	} else if s.Language != "" {
		parts = append(parts, s.Language)
	}
	if s.Codec != "" {
		parts = append(parts, s.Codec)
	}
	if s.Key != "" {
		parts = append(parts, "external")
	}
	return strings.Join(parts, ", ")
}
