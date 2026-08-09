package resolve

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/stupside/castor/internal/media"
)

// This file is resolution's cast phase: castor has already chosen ONE candidate
// (see rank.go) and now reads what that source publishes, which is a different
// question from "is this link worth attempting". It answers it from documents the
// reader is about to open anyway, so what it costs is at most two plain GETs and no
// probe at all.
//
// The facts it establishes outlive it, which is the change this file exists for. Selection used
// to be the only thing that survived the document: the variant list was reduced to one URL and
// the rest dropped, so nothing downstream could tell a source that offered a 1080 rung from one
// that offered a single 4K rung and no alternative (see reportRendition). Now the ladder, the
// framing, the encryption and the real runtime travel out as media.Origin.

// Programs is this resolver as a cast that has changed its mind reads it: re-establish
// what one link publishes, without touching the link the caller handed over.
//
// The copy is the whole of it, and it is why this is a type and not the resolver's own
// method. Resolve rewrites the stream it is given (that is its job: a resolved stream is
// what castor reads), and the links a cast walks are the ranker's ordering, held by the
// intent for the life of the cast. Resolving one in place would rewrite the record of
// what was tried, and narrowing a master to a rung would leave the ordering holding a
// rung where it had published a master.
type Programs struct{ r *Resolver }

// NewPrograms binds the port to the resolver the composition root already built.
func NewPrograms(r *Resolver) Programs { return Programs{r: r} }

// Refetch establishes what one link publishes, on a copy of it. The shallow copy is
// enough because resolution writes only the fields it establishes (the URL, the audio
// rendition, the container, what a probe measured, leniency) and shares the captured headers,
// which are extraction's and which every rendition of that link answers to alike.
func (p Programs) Refetch(ctx context.Context, s *media.Stream) (*media.Stream, media.Origin, media.Rendition, error) {
	link := *s
	return p.r.Resolve(ctx, &link)
}

// identify fills in what the source is when the caller could not say, and what it establishes
// lands on the stream beside the ladder rather than travelling out as a second value: the
// measurement is paid for once, by whichever of the two parties measures, and every later
// reader asks the stream (see media.Stream.Height).
//
// A caller that already knew spends no probe, and neither kind of such caller loses anything
// by it: a ranked candidate was measured to be admitted at all (see admitted), and a URL cast
// by hand names its container in its own extension and is a source nothing has ever opened.
func (r *Resolver) identify(ctx context.Context, stream *media.Stream) error {
	if stream.ContentType != "" {
		return nil
	}
	// The reach is not read here: a source castor cannot even name is not castable
	// whatever the origin said about it, since every input flag and the pass-through
	// decision itself are chosen from the container. It is the ranker, which has
	// other candidates to weigh this one against, that has a use for the difference.
	info, _, err := r.measurer.Measure(ctx, stream)
	if err != nil {
		return fmt.Errorf("probing stream: %w", err)
	}
	stream.ContentType = info.ContentType
	stream.Height = info.VideoHeight
	stream.Duration = info.Duration
	stream.Probed = true
	return nil
}

