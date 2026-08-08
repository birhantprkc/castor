package resolve

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stupside/castor/internal/media"
)

// This file is candidate selection: what castor is willing to attempt, and in what
// order. It is one table and one comparison, written down once, because the same
// question used to be answered in three places that could disagree: a four-arm
// switch inside the ranking goroutine (measure, judge, log and mutate a struct, all
// in one arm), a separate probe loop behind --dry-run that walked and printed a
// different set, and the pick itself. A user reading --dry-run was reading a
// ranking no cast would ever walk.
//
// The shape is core.deliveries': the rules are data, ordered, first match wins, and
// what a row decides is a field on the row rather than control flow around it.

// minContentDuration is the shortest runtime treated as real content. Pre-roll
// ads and ad-pods run well under it; the shortest real title (a ~11-minute
// episode) sits above. A candidate whose duration is known and shorter is an ad
// and is dropped like any other decoy. Unknown duration (live, no endlist) is
// not treated as short.
const minContentDuration = 5 * time.Minute

// maxProbePerHost caps how many candidates from one host RankStreams probes. An
// embed proxy emits a master plus a long tail of variant playlists behind one
// signature; probing all of them trips the host's rate limiter (HTTP 429),
// which poisons the ranking and kills the pull. Candidates arrive master-first,
// so the first few per host keep the master and drop the redundant tail.
const maxProbePerHost = 5

// limitPerHost keeps at most maxProbePerHost candidates per host, in order, so
// the probe stage can't fire a dozen redundant variant requests at one proxy
// and trip its rate limiter.
//
// It stays a filter ahead of the admissions table rather than a row inside it: a
// row is a judgement on a measurement, and the whole point of this one is that the
// measurement is never taken. Dropping the tail after probing it would already have
// spent the requests the cap exists to save.
func limitPerHost(ctx context.Context, streams []*media.Stream) []*media.Stream {
	seen := make(map[string]int, len(streams))
	kept := make([]*media.Stream, 0, len(streams))
	dropped := 0
	for _, s := range streams {
		host := s.URL.Hostname()
		if seen[host] >= maxProbePerHost {
			dropped++
			continue
		}
		seen[host]++
		kept = append(kept, s)
	}
	if dropped > 0 {
		slog.InfoContext(ctx, "skipped redundant variant candidates to avoid rate limiting",
			"dropped", dropped, "kept", len(kept), "per_host_cap", maxProbePerHost)
	}
	return kept
}

// measurement is one candidate as the admissions rules read it: the stream ranking
// will hand on, what was measured about it, and how far the origin let castor get.
//
// A nil info means nothing was measured, which is emphatically not a measurement
// that found nothing: the first says the rules know nothing about this source, the
// second says the source carries no program. Reading one as the other is how a
// slideshow playlist and a link nobody answered end up with the same verdict.
type measurement struct {
	stream *media.Stream
	info   *media.StreamInfo
	reach  media.Reach
}

// measureAll measures every candidate concurrently, bounded by the configured
// fan-out because a burst of probes behind one signature is what trips an embed
// proxy's rate limiter (see maxProbePerHost). It judges nothing and rejects
// nothing: every candidate comes back with whatever was learned about it, and the
// table below is the only place a candidate's fate is decided.
func (r *Resolver) measureAll(ctx context.Context, streams []*media.Stream) []measurement {
	out := make([]measurement, len(streams))
	sem := make(chan struct{}, r.cfg.ProbeMaxConcurrency)

	var wg sync.WaitGroup
	for i, s := range streams {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			slog.DebugContext(ctx, "probing stream", "url", s.URL, "index", i+1, "total", len(streams))
			info, reach, err := r.measurer.Measure(ctx, s)
			if err != nil {
				slog.WarnContext(ctx, "probe failed", "url", s.URL, "reach", reach, "error", err)
			}
			// The candidate carries a copy of the stream: measured facts are written onto
			// that copy, so a candidate the table then rejects never leaves fields on
			// extraction's own value reading as though they had been established.
			out[i] = measurement{
				stream: &media.Stream{
					URL:         s.URL,
					AudioURL:    s.AudioURL,
					Headers:     s.Headers,
					ContentType: s.ContentType,
					Bandwidth:   s.Bandwidth,
					Live:        s.Live,
				},
				info:  info,
				reach: reach,
			}
		})
	}
	wg.Wait()
	return out
}

// reason names the rule that decided a candidate's fate. It is carried out of the
// table rather than only logged so the pool-empty error can say what the candidates
// actually were: "no castable stream" on its own reads as a bug in extraction, when
// the truth is nearly always four signed links that went stale while the user was
// choosing what to watch, and the fix for that is to extract again.
type reason string

const (
	reasonCastable  reason = "carried a castable program"
	reasonUnproven  reason = "unmeasurable, admitted as a last resort"
	reasonRefused   reason = "refused by the origin"
	reasonNoProgram reason = "carried no castable video+audio"
	reasonTooShort  reason = "too short to be content, treated as an ad"
	// reasonNoRule is not a row. It is what admit answers when the table matched
	// nothing, which can only happen if the total row at the bottom is removed.
	reasonNoRule reason = "matched no admission rule"
)

