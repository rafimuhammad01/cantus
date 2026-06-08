package services_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"cantus/backend/services"
)

// roundTripperFunc adapts a function to the http.RoundTripper interface.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// makeResponse builds a minimal *http.Response with the given status and JSON body.
func makeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// newTestSigner returns a Signer keyed with 32 'x' bytes.
func newTestSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}
