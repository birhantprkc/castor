package resolve

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/stupside/castor/internal/media"
)

// Resolve establishes the facts a cast needs about its source, in the order they
// depend on each other: what the source is, which of its renditions to read, and
// whether a renderer could fetch it unaided. Only the fields resolution
// establishes are rewritten; everything else is preserved.
func Resolve(ctx context.Context, cfg Config, stream *media.Stream) (*media.Stream, error) {
	if err := identify(ctx, cfg, stream); err != nil {
		return nil, err
	}
	// The remaining two facts are HLS's alone: it is the only container that
	// publishes one program across renditions, and the only one castor opens with
	// relaxed checks.
	if stream.ContentType == media.HLS {
		selectRendition(ctx, cfg, stream)
		verifyRendererCanFetch(ctx, cfg, stream)
	}
	return stream, nil
}

// identify fills in what the source is when the caller could not say: its
// container, and whether it is a live edge. A caller that already knew (a .m3u8
// URL, a ranked candidate) spends no probe here.
func identify(ctx context.Context, cfg Config, stream *media.Stream) error {
	if stream.ContentType != "" {
		return nil
	}
	info, err := probeStream(ctx, cfg.FFprobePath, cfg.ProbeTimeout, stream.URL, stream.Headers)
	if err != nil {
		return fmt.Errorf("probing stream: %w", err)
	}
	stream.ContentType = info.ContentType
	stream.Live = info.Live()
	return nil
}

// selectRendition narrows an HLS master to the single variant to read, and keeps
// the audio rendition that variant plays with. A master that publishes audio
// separately leaves the chosen variant carrying video only, so narrowing to it
// and stopping there is how a cast ends up silent, or dies mapping an audio
// track the variant never had. A master castor cannot read leaves the stream as
// it was, to be attempted whole.
func selectRendition(ctx context.Context, cfg Config, stream *media.Stream) {
	master, err := readPlaylist(ctx, cfg, stream)
	if err != nil {
		slog.WarnContext(ctx, "HLS playlist resolution failed, using original", "error", err)
		return
	}

	variant := pickVariant(master.Variants, cfg.MaxHeight)
	stream.URL = variant.URL
	stream.AudioURL = master.AudioFor(variant)
	stream.Live = stream.Live || master.Live

	if stream.Demuxed() {
		slog.InfoContext(ctx, "source publishes audio separately; both renditions will be read",
			"video", stream.URL.String(), "audio", stream.AudioURL.String())
	}
}

// verifyRendererCanFetch settles the one claim castor would otherwise take on
// trust: that a device handed this URL can read it. Castor opens every HLS input
// with relaxed segment checks (media.HLSInputArgs), which is precisely what lets
// a disguised source through unnoticed, so the source is opened once more under
// default checks. What fails that, no reader applying its own defaults will take
// either, and a renderer is nothing but such a reader.
//
// It runs only while pass-through is still on the table: a source already ruled
// out, header-gated or demuxed just above, is served whatever this would say, so
// the probe is never spent to confirm a decision already made.
func verifyRendererCanFetch(ctx context.Context, cfg Config, stream *media.Stream) {
	if !stream.SelfFetchable() || opensWithoutLeniency(ctx, cfg.FFprobePath, cfg.ProbeTimeout, stream.URL, stream.Headers) {
		return
	}
	stream.NeedsLeniency = true
	slog.InfoContext(ctx, "source opens only under relaxed reader checks; it will be served, not handed to the device",
		"url", stream.URL.String())
}

// readPlaylist fetches an HLS document and reduces it to the variants and audio
// renditions selection works on.
func readPlaylist(ctx context.Context, cfg Config, stream *media.Stream) (hlsMaster, error) {
	body, err := fetchPlaylist(ctx, cfg.HLSTimeout, stream.URL, stream.Headers)
	if err != nil {
		return hlsMaster{}, err
	}
	return parsePlaylist(body, stream.URL)
}

