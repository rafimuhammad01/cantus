package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"cantus/backend/api/handlers"
	"cantus/backend/services"
)

func newBlobFixture(t *testing.T) (*services.LocalDiskStorage, *services.BlobTokener) {
	t.Helper()
	s, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalDiskStorage: %v", err)
	}
	signer, err := services.NewSigner(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s, services.NewBlobTokener(signer)
}

func newBlobRequest(t *testing.T, method, key, op, exp, token string, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method,
		"/internal/blob/"+key+"?op="+op+"&exp="+exp+"&token="+token,
		strings.NewReader(body))
	chiCtx := chi.NewRouteContext()
	chiCtx.URLParams.Add("*", key)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chiCtx))
}

func TestBlob_GET_returnsFile(t *testing.T) {
	s, bt := newBlobFixture(t)
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	key := s.Key("abc12345678", "melody.json")
	if err := s.Commit(context.Background(), key, src); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	exp := time.Now().Add(5 * time.Minute)
	token := bt.Sign(key, "get", exp)
	r := newBlobRequest(t, http.MethodGet, key, "get", strconv.FormatInt(exp.Unix(), 10), token, "")
	w := httptest.NewRecorder()

	handlers.Blob(s, bt)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "hello" {
		t.Errorf("body = %q, want %q", w.Body.String(), "hello")
	}
}

func TestBlob_PUT_writesFile(t *testing.T) {
	s, bt := newBlobFixture(t)
	key := s.Key("abc12345678", "melody.json")
	exp := time.Now().Add(5 * time.Minute)
	token := bt.Sign(key, "put", exp)
	r := newBlobRequest(t, http.MethodPut, key, "put", strconv.FormatInt(exp.Unix(), 10), token, "payload")
	w := httptest.NewRecorder()

	handlers.Blob(s, bt)(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	has, err := s.Has(context.Background(), key)
	if err != nil || !has {
		t.Errorf("Has after PUT = (%v, %v), want (true, nil)", has, err)
	}
}

func TestBlob_rejects(t *testing.T) {
	s, bt := newBlobFixture(t)
	key := s.Key("abc12345678", "melody.json")

	cases := []struct {
		name     string
		method   string
		op       string
		expDelta time.Duration
		token    string // empty = use a valid token for (key, op, exp); non-empty = use this literal
		wantCode int
	}{
		{name: "bad token", method: http.MethodGet, op: "get", expDelta: 5 * time.Minute, token: "deadbeef", wantCode: http.StatusForbidden},
		{name: "expired", method: http.MethodGet, op: "get", expDelta: -1 * time.Second, wantCode: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := time.Now().Add(tc.expDelta)
			tok := tc.token
			if tok == "" {
				tok = bt.Sign(key, tc.op, exp)
			}
			r := newBlobRequest(t, tc.method, key, tc.op, strconv.FormatInt(exp.Unix(), 10), tok, "")
			w := httptest.NewRecorder()
			handlers.Blob(s, bt)(w, r)
			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d; body=%s", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}
