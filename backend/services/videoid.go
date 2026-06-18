package services

import "cantus/backend/models"

// ValidVideoID reports whether s matches the YouTube video ID format.
// The canonical regex lives in models.ValidVideoID; this re-exports it for
// handler and service code that imports services but not models directly.
func ValidVideoID(s string) bool {
	return models.ValidVideoID(s)
}
