package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Lyrics is the parsed lyrics payload returned by LRCLib.
type Lyrics struct {
	Available bool   `json:"available"`
	Synced    []Cue  `json:"synced"`
	Plain     string `json:"plain"`
}

// Cue is a single timed lyric line.
type Cue struct {
	StartMs int    `json:"start_ms"`
	Text    string `json:"text"`
}

// LRCLib is the interface for fetching lyrics from LRCLIB.
type LRCLib interface {
	Get(ctx context.Context, artist, title, album string, durationSec int) (Lyrics, error)
}

// lrclibResponse mirrors the relevant fields of the LRCLIB /api/get JSON response.
type lrclibResponse struct {
	Instrumental bool   `json:"instrumental"`
	PlainLyrics  string `json:"plainLyrics"`
	SyncedLyrics string `json:"syncedLyrics"`
}

// LRCLibClient is the real HTTP-backed LRCLib implementation.
type LRCLibClient struct {
	baseURL string
	client  *http.Client
}

// NewLRCLibClient returns a new LRCLibClient targeting baseURL (default: "https://lrclib.net").
func NewLRCLibClient(baseURL string) *LRCLibClient {
	if baseURL == "" {
		baseURL = "https://lrclib.net"
	}
	return &LRCLibClient{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Get fetches lyrics for the given track metadata from LRCLIB's strict /api/get endpoint.
// Returns Lyrics{Available: false} with nil error on a 404 (no match).
// Returns an error on any other non-200 response.
func (c *LRCLibClient) Get(ctx context.Context, artist, title, album string, durationSec int) (Lyrics, error) {
	params := url.Values{}
	params.Set("track_name", title)
	params.Set("artist_name", artist)
	params.Set("album_name", album)
	params.Set("duration", strconv.Itoa(durationSec))

	u := c.baseURL + "/api/get?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Lyrics{}, fmt.Errorf("lrclib: build request: %w", err)
	}
	req.Header.Set("User-Agent", "Cantus/0.1 (https://github.com/cantus-app/cantus; dev)")

	resp, err := c.client.Do(req)
	if err != nil {
		return Lyrics{}, fmt.Errorf("lrclib: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return Lyrics{Available: false}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Lyrics{}, fmt.Errorf("lrclib: unexpected status %d", resp.StatusCode)
	}

	var raw lrclibResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Lyrics{}, fmt.Errorf("lrclib: decode: %w", err)
	}

	// Instrumental tracks have no lyrics to display.
	if raw.Instrumental {
		return Lyrics{Available: false}, nil
	}

	// No lyrics at all.
	if raw.PlainLyrics == "" && raw.SyncedLyrics == "" {
		return Lyrics{Available: false}, nil
	}

	var cues []Cue
	if raw.SyncedLyrics != "" {
		cues = ParseLRC(raw.SyncedLyrics)
	}

	return Lyrics{
		Available: true,
		Synced:    cues,
		Plain:     raw.PlainLyrics,
	}, nil
}
