# Collapse Cantus to Two Services — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move `/search` and `/shift` from the Python audio-processor into the Go backend, delete the dead `/preview-key` Python endpoint, drop the CPU/GPU client split in Go, and rename `audio-processor/` → `audio-processor-gpu/`. End state: Go owns API + yt-dlp + ytmusic search + audio shift; Python owns only Demucs (`/separate`) and CREPE (`/melody`).

**Architecture:** In-Go ytmusic search via `github.com/raitonoberu/ytmusic` replaces the Python `/search` HTTP path. In-Go pitch shift via `rubberband` + `ffmpeg` CLIs replaces the Python `/shift` HTTP path; because shift now lives in the same process as Storage, callers stream from `storage.Open` → local tempfile → `storage.Commit` (no URL handoff). The GPU processor client is the only remaining processor client and is renamed `ProcessorClient`.

**Tech Stack:** Go 1.22+ (chi, zerolog, aws-sdk-go-v2/s3), `github.com/raitonoberu/ytmusic`, FastAPI (Python — trimmed), `rubberband` and `ffmpeg` CLIs.

**Reference spec:** `docs/superpowers/specs/2026-06-14-collapse-to-two-services-design.md`.

---

## Conventions for every task

- All Go tests are **table-driven**, even single-case (see memory `feedback-table-tests`).
- One TDD cycle per behavior: write failing test → run it red → minimal impl → run green → commit.
- Run formatters before each commit: `cd backend && gofmt -w .` and `cd audio-processor && ruff format .` (or `ruff check --fix`).
- Commit messages use Conventional Commits (`feat:`, `refactor:`, `test:`, `chore:`, `docs:`).
- Don't run the full integration smoke (curl walkthrough) until Task 11. Per-task tests are enough for review checkpoints.

## File structure (new + touched)

**Create**
- `backend/services/ytmusic_search.go` — new search service wrapping `raitonoberu/ytmusic`.
- `backend/services/ytmusic_search_test.go` — table-driven tests with a fake pager interface.
- `backend/services/shift.go` — `Shifter` interface + `CLIShifter` that shells to `rubberband` / `ffmpeg`.
- `backend/services/shift_test.go` — table-driven tests using `CommandRunner` fake.

**Modify (Go)**
- `backend/services/youtube.go`, `backend/services/youtube_test.go` — `PythonYouTubeService.Search` delegates to `YTMusicSearch`; HTTP path removed.
- `backend/services/processor_url.go`, `backend/services/processor_url_test.go` — drop `CPUProcessorClient`; rename `GPUProcessorClient` → `ProcessorClient`, `PythonGPUProcessorClient` → `PythonProcessorClient`.
- `backend/services/job_runner.go`, `backend/services/job_runner_test.go` — Stage 4 uses local `Shifter`; field/arg renames.
- `backend/api/handlers/preview_shift.go`, `_test.go` — both shift blocks switch to local `Shifter` with `storage.Open` + tempfiles + `storage.Commit`.
- `backend/api/handlers/preview_stems.go`, `_test.go` — `gpu services.GPUProcessorClient` → `processor services.ProcessorClient`.
- `backend/api/router.go` — wire `processor` + `shifter` args.
- `backend/cmd/server/main.go` — instantiate new search + shifter; drop CPU client.
- `backend/config/config.go`, `_test.go` — drop CPU vars; rename GPU vars; add `RubberbandPath` / `FFmpegPath`.
- `backend/.env.example` — same renames + new vars.
- `backend/go.mod` / `backend/go.sum` — add `github.com/raitonoberu/ytmusic`.
- `CLAUDE.md` — update Architecture + endpoint list to reflect two-service layout.

**Modify (Python)**
- `audio-processor/main.py` — mount only `/separate` and `/melody`.
- `audio-processor/requirements.txt` — drop `ytmusicapi`, `pyrubberband`. Conditionally drop `librosa` + `soundfile` after verification.

**Delete (Python)**
- `audio-processor/routers/search.py` + `tests/test_search_router.py`
- `audio-processor/routers/shift.py` + `tests/test_shift_router.py`
- `audio-processor/routers/preview_key.py` + `tests/test_preview_key_router.py`
- `audio-processor/services/ytmusic_service.py` + `tests/test_ytmusic_service.py`
- `audio-processor/services/pitch_service.py` + `tests/test_pitch_service.py`
- `audio-processor/services/preview_key_service.py` + `tests/test_preview_key_service.py`

**Rename**
- `audio-processor/` → `audio-processor-gpu/` (single `git mv` at Task 10).

---

## Task 1: Add `raitonoberu/ytmusic` dependency

**Files:**
- Modify: `backend/go.mod`, `backend/go.sum`

- [ ] **Step 1: Add the dependency**

```bash
cd backend && go get github.com/raitonoberu/ytmusic@latest
```

Expected: `go.mod` gains a `require github.com/raitonoberu/ytmusic vX.Y.Z` line; `go.sum` gains hashes.

- [ ] **Step 2: Verify the module compiles**

```bash
cd backend && go build ./...
```

Expected: exit 0, no output.

- [ ] **Step 3: Commit**

```bash
cd backend && git add go.mod go.sum
git commit -m "chore(backend): add raitonoberu/ytmusic dependency"
```

---

## Task 2: New `YTMusicSearch` service in Go

**Why a test seam:** unit tests must not hit YouTube. We wrap `ytmusic`'s pager in a small interface so a fake can drive it.

**Files:**
- Create: `backend/services/ytmusic_search.go`
- Create: `backend/services/ytmusic_search_test.go`

### Step 1: Write the failing test (table-driven)

Add `backend/services/ytmusic_search_test.go`:

```go
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
			name:    "upstream error surfaces",
			pager:   &fakePager{err: errors.New("network")},
			query:   "q", limit: 5, offset: 0,
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

func TestYTMusicSearch_CacheHit(t *testing.T) {
	tests := []struct {
		name string
	}{{name: "second call with same query does not hit pager"}}
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
```

### Step 2: Run the test red

```bash
cd backend && go test ./services -run TestYTMusicSearch -v
```

Expected: FAIL — `undefined: services.NewYTMusicSearch`, `undefined: services.SearchPager`.

### Step 3: Implement `ytmusic_search.go`

Create `backend/services/ytmusic_search.go`:

```go
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
// call so each query gets its own continuation state. ttl is per-entry; once
// the cache reaches maxSize the oldest expired entry is evicted (best-effort).
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
		// Drain pages until we have enough mapped items OR upstream is exhausted.
		for len(mapped) < need {
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
```

### Step 4: Run the tests green

```bash
cd backend && go test ./services -run "TestYTMusicSearch" -v
```

Expected: PASS.

### Step 5: Commit

```bash
cd backend && gofmt -w services/
git add services/ytmusic_search.go services/ytmusic_search_test.go
git commit -m "feat(backend): in-Go YouTube Music search via raitonoberu/ytmusic"
```

---

## Task 3: Wire `YTMusicSearch` into `PythonYouTubeService`

**Approach:** Keep the `PythonYouTubeService` type name (it still owns the yt-dlp download paths). Replace its `Search` method body to delegate to a `*YTMusicSearch` injected via constructor. Remove the HTTP upstream types.

**Files:**
- Modify: `backend/services/youtube.go`
- Modify: `backend/services/youtube_test.go`
- Modify: `backend/cmd/server/main.go` (constructor signature)

### Step 1: Write the failing test

Update `backend/services/youtube_test.go` — replace the upstream-HTTP-roundtripper tests for Search with a single delegation test (keep the existing DownloadPreview / DownloadFull tests unchanged):

```go
func TestPythonYouTubeService_SearchDelegates(t *testing.T) {
	tests := []struct {
		name    string
		page    services.SearchPage
		wantIDs []string
	}{
		{
			name: "returns whatever YTMusicSearch returns",
			page: services.SearchPage{
				Items: []models.SearchResult{
					{VideoID: "aaaaaaaaaaa", Sig: "sig"},
				},
				HasMore: false,
			},
			wantIDs: []string{"aaaaaaaaaaa"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSearchDelegate{page: tt.page}
			svc := services.NewPythonYouTubeService(fake, nil, nil, services.ExecRunner{})
			got, err := svc.Search(context.Background(), "q", 5, 0)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got.Items) != len(tt.wantIDs) {
				t.Fatalf("len: got %d, want %d", len(got.Items), len(tt.wantIDs))
			}
		})
	}
}

type fakeSearchDelegate struct{ page services.SearchPage }

func (f *fakeSearchDelegate) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return f.page, nil
}
```

Drop the old upstream-HTTP tests (`TestPythonYouTubeService_Search_*`) — their behavior moved to `TestYTMusicSearch`.

### Step 2: Run the test red

```bash
cd backend && go test ./services -run TestPythonYouTubeService_SearchDelegates -v
```

Expected: FAIL (constructor signature mismatch, or `Search` field absent).

### Step 3: Update `youtube.go`

In `backend/services/youtube.go`:

1. Delete `upstreamItem`, `upstreamResponse`, `baseURL`, `client` fields of `PythonYouTubeService`.
2. Add a `Searcher` interface and a `search Searcher` field. The constructor takes Searcher first.
3. Replace `Search` body with delegation.

Replacement:

```go
// Searcher is implemented by YTMusicSearch and any other Search backend.
type Searcher interface {
	Search(ctx context.Context, query string, limit, offset int) (SearchPage, error)
}

type PythonYouTubeService struct {
	search  Searcher
	signer  *Signer
	storage Storage
	runner  CommandRunner
}

func NewPythonYouTubeService(search Searcher, signer *Signer, storage Storage, runner CommandRunner) *PythonYouTubeService {
	return &PythonYouTubeService{search: search, signer: signer, storage: storage, runner: runner}
}

func (s *PythonYouTubeService) Search(ctx context.Context, query string, limit, offset int) (SearchPage, error) {
	return s.search.Search(ctx, query, limit, offset)
}
```

Keep `DownloadPreview` and `DownloadFull` unchanged.

### Step 4: Update `cmd/server/main.go`

Replace the `svc := services.NewPythonYouTubeService(cfg.PythonProcessorURL, &http.Client{...}, signer, storage, services.ExecRunner{})` block with:

```go
searchSvc := services.NewYTMusicSearchProd(signer, 600*time.Second, 256)
svc := services.NewPythonYouTubeService(searchSvc, signer, storage, services.ExecRunner{})
```

Remove the now-unused `net/http` import line if no other code uses it (it likely still is — leave alone if so).

### Step 5: Run the tests green

```bash
cd backend && go test ./... -v
```

Expected: PASS (including unrelated tests; the only Search-path test that matters is the new one).

### Step 6: Commit

```bash
cd backend && gofmt -w .
git add services/youtube.go services/youtube_test.go cmd/server/main.go
git commit -m "refactor(backend): delegate Search to in-Go YTMusicSearch"
```

---

## Task 4: Delete Python `/search`

**Files:**
- Delete: `audio-processor/routers/search.py`
- Delete: `audio-processor/services/ytmusic_service.py`
- Delete: `audio-processor/tests/test_search_router.py`
- Delete: `audio-processor/tests/test_ytmusic_service.py`
- Modify: `audio-processor/main.py`

### Step 1: Delete files

