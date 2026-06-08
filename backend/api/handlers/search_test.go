package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cantus/backend/api/handlers"
	"cantus/backend/models"
	"cantus/backend/services"
)

type fakeYouTube struct {
	page         services.SearchPage
	err          error
	calledQuery  string
	calledLimit  int
	calledOffset int
}

func (f *fakeYouTube) Search(ctx context.Context, query string, limit, offset int) (services.SearchPage, error) {
	f.calledQuery, f.calledLimit, f.calledOffset = query, limit, offset
	return f.page, f.err
}

func (f *fakeYouTube) DownloadPreview(_ context.Context, _ string) error { return nil }
func (f *fakeYouTube) DownloadFull(_ context.Context, _ string) error    { return nil }

func TestSearchHandler(t *testing.T) {
	twoItems := []models.SearchResult{
		{VideoID: "aaaaaaaaaa1", Sig: "sig1", Title: "Song A", Artist: "Artist A"},
		{VideoID: "aaaaaaaaaa2", Sig: "sig2", Title: "Song B", Artist: "Artist B"},
	}

	tests := []struct {
		name             string
		url              string
		fakePage         services.SearchPage
		fakeErr          error
		wantStatus       int
		wantItemCount    int
		wantHasMore      bool
		wantErrorField   bool
		wantCalledQuery  string
		wantCalledLimit  int
		wantCalledOffset int
		wantEmptySlice   bool
	}{
		{
			name:             "happy path with defaults",
			url:              "/api/songs/search?q=test",
			fakePage:         services.SearchPage{Items: twoItems, HasMore: true},
			wantStatus:       http.StatusOK,
			wantItemCount:    2,
			wantHasMore:      true,
			wantCalledQuery:  "test",
			wantCalledLimit:  10,
			wantCalledOffset: 0,
		},
		{
			name:             "happy path with explicit limit and offset",
			url:              "/api/songs/search?q=foo&limit=5&offset=20",
			fakePage:         services.SearchPage{Items: twoItems, HasMore: false},
			wantStatus:       http.StatusOK,
			wantCalledQuery:  "foo",
			wantCalledLimit:  5,
			wantCalledOffset: 20,
		},
		{
			name:           "missing q",
			url:            "/api/songs/search",
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "empty q",
			url:            "/api/songs/search?q=",
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "q too long (201 chars)",
			url:            "/api/songs/search?q=" + strings.Repeat("a", 201),
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "limit=0",
			url:            "/api/songs/search?q=test&limit=0",
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "limit=21",
			url:            "/api/songs/search?q=test&limit=21",
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "limit non-numeric",
			url:            "/api/songs/search?q=test&limit=abc",
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "offset negative",
			url:            "/api/songs/search?q=test&offset=-1",
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "offset non-numeric",
			url:            "/api/songs/search?q=test&offset=xyz",
			wantStatus:     http.StatusBadRequest,
			wantErrorField: true,
		},
		{
			name:           "service returns error",
			url:            "/api/songs/search?q=test",
			fakeErr:        errors.New("python down"),
			wantStatus:     http.StatusBadGateway,
			wantErrorField: true,
		},
		{
			name:           "empty results renders array not null",
			url:            "/api/songs/search?q=test",
			fakePage:       services.SearchPage{Items: []models.SearchResult{}, HasMore: false},
			wantStatus:     http.StatusOK,
			wantEmptySlice: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeYouTube{page: tt.fakePage, err: tt.fakeErr}
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			rec := httptest.NewRecorder()

			handlers.Search(fake).ServeHTTP(rec, req)

			if got, want := rec.Code, tt.wantStatus; got != want {
				t.Errorf("status: got %d, want %d", got, want)
			}

			if rec.Code == http.StatusOK {
				var body struct {
					Items   []json.RawMessage `json:"items"`
					HasMore bool              `json:"has_more"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode body: %v", err)
				}

				if tt.wantEmptySlice {
					raw, _ := json.Marshal(body.Items)
					if string(raw) == "null" {
						t.Errorf("items: got null JSON, want []")
					}
					if len(body.Items) != 0 {
						t.Errorf("items: got %d items, want 0", len(body.Items))
					}
				}

				if tt.wantItemCount > 0 && len(body.Items) != tt.wantItemCount {
					t.Errorf("item count: got %d, want %d", len(body.Items), tt.wantItemCount)
				}
				if tt.wantHasMore && !body.HasMore {
					t.Errorf("has_more: got false, want true")
				}
			}

			if tt.wantErrorField {
				var body map[string]interface{}
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode error body: %v", err)
				}
				if _, ok := body["error"]; !ok {
					t.Errorf("expected 'error' field in response body, got: %v", body)
				}
			}

			if tt.wantCalledQuery != "" {
				if fake.calledQuery != tt.wantCalledQuery {
					t.Errorf("calledQuery: got %q, want %q", fake.calledQuery, tt.wantCalledQuery)
				}
				if fake.calledLimit != tt.wantCalledLimit {
					t.Errorf("calledLimit: got %d, want %d", fake.calledLimit, tt.wantCalledLimit)
				}
				if fake.calledOffset != tt.wantCalledOffset {
					t.Errorf("calledOffset: got %d, want %d", fake.calledOffset, tt.wantCalledOffset)
				}
			}
		})
	}
}
