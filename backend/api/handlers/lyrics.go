package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// commitBytes writes data to a temp file and commits it to storage at key.
func commitBytes(ctx context.Context, storage services.Storage, key string, data []byte) error {
	f, err := os.CreateTemp("", "cantus-lyrics-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return storage.Commit(ctx, key, name)
}

// Lyrics returns an http.HandlerFunc that serves cached timed lyrics for a videoID.
// It fetches from LRCLIB on a cache miss and caches 404s as {available:false} too.
//
// Route: GET /api/lyrics/:videoId
// Query params: lyrics_sig, title, artist, album, duration_sec
func Lyrics(signer *services.Signer, storage services.Storage, lrclib services.LRCLib) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		videoID := chi.URLParam(r, "videoId")
		if !services.ValidVideoID(videoID) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid videoId"})
			return
		}

		q := r.URL.Query()
		title := q.Get("title")
		artist := q.Get("artist")
		album := q.Get("album")
		lyricsSig := q.Get("lyrics_sig")

		durationStr := q.Get("duration_sec")
		durationSec, err := strconv.Atoi(durationStr)
		if err != nil || durationSec < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid duration_sec"})
			return
		}

		if !signer.VerifyLyrics(videoID, title, artist, album, durationSec, lyricsSig) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid lyrics_sig"})
			return
		}

		ctx := r.Context()
		log := logger.FromCtx(ctx)
		key := storage.Key(videoID, "lyrics.json")

		// Serve from cache if present.
		ok, err := storage.Has(ctx, key)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("lyrics: storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if ok {
			rc, err := storage.Open(ctx, key)
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("lyrics: storage.Open failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage open failed"})
				return
			}
			defer func() { _ = rc.Close() }()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.Copy(w, rc)
			return
		}

		// Cache miss — fetch from LRCLIB.
		lyrics, err := lrclib.Get(ctx, artist, title, album, durationSec)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("lyrics: lrclib.Get failed")
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "lyrics fetch failed"})
			return
		}

		data, err := json.Marshal(lyrics)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("lyrics: marshal failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "lyrics marshal failed"})
			return
		}

		// Write to storage using a temp-file-less in-memory approach: Commit expects
		// a local path, so we write a temp file, commit it, then serve from the
		// in-memory bytes we already have.
		if commitErr := commitBytes(ctx, storage, key, data); commitErr != nil {
			// Non-fatal: serve the response anyway; next request will re-fetch.
			log.Warn().Err(commitErr).Str("videoId", videoID).Msg("lyrics: cache commit failed")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, bytes.NewReader(data))
	}
}
