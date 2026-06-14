package services

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/raitonoberu/ytmusic"

	"cantus/backend/models"
)

// SearchPager abstracts the raitonoberu ytmusic SearchClient so tests can fake it.
type SearchPager interface {
	Next() (*ytmusic.SearchResult, error)
}

// SearchPagerFactory builds a fresh pager per query.
type SearchPagerFactory func(query string) SearchPager

// nonStudioRE matches parenthetical/bracket tags like "(Live)", "[Acoustic]",
// "(Karaoke Version)" — same keyword set as the prior Python filter.
var nonStudioRE = regexp.MustCompile(
	`(?i)[\(\[][^)\]]*\b(live|session|acoustic|unplugged|karaoke|demo|bootleg|remix|cover|instrumental)\b[^)\]]*[\)\]]`,
)

// cacheEntry mirrors the Python (mapped_list, is_exhausted) semantics.
type cacheEntry struct {
	items     []models.SearchResult
	exhausted bool
	expiresAt time.Time
}

// YTMusicSearch wraps the raitonoberu/ytmusic client with TTL cache,
// non-studio filter, HMAC-signed videoIds, and the SearchPage wire shape.
type YTMusicSearch struct {
	factory SearchPagerFactory
	signer  *Signer
	ttl     time.Duration
	maxSize int

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewYTMusicSearch builds a YTMusicSearch. factory returns a fresh pager per
// call so each query gets its own continuation state.
func NewYTMusicSearch(factory SearchPagerFactory, signer *Signer, ttl time.Duration, maxSize int) *YTMusicSearch {
	return &YTMusicSearch{
		factory: factory,
		signer:  signer,
		ttl:     ttl,
		maxSize: maxSize,
		cache:   make(map[string]cacheEntry),
	}
}

// NewYTMusicSearchProd returns a YTMusicSearch wired to the real raitonoberu
// client via TrackSearch (song-entity filter).
func NewYTMusicSearchProd(signer *Signer, ttl time.Duration, maxSize int) *YTMusicSearch {
	return NewYTMusicSearch(
		func(q string) SearchPager { return ytmusic.TrackSearch(q) },
		signer, ttl, maxSize,
	)
}

// Search returns a page of song-entity results, signing each videoId.
func (s *YTMusicSearch) Search(ctx context.Context, query string, limit, offset int) (SearchPage, error) {
	if err := ctx.Err(); err != nil {
		return SearchPage{}, fmt.Errorf("ytmusic search: %w", err)
	}
	need := offset + limit

	s.mu.Lock()
	entry, ok := s.cache[query]
	if ok && time.Now().After(entry.expiresAt) {
		delete(s.cache, query)
		ok = false
	}
	s.mu.Unlock()

	if !ok {
		entry = cacheEntry{}
	}

	if !ok || (len(entry.items) < need && !entry.exhausted) {
		pager := s.factory(query)
		var mapped []models.SearchResult
		exhausted := false
		const maxPages = 10
		pages := 0
		for len(mapped) < need {
			if pages >= maxPages {
				exhausted = true
				break
			}
			pages++
			res, err := pager.Next()
			if err != nil {
				return SearchPage{}, fmt.Errorf("ytmusic search: %w", err)
			}
			if res == nil || len(res.Tracks) == 0 {
				exhausted = true
				break
			}
			for _, tr := range res.Tracks {
				if !ValidVideoID(tr.VideoID) {
					continue
				}
				if nonStudioRE.MatchString(tr.Title) {
					continue
				}
				mapped = append(mapped, s.mapItem(tr))
			}
		}
		entry = cacheEntry{items: mapped, exhausted: exhausted, expiresAt: time.Now().Add(s.ttl)}
		s.mu.Lock()
		if len(s.cache) >= s.maxSize {
			s.evictOneLocked()
		}
		s.cache[query] = entry
		s.mu.Unlock()
	}

	end := offset + limit
	if end > len(entry.items) {
		end = len(entry.items)
	}
	start := offset
	if start > len(entry.items) {
		start = len(entry.items)
	}
	page := append([]models.SearchResult(nil), entry.items[start:end]...)
	hasMore := len(entry.items) > offset+limit
	return SearchPage{Items: page, HasMore: hasMore}, nil
}

func (s *YTMusicSearch) mapItem(t *ytmusic.TrackItem) models.SearchResult {
	artist := ""
	for i, a := range t.Artists {
		if i > 0 {
			artist += ", "
		}
		artist += a.Name
	}
	thumb := ""
	if n := len(t.Thumbnails); n > 0 {
		thumb = t.Thumbnails[n-1].URL
	}
	return models.SearchResult{
		VideoID:      t.VideoID,
		Sig:          s.signer.Sign(t.VideoID),
		Title:        t.Title,
		Artist:       artist,
		Album:        t.Album.Name,
		DurationSec:  t.Duration,
		ThumbnailURL: thumb,
	}
}

// evictOneLocked drops one already-expired entry; if none expired, drops an
// arbitrary entry. Best-effort: this is a 256-entry beta cache, not an LRU.
func (s *YTMusicSearch) evictOneLocked() {
	now := time.Now()
	for k, v := range s.cache {
		if now.After(v.expiresAt) {
			delete(s.cache, k)
			return
		}
	}
	for k := range s.cache {
		delete(s.cache, k)
		return
	}
}
