package models

type SearchResult struct {
	VideoID      string `json:"video_id"`
	Sig          string `json:"sig"`
	LyricsSig    string `json:"lyrics_sig"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Album        string `json:"album"`
	DurationSec  int    `json:"duration_sec"`
	ThumbnailURL string `json:"thumbnail_url"`
}
