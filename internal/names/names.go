// Package names builds display labels and file-safe stems for media items,
// the same way for every server kind.
package names

import (
	"fmt"
	"strings"
)

// Episode is "Show S01E02 – Title".
func Episode(show string, season, episode int, title string) string {
	return fmt.Sprintf("%s S%02dE%02d – %s", show, season, episode, title)
}

// Movie is "Title (Year)", or just the title without a year. A title that
// already ends in the year (an unmatched file named that way) is kept.
func Movie(title string, year int) string {
	if year > 0 && !strings.HasSuffix(title, fmt.Sprintf("(%d)", year)) {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// EpisodeStem is a file-safe "Show_S01E02_Title".
func EpisodeStem(show string, season, episode int, title string) string {
	return Sanitize(fmt.Sprintf("%s S%02dE%02d %s", show, season, episode, title))
}

// Sanitize keeps letters, digits, '-' and '.', turns separators into '_'
// and drops everything else. Empty input becomes "clip".
func Sanitize(s string) string {
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
