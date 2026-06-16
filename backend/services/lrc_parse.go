package services

import (
	"regexp"
	"strconv"
	"strings"
)

// lrcLineRE matches a single LRC timestamp line.
// Group 1: minutes, Group 2: seconds, Group 3: sub-second digits (optional), Group 4: lyric text.
var lrcLineRE = regexp.MustCompile(`^\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\](.*)$`)

// ParseLRC parses an LRC-format string and returns a slice of Cue values.
// Empty-text cues (gap markers) are preserved. Lines that don't match the
// timestamp format are silently skipped. Output order matches input order —
// LRCLIB returns cues sorted by time so no re-sort is applied.
func ParseLRC(s string) []Cue {
	var cues []Cue
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		m := lrcLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mins, _ := strconv.Atoi(m[1])
		secs, _ := strconv.Atoi(m[2])
		sub := m[3]
		text := m[4]

		ms := mins*60_000 + secs*1000
		if sub != "" {
			switch len(sub) {
			case 1:
				// single digit: treat as hundreds of ms
				d, _ := strconv.Atoi(sub)
				ms += d * 100
			case 2:
				// two digits: LRC centiseconds → ms
				d, _ := strconv.Atoi(sub)
				ms += d * 10
			case 3:
				// three digits: already ms
				d, _ := strconv.Atoi(sub)
				ms += d
			}
		}

		cues = append(cues, Cue{StartMs: ms, Text: text})
	}
	return cues
}
