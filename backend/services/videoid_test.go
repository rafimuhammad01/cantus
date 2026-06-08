package services_test

import (
	"testing"

	"cantus/backend/services"
)

func TestValidVideoID(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{
			name: "empty string",
			s:    "",
			want: false,
		},
		{
			name: "10 chars too short",
			s:    "dQw4w9WgXc",
			want: false,
		},
		{
			name: "12 chars too long",
			s:    "dQw4w9WgXcQQ",
			want: false,
		},
		{
			name: "11 chars with slash",
			s:    "dQw4w9WgX/Q",
			want: false,
		},
		{
			name: "11 chars with dot",
			s:    "dQw4w9WgX.Q",
			want: false,
		},
		{
			name: "11 chars with space",
			s:    "dQw4w9WgX Q",
			want: false,
		},
		{
			name: "valid rickroll id",
			s:    "dQw4w9WgXcQ",
			want: true,
		},
		{
			name: "valid 11 underscores",
			s:    "___________",
			want: true,
		},
		{
			name: "valid 11 hyphens",
			s:    "-----------",
			want: true,
		},
		{
			name: "valid 11 digits",
			s:    "12345678901",
			want: true,
		},
		{
			name: "valid mixed case",
			s:    "AbCdEfGhIjK",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := services.ValidVideoID(tt.s)
			if got != tt.want {
				t.Errorf("ValidVideoID(%q): got %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}