// verdict is what the table decided about one candidate: whether it enters the pool
// at all, whether it may only ever be a last resort inside it, and which rule said
// so.
type verdict struct {
	reason     reason
	admit      bool
	lastResort bool
}

// admissionRule is one row: the shape it recognises, and what that shape earns.
// Rows hold no logic beyond `when`, which is what lets the whole policy be read as
// data and exercised without an ffprobe.
type admissionRule struct {
	reason     reason
	when       func(m measurement) bool
	admit      bool
	lastResort bool
}

// admissions decides what castor will attempt, first match wins. Order is the
// contract: a row above another shadows it deliberately, and the rows that read
// m.info without a nil check are sound only because the row above them admits every
// candidate that has none.
//
// The row that is NOT here is the one this table replaced. A candidate whose probe
// failed used to be kept at bandwidth 0, on the theory that the puller reconnects
// where ffprobe gave up. It cost a real cast: every other candidate in that run was
// a hard-rejected decoy, so the survivor was a link castor had already killed its
// own probe against, it was announced as "best stream selected bitrate=0 height=0",
// and the cast then spent minutes failing against it. What survives of the theory is
// narrower and evidence-based: a candidate is kept only while the origin has not
// refused it (media.ReachUnproven), and it is kept strictly below every measured
// candidate (see preference), so it can win only when nothing measured was admitted
// at all.
var admissions = []admissionRule{{
	// A spent signed link answers 403, and answers it to every reader alike, so
	// there is nothing for the puller's reconnects to rescue. This is the shape that
	// dominates a pool once extraction is a few minutes stale.
	reason: reasonRefused,
	when:   func(m measurement) bool { return m.reach == media.ReachRefused },
}, {
	// Nothing was measured and the origin never said no: a killed probe, a reset, a
	// 429 aimed at castor's own fan-out. The reader that follows gets reconnects, a
	// wider retry status set and minutes where ffprobe had seconds, so the link is
	// unproven rather than dead. Admitted, never preferred.
	reason:     reasonUnproven,
	when:       func(m measurement) bool { return m.info == nil },
	admit:      true,
	lastResort: true,
}, {
	// Measured cleanly and carries nothing a renderer can play: an image-only
	// "video" track (the slideshow playlists aggregators serve) or video with no
	// audio. Such a candidate probes fine and often carries the highest bandwidth in
	// the pool, so it has to be dropped rather than ranked.
	reason: reasonNoProgram,
	when:   func(m measurement) bool { return !m.info.Playable() },
}, {
	// A known duration under a feature's is a spliced-in ad, and ads are encoded
	// well above the title they interrupt.
	reason: reasonTooShort,
	when:   func(m measurement) bool { return m.info.Duration > 0 && m.info.Duration < minContentDuration },
}, {
	// The measured, castable candidate. It is a row rather than the table's
	// fall-through so that "castor will attempt this" is stated as explicitly as
	// every refusal above it, and so admit's own default arm means what it says.
	reason: reasonCastable,
	when:   func(measurement) bool { return true },
	admit:  true,
}}

// admit walks the table and returns the first matching row's verdict. A shape no
// row recognises is refused with the shape named, not admitted by default: the last
// row matches everything, so arriving here means a row was deleted, and a table
// that has stopped covering a shape must say so rather than cast it.
func admit(m measurement) verdict {
	for _, rule := range admissions {
		if rule.when(m) {
			return verdict{reason: rule.reason, admit: rule.admit, lastResort: rule.lastResort}
		}
	}
	return verdict{reason: reasonNoRule}
}

// logRejection reports a dropped candidate together with the facts its reason was
// read from. The reason alone is not enough to act on: "carried no castable
// video+audio" is diagnosed by which of the two was missing (audio, and it is a
// slideshow; video, and it is the audio rendition listed as a variant), and "too
// short" by the runtime that decided it.
func logRejection(ctx context.Context, m measurement, v verdict) {
	attrs := []any{"url", m.stream.URL.String(), "reason", string(v.reason), "reach", m.reach}
	if m.info != nil {
		attrs = append(attrs, "has_video", m.info.HasVideo, "has_audio", m.info.HasAudio, "duration", m.info.Duration)
	}
	slog.WarnContext(ctx, "candidate rejected", attrs...)
}

// tally counts rejections by the rule that made them, in table order, for the error
// a caller sees when nothing was admitted. Naming the shapes is the whole value:
// "3 refused by the origin" tells the user to extract again, while a bare count of
// candidates tells them nothing they can act on.
func tally(rejected map[reason]int) string {
	counts := make([]string, 0, len(rejected))
	for _, rule := range admissions {
		if n := rejected[rule.reason]; n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, rule.reason))
		}
	}
	if n := rejected[reasonNoRule]; n > 0 {
		counts = append(counts, fmt.Sprintf("%d %s", n, reasonNoRule))
	}
	return strings.Join(counts, ", ")
}

