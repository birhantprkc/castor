package source

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stupside/castor/internal/media"
)

type Ranker struct {
	cfg    Config
	probes Probes
}

// NewRanker binds ranking to the measurement it pays in.
func NewRanker(cfg Config, probes Probes) *Ranker {
	return &Ranker{cfg: cfg, probes: probes}
}

// RankStreams measures every candidate and returns the order a cast walks them in, best first.
func (r *Ranker) RankStreams(ctx context.Context, streams []*Candidate) ([]*Candidate, error) {
	slog.InfoContext(ctx, "ranking streams", "count", len(streams))
	if len(streams) == 0 {
		return nil, fmt.Errorf("no streams to rank")
	}
	valid := make([]*Candidate, 0, len(streams))
	for _, stream := range streams {
		if stream != nil && stream.URL != nil {
			valid = append(valid, stream)
		}
	}
	if dropped := len(streams) - len(valid); dropped > 0 {
		slog.WarnContext(ctx, "discarded malformed stream candidates", "dropped", dropped, "kept", len(valid))
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("no streams with a URL to rank")
	}
	streams = valid
	streams = limitPerHost(ctx, streams)

	pool := make([]*Candidate, 0, len(streams))
	rejected := make(map[reason]int)
	for _, c := range r.measureAll(ctx, streams) {
		v := admit(c)
		if !v.admit {
			rejected[v.reason]++
			logRejection(ctx, c, v)
			continue
		}
		admitted(c, v)
		pool = append(pool, c)
	}
	if len(pool) == 0 {
		return nil, fmt.Errorf("no castable stream: none of %d candidates was admitted (%s)", len(streams), tally(rejected))
	}
	if len(rejected) > 0 {
		slog.InfoContext(ctx, "rejected candidates", "kept", len(pool), "detail", tally(rejected))
	}

	order := ranked(pool, r.cfg.MaxHeight)
	best := order[0]
	slog.InfoContext(ctx, "best stream selected", "url", best.URL.String(),
		"bitrate", int64(best.Bitrate()), "height", best.height(), "last_resort", best.LastResort,
		"reason", string(best.reason),
		"renditions", best.Ladder, "alternatives", len(order)-1)
	return order, nil
}

// Measure is the other half of RankStreams, for the one link an operator named himself.
func (r *Ranker) Measure(ctx context.Context, stream *Candidate) (*Candidate, error) {
	if stream == nil || stream.URL == nil {
		return nil, fmt.Errorf("no stream with a URL to measure")
	}
	one := r.measureAll(ctx, []*Candidate{stream})[0]
	verdict := admit(one)
	// The operator named it, so nothing is ranked against it: only the admission is overruled.
	verdict.admit = true
	admitted(one, verdict)
	slog.InfoContext(ctx, "direct link measured", "url", one.URL.String(),
		"bitrate", int64(one.Bitrate()), "height", one.height(),
		"reason", string(one.reason), "last_resort", one.LastResort)
	return one, nil
}

// minContentDuration is the shortest runtime treated as real content; pre-roll ads run well under it.
const minContentDuration = 5 * time.Minute

const maxProbePerHost = 5

// limitPerHost measures at most maxProbePerHost links per host, the ones whose documents promise a ladder first.
func limitPerHost(ctx context.Context, streams []*Candidate) []*Candidate {
	seen := make(map[string]int, len(streams))
	kept := make([]*Candidate, 0, len(streams))
	dropped := 0
	for _, s := range slices.SortedStableFunc(slices.Values(streams), byLadderEvidence) {
		host := s.URL.Hostname()
		if seen[host] >= maxProbePerHost {
			dropped++
			continue
		}
		seen[host]++
		kept = append(kept, s)
	}
	if dropped > 0 {
		slog.InfoContext(ctx, "skipped candidates beyond the per-host measurement cap",
			"dropped", dropped, "kept", len(kept), "per_host_cap", maxProbePerHost)
	}
	return kept
}

// height is what a probe measured of this candidate's picture, or zero when no measurement established one.
func (c *Candidate) height() int {
	if c.Probe == nil {
		return 0
	}
	return c.Probe.VideoHeight
}

