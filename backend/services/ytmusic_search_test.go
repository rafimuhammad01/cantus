package services_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raitonoberu/ytmusic"

	"cantus/backend/services"
)

// fakePager returns canned pages on successive Next() calls.
type fakePager struct {
	pages [][]*ytmusic.TrackItem
	err   error
	calls int
}

func (f *fakePager) Next() (*ytmusic.SearchResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.calls >= len(f.pages) {
		return &ytmusic.SearchResult{Tracks: nil}, nil
	}
	page := f.pages[f.calls]
	f.calls++
	return &ytmusic.SearchResult{Tracks: page}, nil
}

// infiniteLivePager always returns a page of "(Live)" tracks that all fail the
// non-studio filter. Without a page-count cap the drain loop would spin forever.
type infiniteLivePager struct{}

func (p *infiniteLivePager) Next() (*ytmusic.SearchResult, error) {
	return &ytmusic.SearchResult{Tracks: []*ytmusic.TrackItem{
		track("aaaaaaaaaaa", "Yesterday (Live)", "Beatles", "", 180, "u"),
		track("bbbbbbbbbbb", "Yesterday (Live)", "Beatles", "", 180, "u"),
	}}, nil
}

func track(videoID, title, artist, album string, durSec int, thumbURL string) *ytmusic.TrackItem {
	return &ytmusic.TrackItem{
		VideoID:    videoID,
		Title:      title,
		Artists:    []ytmusic.Artist{{Name: artist}},
		Album:      ytmusic.Album{Name: album},
		Duration:   durSec,
		Thumbnails: []ytmusic.Thumbnail{{URL: thumbURL}},
	}
}

func TestYTMusicSearch(t *testing.T) {
	tests := []struct {
		name        string
		pager       *fakePager
		query       string
		limit       int
		offset      int
		wantIDs     []string
		wantHasMore bool
		wantErr     bool
	}{
		{
			name: "happy path maps fields and signs",
			pager: &fakePager{pages: [][]*ytmusic.TrackItem{{
				track("dQw4w9WgXcQ", "Bohemian Rhapsody", "Queen", "A Night At The Opera", 354, "https://t/1.jpg"),
				track("aaaaaaaaaaa", "Another", "Artist", "Album", 200, "https://t/2.jpg"),
			}}},
			query: "queen", limit: 10, offset: 0,
			wantIDs:     []string{"dQw4w9WgXcQ", "aaaaaaaaaaa"},
			wantHasMore: false,
		},
		{
			name: "drops non-studio titles",
			pager: &fakePager{pages: [][]*ytmusic.TrackItem{{
				track("aaaaaaaaaaa", "Yesterday (Live at Abbey Road)", "Beatles", "", 180, "u"),
				track("bbbbbbbbbbb", "Yesterday", "Beatles", "", 180, "u"),
				track("ccccccccccc", "Yesterday (Acoustic Version)", "Beatles", "", 180, "u"),
			}}},
			query: "yesterday", limit: 10, offset: 0,
			wantIDs: []string{"bbbbbbbbbbb"},
		},
		{
			name: "drops invalid videoIds",
			pager: &fakePager{pages: [][]*ytmusic.TrackItem{{
				track("short", "Title", "Artist", "", 1, "u"),
				track("aaaaaaaaaaa", "Title2", "Artist", "", 1, "u"),
			}}},
			query: "q", limit: 10, offset: 0,
			wantIDs: []string{"aaaaaaaaaaa"},
		},
		{
			name: "pagination accumulates across Next() calls",
			pager: &fakePager{pages: [][]*ytmusic.TrackItem{
				{track("aaaaaaaaaaa", "T1", "A", "", 1, "u"), track("bbbbbbbbbbb", "T2", "A", "", 1, "u")},
				{track("ccccccccccc", "T3", "A", "", 1, "u"), track("ddddddddddd", "T4", "A", "", 1, "u")},
			}},
			query: "q", limit: 2, offset: 2,
			wantIDs:     []string{"ccccccccccc", "ddddddddddd"},
			wantHasMore: false,
		},
		{
			name:  "upstream error surfaces",
			pager: &fakePager{err: errors.New("network")},
			query: "q", limit: 5, offset: 0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signer := newTestSigner(t)
			s := services.NewYTMusicSearch(
				func(string) services.SearchPager { return tt.pager },
				signer, 600*time.Second, 256,
			)
			got, err := s.Search(context.Background(), tt.query, tt.limit, tt.offset)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			gotIDs := make([]string, len(got.Items))
			for i, it := range got.Items {
				gotIDs[i] = it.VideoID
				if it.Sig == "" {
					t.Errorf("item %d: empty Sig", i)
				}
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("ids: got %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("id[%d]: got %q, want %q", i, gotIDs[i], tt.wantIDs[i])
				}
			}
			if got.HasMore != tt.wantHasMore {
				t.Errorf("HasMore: got %v, want %v", got.HasMore, tt.wantHasMore)
			}
		})
	}
}

// TestYTMusicSearch_MaxPagesCap verifies the drain loop exits rather than
// spinning forever when every page only contains tracks that fail the
// non-studio filter.
func TestYTMusicSearch_MaxPagesCap(t *testing.T) {
	tests := []struct {
		name        string
		wantItems   int
		wantHasMore bool
	}{
		{
			name:        "infinite live-only pager is bounded by maxPages and returns empty",
			wantItems:   0,
			wantHasMore: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signer := newTestSigner(t)
			s := services.NewYTMusicSearch(
				func(string) services.SearchPager { return &infiniteLivePager{} },
				signer, 600*time.Second, 256,
			)
			got, err := s.Search(context.Background(), "yesterday", 5, 0)
			if err != nil {
				t.Fatalf("Search: unexpected error: %v", err)
			}
			if len(got.Items) != tt.wantItems {
				t.Errorf("items: got %d, want %d", len(got.Items), tt.wantItems)
			}
			if got.HasMore != tt.wantHasMore {
				t.Errorf("HasMore: got %v, want %v", got.HasMore, tt.wantHasMore)
			}
		})
	}
}

func TestYTMusicSearch_CacheHit(t *testing.T) {
	tests := []struct{ name string }{{name: "second call with same query does not hit pager"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pager := &fakePager{pages: [][]*ytmusic.TrackItem{{
				track("aaaaaaaaaaa", "T", "A", "", 1, "u"),
			}}}
			signer := newTestSigner(t)
			factoryCalls := 0
			s := services.NewYTMusicSearch(
				func(string) services.SearchPager { factoryCalls++; return pager },
				signer, 600*time.Second, 256,
			)
			ctx := context.Background()
			if _, err := s.Search(ctx, "q", 1, 0); err != nil {
				t.Fatalf("first: %v", err)
			}
			if _, err := s.Search(ctx, "q", 1, 0); err != nil {
				t.Fatalf("second: %v", err)
			}
			if factoryCalls != 1 {
				t.Errorf("factoryCalls: got %d, want 1", factoryCalls)
			}
		})
	}
}