```bash
cd audio-processor && rm routers/search.py services/ytmusic_service.py tests/test_search_router.py tests/test_ytmusic_service.py
```

### Step 2: Remove from `main.py`

In `audio-processor/main.py`, delete these two lines:

```python
from routers import search as search_router
...
app.include_router(search_router.router)
```

### Step 3: Run the Python suite

```bash
cd audio-processor && pytest
```

Expected: PASS. No failing imports.

### Step 4: Commit

```bash
git add -A audio-processor/
git commit -m "refactor(audio-processor): drop /search (moved to Go backend)"
```

---

## Task 5: New `Shifter` service in Go

**Why CLI shell-out:** pyrubberband is itself a shim over the `rubberband` CLI. Going direct from Go skips a process hop and removes the Python boundary for `/shift`.

**Pipeline:** if input is `.mp3`, ffmpeg-decode to scratch WAV; `rubberband -p <semitones> <wav-in> <wav-out>`; if output is `.mp3`, ffmpeg-encode WAV → MP3 at 128k/44100.

**Files:**
- Create: `backend/services/shift.go`
- Create: `backend/services/shift_test.go`

### Step 1: Write the failing test (table-driven)

Create `backend/services/shift_test.go`:

```go
package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cantus/backend/services"
)

// recordingRunner records every command invocation for assertions.
type recordingRunner struct {
	calls [][]string
	errs  []error
	// onCall optionally writes the expected output file so downstream steps can proceed.
	onCall func(name string, args []string) error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.onCall != nil {
		if err := r.onCall(name, args); err != nil {
			return err
		}
	}
	if len(r.errs) >= len(r.calls) && r.errs[len(r.calls)-1] != nil {
		return r.errs[len(r.calls)-1]
	}
	return nil
}

func TestCLIShifter_Shift(t *testing.T) {
	tests := []struct {
		name       string
		inExt      string // ".wav" or ".mp3"
		outExt     string
		semitones  float64
		wantCalls  []string // first token of each invocation, in order
		wantPFlag  string   // expected -p semitone string
	}{
		{name: "wav→wav uses only rubberband", inExt: ".wav", outExt: ".wav", semitones: -3,
			wantCalls: []string{"rubberband"}, wantPFlag: "-3"},
		{name: "mp3→mp3 decodes, shifts, encodes", inExt: ".mp3", outExt: ".mp3", semitones: 5,
			wantCalls: []string{"ffmpeg", "rubberband", "ffmpeg"}, wantPFlag: "5"},
		{name: "wav→mp3 shifts then encodes", inExt: ".wav", outExt: ".mp3", semitones: 0,
			wantCalls: []string{"rubberband", "ffmpeg"}, wantPFlag: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in"+tt.inExt)
			out := filepath.Join(dir, "out"+tt.outExt)
			if err := os.WriteFile(in, []byte("audio bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{
				onCall: func(_ string, args []string) error {
					// Touch the output file argument so downstream rubberband/ffmpeg sees input.
					last := args[len(args)-1]
					return os.WriteFile(last, []byte("x"), 0o644)
				},
			}
			s := services.NewCLIShifter("rubberband", "ffmpeg", runner)
			if err := s.Shift(context.Background(), in, out, tt.semitones); err != nil {
				t.Fatalf("Shift: %v", err)
			}
			if len(runner.calls) != len(tt.wantCalls) {
				t.Fatalf("calls: got %d, want %d (%v)", len(runner.calls), len(tt.wantCalls), runner.calls)
			}
			for i, want := range tt.wantCalls {
				if runner.calls[i][0] != want {
					t.Errorf("call[%d]: got %q, want %q", i, runner.calls[i][0], want)
				}
			}
			// Find the rubberband call and assert the -p flag.
			for _, call := range runner.calls {
				if call[0] != "rubberband" {
					continue
				}
				joined := strings.Join(call, " ")
				if !strings.Contains(joined, "-p "+tt.wantPFlag) {
					t.Errorf("rubberband args missing -p %s: %q", tt.wantPFlag, joined)
				}
			}
			if _, err := os.Stat(out); err != nil {
				t.Errorf("output not created: %v", err)
			}
		})
	}
}

func TestCLIShifter_Shift_RunnerError(t *testing.T) {
	tests := []struct{ name string }{{name: "rubberband failure surfaces"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in.wav")
			out := filepath.Join(dir, "out.wav")
			_ = os.WriteFile(in, []byte("x"), 0o644)
			runner := &recordingRunner{onCall: func(name string, _ []string) error {
				if name == "rubberband" {
					return errors.New("boom")
				}
				return nil
			}}
			s := services.NewCLIShifter("rubberband", "ffmpeg", runner)
			if err := s.Shift(context.Background(), in, out, 0); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}
```

### Step 2: Run the test red

```bash
cd backend && go test ./services -run TestCLIShifter -v
```

Expected: FAIL — `undefined: services.NewCLIShifter`.

### Step 3: Implement `shift.go`

Create `backend/services/shift.go`:

