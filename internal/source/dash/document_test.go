package dash

import (
	"testing"

	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/sourcetest"
)

func TestLadderComesFromTheDocumentsOwnSyntax(t *testing.T) {
	parse := source.Formats{Format{}}.Parse
	base := sourcetest.URL(t, "https://cdn.example/a/index")
	if got, _ := parse(ffmpegPresentation, base); got != source.LadderMultivariant {
		t.Errorf("presentation ladder = %v, want multivariant", got)
	}
	if got, _ := parse("<!doctype html><html>403 Forbidden</html>", base); got != source.LadderUnknown {
		t.Errorf("error page ladder = %v, want unknown", got)
	}
}
