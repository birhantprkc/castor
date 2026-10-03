package source_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
	"github.com/stupside/castor/services/mediaserver/internal/source/dash"
	"github.com/stupside/castor/services/mediaserver/internal/source/hls"
	"github.com/stupside/castor/services/mediaserver/internal/source/sourcetest"
	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// format is a grammar that recognises token, reading a ladder when whole.
type format struct {
	token string
	whole bool
	refs  []string
}

func (format) Identity() source.Identity { return source.Identity{} }
func (f format) Recognize(body string) source.Reading {
	if !strings.Contains(body, f.token) {
		return source.Reading{}
	}
	if f.whole {
		return source.Reading{Ladder: source.LadderMultivariant, Refs: f.refs}
	}
	return source.Reading{Ladder: source.LadderSole, Refs: f.refs}
}

func (format) Timeline(source.Client, media.Input, media.TrackKind) timeline.Source { return nil }

func (format) InputArgs(int) []string { return nil }

func (format) Resolve(context.Context, source.Env, source.Subject) (source.Resolution, error) {
	return source.Resolution{}, nil
}

func TestParseReadsABodyInTheFirstGrammarThatRecognisesIt(t *testing.T) {
	formats := source.Formats{
		format{token: "#A", whole: true, refs: []string{"v/1080", "https://other.example/360"}},
		format{token: "#B", refs: []string{"never"}},
	}
	base := sourcetest.URL(t, "https://cdn.example/a/index")

	doc := formats.Parse("#A #B", base)
	if doc.Ladder != source.LadderMultivariant {
		t.Fatalf("ladder = %v, want the first grammar's reading", doc.Ladder)
	}
	got := make([]string, 0, len(doc.Names))
	for _, u := range doc.Names {
		got = append(got, u.String())
	}
	if want := []string{"https://cdn.example/a/v/1080", "https://other.example/360"}; !slices.Equal(got, want) {
		t.Errorf("names = %q, want %q resolved against the base", got, want)
	}
	if doc := formats.Parse("<html>403 Forbidden</html>", base); doc.Ladder != source.LadderUnknown || doc.Names != nil {
		t.Errorf("Parse = %v %v, want nothing read from an error page", doc.Ladder, doc.Names)
	}
}

func TestALinkIsNamedByTheFormatThatDeclaresItsName(t *testing.T) {
	formats := source.Formats{hls.Format{}, dash.Format{}}
	for _, tc := range []struct {
		raw, mime, want string
	}{
		{"https://cdn.example/MASTER.M3U8?token=x", "", media.HLS},
		{"https://cdn.example/manifest.mpd", "", media.DASH},
		{"https://cdn.example/manifest", "application/dash+xml", media.DASH},
		{"https://cdn.example/index", "Application/VND.Apple.MPEGURL", media.HLS},
		{"https://cdn.example/movie.mp4", "", media.MP4},
		// The name the link spells wins over the type the server claims.
		{"https://cdn.example/master.m3u8", "video/mp4", media.HLS},
		{"https://cdn.example/player/embed", "text/html", ""},
	} {
		if got := formats.ContentTypeOf(sourcetest.URL(t, tc.raw), tc.mime); got != tc.want {
			t.Errorf("ContentTypeOf(%s, %q) = %q, want %q", tc.raw, tc.mime, got, tc.want)
		}
	}
}