// measureAll measures every candidate concurrently, bounded by the configured fan-out.
func (r *Ranker) measureAll(ctx context.Context, streams []*Candidate) []*Candidate {
	out := make([]*Candidate, len(streams))
	sem := make(chan struct{}, r.cfg.ProbeMaxConcurrency)

	var wg sync.WaitGroup
	for i, s := range streams {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			slog.DebugContext(ctx, "probing stream", "url", s.URL, "index", i+1, "total", len(streams))
			info, reach, err := r.probes(s).Probe(ctx)
			var probed *media.ProbeInfo
			contentType := s.ContentType
			if err != nil {
				slog.WarnContext(ctx, "probe failed", "url", s.URL, "reach", reach, "error", err)
			} else {
				probed = &info
				// A container the URL did not name is one the probe just established.
				contentType = cmp.Or(contentType, info.ContentType)
			}
			out[i] = &Candidate{
				URL:         s.URL,
				Headers:     s.Headers,
				ContentType: contentType,
				// Carried, never re-derived: the ladder was read from a body only the browser held.
				Ladder: s.Ladder,
				Probe:  probed,
				reach:  reach,
			}
		})
	}
	wg.Wait()
	return out
}

// reason names the rule that decided a candidate's fate.
type reason string

const (
	reasonCastable        reason = "carried a castable program"
	reasonUnproven        reason = "unmeasurable, admitted as a last resort"
	reasonRefused         reason = "refused by the origin"
	reasonBrowserInternal reason = "a browser-internal handle: the real stream was never captured"
	reasonNoProgram       reason = "carried no video program"
	reasonTooShort        reason = "too short to be content, treated as an ad"
	// A header captured instead of the thing it heads; the move is casting the manifest that lists it.
	reasonFragmentHeader reason = "an MP4 header with no timeline of its own, admitted as a last resort"
)

// admissionRule is one row: the shape it recognises, and what that shape earns.
type admissionRule struct {
	reason     reason
	when       func(c *Candidate) bool
	admit      bool
	lastResort bool
}

// admissionTable is the ordered rows plus the row that answers whatever they did not.
type admissionTable struct {
	rules []admissionRule
	total admissionRule
}

var browserInternalSchemes = []string{"blob", "filesystem"}

// browserInternal reports a URL that names bytes inside a browser.
func browserInternal(u *url.URL) bool {
	return slices.Contains(browserInternalSchemes, u.Scheme)
}

// admissions decides what castor will attempt, first match wins; Order is the contract.
var admissions = admissionTable{rules: []admissionRule{{
	// First, because no measurement can rescue it; structurally uncastable is not unproven.
	reason: reasonBrowserInternal,
	when:   func(c *Candidate) bool { return browserInternal(c.URL) },
}, {
	// A spent signed link answers 403 to every reader alike, so nothing rescues it.
	reason: reasonRefused,
	when:   func(c *Candidate) bool { return c.reach == media.ReachRefused },
}, {
	reason:     reasonUnproven,
	when:       func(c *Candidate) bool { return c.Probe == nil },
	admit:      true,
	lastResort: true,
}, {
	// Measured cleanly and carries no moving picture; audio is not required.
	reason: reasonNoProgram,
	when:   func(c *Candidate) bool { return !movingPicture(c.Probe) },
}, {
	// A known duration under a feature's is a spliced-in ad, and ads are encoded well above the title.
	reason: reasonTooShort,
	when: func(c *Candidate) bool {
		return c.Probe.Duration > 0 && c.Probe.Duration < minContentDuration
	},
}, {
	reason: reasonFragmentHeader,
	when: func(c *Candidate) bool {
		return c.Probe.ContentType == media.MP4 && c.Probe.Duration == 0
	},
	admit:      true,
	lastResort: true,
}}, total: admissionRule{
	reason: reasonCastable,
	admit:  true,
}}

// admit returns the first matching row's verdict, or the total row's.
func admit(c *Candidate) admissionRule {
	for _, rule := range admissions.rules {
		if rule.when(c) {
			return rule
		}
	}
	return admissions.total
}