// candidate is an admitted measurement reduced to what the ordering compares.
type candidate struct {
	stream *media.Stream
	height int // measured video height; 0 if unknown or nothing was measured
	// lastResort marks a candidate admitted with no measurement behind it. The
	// ordering compares it first, so such a candidate is picked only when there was
	// nothing measured to pick.
	lastResort bool
}

// admitted turns a measurement and its verdict into the candidate the ordering
// ranks, writing the measured facts onto the stream that will be cast. A
// last-resort candidate keeps whatever bandwidth extraction gave it (in practice 0,
// since nothing but this package ever sets it) and no height, because nothing was
// measured to set either from.
func admitted(m measurement, v verdict) candidate {
	c := candidate{stream: m.stream, lastResort: v.lastResort}
	if m.info != nil {
		// The floor of 1 keeps a measured candidate distinguishable from an unmeasured
		// one: ffprobe routinely reports no top-level bit_rate for an HLS master, which
		// is the field's most common shape. It is a weak signal on purpose and must
		// stay one. Whether ffprobe managed to report a bit_rate is a property of the
		// container it was pointed at, not of the picture, so preference compares it
		// only after resolution and never instead of it.
		c.stream.Bandwidth = max(m.info.BitRate, 1)
		c.stream.Live = m.info.Live()
		c.height = m.info.VideoHeight
	}
	return c
}

// exceedsCap reports whether a candidate's own resolution is a hard limit above
// maxHeight. HLS masters are exempt: a master lists every variant and is capped
// when Resolve picks one, so its single-variant probe height is not a ceiling.
func (c candidate) exceedsCap(maxHeight int) bool {
	return c.stream.ContentType != media.HLS && c.height > 0 && c.height > maxHeight
}

// preference orders two admitted candidates, the better one greater, in the manner
// of a slices.MaxFunc comparator: one within the height cap is always preferred
// over one that exceeds it (so a direct 1080p beats a direct 4K when capped at
// 1080, even at a lower bitrate); ties, and the all-over-cap case, fall to the
// tallest probed height, and only then to the highest bandwidth. A height nobody
// measured is compared as unknown rather than as short, so the two candidates fall
// straight through to bandwidth.
//
// Resolution leads and bitrate follows, which is the opposite of what this used to
// do, because the two are not equally trustworthy. A height is measured off the
// picture. A bandwidth is whatever ffprobe happened to be able to report, and it
// cannot report a top-level bit_rate for an HLS master at all, so every such
// candidate arrives floored to 1 (see admitted). With bitrate compared first, that
// floor stopped being a tiebreak and became the ranking: ANY candidate ffprobe
// could measure beat ANY candidate it could not, at any resolution, because the
// height comparison was never reached. A field run picked a 462 bit/s measurement
// at 800 lines over a floored master at 1600, and the shape that loses hardest is
// the one that matters most, since a plain recording is trivial for ffprobe to
// measure while a proper fMP4 release is exactly what it reports nothing for.
//
// This is not a quality detector and must not be mistaken for one. At equal
// resolution nothing a probe can see separates a good encode from a bad one, so
// what this buys is that detail is never traded away for a number describing how
// the source was packaged.
//
// The last-resort tier is compared before all of it, and that ordering is the
// invisible half of the pick that sent a cast at a dead link: an unmeasured
// candidate has height 0, height 0 is within any cap, and the within-cap tier is
// compared before bandwidth, so a link nobody could open used to beat a measured
// 2160p at 20 Mbit/s under a 1080 cap. Comparing the tier first makes that
// unrepresentable rather than patched.
func preference(a, b candidate, maxHeight int) int {
	if a.lastResort != b.lastResort {
		if b.lastResort {
			return 1 // a was measured, b was not: a wins
		}
		return -1
	}
	if ao, bo := a.exceedsCap(maxHeight), b.exceedsCap(maxHeight); ao != bo {
		if bo {
			return 1 // a is within the cap, b exceeds it: a wins
		}
		return -1
	}
	// Only when BOTH heights were measured. A height of 0 is ffprobe declining to
	// say, not a short picture, and ranking it as short would punish a candidate for
	// being hard to measure in exactly the way the bitrate ordering above used to.
	// With one height unknown there is nothing to compare, so the weaker signal is
	// all that is left and bandwidth decides.
	if a.height > 0 && b.height > 0 {
		if taller := cmp.Compare(a.height, b.height); taller != 0 {
			return taller
		}
	}
	return cmp.Compare(a.stream.Bandwidth, b.stream.Bandwidth)
}

// ranked is the ordering a cast walks: best first, every admitted candidate
// present. It is an ordering rather than a single winner because the head is not
// the only answer that matters. A cast that fails against the head has somewhere to
// go, and --dry-run can print what castor would actually attempt instead of a
// separately probed list that agreed with it only by luck.
//
// The sort is stable so that candidates the rules cannot separate keep the order
// extraction found them in, which is master-first: for the tied HLS masters
// described above, that is the difference between a reproducible pick and one that
// depends on which probe returned first.
func ranked(pool []candidate, maxHeight int) []candidate {
	order := slices.Clone(pool)
	slices.SortStableFunc(order, func(a, b candidate) int { return preference(b, a, maxHeight) })
	return order
}