// pickVariant chooses which HLS variant to pull: the highest-bandwidth one no
// taller than maxHeight (a variant with unknown height, 0, is always eligible).
// If every variant is taller than the cap, it takes the shortest so the encoder
// has the least to downscale. variants is never empty (parsePlaylist guarantees
// at least the synthetic media-playlist entry).
//
// Variants that carry no video are excluded first: a master can list its audio
// rendition as a variant of its own, and such an entry has no RESOLUTION to
// exclude it by the cap and often the highest bandwidth of the lot, so on
// bandwidth alone it would win and the cast would be audio with no picture.
func pickVariant(variants []hlsVariant, maxHeight int) hlsVariant {
	if withVideo := slices.DeleteFunc(slices.Clone(variants), func(v hlsVariant) bool {
		return !v.HasVideo
	}); len(withVideo) > 0 {
		variants = withVideo
	}

	eligible := slices.DeleteFunc(slices.Clone(variants), func(v hlsVariant) bool {
		return v.Height > maxHeight
	})
	if len(eligible) > 0 {
		return slices.MaxFunc(eligible, func(a, b hlsVariant) int {
			return cmp.Compare(a.Bandwidth, b.Bandwidth)
		})
	}
	return slices.MinFunc(variants, func(a, b hlsVariant) int {
		return cmp.Compare(a.Height, b.Height)
	})
}

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

// candidate is a probed stream plus the signals RankStreams ranks on.
type candidate struct {
	stream *media.Stream
	height int  // probed video height; 0 if unknown or the probe failed
	decoy  bool // probed cleanly but unplayable (no video+audio) or an ad
}

// exceedsCap reports whether a candidate's own resolution is a hard limit above
// maxHeight. HLS masters are exempt: a master lists every variant and is capped
// when Resolve picks one, so its single-variant probe height is not a ceiling.
func (c candidate) exceedsCap(maxHeight int) bool {
	return c.stream.ContentType != media.HLS && c.height > 0 && c.height > maxHeight
}

// bestCandidate picks the stream to cast: one within the height cap is always
// preferred over one that exceeds it (so a direct 1080p beats a direct 4K when
// capped at 1080, even at a lower bitrate); ties, and the all-over-cap case,
// fall back to highest bandwidth, then tallest probed height. The height
// tiebreak matters because ffprobe frequently can't report a top-level
// bit_rate for an HLS master, which floors every such candidate's bandwidth to
// the same value (see RankStreams) and would otherwise leave the pick to
// whichever candidate happened to be probed first.
func bestCandidate(pool []candidate, maxHeight int) candidate {
	return slices.MaxFunc(pool, func(a, b candidate) int {
		if ao, bo := a.exceedsCap(maxHeight), b.exceedsCap(maxHeight); ao != bo {
			if bo {
				return 1 // a is within the cap, b exceeds it: a wins
			}
			return -1
		}
		return cmp.Or(
			cmp.Compare(a.stream.Bandwidth, b.stream.Bandwidth),
			cmp.Compare(a.height, b.height),
		)
	})
}