```go
package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Shifter pitch-shifts an audio file by a number of semitones.
// inputPath and outputPath are local filesystem paths; the implementation
// handles MP3↔WAV transcoding internally so callers don't have to care.
type Shifter interface {
	Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error
}

// CLIShifter shells out to the `rubberband` CLI for pitch shifting and to
// `ffmpeg` for MP3↔WAV transcoding when the input or output is .mp3.
type CLIShifter struct {
	Rubberband string
	FFmpeg     string
	Runner     CommandRunner
}

// NewCLIShifter returns a CLIShifter with the given binary paths and runner.
// Pass "rubberband"/"ffmpeg" to resolve via $PATH.
func NewCLIShifter(rubberband, ffmpeg string, runner CommandRunner) *CLIShifter {
	return &CLIShifter{Rubberband: rubberband, FFmpeg: ffmpeg, Runner: runner}
}

// Shift decodes (if MP3) → rubberband-shifts → encodes (if MP3 out).
// Scratch tempfiles live in the same directory as outputPath and are cleaned up.
func (s *CLIShifter) Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error {
	if _, err := os.Stat(inputPath); err != nil {
		return fmt.Errorf("shift: stat input: %w", err)
	}
	outDir := filepath.Dir(outputPath)
	if outDir == "" {
		outDir = "."
	}

	inIsMP3 := strings.EqualFold(filepath.Ext(inputPath), ".mp3")
	outIsMP3 := strings.EqualFold(filepath.Ext(outputPath), ".mp3")

	// Pipeline stages produce these files in turn; each is cleaned up at the end.
	var scratch []string
	defer func() {
		for _, p := range scratch {
			_ = os.Remove(p)
		}
	}()

	wavIn := inputPath
	if inIsMP3 {
		f, err := os.CreateTemp(outDir, "shift-in-*.wav")
		if err != nil {
			return fmt.Errorf("shift: tempfile in: %w", err)
		}
		_ = f.Close()
		wavIn = f.Name()
		scratch = append(scratch, wavIn)
		// -y overwrites the tempfile (which we just created empty).
		if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", inputPath, "-ar", "44100", "-ac", "2", wavIn); err != nil {
			return fmt.Errorf("shift: ffmpeg decode: %w", err)
		}
	}

	wavOut := outputPath
	if outIsMP3 {
		f, err := os.CreateTemp(outDir, "shift-out-*.wav")
		if err != nil {
			return fmt.Errorf("shift: tempfile out: %w", err)
		}
		_ = f.Close()
		wavOut = f.Name()
		scratch = append(scratch, wavOut)
	}

	pArg := strconv.FormatFloat(semitones, 'f', -1, 64)
	if err := s.Runner.Run(ctx, s.Rubberband, "-p", pArg, wavIn, wavOut); err != nil {
		return fmt.Errorf("shift: rubberband: %w", err)
	}

	if outIsMP3 {
		if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", wavOut, "-b:a", "128k", "-ar", "44100", outputPath); err != nil {
			return fmt.Errorf("shift: ffmpeg encode: %w", err)
		}
	}

	return nil
}
```

### Step 4: Run the tests green

```bash
cd backend && go test ./services -run TestCLIShifter -v
```

Expected: PASS.

### Step 5: Commit

```bash
cd backend && gofmt -w services/
git add services/shift.go services/shift_test.go
git commit -m "feat(backend): CLI-based Shifter wrapping rubberband + ffmpeg"
```

---

## Task 6: Use `Shifter` in `job_runner` Stage 4

The current code uses `cpu.Shift(inURL, outURL, semitones)` — URL handoff across a Python boundary. Now Shift is local: stream from Storage → tempfile → run `Shifter` → `Storage.Commit`.

**Files:**
- Modify: `backend/services/job_runner.go`
- Modify: `backend/services/job_runner_test.go`

### Step 1: Update the test fake

In `backend/services/job_runner_test.go`, replace `fakeCPUJob` with `fakeShifter`:

```go
type fakeShifter struct {
	calls []struct {
		In, Out string
		Semi    float64
	}
	shiftFn func(ctx context.Context, in, out string, st float64) error
}

func (f *fakeShifter) Shift(ctx context.Context, in, out string, st float64) error {
	f.calls = append(f.calls, struct {
		In, Out string
		Semi    float64
	}{in, out, st})
	if f.shiftFn != nil {
		return f.shiftFn(ctx, in, out, st)
	}
	// Write a non-empty file at out so storage.Commit downstream finds bytes.
	return os.WriteFile(out, []byte("shifted"), 0o644)
}
```

Update `newTestSetup` signature: replace `fakeCPU *fakeCPUJob` with `fakeShifter *fakeShifter`. Update the runner constructor call:

```go
runner = services.NewJobRunner(fakeYT, storage, &fakeGPUJob{}, fakeShifter, jobStore, maxConcurrent)
```

Update all call sites of `newTestSetup` to drop the cpu fake and use the shifter fake. The default no-op shift writes "shifted" to the output path so `storage.Commit` finds non-empty bytes.

For tests that assert on shift semantics (look for `fakeCPU.calls`), rename to `fakeShifter.calls` — `In` and `Out` are now **filesystem paths** (under temp dir), not URLs.

### Step 2: Run the tests red

```bash
cd backend && go test ./services -run TestJobRunner -v
```

Expected: FAIL — compile errors from the constructor signature change.

### Step 3: Update `job_runner.go`

Replace the `cpu CPUProcessorClient` field and arg with `shifter Shifter`. For minimal diff in this task, keep the field name `gpu` for now — the `gpu → processor` rename happens in Task 10's sweep.

Replace Stage 4 (lines ~166–190) with:

