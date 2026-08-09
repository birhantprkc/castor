package resolve

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

// This file is candidate selection: what castor is willing to attempt, and in what
// order. It is one table and one comparison, written down once, because the same
// question used to be answered in three places that could disagree: a four-arm
// switch inside the ranking goroutine (measure, judge, log and mutate a struct, all
// in one arm), a separate probe loop behind --dry-run that walked and printed a
// different set, and the pick itself. A user reading --dry-run was reading a
// ranking no cast would ever walk.
//
// The shape is every other ordered table in castor's: the rules are data, walked in
// declaration order, first match wins, what a row decides is a field on the row rather
// than control flow around it, and the row that answers whatever the refusals did not is
// held apart as the total row so a walk cannot fall off the end of the table.

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

// measurement is one candidate at every stage of ranking: the stream ranking will hand on,
// what was measured about it, how far the origin let castor get, and, once the table has
// ruled, whether it may only ever be a last resort.
//
// It is one value and it used to be two, a measurement the rules read and a candidate the
// ordering compared, and the copy between them was where the measured height stopped (see
// media.Stream.Height). Everything measured now lands on the stream itself, which is the only
// value that survives this package.
//
// A nil info means nothing was measured, which is emphatically not a measurement
// that found nothing: the first says the rules know nothing about this source, the
// second says the source carries no program. Reading one as the other is how a
// slideshow playlist and a link nobody answered end up with the same verdict.
type measurement struct {
	stream *media.Stream
	info   *media.ProbeInfo
	reach  media.Reach
	// lastResort marks a candidate admitted with no measurement behind it. The ordering
	// compares it first, so such a candidate is picked only when there was nothing measured
	// to pick.
	lastResort bool
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
					// Carried, never re-derived: the ladder was read from a body only the
					// browser held, and extraction has already been torn down.
					Ladder: s.Ladder,
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
	reasonCastable reason = "carried a castable program"
	reasonUnproven reason = "unmeasurable, admitted as a last resort"
	reasonRefused  reason = "refused by the origin"
	// reasonBrowserInternal is worded as the diagnosis and not as the refusal, because
	// the next move it calls for is the opposite of every other reason's. A pool of
	// refusals means the signed links went stale while the user was choosing what to
	// watch, so extracting again fixes it. A pool of nothing but browser handles means
	// the page fed its player from memory and the real media requests were never
	// captured at all, so the same extraction produces the same handle again: the title
	// has to be reached some other way.
	reasonBrowserInternal reason = "a browser-internal handle: the real stream was never captured"
	reasonNoProgram       reason = "carried no castable video+audio"
	reasonTooShort        reason = "too short to be content, treated as an ad"
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
// data and exercised without an ffprobe. `when` is nil on the total row, which is
// never asked: see admissionTable.
type admissionRule struct {
	reason     reason
	when       func(m measurement) bool
	admit      bool
	lastResort bool
}

// admissionTable is the ordered rows plus the row that answers whatever they did not.
// The total row carries no predicate, so admit answers with a verdict for every
// candidate and there is no "matched no rule" outcome for the tally to count.
type admissionTable struct {
	rules []admissionRule
	total admissionRule
}

// browserInternalSchemes are the URL schemes a browser mints to name bytes it is
// already holding rather than a resource somebody can fetch. blob: is what
// URL.createObjectURL hands back for a Blob or a MediaSource, and filesystem: is the
// same kind of handle over the sandboxed filesystem API. Both resolve only inside the
// document that created them, and that document is torn down before ranking even
// starts. ffprobe answers both before it opens a socket:
//
//	blob:https://play.tv3.lt/17147e13-...: Protocol not found
//
// This is a denylist of what a browser mints, never an allowlist of what a reader can
// fetch, and the asymmetry is the whole safety of it. ffprobe -protocols lists three
// dozen input protocols, data:, file:, srt: and ipfs: among them, so an allowlist
// would reject every scheme castor forgot to enumerate: convicting a candidate over
// something castor never established is exactly what media.ReachUnproven and
// media.LadderUnknown exist to prevent. data: is a real ffmpeg input protocol (it
// answers "Invalid data found when processing input", i.e. it got past the protocol
// lookup and read the payload) and file: is how a local cast is spelled, so neither
// may ever appear here.
var browserInternalSchemes = []string{"blob", "filesystem"}

// browserInternal reports a URL that names bytes inside a browser. The scheme is a
// structural fact about the URL, which is why this needs no knowledge of any site and
// no pattern guessing: the rest of a blob: handle is a UUID and says nothing at all.
//
// No case folding, deliberately: net/url lowercases a scheme while parsing, so a
// capture spelled "BLOB:https://..." arrives with Scheme "blob" and folding it again
// here would be a second answer to a question net/url has already answered.
func browserInternal(u *url.URL) bool {
	return slices.Contains(browserInternalSchemes, u.Scheme)
}

// admissions decides what castor will attempt, first match wins. Order is the
// contract: a row above another shadows it deliberately, and the rows that read
// m.info without a nil check are sound only because the row above them admits every
// candidate that has none. The stream and its URL need no such row: a measurement is
// only ever built around a candidate (see measureAll), and a candidate only exists
// once its URL parsed (see extract.streamsFrom), so a measurement with no URL on it is
// a shape nothing can produce.
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
var admissions = admissionTable{rules: []admissionRule{{
	// A handle no reader outside the browser can open, and the one row here that reads
	// the URL instead of a measurement. It is first because no measurement can rescue
	// it, and specifically because of the reasonUnproven row below: a field run captured
	// blob:https://play.tv3.lt/17147e13-... off a news site whose player fed a
	// MediaSource, ffprobe answered "Protocol not found" so nothing was measured, and an
	// unmeasured candidate is admitted as a last resort. That handle was then announced
	// as the best stream and the cast died against a URL nothing could ever have opened.
	//
	// That leniency is right for a timeout, where the reader gets reconnects and minutes
	// where ffprobe had seconds, and it is meaningless here: there is no protocol to
	// retry, and a renderer handed the URL would fail the same way. Structurally
	// uncastable is not unproven.
	reason: reasonBrowserInternal,
	when:   func(m measurement) bool { return browserInternal(m.stream.URL) },
}, {
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
}}, total: admissionRule{
	// The measured, castable candidate, and the total row: it is stated as explicitly as
	// every refusal above it, because "castor will attempt this" is a decision and not the
	// absence of one.
	reason: reasonCastable,
	admit:  true,
}}

// admit walks the table and returns the first matching row's verdict, or the total
// row's, which is what a candidate none of the refusals recognised earns.
func admit(m measurement) verdict {
	rule := admissions.total
	for _, candidate := range admissions.rules {
		if candidate.when(m) {
			rule = candidate
			break
		}
	}
	return verdict{reason: rule.reason, admit: rule.admit, lastResort: rule.lastResort}
}

// logRejection reports a dropped candidate together with the facts its reason was
// read from. The reason alone is not enough to act on: "carried no castable
// video+audio" is diagnosed by which of the two was missing (no audio codec, and it is a
// slideshow; no video codec, and it is the audio rendition listed as a variant), and "too
// short" by the runtime that decided it.
func logRejection(ctx context.Context, m measurement, v verdict) {
	attrs := []any{"url", m.stream.URL.String(), "reason", string(v.reason), "reach", m.reach}
	if m.info != nil {
		attrs = append(attrs, "video", string(m.info.VideoCodec), "audio", string(m.info.AudioCodec), "duration", m.info.Duration)
	}
	slog.WarnContext(ctx, "candidate rejected", attrs...)
}

// tally counts rejections by the rule that made them, in table order, for the error
// a caller sees when nothing was admitted. Naming the shapes is the whole value:
// "3 refused by the origin" tells the user to extract again, while a bare count of
// candidates tells them nothing they can act on.
func tally(rejected map[reason]int) string {
	counts := make([]string, 0, len(rejected))
	for _, rule := range admissions.rules {
		if n := rejected[rule.reason]; n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, rule.reason))
		}
	}
	return strings.Join(counts, ", ")
}

