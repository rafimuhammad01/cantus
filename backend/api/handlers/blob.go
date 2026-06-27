package handlers

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"cantus/backend/services"
)

// Blob serves GET (read) and PUT (write) on /internal/blob/{key}, gated by a
// short-lived HMAC token. Used by LocalDiskStorage to give Python a URL it can
// hit just like an R2 presigned URL.
func Blob(storage services.Storage, bt *services.BlobTokener) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := chi.URLParam(r, "*")
		op := r.URL.Query().Get("op")
		token := r.URL.Query().Get("token")
		expStr := r.URL.Query().Get("exp")

		expUnix, err := strconv.ParseInt(expStr, 10, 64)
		if err != nil {
			http.Error(w, "bad exp", http.StatusBadRequest)
			return
		}
		if err := bt.Verify(key, op, token, expUnix, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		switch op {
		case "get":
			rc, err := storage.Open(r.Context(), key)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			defer func() { _ = rc.Close() }()
			// Set Content-Type from the key's extension so the browser (and any
			// Blob constructed from this response) tags the bytes correctly.
			// Without this, http sniffs the body — raw MPEG frames without an
			// ID3 header sniff as application/octet-stream, and Safari refuses
			// to decode the resulting blob ("operation is not supported").
			if ct := mime.TypeByExtension(path.Ext(key)); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			if _, err := io.Copy(w, rc); err != nil {
				// Headers may already be sent; nothing useful to surface.
				return
			}
		case "put":
			tmp, err := os.CreateTemp("", "blob-*")
			if err != nil {
				http.Error(w, "stage", http.StatusInternalServerError)
				return
			}
			defer func() { _ = os.Remove(tmp.Name()) }()
			if _, err := io.Copy(tmp, r.Body); err != nil {
				_ = tmp.Close()
				http.Error(w, "stage", http.StatusInternalServerError)
				return
			}
			if err := tmp.Close(); err != nil {
				http.Error(w, "stage", http.StatusInternalServerError)
				return
			}
			if err := storage.Commit(r.Context(), key, tmp.Name()); err != nil {
				http.Error(w, "commit", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "bad op", http.StatusBadRequest)
		}
	}
}
