package plex

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Timeline is a player's own report of what it is doing, obtained through
// Plex Companion. Unlike the session record on the server, it is live.
type Timeline struct {
	Type      string `xml:"type,attr"`
	State     string `xml:"state,attr"`
	Time      Int    `xml:"time,attr"`
	Duration  Int    `xml:"duration,attr"`
	RatingKey string `xml:"ratingKey,attr"`
}

// Position returns the player's current playback position.
func (t *Timeline) Position() time.Duration { return time.Duration(t.Time) * time.Millisecond }

// UnmarshalXMLAttr lets Int decode from XML attributes too.
func (i *Int) UnmarshalXMLAttr(attr xml.Attr) error {
	return i.UnmarshalJSON([]byte(`"` + attr.Value + `"`))
}

// PlayerTimeline asks the player identified by machineID for its video
// timeline, relayed through the server. Returns an error for players that do
// not support Plex Companion (such as Plex Web).
func (c *Client) PlayerTimeline(ctx context.Context, machineID string) (*Timeline, error) {
	if machineID == "" {
		return nil, fmt.Errorf("player has no machine identifier")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/player/timeline/poll?wait=0&commandID=1", nil)
	if err != nil {
		return nil, err
	}
	c.headers(req)
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("X-Plex-Target-Client-Identifier", machineID)
	req.Header.Set("X-Plex-Device-Name", Product)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("player timeline: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		Timelines []Timeline `xml:"Timeline"`
	}
	if err := xml.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("player timeline: %w", err)
	}
	for i := range out.Timelines {
		if out.Timelines[i].Type == "video" {
			return &out.Timelines[i], nil
		}
	}
	return nil, fmt.Errorf("player timeline: no video timeline in response")
}

// LivePosition returns the best available playback position for a session:
// the player's own report when Plex Companion works, otherwise the server's
// last-known offset. The source string says which one was used.
func (c *Client) LivePosition(ctx context.Context, s *Item) (time.Duration, string) {
	if s.Player != nil {
		tl, err := c.PlayerTimeline(ctx, s.Player.MachineIdentifier)
		if err == nil && (tl.RatingKey == "" || tl.RatingKey == s.RatingKey) && tl.State != "stopped" {
			return tl.Position(), "player"
		}
	}
	return s.Offset(), "server"
}
