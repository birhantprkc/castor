package source

import (
	"maps"
	"net/http"
	"slices"
	"testing"
)

func TestNormalizeStreamHeaders(t *testing.T) {
	in := http.Header{"Referer": {"https://player.example/watch"}, "Range": {"bytes=0-99"}, "Accept-Encoding": {"br"}}
	want := http.Header{"Referer": {"https://player.example/watch"}, "Origin": {"https://player.example"}}
	if got := NormalizeStreamHeaders(in); !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("NormalizeStreamHeaders = %v, want %v", got, want)
	}
}