```go
	// Stage 4: Pitch-shift the instrumental stem to the requested key.
	r.update(jobID, models.StatusShifting, "shifting instrumental to your key")
	shiftedName := "shifted/" + strconv.Itoa(semitones) + "/audio.mp3"
	shiftedKey := r.storage.Key(videoID, shiftedName)
	shiftedHas, _ := r.storage.Has(ctx, shiftedKey)
	if !shiftedHas {
		noVocalsKey := r.storage.Key(videoID, "no_vocals.wav")
		rc, err := r.storage.Open(ctx, noVocalsKey)
		if err != nil {
			r.fail(jobID, "open no_vocals: "+err.Error())
			return
		}
		scratchIn, err := os.CreateTemp("", "cantus-shift-in-*.wav")
		if err != nil {
			_ = rc.Close()
			r.fail(jobID, "tempfile in: "+err.Error())
			return
		}
		if _, err := io.Copy(scratchIn, rc); err != nil {
			_ = rc.Close()
			_ = scratchIn.Close()
			_ = os.Remove(scratchIn.Name())
			r.fail(jobID, "copy no_vocals to scratch: "+err.Error())
			return
		}
		_ = rc.Close()
		_ = scratchIn.Close()
		defer func() { _ = os.Remove(scratchIn.Name()) }()

		scratchOut, err := os.CreateTemp("", "cantus-shift-out-*.mp3")
		if err != nil {
			r.fail(jobID, "tempfile out: "+err.Error())
			return
		}
		_ = scratchOut.Close()
		defer func() { _ = os.Remove(scratchOut.Name()) }()

		if err := r.shifter.Shift(ctx, scratchIn.Name(), scratchOut.Name(), float64(semitones)); err != nil {
			r.fail(jobID, "shift failed: "+err.Error())
			return
		}
		if err := r.storage.Commit(ctx, shiftedKey, scratchOut.Name()); err != nil {
			r.fail(jobID, "commit shifted: "+err.Error())
			return
		}
		if err := r.storage.Verify(ctx, shiftedKey); err != nil {
			r.fail(jobID, "shift output not materialized: "+err.Error())
			return
		}
	}
```

Add the imports `io` and `os` if not already present.

Update `NewJobRunner` signature: replace `cpu CPUProcessorClient` arg with `shifter Shifter`. Update the struct.

### Step 4: Run the tests green

```bash
cd backend && go test ./services -run TestJobRunner -v
```

Expected: PASS. Tests that asserted on URL strings now assert on filesystem paths — confirm those assertions were already updated in Step 1; if not, switch them to `strings.HasSuffix(call.In, ".wav")` and similar shape-only checks.

### Step 5: Commit

```bash
cd backend && gofmt -w services/
git add services/job_runner.go services/job_runner_test.go
git commit -m "refactor(job_runner): shift instrumental locally via Shifter + Storage"
```

---

## Task 7: Use `Shifter` in `preview_shift` handler

Both shift blocks (stem-shift path and legacy preview-shift path) switch to local `Shifter` + tempfiles + `storage.Commit`. The handler's `cpu services.CPUProcessorClient` arg becomes `shifter services.Shifter`.

**Files:**
- Modify: `backend/api/handlers/preview_shift.go`
- Modify: `backend/api/handlers/preview_shift_test.go`
- Modify: `backend/api/router.go`

### Step 1: Update the test fake

In `backend/api/handlers/preview_shift_test.go`, replace `fakeCPUProcessor` with `fakeShifter`:

```go
type fakeShifter struct {
	shiftErr  error
	shiftFn   func(ctx context.Context, inPath, outPath string, semitones float64) error
	callCount int
	lastSemi  float64
}

func (f *fakeShifter) Shift(ctx context.Context, inPath, outPath string, semitones float64) error {
	f.callCount++
	f.lastSemi = semitones
	if f.shiftFn != nil {
		return f.shiftFn(ctx, inPath, outPath, semitones)
	}
	// Default: write non-empty bytes to outPath so storage.Commit succeeds.
	if err := os.WriteFile(outPath, []byte("shifted"), 0o644); err != nil {
		return err
	}
	return f.shiftErr
}
```

Update `shiftRouter` signature to take `services.Shifter`. Update every test that wired `fakeCPUProcessor` to use `fakeShifter`. Tests that asserted `inURL` / `outURL` arguments switch to asserting only file extension / non-emptiness (shapes change from URL strings to local paths).

### Step 2: Run the tests red

```bash
cd backend && go test ./api/handlers -run TestPreviewShift -v
```

Expected: FAIL — compile errors from signature changes.

### Step 3: Rewrite both shift blocks

In `backend/api/handlers/preview_shift.go`, replace the handler signature:

```go
func PreviewShift(
	signer *services.Signer,
	storage services.Storage,
	ytSvc services.YouTubeService,
	shifter services.Shifter,
) http.HandlerFunc {
```

Replace **both** shift blocks. Each block follows the same pattern: `storage.Open(inKey)` → copy to scratch → `shifter.Shift(scratchIn, scratchOut, semitones)` → `storage.Commit(outKey, scratchOut)` → `storage.Verify(outKey)`. Drop the `SignGet` / `SignPut` calls in these blocks.

Extract a small inline helper at the top of the file (above the handler) to keep the two blocks DRY:

```go
// shiftViaStorage downloads inKey to scratch, runs shifter, commits scratch out to outKey.
// Returns nil on success or an error suitable for handler logging.
func shiftViaStorage(
	ctx context.Context,
	storage services.Storage,
	shifter services.Shifter,
	inKey, outKey string,
	inExt, outExt string,
	semitones float64,
) error {
	rc, err := storage.Open(ctx, inKey)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer func() { _ = rc.Close() }()

	scratchIn, err := os.CreateTemp("", "cantus-pshift-in-*"+inExt)
	if err != nil {
		return fmt.Errorf("tempfile in: %w", err)
	}
	defer func() { _ = os.Remove(scratchIn.Name()) }()
	if _, err := io.Copy(scratchIn, rc); err != nil {
		_ = scratchIn.Close()
		return fmt.Errorf("copy input: %w", err)
	}
	_ = scratchIn.Close()

	scratchOut, err := os.CreateTemp("", "cantus-pshift-out-*"+outExt)
	if err != nil {
		return fmt.Errorf("tempfile out: %w", err)
	}
	_ = scratchOut.Close()
	defer func() { _ = os.Remove(scratchOut.Name()) }()

	if err := shifter.Shift(ctx, scratchIn.Name(), scratchOut.Name(), semitones); err != nil {
		return fmt.Errorf("shift: %w", err)
	}
	if err := storage.Commit(ctx, outKey, scratchOut.Name()); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if err := storage.Verify(ctx, outKey); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return nil
}
```

