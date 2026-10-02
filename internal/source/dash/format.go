// Package dash is castor's DASH reader: it translates a presentation into the segments castor republishes.
package dash

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/Eyevinn/dash-mpd/mpd"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// Format is DASH as a source.Format: the other source shape that publishes a choice.
type Format struct{}

var _ source.Format = Format{}

func (Format) Identity() source.Identity {
	return source.Identity{ContentType: media.DASH, Extensions: []string{".mpd"}, MIMETypes: []string{media.DASH}}
}

// Resolve reads the presentation, chooses the representation the ceiling admits and the sound to play with it.
func (Format) Resolve(ctx context.Context, env source.Env, s source.Subject) (source.Resolution, error) {
	stream, origin := s.Stream, s.Origin
	body, _, status, err := env.Client.Fetch(ctx, stream.URL, stream.Headers)
	if err != nil {
		return source.Resolution{Origin: origin}, fmt.Errorf("reading the DASH presentation (status %d): %w", status, err)
	}
	m, ok := parse(body)
	if !ok {
		return source.Resolution{Origin: origin}, errors.New("the document is not a DASH presentation castor can read")
	}
	periods := m.placed()
	film := feature(periods, m.live())

	origin.Live, origin.Segmented, origin.Framing = m.live(), true, media.FramingOutOfBand
	// Periods are pieces encoded apart, whose parameters and timestamps restart at each boundary.
	origin.Spliced = len(periods) > 1
	if total := periods[len(periods)-1].start + periods[len(periods)-1].duration; !origin.Live && total > 0 {
		origin.Duration = total
	}
	origin.Protection = protection(periods)
	origin.Renditions = ladder(film, stream.Probe)

	var picture, sound *offered
	var chosen source.Rendition
	if len(origin.Renditions) > 0 {
		chosen = choose(origin, s.Chosen, env.MaxHeight)
		source.ReportRendition(ctx, chosen, origin, env.MaxHeight)
		video := offers(film.Period, media.TrackVideo)
		picture = &video[slices.IndexFunc(video, func(o offered) bool { return o.rep.Id == chosen.Representation })]
	}
	if picture == nil || !picture.muxed() {
		if audio := offers(film.Period, media.TrackAudio); len(audio) > 0 {
			best := slices.MaxFunc(audio, byAudio)
			sound = &best
		}
	}
	if picture == nil && sound == nil {
		return source.Resolution{Origin: origin}, errors.New("the DASH presentation offers neither picture nor sound castor can cast")
	}

	fetch := media.Fetch{Segmented: true, Framing: media.FramingOutOfBand, Live: origin.Live, Spliced: origin.Spliced}
	headers := source.WithSession(stream.Headers, env.Client.Session(stream.URL))
	input := func(id media.InputID, o *offered) media.Input {
		return media.Input{ID: id, URL: stream.URL, Representation: o.rep.Id, Headers: headers, ContentType: media.DASH, Fetch: fetch}
	}
	var inputs []media.Input
	var tracks []media.TrackRef
	end := media.EndAtLongest
	switch {
	case picture != nil && sound != nil:
		inputs = []media.Input{input(media.PrimaryInputID, picture), input(media.AudioInputID, sound)}
		tracks = []media.TrackRef{{Input: media.PrimaryInputID, Kind: media.TrackVideo}, {Input: media.AudioInputID, Kind: media.TrackAudio}}
		// Sound missing from a Period, or from one a live presentation has yet to publish, would cut the film short at the shortest input.
		if w, _ := wantedIn(periods, film, media.TrackAudio, sound.rep.Id); !origin.Live && !slices.ContainsFunc(periods, func(p placed) bool {
			_, has := w.in(p.Period)
			return !has
		}) {
			end = media.EndAtShortest
		}
	case picture != nil:
		inputs = []media.Input{input(media.PrimaryInputID, picture)}
		tracks = []media.TrackRef{{Input: media.PrimaryInputID, Kind: media.TrackVideo}, {Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true}}
	default:
		inputs = []media.Input{input(media.PrimaryInputID, sound)}
		tracks = []media.TrackRef{{Input: media.PrimaryInputID, Kind: media.TrackAudio}}
	}
	if sound != nil {
		slog.InfoContext(ctx, "the presentation publishes sound apart; both representations will be read",
			"video", chosen.Representation, "audio", sound.rep.Id, "periods", len(periods))
	}

	program, err := media.NewProgram(media.Program{Inputs: inputs, Tracks: tracks, ClockInput: media.PrimaryInputID, EndPolicy: end})
	if err != nil {
		return source.Resolution{Origin: origin, Rendition: chosen}, fmt.Errorf("normalizing the DASH program: %w", err)
	}
	published, err := source.ProgramFor(&stream)
	if err != nil {
		return source.Resolution{Origin: origin, Rendition: chosen}, err
	}
	if program, err = source.Described(program, chosen, published); err != nil {
		return source.Resolution{Origin: origin, Rendition: chosen}, fmt.Errorf("binding the chosen representation: %w", err)
	}
	return source.Resolution{Program: program, Origin: origin, Rendition: chosen}, nil
}

// choose is the rung a caller already settled on when the presentation still offers it, else the one the ceiling admits.
func choose(origin source.Origin, settled source.Rendition, ceiling media.HeightCap) source.Rendition {
	if i := slices.IndexFunc(origin.Renditions, func(r source.Rendition) bool {
		return settled.Representation != "" && r.Representation == settled.Representation
	}); i >= 0 {
		return origin.Renditions[i]
	}
	return origin.Choose(ceiling, byHeight)
}

// signature is the root element every presentation is written under.
const signature = "<MPD"

// Recognize reads a presentation written as a document whose root element is a required signature.
func (Format) Recognize(body string) source.Reading {
	if !strings.Contains(body, signature) {
		return source.Reading{}
	}
	read := source.Reading{Ladder: source.LadderMultivariant}
	// A sniffed head is cut short and parses as nothing, but sniffing only asks for the ladder.
	if m, ok := parse(body); ok {
		read.Refs = m.references()
	}
	return read
}

// references is every resource the presentation names outright, templated names aside.
func (m presentation) references() []string {
	var out []string
	add := func(refs ...string) {
		for _, ref := range refs {
			if ref = strings.TrimSpace(ref); ref != "" && !strings.Contains(ref, "$") {
				out = append(out, ref)
			}
		}
	}
	level := func(bases []*mpd.BaseURLType, t *mpd.SegmentTemplateType, l *mpd.SegmentListType, b *mpd.SegmentBaseType) {
		for _, u := range bases {
			add(string(u.Value))
		}
		var inits []*mpd.URLType
		if t != nil {
			add(t.Media, t.Initialization)
			inits = append(inits, t.SegmentBaseType.Initialization)
		}
		if l != nil {
			for _, u := range l.SegmentURL {
				add(string(u.Media))
			}
			inits = append(inits, l.SegmentBaseType.Initialization)
		}
		if b != nil {
			inits = append(inits, b.Initialization)
		}
		for _, i := range inits {
			if i != nil {
				add(string(i.SourceURL))
			}
		}
	}
	level(m.BaseURL, nil, nil, nil)
	for _, p := range m.Periods {
		level(p.BaseURLs, p.SegmentTemplate, p.SegmentList, p.SegmentBase)
		for _, s := range p.AdaptationSets {
			level(s.BaseURLs, s.SegmentTemplate, s.SegmentList, s.SegmentBase)
			for _, r := range s.Representations {
				level(r.BaseURLs, r.SegmentTemplate, r.SegmentList, r.SegmentBase)
			}
		}
	}
	return out
}
