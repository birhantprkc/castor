package media

import (
	"maps"
	"net/http"
	"slices"
	"testing"
)

func TestStreamInfoPlayable(t *testing.T) {
	cases := []struct {
		name string
		info StreamInfo
		want bool
	}{
		{"video+audio", StreamInfo{HasVideo: true, HasAudio: true}, true},
		{"video only", StreamInfo{HasVideo: true}, false},
		{"audio only", StreamInfo{HasAudio: true}, false},
		{"neither (image decoy)", StreamInfo{}, false},
	}
	for _, c := range cases {
		if got := c.info.Playable(); got != c.want {
			t.Errorf("%s: Playable() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeStreamHeaders(t *testing.T) {
	cases := []struct {
		name string
		in   http.Header
		want http.Header
	}{
		{
			name: "derives Origin from Referer when absent",
			in:   http.Header{"Referer": {"https://player.cinezo.live/"}, "User-Agent": {"x"}},
			want: http.Header{"Referer": {"https://player.cinezo.live/"}, "User-Agent": {"x"}, "Origin": {"https://player.cinezo.live"}},
		},
		{
			name: "keeps an explicit Origin",
			in:   http.Header{"Referer": {"https://a.example/watch"}, "Origin": {"https://keep.example"}},
			want: http.Header{"Referer": {"https://a.example/watch"}, "Origin": {"https://keep.example"}},
		},
		{
			name: "drops Range and Accept-Encoding",
			in:   http.Header{"Referer": {"https://a.example/"}, "Range": {"bytes=0-99"}, "Accept-Encoding": {"br, zstd"}},
			want: http.Header{"Referer": {"https://a.example/"}, "Origin": {"https://a.example"}},
		},
		{
			name: "no Referer means no derived Origin",
			in:   http.Header{"User-Agent": {"x"}},
			want: http.Header{"User-Agent": {"x"}},
		},
		{
			name: "nil stays nil",
			in:   nil,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeStreamHeaders(c.in)
			if !maps.EqualFunc(got, c.want, slices.Equal) {
				t.Errorf("NormalizeStreamHeaders(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