// RankStreams probes every candidate in parallel and returns the highest-
// bandwidth playable one. A stream that probes cleanly but carries no castable
// video+audio, or is too short to be anything but a spliced-in ad, is dropped
// hard so it can't win when the real sources are unreachable. A stream whose
// probe fails (403/timeout/reset) is kept at zero bandwidth as a last resort,
// since the puller reconnects differently and may still succeed. If every
// candidate is a decoy, ranking fails cleanly.
func RankStreams(ctx context.Context, cfg Config, streams []*media.Stream) (*media.Stream, error) {
	slog.InfoContext(ctx, "ranking streams", "count", len(streams))
	if len(streams) == 0 {
		return nil, fmt.Errorf("no streams to rank")
	}
	streams = limitPerHost(ctx, streams)

	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.ProbeMaxConcurrency)
	cands := make([]candidate, len(streams))

	for i, s := range streams {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			slog.DebugContext(ctx, "probing stream", "url", s.URL, "index", i+1, "total", len(streams))
			info, err := probeStream(ctx, cfg.FFprobePath, cfg.ProbeTimeout, s.URL, s.Headers)
			out := &media.Stream{
				URL:         s.URL,
				AudioURL:    s.AudioURL,
				Headers:     s.Headers,
				ContentType: s.ContentType,
				Bandwidth:   s.Bandwidth,
				Live:        s.Live,
			}
			switch {
			case err != nil:
				// Transient failure (403/timeout/reset): keep as a zero-bandwidth fallback.
				slog.WarnContext(ctx, "probe failed", "url", s.URL, "error", err)
			case !info.Playable():
				// Probed cleanly but no castable video+audio → decoy, drop hard.
				slog.WarnContext(ctx, "stream rejected: no castable video+audio",
					"url", s.URL, "has_video", info.HasVideo, "has_audio", info.HasAudio)
				cands[i] = candidate{stream: out, decoy: true}
				return
			case info.Duration > 0 && info.Duration < minContentDuration:
				// Too short to be a feature/episode → spliced-in ad, drop hard so it
				// can't win over the real title on bandwidth.
				slog.WarnContext(ctx, "stream rejected: too short to be feature content, treating as ad",
					"url", s.URL, "duration", info.Duration)
				cands[i] = candidate{stream: out, decoy: true}
				return
			default:
				out.Bandwidth = max(info.BitRate, 1)
				out.Live = info.Live()
				slog.DebugContext(ctx, "probed stream", "url", s.URL, "bitrate", info.BitRate, "height", info.VideoHeight, "live", out.Live)
			}
			cands[i] = candidate{stream: out}
			if info != nil {
				cands[i].height = info.VideoHeight
			}
		})
	}
	wg.Wait()

	pool := make([]candidate, 0, len(cands))
	decoys := 0
	for _, c := range cands {
		if c.decoy {
			decoys++
			continue
		}
		pool = append(pool, c)
	}
	if len(pool) == 0 {
		return nil, fmt.Errorf("no castable stream: all %d candidates were unreachable, carried no video+audio, or were ads", len(streams))
	}

	best := bestCandidate(pool, cfg.MaxHeight)
	if decoys > 0 {
		slog.InfoContext(ctx, "rejected decoy streams", "count", decoys, "kept", len(pool))
	}
	slog.InfoContext(ctx, "best stream selected", "url", best.stream.URL.String(), "bitrate", best.stream.Bandwidth, "height", best.height)
	return best.stream, nil
}

// StreamDetail holds a stream URL and its probed bit rate, for display.
type StreamDetail struct {
	URL     string
	BitRate int64
}

// ListStreams expands HLS variants and probes each, returning details for
// display. Failures are logged and skipped.
func ListStreams(ctx context.Context, cfg Config, streams []*media.Stream) []StreamDetail {
	var details []StreamDetail
	for _, s := range streams {
		if s.ContentType == media.HLS {
			master, err := readPlaylist(ctx, cfg, s)
			if err != nil {
				slog.WarnContext(ctx, "HLS variant resolution failed", "url", s.URL, "error", err)
				continue
			}
			for _, v := range master.Variants {
				info, err := probeStream(ctx, cfg.FFprobePath, cfg.ProbeTimeout, v.URL, s.Headers)
				if err != nil {
					slog.WarnContext(ctx, "probe failed", "url", v.URL, "error", err)
					continue
				}
				details = append(details, StreamDetail{URL: v.URL.String(), BitRate: info.BitRate})
			}
			continue
		}
		info, err := probeStream(ctx, cfg.FFprobePath, cfg.ProbeTimeout, s.URL, s.Headers)
		if err != nil {
			slog.WarnContext(ctx, "probe failed", "url", s.URL, "error", err)
			continue
		}
		details = append(details, StreamDetail{URL: s.URL.String(), BitRate: info.BitRate})
	}
	return details
}