Then replace both shift blocks with a single call. Stem-shift block (input is no_vocals.wav, output is .mp3):

```go
if err := shiftViaStorage(ctx, storage, shifter, inKey, outKey, ".wav", ".mp3", float64(n)); err != nil {
	log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("stem-shift failed")
	writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
	return
}
serveKey = outKey
serveName = stemShiftedName
```

Legacy-shift block (input is preview.mp3, output is .mp3):

```go
if err := shiftViaStorage(ctx, storage, shifter, inKey, outKey, ".mp3", ".mp3", float64(n)); err != nil {
	log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("legacy-shift failed")
	writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
	return
}
serveKey = outKey
serveName = legacyShiftedName
```

Add imports: `context`, `fmt`, `io`, `os`.

### Step 4: Update `router.go`

In `backend/api/router.go`, change `NewRouter` signature: replace `cpu services.CPUProcessorClient, gpu services.GPUProcessorClient` with `gpu services.GPUProcessorClient, shifter services.Shifter` (CPU client is going away in Task 10; doing this in two steps keeps each diff readable).

Update the `PreviewShift` wiring:

```go
mux.Post("/api/preview-shift", handlers.PreviewShift(signer, storage, svc, shifter))
```

In `backend/cmd/server/main.go`, drop `cpuProc`. Construct the shifter:

```go
shifter := services.NewCLIShifter("rubberband", "ffmpeg", services.ExecRunner{})
// later:
r := api.NewRouter(origins, log, svc, signer, storage, gpuProc, shifter, jobRunner, jobStore, blobTokener)
```

Update `NewJobRunner` call to pass `shifter` instead of `cpuProc`:

```go
jobRunner := services.NewJobRunner(svc, storage, gpuProc, shifter, jobStore, maxJobs)
```

