package catalog

import (
	"slices"
	"testing"
)

func TestEverySiteAndProxyIsTriedInOrder(t *testing.T) {
	sites := Sites{{
		Proxies:   []string{"https://a.example", "https://mirror.example"},
		Templates: Templates{Movie: "/embed/movie/{itemID}", Episode: "/embed/tv/{itemID}/{season}-{episode}"},
	}, {
		Proxies:   []string{"https://b.example"},
		Templates: Templates{Movie: "/embed?type=movie&id={itemID}", Episode: "/embed?id={itemID}&s={season}&e={episode}"},
	}}

	if got, want := sites.MovieURLs("tt1"), []string{
		"https://a.example/embed/movie/tt1",
		"https://mirror.example/embed/movie/tt1",
		"https://b.example/embed?type=movie&id=tt1",
	}; !slices.Equal(got, want) {
		t.Errorf("MovieURLs = %v, want %v", got, want)
	}
	if got, want := sites.EpisodeURLs("tt2", 3, 7), []string{
		"https://a.example/embed/tv/tt2/3-7",
		"https://mirror.example/embed/tv/tt2/3-7",
		"https://b.example/embed?id=tt2&s=3&e=7",
	}; !slices.Equal(got, want) {
		t.Errorf("EpisodeURLs = %v, want %v", got, want)
	}
}