// admitted writes what was measured onto the stream that will be cast, which is the whole of
// how a measurement outlives ranking. A last-resort candidate keeps whatever bandwidth
// extraction gave it (in practice 0, since nothing but this package ever sets it) and no
// height or duration, because nothing was measured to set any of them from.
func admitted(m measurement, v verdict) measurement {
	m.lastResort = v.lastResort
	if m.info != nil {
		// The floor of 1 keeps a measured candidate distinguishable from an unmeasured
		// one: ffprobe routinely reports no top-level bit_rate for an HLS master, which
		// is the field's most common shape. It is a weak signal on purpose and must
		// stay one. Whether ffprobe managed to report a bit_rate is a property of the
		// container it was pointed at, not of the picture, so preference compares it
		// only after resolution and never instead of it.
		m.stream.Bandwidth = max(m.info.BitRate, 1)
		m.stream.Height = m.info.VideoHeight
		m.stream.Duration = m.info.Duration
		m.stream.Probed = true
	}
	return m
}

// carriesLadder reports that this candidate's captured document advertises renditions,
// so a cast against it has rungs to move between. It is the fact the document's own
// tags established during extraction, never a reading of the URL (see media.Ladder).
//
// Unknown answers false and is therefore compared equal to a confirmed single
// rendition. That is deliberate and it is the whole of the leniency: a body Chrome
// evicted, a redirect, a request that never finished all leave this unknown, and none
// of them is evidence against the candidate.
func (m measurement) carriesLadder() bool { return m.stream.Ladder == media.LadderMultivariant }

