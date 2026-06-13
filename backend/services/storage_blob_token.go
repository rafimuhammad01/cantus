package services

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrBlobTokenInvalid = errors.New("blob: invalid token")
	ErrBlobTokenExpired = errors.New("blob: token expired")
)

// BlobTokener signs and verifies short-lived HMAC tokens for /internal/blob URLs.
// Reuses the existing Signer (and thus VIDEO_ID_SIGNING_KEY) so we don't add a
// second secret to manage.
type BlobTokener struct {
	signer *Signer
}

func NewBlobTokener(signer *Signer) *BlobTokener {
	return &BlobTokener{signer: signer}
}

func (b *BlobTokener) payload(key, op string, expUnix int64) string {
	return fmt.Sprintf("%s|%s|%d", key, op, expUnix)
}

// Sign returns a hex-encoded HMAC binding key+op+exp.
func (b *BlobTokener) Sign(key, op string, exp time.Time) string {
	return b.signer.Sign(b.payload(key, op, exp.Unix()))
}

// Verify reports nil iff token matches and exp has not passed at now.
func (b *BlobTokener) Verify(key, op, token string, expUnix int64, now time.Time) error {
	if now.Unix() > expUnix {
		return ErrBlobTokenExpired
	}
	if !b.signer.Valid(b.payload(key, op, expUnix), token) {
		return ErrBlobTokenInvalid
	}
	return nil
}
