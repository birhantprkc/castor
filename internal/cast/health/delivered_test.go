package health

import (
	"errors"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

func TestShortfall(t *testing.T) {
	film := media.Progress{Position: 2 * time.Hour, Bytes: 4 << 30}
	for _, tt := range []struct {
		name    string
		sent    int64
		made    media.Progress
		convict bool
	}{
		{"a renderer that took two minutes of a film", film.Bytes / 60, film, true},
		{"a renderer that never came", 0, film, true},
		{"a renderer handed half the program", film.Bytes / 2, film, false},
		{"a delivery that produced nothing blames no renderer", 0, media.Progress{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var short *Undelivered
			if got := errors.As(Shortfall(tt.sent, tt.made), &short); got != tt.convict {
				t.Fatalf("convicted = %v, want %v", got, tt.convict)
			}
		})
	}
}

func TestNoneFetched(t *testing.T) {
	made := media.Progress{Position: 90 * time.Minute}
	for _, tt := range []struct {
		name    string
		served  int
		made    media.Progress
		convict bool
	}{
		{"a renderer that never came, even with no byte count", 0, made, true},
		{"a renderer that took a fragment", 1, made, false},
		{"a delivery that produced nothing", 0, media.Progress{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var short *Undelivered
			if got := errors.As(NoneFetched(tt.served, tt.made), &short); got != tt.convict {
				t.Fatalf("convicted = %v, want %v", got, tt.convict)
			}
		})
	}
}
