package clip

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseTime accepts "1:23:45", "12:34", "12:34.5", "90", "90.5", "1m30s" and
// "20s" and returns a duration.
func ParseTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		if len(parts) > 3 {
			return 0, fmt.Errorf("bad time %q", s)
		}
		var total float64
		for _, p := range parts {
			v, err := strconv.ParseFloat(p, 64)
			if err != nil || v < 0 {
				return 0, fmt.Errorf("bad time %q", s)
			}
			total = total*60 + v
		}
		return time.Duration(total * float64(time.Second)), nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		if v < 0 {
			return 0, fmt.Errorf("bad time %q", s)
		}
		return time.Duration(v * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad time %q (use 12:34, 90, 90.5 or 1m30s)", s)
	}
	return d, nil
}

// Clock formats a duration as H:MM:SS.
func Clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(math.Round(d.Seconds()))
	return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}

// Stamp formats a duration for file names, e.g. "12m34s" or "1h02m03s".
func Stamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(math.Round(d.Seconds()))
	h, m, sec := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, sec)
	}
	return fmt.Sprintf("%dm%02ds", m, sec)
}

// Seconds formats a duration for ffmpeg arguments.
func Seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
