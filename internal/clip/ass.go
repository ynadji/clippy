package clip

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// shiftASS rewrites an ASS subtitle file whose timestamps are absolute
// source times so that they are relative to start, keeping only cues that
// overlap [start, start+dur). Cues that straddle the edges are clamped.
func shiftASS(in, out string, start, dur time.Duration) error {
	src, err := os.Open(in)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(out)
	if err != nil {
		return err
	}
	defer dst.Close()
	w := bufio.NewWriter(dst)
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "Dialogue:") {
			w.WriteString(line + "\n")
			continue
		}
		// Dialogue: Layer,Start,End,Style,Name,MarginL,MarginR,MarginV,Effect,Text
		fields := strings.SplitN(line, ",", 10)
		if len(fields) < 10 {
			w.WriteString(line + "\n")
			continue
		}
		s, err1 := parseASSTime(fields[1])
		e, err2 := parseASSTime(fields[2])
		if err1 != nil || err2 != nil {
			w.WriteString(line + "\n")
			continue
		}
		s, e = s-start, e-start
		if e <= 0 || s >= dur {
			continue
		}
		if s < 0 {
			s = 0
		}
		if e > dur {
			e = dur
		}
		fields[1], fields[2] = formatASSTime(s), formatASSTime(e)
		w.WriteString(strings.Join(fields, ",") + "\n")
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return w.Flush()
}

// parseASSTime parses H:MM:SS.cc (centiseconds).
func parseASSTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("bad ASS time %q", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || sec < 0 {
		return 0, fmt.Errorf("bad ASS time %q", s)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second)), nil
}

func formatASSTime(d time.Duration) string {
	cs := (d + 5*time.Millisecond) / (10 * time.Millisecond)
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, (cs/6000)%60, (cs/100)%60, cs%100)
}
