package main

import (
	"testing"
	"time"
)

func TestResolveRange(t *testing.T) {
	sec := func(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }
	length := sec(600)
	cases := []struct {
		name       string
		f          clipFlags
		anchor     time.Duration
		start, dur time.Duration
		wantErr    bool
	}{
		{"start/end", clipFlags{start: "1:00", end: "1:15"}, -1, sec(60), sec(15), false},
		{"start/duration", clipFlags{start: "90", duration: "20s"}, -1, sec(90), sec(20), false},
		{"last", clipFlags{last: "20s"}, sec(100), sec(80), sec(20), false},
		{"last clamps at zero", clipFlags{last: "20s"}, sec(5), 0, sec(5), false},
		{"before/after", clipFlags{before: "10", after: "5"}, sec(100), sec(90), sec(15), false},
		{"after only", clipFlags{after: "5"}, sec(100), sec(100), sec(5), false},
		{"at overrides anchor", clipFlags{last: "10", at: "3:00"}, -1, sec(170), sec(10), false},
		{"clamps to length", clipFlags{start: "595", duration: "30"}, -1, sec(595), sec(5), false},
		{"last without session", clipFlags{last: "10"}, -1, 0, 0, true},
		{"end before start", clipFlags{start: "20", end: "10"}, -1, 0, 0, true},
		{"nothing", clipFlags{}, sec(100), 0, 0, true},
		{"start alone", clipFlags{start: "20"}, -1, 0, 0, true},
	}
	for _, c := range cases {
		f := c.f
		start, dur, err := resolveRange(&f, c.anchor, length)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got %v/%v", c.name, start, dur)
			}
			continue
		}
		if err != nil || start != c.start || dur != c.dur {
			t.Errorf("%s: got %v/%v/%v, want %v/%v", c.name, start, dur, err, c.start, c.dur)
		}
	}
}
