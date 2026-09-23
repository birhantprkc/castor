package source

import (
	"testing"

	"github.com/stupside/castor/internal/source/sourcetest"
)

func TestTheFirstFormatClaimingASourceReadsIt(t *testing.T) {
	var first, second []Subject
	formats := Formats{
		format{name: "first", claims: "application/x-claimed", asked: &first},
		format{name: "second", claims: "application/x-claimed", asked: &second},
	}
	resolver := NewResolver(testConfig, &sourcetest.Playlist{}, formats)

	link := &Candidate{URL: sourcetest.URL(t, "https://cdn.example/claimed"), ContentType: "application/x-claimed"}
	rung := Rendition{URL: sourcetest.URL(t, "https://cdn.example/720"), Height: 720}
	if _, err := resolver.RefetchProgram(t.Context(), link, rung); err != nil {
		t.Fatalf("RefetchProgram: %v", err)
	}
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("formats asked %d and %d times, want the first once and the second never", len(first), len(second))
	}
	if first[0].Chosen.URL != rung.URL {
		t.Errorf("format was handed rung %+v, want the one the caller narrowed to", first[0].Chosen)
	}
}