// unreadLadder reports a playlist whose renditions nobody could establish: Chrome had
// no body to hand over, so this document may well be a master and there is no evidence
// either way. It is the lenient half of the cap's exemption, in the convention
// media.ReachUnproven and an unmeasured height already keep: what was never established
// may not convict a candidate.
//
// It is confined to playlists because only a playlist has a document that could have
// advertised anything. A whole file's measured height is final: there is no rung
// beneath it and no body would ever have said there was, so reading its silence as
// doubt would exempt every direct 2160p recording from the ceiling the user set.
func (m measurement) unreadLadder() bool {
	return m.stream.ContentType == media.HLS && m.stream.Ladder == media.LadderUnknown
}

// exceedsCap reports whether a candidate's measured height is a real ceiling above the
// cast's, which is the tier preference compares before any measurement.
//
// A document that advertises renditions is exempt, and the exemption is sound for
// exactly that shape: a master lists every variant, ffprobe reports the height of
// whichever one it happened to open, and the cap binds for real when a rung is picked
// out of it (see pickVariant). A document that advertises NONE is its own ceiling: its
// measured height is what a cast against it reads, and there is no rung to narrow to.
// Exempting one of those for carrying a .m3u8 is how a user capped at 1080 is handed a
// 2160p variant playlist, so the exemption keys on the fact the document's own tags
// established and never on the container it arrived in.
//
// Honouring the ceiling here is also the only place it is free. Preferring the 1080p
// candidate costs a comparison; honouring the same ceiling once a 4K source is being
// read costs a decode, a scale and a realtime re-encode, which a software-only host
// cannot pace.
func (m measurement) exceedsCap(ceiling media.HeightCap) bool {
	if m.carriesLadder() || m.unreadLadder() {
		return false
	}
	return !ceiling.Admits(m.stream.Height)
}

// preference orders two admitted candidates, the better one greater, in the manner
// of a slices.MaxFunc comparator: one within the height cap is always preferred
// over one that exceeds it (so a direct 1080p beats a direct 4K when capped at
// 1080, even at a lower bitrate); then a document that advertises renditions over one
// that does not; then ties, and the all-over-cap case, fall to the tallest probed
// height, and only then to the highest bandwidth. A height nobody measured is compared
// as unknown rather than as short, so the two candidates fall straight through to
// bandwidth.
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
func preference(a, b measurement, ceiling media.HeightCap) int {
	if a.lastResort != b.lastResort {
		if b.lastResort {
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
	// A confirmed ladder is the only signal here about RECOVERY rather than about the picture:
	// a master carries rungs to fall back to, and a source that published one rendition leaves
	// a cast that starts failing with no move to make (see media.Ladder, and reportRendition
	// for the run that ended there).
	//
	// It sits below the cap tier, which states what the user asked for, and above the two
	// measurement tiers, because on a master both of those are structurally weak: ffprobe
	// reports the height of whichever variant it opened, not the master's range, and it cannot
	// report a top-level bit_rate for a master at all, so one arrives floored to 1.
	if al, bl := a.carriesLadder(), b.carriesLadder(); al != bl {
		if al {
			return 1 // a publishes a ladder, b does not: a wins
		}
		return -1
	}
	// Only when BOTH heights were measured. A height of 0 is ffprobe declining to
	// say, not a short picture, and ranking it as short would punish a candidate for
	// being hard to measure in exactly the way the bitrate ordering above used to.
	// With one height unknown there is nothing to compare, so the weaker signal is
	// all that is left and bandwidth decides.
	if a.stream.Height > 0 && b.stream.Height > 0 {
		if taller := cmp.Compare(a.stream.Height, b.stream.Height); taller != 0 {
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
func ranked(pool []measurement, ceiling media.HeightCap) []measurement {
	order := slices.Clone(pool)
	slices.SortStableFunc(order, func(a, b measurement) int { return preference(b, a, ceiling) })
	return order
}
