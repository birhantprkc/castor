package source

import (
	"testing"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/sourcetest"
)

// A link whose content type nobody established is read whole.
func TestAnUnidentifiedLinkResolvesWholeWithoutAProbe(t *testing.T) {
	const raw = "http://a.example/movie"
	claimed := []Subject{}
	formats := Formats{format{name: "claimed", claims: media.HLS, asked: &claimed}}
	resolver := NewResolver(testConfig, &sourcetest.Playlist{}, formats)

	resolved, err := resolver.RefetchProgram(t.Context(), &Candidate{URL: sourcetest.URL(t, raw)}, Rendition{})
	if err != nil {
		t.Fatalf("RefetchProgram: %v", err)
	}
	if len(claimed) != 0 {
		t.Error("a format was asked to read a link whose content type nobody established")
	}
	if got := sourcetest.PrimaryInput(t, resolved.Program).URL.String(); got != raw || len(resolved.Program.Inputs) != 1 {
		t.Errorf("program = %+v, want the one input the link names", resolved.Program.Inputs)
	}
	if _, measured := resolved.Program.Measurement(); measured {
		t.Error("the program carries a measurement nobody took")
	}
}
