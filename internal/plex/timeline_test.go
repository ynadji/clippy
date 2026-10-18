package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlayerTimeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/timeline/poll" || r.Header.Get("X-Plex-Target-Client-Identifier") != "abc" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<MediaContainer commandID="1" location="fullScreenVideo">
  <Timeline type="music" state="stopped"/>
  <Timeline type="video" state="paused" time="245123" duration="671000" ratingKey="6134" key="/library/metadata/6134"/>
</MediaContainer>`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "cid")
	tl, err := c.PlayerTimeline(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if tl.State != "paused" || tl.Position().Milliseconds() != 245123 || tl.RatingKey != "6134" {
		t.Errorf("timeline = %+v", tl)
	}
	item := &Item{RatingKey: "6134", ViewOffset: 22000, Player: &Player{MachineIdentifier: "abc"}}
	if pos, via := c.LivePosition(context.Background(), item); via != "player" || pos.Milliseconds() != 245123 {
		t.Errorf("live = %v via %s", pos, via)
	}
	item.Player.MachineIdentifier = "nope"
	if pos, via := c.LivePosition(context.Background(), item); via != "server" || pos.Milliseconds() != 22000 {
		t.Errorf("fallback = %v via %s", pos, via)
	}
}