// logRejection reports a dropped candidate together with the facts its reason was read from.
func logRejection(ctx context.Context, c *Candidate, v admissionRule) {
	attrs := []any{"url", c.URL.String(), "reason", string(v.reason), "reach", c.reach}
	if probe := c.Probe; probe != nil {
		attrs = append(attrs, "video", string(probe.VideoCodec), "audio", string(probe.AudioCodec), "duration", probe.Duration)
	}
	slog.WarnContext(ctx, "candidate rejected", attrs...)
}

// tally counts rejections by the rule that made them, in table order.
func tally(rejected map[reason]int) string {
	counts := make([]string, 0, len(rejected))
	for _, rule := range admissions.rules {
		if n := rejected[rule.reason]; n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, rule.reason))
		}
	}
	return strings.Join(counts, ", ")
}

// admitted records the table's verdict.
func admitted(c *Candidate, v admissionRule) {
	c.LastResort = v.lastResort
	c.reason = v.reason
}

// carriesLadder reports that this candidate's captured document advertises renditions.
func (c *Candidate) carriesLadder() bool { return c.Ladder == LadderMultivariant }

// unreadLadder reports a playlist whose renditions nobody could establish, so it may well be a master.
func (c *Candidate) unreadLadder() bool {
	return media.IsSegmented(c.ContentType) && c.Ladder == LadderUnknown
}

// byLadderEvidence orders a document that advertises renditions first, then one nobody could read.
func byLadderEvidence(a, b *Candidate) int {
	evidence := func(c *Candidate) int {
		switch {
		case c.carriesLadder():
			return 2
		case c.unreadLadder():
			return 1
		}
		return 0
	}
	return cmp.Compare(evidence(b), evidence(a))
}

// exceedsCap reports whether a candidate's measured height is a real ceiling above the cast's.
func (c *Candidate) exceedsCap(ceiling media.HeightCap) bool {
	if c.carriesLadder() || c.unreadLadder() {
		return false
	}
	return !ceiling.Admits(c.height())
}

func preference(a, b *Candidate, ceiling media.HeightCap) int {
	if a.LastResort != b.LastResort {
		if b.LastResort {
			return 1 // a was measured, b was not: a wins
		}
		return -1
	}
	if ao, bo := a.exceedsCap(ceiling), b.exceedsCap(ceiling); ao != bo {
		if bo {
			return 1 // a is within the cap, b exceeds it: a wins
		}
		return -1
	}
	// The only signal here about RECOVERY rather than the picture: a master carries rungs to fall back to.
	if al, bl := a.carriesLadder(), b.carriesLadder(); al != bl {
		if al {
			return 1 // a publishes a ladder, b does not: a wins
		}
		return -1
	}
	// A height is evidence that the selected track is a picture; zero is unknown, not short.
	if ah, bh := a.height() > 0, b.height() > 0; ah != bh {
		if ah {
			return 1
		}
		return -1
	}
	if taller := cmp.Compare(a.height(), b.height()); taller != 0 {
		return taller
	}
	if wider := cmp.Compare(a.Bitrate(), b.Bitrate()); wider != 0 {
		return wider
	}
	// Smaller is deliberately preferred only as a reproducible final tie-break.
	return cmp.Compare(b.URL.String(), a.URL.String())
}

// ranked is the ordering a cast walks: best first, every admitted candidate present.
func ranked(pool []*Candidate, ceiling media.HeightCap) []*Candidate {
	order := slices.Clone(pool)
	slices.SortStableFunc(order, func(a, b *Candidate) int { return preference(b, a, ceiling) })
	return order
}

// movingPicture reports whether the selected video track carries a moving picture, not a still image.
func movingPicture(p *media.ProbeInfo) bool {
	return p.VideoCodec != "" && !stillImageCodecs[p.VideoCodec]
}

// stillImageCodecs are the ffprobe codec names observed for image tracks published as video.
var stillImageCodecs = map[media.Codec]bool{
	"png": true, "apng": true, "mjpeg": true, "jpeg": true, "jpegls": true,
	"bmp": true, "gif": true, "tiff": true, "webp": true, "ppm": true,
}
