package plex

import (
	"encoding/json"
	"testing"
)

func TestFlexibleNumbers(t *testing.T) {
	// Field shapes as seen in the wild from /status/sessions.
	raw := `{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":"48213","type":"episode","title":"Ep","grandparentTitle":"Show",
	 "parentIndex":"2","index":3,"viewOffset":"771000","duration":2890000,
	 "Media":[{"id":"1234","videoCodec":"hevc","selected":true,"Part":[{"id":"5678","key":"/library/parts/5678/1/file.mkv","file":"/data/x.mkv","selected":"1",
	   "Stream":[{"id":"1","streamType":"1","codec":"hevc","index":0,"selected":true},
	             {"id":"2","streamType":2,"codec":"aac","index":"1","selected":"1"},
	             {"id":"3","streamType":3,"codec":"srt","index":"2","selected":false}]}]}],
	 "User":{"id":"1","title":"yacin"},"Player":{"title":"TV","state":"paused"}}]}}`
	var c container
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	it := c.MediaContainer.Metadata[0]
	if it.Label() != "Show S02E03 – Ep" {
		t.Errorf("label = %q", it.Label())
	}
	if it.Offset().Seconds() != 771 {
		t.Errorf("offset = %v", it.Offset())
	}
	p := it.SelectedPart()
	if p == nil || p.Key != "/library/parts/5678/1/file.mkv" {
		t.Fatalf("part = %+v", p)
	}
	if a := p.SelectedStream(StreamAudio); a == nil || a.Index != 1 {
		t.Errorf("selected audio = %+v", a)
	}
	if subs := p.Streams(StreamSubtitle); len(subs) != 1 || subs[0].Selected {
		t.Errorf("subs = %+v", subs)
	}
}

func TestActiveStream(t *testing.T) {
	// Plex for iOS: one audio stream listed, not marked selected.
	p := Part{Stream: []Stream{{StreamType: 1, Index: 0}, {StreamType: 2, Index: 2}, {StreamType: 3, Index: 3}}}
	if a := p.ActiveStream(StreamAudio); a == nil || a.Index != 2 {
		t.Errorf("sole unmarked audio stream should be active, got %+v", a)
	}
	// A marked stream wins over position.
	p = Part{Stream: []Stream{{StreamType: 2, Index: 1}, {StreamType: 2, Index: 2, Selected: true}}}
	if a := p.ActiveStream(StreamAudio); a == nil || a.Index != 2 {
		t.Errorf("selected stream should be active, got %+v", a)
	}
	// Several unmarked streams: unknown.
	p = Part{Stream: []Stream{{StreamType: 2, Index: 1}, {StreamType: 2, Index: 2}}}
	if a := p.ActiveStream(StreamAudio); a != nil {
		t.Errorf("ambiguous streams should give nil, got %+v", a)
	}
}

func TestTranscodeSessionLookups(t *testing.T) {
	// A transcoding session: the part has no key and its streams no index,
	// but their ids match the file's streams.
	session := Part{ID: 157556, Stream: []Stream{
		{ID: 313987, StreamType: 1, Codec: "h264"},
		{ID: 313988, StreamType: 2, Codec: "aac", Selected: true, LanguageCode: "zho"},
	}}
	if !session.IsTranscode() {
		t.Error("a keyless part should count as a transcode")
	}
	file := Item{Media: []Media{{Part: []Part{{ID: 157556, Key: "/library/parts/157556/1/file.mkv", Stream: []Stream{
		{ID: 313987, StreamType: 1, Index: 0, Codec: "h264"},
		{ID: 313988, StreamType: 2, Index: 1, Codec: "flac", LanguageCode: "zho"},
		{ID: 313989, StreamType: 2, Index: 3, Codec: "flac", LanguageCode: "eng"},
	}}}}}}
	fp := file.PartByID(157556)
	if fp == nil || fp.Key == "" {
		t.Fatal("part not found by id")
	}
	a := FileStream(session.ActiveStream(StreamAudio), fp)
	if a == nil || a.Index != 1 || a.Codec != "flac" {
		t.Errorf("session audio should map to the file's stream 1 (flac), got %+v", a)
	}
	if s := FileStream(&Stream{ID: 999}, fp); s == nil || s.ID != 999 {
		t.Error("an unknown stream should fall back to itself")
	}
}
