// Package catalog maps a title to the pages the operator's sites publish it at.
package catalog

import (
	"strconv"
	"strings"
)

// site is a set of proxy hosts and the URL templates to reach a movie or episode page.
type site struct {
	Proxies   []string  `yaml:"proxies" validate:"required,min=1"`
	Templates templates `yaml:"templates" validate:"required"`
}

type templates struct {
	Movie   string `yaml:"movie" validate:"required"`
	Episode string `yaml:"episode" validate:"required"`
}

// Sites is every site the operator configured, tried in order.
type Sites []site

func (s Sites) MovieURLs(itemID string) []string {
	var urls []string
	for _, site := range s {
		urls = append(urls, site.expand(site.Templates.Movie, "{itemID}", itemID)...)
	}
	return urls
}

func (s Sites) EpisodeURLs(itemID string, season, episode uint) []string {
	var urls []string
	for _, site := range s {
		urls = append(urls, site.expand(site.Templates.Episode,
			"{itemID}", itemID,
			"{season}", strconv.FormatUint(uint64(season), 10),
			"{episode}", strconv.FormatUint(uint64(episode), 10),
		)...)
	}
	return urls
}

// expand substitutes pairs into tmpl and prefixes the result with every proxy host.
func (s site) expand(tmpl string, pairs ...string) []string {
	route := strings.NewReplacer(pairs...).Replace(tmpl)
	urls := make([]string, len(s.Proxies))
	for i, proxy := range s.Proxies {
		urls[i] = proxy + route
	}
	return urls
}
