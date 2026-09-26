package origin

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

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

// pixelFormats is the 4:2:0 format each supported bit depth encodes in.
var pixelFormats = map[int]string{8: "yuv420p", 10: "yuv420p10le"}

// Stream is a resolved spec: the strategies it names, bound. Audio is nil for a silent stream.
type Stream struct {
	Packager Packager
	Video    VideoCodec
	// Heights are the renditions, ascending; the last is the tallest.
	Heights  []int
	Depth    int
	Transfer Transfer
	Audio    AudioCodec
	Channels int
	Carriage Carriage
	Segments SegmentsSpec
	Seconds  int
	Entry    EntrySpec

	// Knobs a quirk bends; zero is the plain synthetic source.
	Rate     string   // testsrc frame rate, as ffmpeg takes it ("15", "59.94", "24000/1001")
	VideoIn  []string // input options before the testsrc -i
	AudioIn  []string // input options before the sine -i
	Filters  []string // filters after the transfer tag, before the ladder splits
	VideoOut []string // after the video encoder's arguments; a repeated option wins
	AudioOut []string // after the audio encoder's arguments
	MuxOut   []string // before the packager's arguments

	// Facts a quirk records for the judge.
	Rotation   int
	AudioDelay time.Duration
	SampleRate int
	Interlaced bool
	Chroma     int
}

// Height is the tallest rendition.
func (s Stream) Height() int { return s.Heights[len(s.Heights)-1] }

// Rung is the tallest rendition within ceiling, and false when every rendition is taller.
func (s Stream) Rung(ceiling int) (int, bool) {
	for _, h := range slices.Backward(s.Heights) {
		if h <= ceiling {
			return h, true
		}
	}
	return 0, false
}

func (s Stream) layout() Layout {
	return Layout{Rungs: len(s.Heights), Audio: s.Audio != nil, Carriage: s.Carriage, SegmentExt: s.Segments.Extension}
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
	heights := slices.Sorted(slices.Values(append(slices.Clone(spec.Video.Ladder), spec.Video.Height)))
	heights = slices.DeleteFunc(heights, func(h int) bool { return h == 0 })
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
