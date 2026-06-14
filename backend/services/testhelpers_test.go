package services_test

import (
	"strings"
	"testing"

	"cantus/backend/services"
)

// newTestSigner returns a Signer keyed with 32 'x' bytes.
func newTestSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}