(Note: `NewJobRunner`'s gpu/shifter arg order is whatever you chose in Task 6 — match it.)

### Step 5: Run the tests green

```bash
cd backend && go test ./... -v
```

Expected: PASS. If any other test files reference `cpuProc` or `services.CPUProcessorClient`, fix them — they should only be the processor_url tests, which are addressed in Task 9.

### Step 6: Commit

```bash
cd backend && gofmt -w .
git add api/handlers/preview_shift.go api/handlers/preview_shift_test.go api/router.go cmd/server/main.go
git commit -m "refactor(preview-shift): shift locally via Shifter + Storage"
```

---

## Task 8: Delete Python `/shift`

**Files:**
- Delete: `audio-processor/routers/shift.py`, `audio-processor/services/pitch_service.py`
- Delete: `audio-processor/tests/test_shift_router.py`, `audio-processor/tests/test_pitch_service.py`
- Modify: `audio-processor/main.py`

- [ ] **Step 1: Delete files**

```bash
cd audio-processor && rm routers/shift.py services/pitch_service.py tests/test_shift_router.py tests/test_pitch_service.py
```

- [ ] **Step 2: Remove from `main.py`**

Delete the `shift_router` import and `app.include_router(shift_router.router)` lines.

- [ ] **Step 3: Run Python tests**

```bash
cd audio-processor && pytest
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add -A audio-processor/
git commit -m "refactor(audio-processor): drop /shift (moved to Go backend)"
```

---

## Task 9: Delete Python `/preview-key`

The Go `PreviewKey` handler reads `melody.json` directly; the Python endpoint has been dead code since the URL-handoff refactor.

**Files:**
- Delete: `audio-processor/routers/preview_key.py`, `audio-processor/services/preview_key_service.py`
- Delete: `audio-processor/tests/test_preview_key_router.py`, `audio-processor/tests/test_preview_key_service.py`
- Modify: `audio-processor/main.py`

- [ ] **Step 1: Delete files**

```bash
cd audio-processor && rm routers/preview_key.py services/preview_key_service.py tests/test_preview_key_router.py tests/test_preview_key_service.py
```

- [ ] **Step 2: Remove from `main.py`**

Delete the `preview_key_router` import and `app.include_router(preview_key_router.router)` lines.

- [ ] **Step 3: Run Python tests**

```bash
cd audio-processor && pytest
```

Expected: PASS. `audio-processor/main.py` should now mount only `/separate`, `/melody`, and `/health`.

- [ ] **Step 4: Commit**

```bash
git add -A audio-processor/
git commit -m "refactor(audio-processor): drop dead /preview-key endpoint"
```

---

## Task 10: Drop CPU client; rename GPU → Processor; config renames

This is the sweeping rename pass. Single commit because the diff is structural and trivially mechanical.

**Files:**
- Modify: `backend/services/processor_url.go`, `backend/services/processor_url_test.go`
- Modify: `backend/services/job_runner.go`, `backend/services/job_runner_test.go`
- Modify: `backend/api/handlers/preview_stems.go`, `backend/api/handlers/preview_stems_test.go`
- Modify: `backend/api/router.go`, `backend/cmd/server/main.go`
- Modify: `backend/config/config.go`, `backend/config/config_test.go`, `backend/.env.example`

### Step 1: Update `processor_url.go`

Delete `CPUProcessorClient` interface, `PythonCPUProcessorClient` struct, `NewPythonCPUProcessorClient`, and the `Shift` + `PreviewKey` methods. Rename:

- `GPUProcessorClient` → `ProcessorClient`
- `PythonGPUProcessorClient` → `PythonProcessorClient`
- `NewPythonGPUProcessorClient` → `NewPythonProcessorClient`

Update doc comments:

```go
// ProcessorClient covers GPU-bound Python work: Demucs vocal separation
// and CREPE melody extraction. The Python service is reached over HTTP with
// presigned URL handoff for audio data.
type ProcessorClient interface {
	Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error
	Melody(ctx context.Context, vocalsInputURL, outputURL string) error
}
```

### Step 2: Update `processor_url_test.go`

Delete `TestPythonCPUProcessorClient_*` tests entirely. Rename `TestPythonGPUProcessorClient_*` → `TestPythonProcessorClient_*` and any `NewPythonGPUProcessorClient` calls.

### Step 3: Update job_runner + handlers + router + main

Across the files listed, mechanically rename:

- field/arg `gpu` → `processor`
- type `services.GPUProcessorClient` → `services.ProcessorClient`
- `fakeGPUJob` → `fakeProcessorJob` (in `job_runner_test.go`)
- `fakeGPUProcessor` → `fakeProcessor` (in `preview_stems_test.go` if named that way; verify before renaming)
- helper `services.NewPythonGPUProcessorClient` → `services.NewPythonProcessorClient`

Run from `backend/`:

```bash
grep -rl "GPUProcessorClient\|PythonGPUProcessorClient\|NewPythonGPUProcessorClient\|fakeGPUJob" .
```

Edit each hit. Do **not** use blind `sed -i` — read each file first to avoid mangling unrelated identifiers (e.g. `gpu` substring in `gpuProc` variable names).

### Step 4: Update `config.go`

In `backend/config/config.go`:

1. Delete struct fields `CPUProcessorURL`, `CPUProcessorTimeoutSeconds`.
2. Rename `GPUProcessorURL` → `ProcessorURL`, `GPUProcessorTimeoutSeconds` → `ProcessorTimeoutSeconds`.
3. Add struct fields `RubberbandPath`, `FFmpegPath` (strings).
4. In `Load`:
   - Drop the `CPUProcessorURL` / `CPU_PROCESSOR_TIMEOUT_SECONDS` reads.
   - Rename `GPU_PROCESSOR_URL` env read → `PROCESSOR_URL` (default still `cfg.PythonProcessorURL`).
   - Rename `GPU_PROCESSOR_TIMEOUT_SECONDS` → `PROCESSOR_TIMEOUT_SECONDS` (default 180).
   - Add `cfg.RubberbandPath = getEnvString("RUBBERBAND_PATH", "rubberband")`.
   - Add `cfg.FFmpegPath = getEnvString("FFMPEG_PATH", "ffmpeg")`.

### Step 5: Update `config_test.go`

Update tests for renamed/dropped/added fields. Keep table-driven format. Example new case:

```go
{
	name: "defaults rubberband/ffmpeg paths",
	env:  baseEnv(t),
	want: &config.Config{ /* ... */ RubberbandPath: "rubberband", FFmpegPath: "ffmpeg" },
},
```

### Step 6: Update `cmd/server/main.go`

Replace the explicit `rubberband` / `ffmpeg` strings introduced in Task 7 with `cfg.RubberbandPath` / `cfg.FFmpegPath`:

```go
shifter := services.NewCLIShifter(cfg.RubberbandPath, cfg.FFmpegPath, services.ExecRunner{})
```

Replace `cfg.GPUProcessorURL` → `cfg.ProcessorURL`, `cfg.GPUProcessorTimeoutSeconds` → `cfg.ProcessorTimeoutSeconds`. Use the renamed constructor:

```go
processor := services.NewPythonProcessorClient(
	cfg.ProcessorURL,
	&http.Client{Timeout: time.Duration(cfg.ProcessorTimeoutSeconds) * time.Second},
)
```

Replace remaining `gpuProc` → `processor`.

### Step 7: Update `.env.example`

Replace the processor-URL block:

```
# Processor URL — points at the Python GPU service (Demucs + CREPE).
# Defaults to PYTHON_PROCESSOR_URL when unset.
# PROCESSOR_URL=
PROCESSOR_TIMEOUT_SECONDS=180

# Pitch-shift CLI binaries. Defaults resolve via $PATH.
# RUBBERBAND_PATH=rubberband
# FFMPEG_PATH=ffmpeg
```

Delete the CPU_PROCESSOR_* and GPU_PROCESSOR_* lines.

### Step 8: Run the full test suite

```bash
cd backend && go vet ./... && go test ./... -v
```

Expected: PASS, no vet warnings.

### Step 9: Commit

```bash
cd backend && gofmt -w .
git add services/ api/ cmd/ config/ .env.example
git commit -m "refactor(backend): drop CPU client; rename GPU client → ProcessorClient"
```

---

## Task 11: Rename `audio-processor/` → `audio-processor-gpu/`

Single atomic rename + dependency trim.

**Files:**
- Rename directory.
- Modify: `audio-processor-gpu/requirements.txt` (post-rename).

### Step 1: Grep for any leftover references

```bash
grep -rn "audio-processor" /Users/rafimuhammad/Documents/development/cantus --include="*.go" --include="*.md" --include="*.py" --include="*.toml" --include="*.yml" --include="*.yaml" --include=".env*"
```

Expected: only docs hits (`CLAUDE.md`, `README.md`, spec files) and the directory itself. There should be no Go code references (Go service talks via `PROCESSOR_URL`, not a path).

### Step 2: Rename the directory

```bash
cd /Users/rafimuhammad/Documents/development/cantus && git mv audio-processor audio-processor-gpu
```

### Step 3: Verify dev startup commands still work

Run a quick sanity boot. From `audio-processor-gpu/`:

```bash
cd audio-processor-gpu && uvicorn main:app --port 8090 &
sleep 3 && curl -s http://localhost:8090/health && kill %1
```

Expected: `{"status":"ok"}`.

### Step 4: Trim `requirements.txt`

Edit `audio-processor-gpu/requirements.txt`. Delete these lines (exact match):

- `ytmusicapi==1.12.1`
- `pyrubberband==0.4.0`

For `librosa` and `soundfile` — they are listed in `MEMORY.md` as required (CREPE needs librosa for resampling). **Do NOT remove `librosa` or `soundfile`.** This is locked by the memory entry `feedback-librosa-required`.

Also remove (transitive deps of pyrubberband only — verify before deleting): none.

### Step 5: Verify the trimmed requirements still install + pass tests

```bash
cd audio-processor-gpu && python3.12 -m venv .venv-verify && source .venv-verify/bin/activate && pip install -r requirements.txt && pytest && deactivate && rm -rf .venv-verify
```

Expected: install completes (slow, may take minutes — Demucs deps), `pytest` PASSES.

If any test fails on missing imports, restore the missing dep in `requirements.txt` and re-run.

### Step 6: Commit

```bash
cd /Users/rafimuhammad/Documents/development/cantus
git add -A
git commit -m "chore: rename audio-processor → audio-processor-gpu; trim deps"
```

---

## Task 12: Docs — `CLAUDE.md` + integration smoke

### Step 1: Update `CLAUDE.md`

In `/Users/rafimuhammad/Documents/development/cantus/CLAUDE.md`:

- Update the **Services** section: drop the `cd audio-processor` line, replace with `cd audio-processor-gpu`. Keep the same uvicorn invocation.
- Update the **Architecture** ASCII diagram: the Python service now offers only `Demucs` + `CREPE`. Drop `ytmusicapi` and `pyrubberband + soundfile`. Move `ytmusicapi (Go: raitonoberu/ytmusic)` and `rubberband + ffmpeg (CLI)` under the Go box.
- Update the **API Endpoints** section: no changes needed (endpoint surface is identical), but the prose "Three-stage pipeline" line should now reference that Search and Shift are in-process Go calls.
- Update the **Important Notes** section: replace the `ProcessorClient is split` bullet with:

```
- **ProcessorClient** in `backend/services/processor_url.go`: single URL-based client that talks to the Python GPU service for Separate (Demucs) and Melody (CREPE). Configured via `PROCESSOR_URL` (defaults to `PYTHON_PROCESSOR_URL` for dev).
- **In-process audio shift** in `backend/services/shift.go`: `CLIShifter` shells to `rubberband` (+ `ffmpeg` for MP3↔WAV). Replaces the prior `/shift` Python endpoint. Stage 4 of `job_runner` and both shift paths in `preview_shift.go` stream from `Storage.Open` → tempfile → `Shifter.Shift` → `Storage.Commit`; no URL handoff needed for local-process work.
- **In-process YouTube Music search** in `backend/services/ytmusic_search.go`: wraps `github.com/raitonoberu/ytmusic` (Go drop-in for ytmusicapi). Same TTL cache (10min, 256 entries) and non-studio regex filter as before.
```

### Step 2: Run the integration smoke (manual)

Boot both services in two terminals:

```bash
cd backend && VIDEO_ID_SIGNING_KEY=$(openssl rand -hex 32) go run ./...
cd audio-processor-gpu && uvicorn main:app --port 8090 --reload
```

Walk:

```bash
curl localhost:8080/health
curl "localhost:8080/api/songs/search?q=bohemian+rhapsody" | head
# Pick a videoId+sig from the response, then:
curl "localhost:8080/api/preview/<videoId>?sig=<sig>" -o preview.mp3
curl -X POST localhost:8080/api/preview-shift -H 'content-type: application/json' \
  -d '{"video_id":"<videoId>","sig":"<sig>","semitones":-2}' -o preview-shift.mp3
curl -X POST localhost:8080/api/generate -H 'content-type: application/json' \
  -d '{"video_id":"<videoId>","sig":"<sig>","semitones":0}'
# follow status SSE briefly, then:
curl "localhost:8080/api/audio/<videoId>/0?sig=<sig>" -o audio.mp3
curl "localhost:8080/api/melody/<videoId>/0?sig=<sig>" | head
```

Expected:
- `search` returns JSON with `videoId`, `sig`, `title`, `artist`, etc.
- `preview` returns ~30s MP3.
- `preview-shift` returns a shifted ~30s MP3.
- `generate` returns a `job_id`; status SSE progresses through `downloading → separating → melody → shifting → done`.
- `audio` and `melody` return the final MP3 and pitch JSON.

If anything fails, fix in place. If `preview-shift` returns chipmunky audio when stems exist, recheck the resolution order in `preview_shift.go`.

### Step 3: Commit

```bash
git add CLAUDE.md
git commit -m "docs: collapse to two services — Go owns search + shift"
```

---

## Self-review checklist (run before declaring done)

- [ ] `cd backend && go vet ./... && go test ./...` passes.
- [ ] `cd audio-processor-gpu && pytest` passes.
- [ ] `grep -rn "CPUProcessorClient\|GPUProcessorClient\|cpu.Shift\|PythonCPUProcessorClient" backend/` returns no hits.
- [ ] `grep -rn "audio-processor[^-/]" .` returns no source-code hits (only docs/spec references that mention the prior name historically are acceptable).
- [ ] `grep -rn "GPU_PROCESSOR_URL\|CPU_PROCESSOR_URL" .` returns no hits (except possibly the prior spec, which is allowed to mention the deleted vars).
- [ ] Diagram in `CLAUDE.md` reflects the two-service shape.
- [ ] `audio-processor-gpu/main.py` mounts only `/separate` + `/melody` + `/health`.
- [ ] Memory `project-deployment-plan` change list items "Backend Go #3" (CPU/GPU split) and the Python CPU section are now marked done; update on completion.