// program narrows an HLS source to the single rendition to read and returns what the
// source published, the ladder included. It keeps the audio rendition the chosen
// variant plays with: a master that publishes audio separately leaves that variant
// carrying video only, so narrowing to it and stopping there is how a cast ends up
// silent, or dies mapping an audio track the variant never had.
//
// A document castor cannot read costs the facts, not the cast: the stream is left as
// it was and attempted whole, because the reader that follows carries headers,
// reconnects and minutes that this one GET does not. Liveness then stands as the probe
// left it, which is the one case where a probe's silence is the best witness there is,
// because nothing else looked at all.
//
// The chosen rung travels out beside the origin, and it is the one fact this narrowing
// used to spend and throw away. Which rung a cast is reading is the source layer's
// statement to make: nothing downstream can reconstruct it, since the choice rewrites
// stream.URL with the variant's own URL and the variant is then gone. It is what a
// recovery measures a lighter rung against, because the ceiling a degrade aims under is
// the product of the rate the source DECLARED for the rung being read and the speed the
// reader achieved on it, and an attempt that cannot name its rung has neither.
func (r *Resolver) program(ctx context.Context, stream *media.Stream, origin media.Origin) (media.Origin, media.Rendition) {
	origin.Segmented = true

	doc, status, err := r.readPlaylist(ctx, stream.URL, stream.Headers)
	if err != nil {
		// The status is logged with the failure because the two shapes behind it call
		// for opposite responses: 403 means the signed link is spent and only a fresh
		// extraction helps, while no answer at all (status 0) is transient.
		slog.WarnContext(ctx, "HLS playlist resolution failed, using original", "error", err, "status", status)
		return origin, media.Rendition{}
	}
	if len(doc.Variants) == 0 {
		// A master listing nothing but I-frame playlists reduces to no castable
		// rendition at all. Attempting the source whole is the same posture as an
		// unreadable document, and it is the only one that does not ask the selection
		// below to choose from an empty set.
		slog.WarnContext(ctx, "playlist offers no castable rendition, using original", "url", stream.URL.String())
		return origin, media.Rendition{}
	}
	origin.Renditions = ladder(doc.Variants)

	variant := pickVariant(doc.Variants, r.cfg.MaxHeight)
	chosen := media.Rendition{URL: variant.URL, Bitrate: media.Bitrate(variant.Bandwidth), Height: variant.Height}
	stream.URL = variant.URL
	stream.AudioURL = doc.AudioFor(variant)

	reportRendition(ctx, variant, origin, r.cfg.MaxHeight)
	if stream.Demuxed() {
		slog.InfoContext(ctx, "source publishes audio separately; both renditions will be read",
			"video", stream.URL.String(), "audio", stream.AudioURL.String())
	}

	// The segment facts are stated by the document that LISTS the segments, so a
	// master costs one more GET: the chosen variant's own playlist, with the same
	// headers, and the very next document the reader opens. It is worth one request
	// because EXT-X-MAP is knowable no other way before the read starts, and it is
	// what a fragile-read policy keys on: abandoning an fMP4 fragment mid-read
	// truncates it, a truncated AVCC stream desyncs the h264_mp4toannexb filter a copy
	// into MPEG-TS cannot do without, and that kills a cast forty minutes in.
	//
	// One GET per CAST, never one per candidate. Ranking is where a burst of requests
	// behind one signature earns an embed proxy's 429 (see maxProbePerHost), and this
	// runs after exactly one candidate has been chosen.
	segments := doc
	if doc.Multivariant {
		segments, status, err = r.readPlaylist(ctx, variant.URL, stream.Headers)
		if err != nil {
			slog.WarnContext(ctx, "the chosen rendition's playlist could not be read; the source's own facts stay unknown",
				"error", err, "status", status, "url", variant.URL.String())
			return origin, chosen
		}
	}
	origin.Framing = segments.Framing
	origin.Encrypted = segments.Encrypted
	// The document that LISTS the segments is the only witness worth having, so it decides
	// outright rather than joining a vote: EXT-X-ENDLIST is proof that the program has an end
	// (playlist.Closed), while a probe reporting no duration is proof of nothing, because
	// ffprobe routinely reports none for a playlist it read perfectly well.
	//
	// ORing the two is how a VOD master was read as a live edge and stayed one: the probe's
	// silence set it before any document was fetched, and an endlist could not clear what an OR
	// had already decided. A live read is paced at exactly realtime, and no judgement about a
	// starving upstream can be formed on a read that was never allowed to run ahead, so that
	// one guess switched off every deliverability verdict castor has.
	origin.Live = segments.Live
	if segments.Duration > 0 {
		// The EXTINF sum wins over anything measured: ffprobe reports no duration at all
		// for most playlists, and this is the program's actual runtime rather than
		// whatever fell out of a probe that read one segment.
		origin.Duration = segments.Duration
	}
	return origin, chosen
}

