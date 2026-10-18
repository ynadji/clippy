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
