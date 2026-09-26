package source

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/sourcetest"
	"github.com/stupside/castor/internal/source/timeline"
)

// format is a grammar that recognises token, reading a ladder when whole.
type format struct {
	token string
	whole bool
	refs  []string
}

func (format) Name() string       { return "fake" }
func (format) Identity() Identity { return Identity{} }
func (f format) Recognize(body string) Reading {
	if !strings.Contains(body, f.token) {
		return Reading{}
	}
	if f.whole {
		return Reading{Ladder: LadderMultivariant, Refs: f.refs}
	}
	return Reading{Ladder: LadderSole, Refs: f.refs}
}

func (format) Timeline(Env, media.Input, media.TrackKind) timeline.Source { return nil }

func (format) Resolve(context.Context, Env, Subject) (Resolution, error) {
	return Resolution{}, nil
}
func TestParseReadsABodyInTheFirstGrammarThatRecognisesIt(t *testing.T) {
	formats := Formats{
		format{token: "#A", whole: true, refs: []string{"v/1080", "https://other.example/360"}},
		format{token: "#B", refs: []string{"never"}},
	}
	base := sourcetest.URL(t, "https://cdn.example/a/index")

	doc := formats.Parse("#A #B", base)
	if doc.Ladder != LadderMultivariant {
		t.Fatalf("ladder = %v, want the first grammar's reading", doc.Ladder)
	}
	got := make([]string, 0, len(doc.Names))
	for _, u := range doc.Names {
		got = append(got, u.String())
	}
	if want := []string{"https://cdn.example/a/v/1080", "https://other.example/360"}; !slices.Equal(got, want) {
		t.Errorf("names = %q, want %q resolved against the base", got, want)
	}
	if doc := formats.Parse("<html>403 Forbidden</html>", base); doc.Ladder != LadderUnknown || doc.Names != nil {
		t.Errorf("Parse = %v %v, want nothing read from an error page", doc.Ladder, doc.Names)
	}
}