// reportRendition says what castor is about to read and, when the source published
// nothing under the user's ceiling, says that too.
//
// It exists because the run this whole phase was written for was silent about the
// only thing that mattered. The cap was 1080, the source offered exactly one
// rendition at 3840x1600, pickVariant correctly took the shortest on offer, and
// castor then pulled 17 Mbit/s of 4K to serve a 2 Mbit/s re-encode while every line
// in the log was consistent with a cap that had been applied. The encode still
// scales to the ceiling; what was missing was any way to see that the READ could
// not, and that there had been nothing else to choose.
func reportRendition(ctx context.Context, variant hlsVariant, origin media.Origin, ceiling media.HeightCap) {
	level, msg := slog.LevelInfo, "rendition selected"
	if !ceiling.Admits(variant.Height) {
		level, msg = slog.LevelWarn, "no rendition under the height cap; reading the shortest on offer and scaling it down"
	}
	slog.Log(ctx, level, msg,
		"height", variant.Height, "cap", int(ceiling), "declared_bitrate", variant.Bandwidth,
		"renditions", len(origin.Renditions), "sole", origin.Sole())
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
func (r *Resolver) verifyRendererCanFetch(ctx context.Context, stream *media.Stream) {
	if !stream.SelfFetchable() || r.measurer.OpensUnaided(ctx, stream) {
		return
	}
	stream.NeedsLeniency = true
	slog.InfoContext(ctx, "source opens only under relaxed reader checks; it will be served, not handed to the device",
		"url", stream.URL.String())
}

// readPlaylist fetches one HLS document and reduces it to the facts it states. The
// origin's status travels out with the error so the caller can tell a refusal from
// silence.
func (r *Resolver) readPlaylist(ctx context.Context, u *url.URL, headers http.Header) (hlsDocument, int, error) {
	body, status, err := r.playlists.Fetch(ctx, u, headers)
	if err != nil {
		return hlsDocument{}, status, err
	}
	doc, err := parsePlaylist(body, u)
	return doc, status, err
}

// pickVariant chooses which HLS variant to pull: the highest-bandwidth one the ceiling
// admits (a variant declaring no RESOLUTION is admitted, in the one convention every reader
// of the cap keeps, see media.HeightCap.Admits). If the ceiling admits none of them, it
// takes the shortest so the encoder has the least to downscale. variants is never empty
// (parsePlaylist guarantees at least the synthetic media-playlist entry, and program
// refuses a document that offers nothing castable).
func pickVariant(variants []hlsVariant, ceiling media.HeightCap) hlsVariant {
	variants = castable(variants)

	eligible := slices.DeleteFunc(slices.Clone(variants), func(v hlsVariant) bool {
		return !ceiling.Admits(v.Height)
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

// castable narrows a variant list to the rungs that carry video, keeping the whole
// list when none of them declares any.
//
// A master can list its audio rendition as a variant of its own, and such an entry
// has no RESOLUTION to exclude it by the cap and often the highest bandwidth of the
// lot, so on bandwidth alone it would win and the cast would be audio with no
// picture. That is also why it must not appear in the published ladder: a rung
// nothing can be cast from is not a rung to fall back to.
func castable(variants []hlsVariant) []hlsVariant {
	withVideo := slices.DeleteFunc(slices.Clone(variants), func(v hlsVariant) bool {
		return !v.HasVideo
	})
	if len(withVideo) == 0 {
		return variants
	}
	return withVideo
}

// ladder is the choice the source offered, in the vocabulary the cast layer reads:
// publication order preserved, audio-only rungs excluded, declared bitrates carried
// as declared. A single entry is the honest answer for a media playlist, which
// offered nothing, and for a master that published one rendition, which offered
// nothing either (see media.Origin.Sole).
func ladder(variants []hlsVariant) []media.Rendition {
	rungs := castable(variants)
	out := make([]media.Rendition, len(rungs))
	for i, v := range rungs {
		out[i] = media.Rendition{URL: v.URL, Bitrate: media.Bitrate(v.Bandwidth), Height: v.Height}
	}
	return out
}
