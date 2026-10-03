package origin

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/stupside/castor/e2e/strategy"
)

// Spec is a case's stream section.
type Spec struct {
	Packaging string       `yaml:"packaging"`
	Video     VideoSpec    `yaml:"video"`
	Audio     *AudioSpec   `yaml:"audio"`
	Segments  SegmentsSpec `yaml:"segments"`
	Seconds   int          `yaml:"seconds"`
	// Quirks bend the stream in order, after everything else is bound.
	Quirks strategy.Choices `yaml:"quirks"`
	// Entry serves the stream's entry under another name and type, as script-generated playlists are.
	Entry EntrySpec `yaml:"entry"`
}

// EntrySpec renames the entry castor is pointed at, as in `entry: {path: "playlist.php?id=42", served_as: text/html}`.
type EntrySpec struct {
	Path     string `yaml:"path"`
	ServedAs string `yaml:"served_as"`
}

type VideoSpec struct {
	Codec string `yaml:"codec"`
	// Height is one rendition; Ladder is several, and the two are exclusive.
	Height   int    `yaml:"height"`
	Ladder   []int  `yaml:"ladder"`
	Depth    int    `yaml:"depth"`
	Transfer string `yaml:"transfer"`
}

type AudioSpec struct {
	Codec    string   `yaml:"codec"`
	Channels int      `yaml:"channels"`
	Carriage Carriage `yaml:"carriage"`
}

// SegmentsSpec renames segments and the type they are served as, as embed CDNs disguise them.
type SegmentsSpec struct {
	Extension string `yaml:"extension"`
	ServedAs  string `yaml:"served_as"`
}

// Catalog is every strategy a spec may name.
type Catalog struct {
	Packagers strategy.Registry[Packager]
	Video     strategy.Registry[VideoCodec]
	Audio     strategy.Registry[AudioCodec]
	Transfers strategy.Registry[Transfer]
	Quirks    strategy.Registry[strategy.Factory[Quirk]]
}

// Resolve binds spec to the catalog's strategies and refuses what cannot be published.
func (c Catalog) Resolve(spec Spec) (Stream, error) {
	packager, err := c.Packagers.Lookup(spec.Packaging)
	if err != nil {
		return Stream{}, fmt.Errorf("stream.packaging: %w", err)
	}
	video, err := c.Video.Lookup(spec.Video.Codec)
	if err != nil {
		return Stream{}, fmt.Errorf("stream.video.codec: %w", err)
	}
	transfer, err := c.Transfers.Lookup(cmp.Or(spec.Video.Transfer, "sdr"))
	if err != nil {
		return Stream{}, fmt.Errorf("stream.video.transfer: %w", err)
	}
	heights := slices.DeleteFunc(slices.Concat(spec.Video.Ladder, []int{spec.Video.Height}), func(h int) bool { return h == 0 })
	slices.Sort(heights)
	if len(heights) == 0 || (spec.Video.Height != 0 && len(spec.Video.Ladder) > 0) || heights[0] < 2 {
		return Stream{}, fmt.Errorf("stream.video: want a height, or a ladder of heights, not both")
	}
	depth := cmp.Or(spec.Video.Depth, 8)
	if _, ok := pixelFormats[depth]; !ok {
		return Stream{}, fmt.Errorf("stream.video.depth %d: want one of %v", depth, slices.Sorted(maps.Keys(pixelFormats)))
	}
	s := Stream{Packager: packager, Video: video, Heights: slices.Compact(heights), Depth: depth, Transfer: transfer,
		Segments: spec.Segments, Seconds: cmp.Or(spec.Seconds, 6)}
	if spec.Audio != nil {
		if s.Audio, err = c.Audio.Lookup(spec.Audio.Codec); err != nil {
			return Stream{}, fmt.Errorf("stream.audio.codec: %w", err)
		}
		if spec.Audio.Channels < 1 {
			return Stream{}, fmt.Errorf("stream.audio.channels %d: want at least one", spec.Audio.Channels)
		}
		s.Channels, s.Carriage = spec.Audio.Channels, cmp.Or(spec.Audio.Carriage, Muxed)
		if !slices.Contains(carriages, s.Carriage) {
			return Stream{}, fmt.Errorf("stream.audio.carriage %q: want one of %v", s.Carriage, carriages)
		}
	}
	if err := packager.Supports(s.layout()); err != nil {
		return Stream{}, fmt.Errorf("stream: %w", err)
	}
	if spec.Entry.Path != "" && (strings.Contains(spec.Entry.Path, "/") || spec.Entry.ServedAs == "") {
		return Stream{}, errors.New("stream.entry: want a path with no slash and the type it is served as")
	}
	s.Entry = spec.Entry
	for _, choice := range spec.Quirks {
		quirk, err := strategy.Build(c.Quirks, choice)
		if err != nil {
			return Stream{}, fmt.Errorf("stream.quirks.%s: %w", choice.Name, err)
		}
		if err := quirk.Bend(&s); err != nil {
			return Stream{}, fmt.Errorf("stream.quirks.%s: %w", choice.Name, err)
		}
	}
	return s, nil
}
