package dash

import (
	"cmp"
	"slices"

	"github.com/Eyevinn/dash-mpd/mpd"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

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
