package dash

import (
	"cmp"
	"slices"
	"strings"

	"github.com/Eyevinn/dash-mpd/mpd"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// offered is one representation a Period offers, with the set it inherits from.
type offered struct {
	set *mpd.AdaptationSetType
	rep *mpd.RepresentationType
}

func (o offered) kind() media.TrackKind { return kind(o.set, o.rep) }
func (o offered) height() int           { return height(o.set, o.rep) }
func (o offered) codecs() string        { return codecs(o.set, o.rep) }

// muxed is a video representation whose segments carry its sound too.
func (o offered) muxed() bool {
	return o.kind() == media.TrackVideo && slices.ContainsFunc(strings.Split(o.codecs(), ","), func(c string) bool {
		return !source.DeclaresVideo(c) && strings.TrimSpace(c) != ""
	})
}

// offers is every representation of a kind a Period offers to cast, trick-play sets aside.
func offers(p *mpd.Period, k media.TrackKind) []offered {
	var out []offered
	for _, s := range p.AdaptationSets {
		if trickMode(s) {
			continue
		}
		for _, r := range s.Representations {
			if kind(s, r) == k {
				out = append(out, offered{set: s, rep: r})
			}
		}
	}
	return out
}

// feature is the Period a choice is made in: live, the one still open; else the longest, since ads are short and the film is what is chosen for.
func feature(periods []placed, live bool) placed {
	if live {
		return periods[len(periods)-1]
	}
	return slices.MaxFunc(periods, func(a, b placed) int {
		return cmp.Or(cmp.Compare(a.duration, b.duration), cmp.Compare(len(offers(a.Period, media.TrackVideo)), len(offers(b.Period, media.TrackVideo))), cmp.Compare(b.index, a.index))
	})
}

// ladder is the video rungs the feature offers as the presentation declares them; a measurement only fills a height it omits.
func ladder(p placed, probe *media.ProbeInfo) []source.Rendition {
	video := offers(p.Period, media.TrackVideo)
	rungs := make([]source.Rendition, len(video))
	for i, o := range video {
		rungs[i] = source.Rendition{
			Representation: o.rep.Id,
			Height:         o.height(),
			Bitrate:        media.Bitrate(o.rep.Bandwidth),
			Declared:       source.DeclaredEnvelope(o.codecs(), o.height()),
		}
	}
	// ffprobe numbers the first Period's representations in document order, so only then do the two pair by position.
	if probe != nil && p.index == 0 && len(probe.VideoHeights) == len(rungs) {
		for i := range rungs {
			if rungs[i].Height == 0 && probe.VideoHeights[i] > 0 {
				rungs[i].Height = probe.VideoHeights[i]
				if rungs[i].Declared != nil {
					rungs[i].Declared.VideoHeight = rungs[i].Height
				}
			}
		}
	}
	return rungs
}

// byHeight prefers the tallest rung, then the richer, then the one every renderer decodes.
func byHeight(a, b source.Rendition) int {
	return cmp.Or(cmp.Compare(a.Height, b.Height), cmp.Compare(a.Bitrate, b.Bitrate), cmp.Compare(avc(a), avc(b)))
}

func avc(r source.Rendition) int {
	if r.Declared != nil && r.Declared.VideoCodec == media.CodecH264 {
		return 1
	}
	return 0
}

// audioRank orders sound a renderer is likeliest to decode first.
var audioRank = []string{"mp4a", "ec-3", "ac-3", "opus"}

func rank(codecs string) int {
	family, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(codecs)), ".")
	if i := slices.Index(audioRank, family); i >= 0 {
		return len(audioRank) - i
	}
	return 0
}

// byAudio prefers the main track, then the codec a renderer likeliest decodes, then the richer.
func byAudio(a, b offered) int {
	return cmp.Or(cmp.Compare(boolean(mainRole(a.set)), boolean(mainRole(b.set))), cmp.Compare(rank(a.codecs()), rank(b.codecs())), cmp.Compare(a.rep.Bandwidth, b.rep.Bandwidth))
}

func boolean(b bool) int {
	if b {
		return 1
	}
	return 0
}

// wanted is what an input reads, matched in each Period by its id first and by what it is after.
type wanted struct {
	kind     media.TrackKind
	id       string
	height   int
	lang     string
	codecs   string
	channels int
}

// wantedIn describes the representation of kind that id names; ids are only unique within a Period, so the one chosen from is asked first.
func wantedIn(periods []placed, chosenIn placed, kind media.TrackKind, id string) (wanted, bool) {
	for _, p := range slices.Concat([]placed{chosenIn}, periods) {
		for _, o := range offers(p.Period, kind) {
			if o.rep.Id == id {
				return wanted{kind: kind, id: id, height: o.height(), lang: o.set.Lang, codecs: o.codecs(), channels: channels(o.set, o.rep)}, true
			}
		}
	}
	return wanted{}, false
}

// in is the representation a Period offers for w: its own id, else the nearest in kind.
func (w wanted) in(p *mpd.Period) (offered, bool) {
	candidates := offers(p, w.kind)
	if len(candidates) == 0 {
		return offered{}, false
	}
	if i := slices.IndexFunc(candidates, func(o offered) bool { return o.rep.Id == w.id }); i >= 0 {
		return candidates[i], true
	}
	// Heights nobody declared compare as nothing, so the richest stream stands in for the tallest.
	if w.kind == media.TrackVideo && w.height == 0 {
		return slices.MaxFunc(candidates, func(a, b offered) int { return cmp.Compare(a.rep.Bandwidth, b.rep.Bandwidth) }), true
	}
	if w.kind == media.TrackVideo {
		under := slices.DeleteFunc(slices.Clone(candidates), func(o offered) bool { return o.height() > w.height })
		if len(under) == 0 {
			return slices.MinFunc(candidates, func(a, b offered) int { return cmp.Compare(a.height(), b.height()) }), true
		}
		return slices.MaxFunc(under, func(a, b offered) int {
			return cmp.Or(cmp.Compare(a.height(), b.height()), cmp.Compare(a.rep.Bandwidth, b.rep.Bandwidth))
		}), true
	}
	return slices.MaxFunc(candidates, func(a, b offered) int {
		return cmp.Or(cmp.Compare(boolean(a.set.Lang == w.lang), boolean(b.set.Lang == w.lang)),
			cmp.Compare(boolean(rank(a.codecs()) == rank(w.codecs)), boolean(rank(b.codecs()) == rank(w.codecs))),
			cmp.Compare(boolean(channels(a.set, a.rep) == w.channels), boolean(channels(b.set, b.rep) == w.channels)),
			byAudio(a, b))
	}), true
}
