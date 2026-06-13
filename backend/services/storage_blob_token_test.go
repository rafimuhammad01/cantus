package services_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"cantus/backend/services"
)

func TestBlobToken_signAndVerify(t *testing.T) {
	signer, err := services.NewSigner(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	bt := services.NewBlobTokener(signer)
	now := time.Unix(1700000000, 0)
	exp := now.Add(5 * time.Minute)
	token := bt.Sign("abc/melody.json", "get", exp)
	if err := bt.Verify("abc/melody.json", "get", token, exp.Unix(), now); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestBlobToken_rejects(t *testing.T) {
	signer, _ := services.NewSigner(strings.Repeat("k", 32))
	bt := services.NewBlobTokener(signer)
	now := time.Unix(1700000000, 0)

	cases := []struct {
		name      string
		signKey   string
		signOp    string
		signExp   time.Time
		verifyKey string
		verifyOp  string
		verifyExp time.Time
		wantErr   error
	}{
		{
			name:    "expired",
			signKey: "abc/melody.json", signOp: "get",
			signExp:   now.Add(-1 * time.Second),
			verifyKey: "abc/melody.json", verifyOp: "get",
			verifyExp: now.Add(-1 * time.Second),
			wantErr:   services.ErrBlobTokenExpired,
		},
		{
			name:    "wrong op",
			signKey: "abc/melody.json", signOp: "get",
			signExp:   now.Add(5 * time.Minute),
			verifyKey: "abc/melody.json", verifyOp: "put",
			verifyExp: now.Add(5 * time.Minute),
			wantErr:   services.ErrBlobTokenInvalid,
		},
		{
			name:    "wrong key",
			signKey: "abc/melody.json", signOp: "get",
			signExp:   now.Add(5 * time.Minute),
			verifyKey: "xyz/melody.json", verifyOp: "get",
			verifyExp: now.Add(5 * time.Minute),
			wantErr:   services.ErrBlobTokenInvalid,
		},
		{
			name:    "tampered exp",
			signKey: "abc/melody.json", signOp: "get",
			signExp:   now.Add(5 * time.Minute),
			verifyKey: "abc/melody.json", verifyOp: "get",
			verifyExp: now.Add(10 * time.Minute),
			wantErr:   services.ErrBlobTokenInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := bt.Sign(tc.signKey, tc.signOp, tc.signExp)
			err := bt.Verify(tc.verifyKey, tc.verifyOp, token, tc.verifyExp.Unix(), now)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Verify: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}
