package source

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stupside/castor/internal/source/sourcetest"
)

type format struct {
	name   string
	claims string
	token  string
	whole  bool
	refs   []string

	asked *[]Subject
}

func (f format) Name() string       { return f.name }
func (f format) Identity() Identity { return Identity{ContentType: f.claims} }
func (f format) Recognize(body string) (Ladder, []string) {
	if !strings.Contains(body, f.token) {
		return LadderUnknown, nil
	}
	if f.whole {
		return LadderMultivariant, f.refs
	}
	return LadderSole, f.refs
}

func (f format) Resolve(_ context.Context, _ Env, s Subject) (Resolution, error) {
	*f.asked = append(*f.asked, s)
	return Resolution{Origin: s.Origin, Rendition: s.Chosen}, nil
}

func TestParseReadsABodyInTheFirstGrammarThatRecognisesIt(t *testing.T) {
	formats := Formats{
		format{token: "#A", whole: true, refs: []string{"v/1080", "https://other.example/360"}},
		format{token: "#B", refs: []string{"never"}},
	}
	base := sourcetest.URL(t, "https://cdn.example/a/index")

	ladder, names := formats.Parse("#A #B", base)
	if ladder != LadderMultivariant {
		t.Fatalf("ladder = %v, want the first grammar's reading", ladder)
	}
	got := make([]string, 0, len(names))
	for _, u := range names {
		got = append(got, u.String())
	}
	if want := []string{"https://cdn.example/a/v/1080", "https://other.example/360"}; !slices.Equal(got, want) {
		t.Errorf("names = %q, want %q resolved against the base", got, want)
	}
	if ladder, names := formats.Parse("<html>403 Forbidden</html>", base); ladder != LadderUnknown || names != nil {
		t.Errorf("Parse = %v %v, want nothing read from an error page", ladder, names)
	}
}
