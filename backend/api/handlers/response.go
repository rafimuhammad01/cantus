package handlers

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes a JSON response with the given status and body.
// Uses json.NewEncoder which appends a trailing newline — standard for
// HTTP JSON responses. The encoder error is intentionally ignored:
// once headers and status are written, the connection state is committed.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
