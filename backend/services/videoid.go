package services

import "regexp"

var videoIDRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ValidVideoID reports whether s matches the YouTube video ID format.
func ValidVideoID(s string) bool {
	return videoIDRegex.MatchString(s)
}
