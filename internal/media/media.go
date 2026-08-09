package media

import (
	"iter"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	MP4    = "video/mp4"
	MKV    = "video/x-matroska"
	WebM   = "video/webm"
	AVI    = "video/x-msvideo"
	MOV    = "video/quicktime"
	HLS    = "application/x-mpegURL"
	MPEGTS = "video/mp2t"
	// FLV is here for the same reason MPEGTS is: ffprobe reports it for real
	// sources in the wild, and a container castor has no name for used to abort
	// the whole cast at resolution (see FormatToContentType). Castor cannot
	// produce it, which is fine: naming it only means the source is read rather
	// than refused.
	FLV = "video/x-flv"
)

// HLS output filenames, shared by the muxer (ffmpeg's hls muxer writes them
// into its working directory) and the HLS server (which serves that directory).
const (
	HLSPlaylistName   = "stream.m3u8"
	HLSInitName       = "init.mp4"
	HLSSegmentPattern = "seg_%05d.m4s"
)

// HLSArtifactTypes is what a response carrying each of those files says it carries, by
// extension. It lives beside the names for the reason the registry exists at all: the server
// fronting a segmented delivery used to keep a table of its own, and it answered a playlist
// request with a spelling of the HLS type (application/vnd.apple.mpegurl) that was not the one
// the renderer had just been told to expect at Play (HLS, above). One file, two names for what
// it is, decided in two places.
var HLSArtifactTypes = map[string]string{
	path.Ext(HLSPlaylistName): HLS,
	".m4s":                    "video/iso.segment",
	path.Ext(HLSInitName):     MP4,
}

// HLSInputArgs contains ffmpeg/ffprobe flags that relax extension checks
// for HLS playlists and DASH manifests.
var HLSInputArgs = []string{
	"-allowed_extensions", "ALL",
	"-allowed_segment_extensions", "ALL",
	"-extension_picky", "0",
	"-seg_format_options", "extension_picky=0",
}

// Stream is one castable program: where castor reads it from, and what it needs
// to read it. A program is usually one URL carrying both tracks, but a source is
// free to publish them separately (an HLS master with an audio rendition group),
// in which case it takes two reads to get a whole program.
type Stream struct {
	URL *url.URL

	// AudioURL is the companion audio rendition, set when the source publishes
	// its tracks separately: URL then carries video alone and the two are read
	// together as one program. nil is the ordinary case, audio muxed into URL.
	AudioURL *url.URL

	// NeedsLeniency marks a source castor could only open by relaxing its
	// reader's default checks, e.g. an HLS playlist whose MPEG-TS segments are
	// served under an image extension. Nothing about the URL says so; it is
	// established by probing it (see resolve.verifyRendererCanFetch, which asks its
	// measurer whether the source opens unaided).
	NeedsLeniency bool

	// The four fields below are what castor ESTABLISHED about this link, and they ride on the
	// value the cast reads because the party that needs each of them runs long after the party
	// that established it. Zero is the absence of evidence throughout, and every reader keeps
	// it lenient (see HeightCap.Admits, Origin.ProjectedRuntime, LadderUnknown). Height on a
	// record local to the ranker is what handed a measured 2160p source to a self-fetching
	// renderer under a 1080 ceiling: the number was logged and then dropped at the package
	// boundary, so the composition asked a height nobody had told it.

	// Ladder is what the captured document's own tags said about renditions, which is the only
	// way to tell a master from one of its rungs: a probe of a master reports whichever variant
	// ffprobe chose, so a variant playlist's probed height is a real ceiling and a master's is
	// not. Unknown must never be read as "not a master".
	Ladder Ladder

	// Height and Duration are what a probe of this link measured.
	Height   int
	Duration time.Duration

	// Probed tells the two shapes behind Duration 0 apart: a container somebody opened that
	// states no runtime (a stream with no ending in it) against a link nobody has opened (a URL
	// cast by hand, a candidate whose probe was killed). The two get opposite read policies,
	// one paced at exactly realtime and one allowed to run ahead (see Origin.Live).
	Probed bool

	Headers     http.Header
	Bandwidth   int64
	ContentType string
}

// HeightCap is the ceiling the operator set on what may reach the renderer, as every party
// that has to honour it reads it.
//
// It is a type rather than an int threaded through four signatures because the same number is
// asked of four kinds of evidence: a rung an HLS master declared, a height the ranker
// measured, a height a leg's own probe measured, and a variant's RESOLUTION during selection.
// Four hand-written comparisons are four chances for the leniency below to drift, and one
// drifting is how max_height bound a buffered cast and did nothing at all to a remux of the
// same source, decided by a routing decision no log line reported.
type HeightCap int

// Admits reports whether a picture of this height may reach the renderer.
//
// An unestablished height (0) is admitted, and that is absence of evidence rather than a
// sentinel: a container that states no height, a playlist that declares no RESOLUTION and a
// probe that failed are all ordinary things, none of them says the picture is tall, and
// refusing them would cost every unmeasured cast a decode, a scale and a re-encode to bound
// a height that in all likelihood already fits, while every undeclared pass-through loses
// the cheapest leg there is. It is the leniency ReachUnproven and LadderUnknown keep:
// nothing established may convict.
//
// The ceiling itself has no sentinel. max_height is `validate:"required,min=1"`, so a cast
// that asks has a ceiling somebody typed, and reading zero as "no ceiling" would borrow the
// capability model's convention, where zero means "the device told us nothing" (VideoSupport
// carries no resolution at all for that reason). To lift the ceiling, set it above anything
// you own.
func (c HeightCap) Admits(height int) bool { return height == 0 || height <= int(c) }

// Demuxed reports whether the program's tracks live at separate URLs, so a
// reader needs both.
func (s *Stream) Demuxed() bool { return s.AudioURL != nil }

// SelfFetchable reports whether a renderer handed nothing but this stream's URL
// can pull the whole program itself. A pass-through gives the device that URL
// and nothing else, which rules out two shapes:
//
//   - a header-gated source. None of the request headers castor captured while
//     extracting (Referer, Origin, Cookie, User-Agent) travel with the URL, and
//     castor cannot know which of them the origin gates on, so a URL it only ever
//     fetched with headers is not proven fetchable without them.
//   - a demuxed program. One URL is one rendition, so the renderer would play
//     the video and none of the audio.
//   - a source only a lenient reader opens. A renderer fetching for itself
//     applies its own defaults, and refuses what castor had to relax a check to
//     read at all.
//
// Either way the device fails where castor succeeds, and silently: it accepts the
// load and then sits idle, or plays in silence. Such a source is served locally
// instead, castor reading what it needs and giving the renderer a single LAN URL.
// A header-free, self-contained source (the direct URL a user casts by hand)
// passes through.
func (s *Stream) SelfFetchable() bool {
	return len(s.Headers) == 0 && !s.Demuxed() && !s.NeedsLeniency
}

// DeliveryKind is how castor's local HTTP server hands a produced stream to the
// renderer, and the single fact the delivery driver keys the serving mechanism
// on. It is carried as data on FormatInfo, so a producible format's delivery is
// declared alongside it rather than decided by a content-type conditional.
type DeliveryKind int

const (
	// DeliverStream is one growing output the replay server fronts, handing every
	// client the stream from byte 0 (MPEG-TS, fragmented mp4).
	DeliverStream DeliveryKind = iota
	// DeliverSegmented is a live playlist plus rolling segments in a directory the
	// HLS server fronts.
	DeliverSegmented
)

// Framing is where a container keeps the decoder configuration of the elementary
// streams it carries, and the one property of a container a stream copy has to
// agree with: a bitstream that carries its configuration one way cannot simply be
// dropped into a container that expects the other.
//
// It is carried as data on FormatInfo so each producible container declares its
// own, rather than a copy path enumerating container names it believes are one or
// the other. Everything about repacking a copied track is derived from it (see
// the copy adaptation tables in the ffmpeg package).
//
// The zero value is deliberately not an answer. Framing is the input to the one
// decision whose wrong answer is either fatal or silently destructive: pushing
// aac_adtstoasc at an in-band container exits cleanly while the muxer discards
// almost every audio packet, leaving a stream nothing can decode. FormatInfo is
// an exported struct with exported fields, so a record
// built anywhere but the registry would otherwise silently declare itself in-band
// and get the answer for free. Unknown instead makes that a hard error at
// argument-build time, before ffmpeg starts.
type Framing int

const (
	// FramingUnknown is the zero value: a container that has not declared how it
	// frames its streams. No adaptation matches it and no encode may target it.
	FramingUnknown Framing = iota
	// FramingInBand repeats each track's decoder configuration inside the stream,
	// ahead of every frame: ADTS headers on AAC, Annex B start codes on H.264 and
	// HEVC. MPEG-TS is the one castor produces. A muxer writing it emits those
	// headers itself, so a bare bitstream copied in needs nothing done to it.
	FramingInBand
	// FramingOutOfBand declares each track's decoder configuration once, in the
	// container header (MP4's sample description boxes), and carries bare frames
	// after it. A track arriving with in-band headers has to be repacked before it
	// can be copied in, or the muxer rejects its very first packet ("Malformed AAC
	// bitstream detected", exit 255, 0 of 189 audio packets written).
	FramingOutOfBand
)

// String names the framing for a log line. Unknown is the default arm and prints as
// itself: a source whose playlist castor never read has no framing, and a line
// showing "0" for that reads as a value rather than as the absence of one.
func (f Framing) String() string {
	switch f {
	case FramingInBand:
		return "in-band"
	case FramingOutOfBand:
		return "out-of-band"
	default:
		return "unknown"
	}
}

// FormatInfo describes a container castor can produce: the MIME type the device
// is told it is fetching, the file extension it carries, the ffmpeg muxer (-f)
// that writes it, how it is delivered, and how it frames the streams inside it.
type FormatInfo struct {
	ContentType string
	Extension   string
	Muxer       string
	Delivery    DeliveryKind
	Framing     Framing
}

// formatRegistry is the vocabulary of containers castor can produce, keyed by
// content type. It is the single source of truth the media helpers, the delivery
// driver and the copy adaptations read; adding a producible format is one row
// here plus one ffmpeg.containerTuning entry for its muxer (nothing else
// enumerates formats, deliveries or muxers).
//
// HLS declares FramingOutOfBand because castor writes its segments as fMP4, and
// that is a real coupling to ffmpeg.containerTuning["hls"], which sets
// -hls_segment_type fmp4. The same source and the same muxer with
// -hls_segment_type mpegts copies ADTS AAC untouched at exit 0, so flipping that
// one flag without flipping this field would hand an aac_adtstoasc to an in-band
// destination, which exits 0 and destroys the audio.
// TestHLSSegmentTypeMatchesDeclaredFraming binds the two.
var formatRegistry = map[string]FormatInfo{
	MPEGTS: {ContentType: MPEGTS, Extension: ".ts", Muxer: MuxerMPEGTS, Delivery: DeliverStream, Framing: FramingInBand},
	MP4:    {ContentType: MP4, Extension: ".mp4", Muxer: MuxerMP4, Delivery: DeliverStream, Framing: FramingOutOfBand},
	HLS:    {ContentType: HLS, Extension: ".m3u8", Muxer: MuxerHLS, Delivery: DeliverSegmented, Framing: FramingOutOfBand},
}

// The ffmpeg muxer names castor writes. They live beside the registry rows that
// carry them because the registry is what assigns a muxer to a container: a
// consumer keying a rule on a muxer (the copy adaptations, the container tuning
// table, the one carriage rule about mp4's own implementation) is matching
// against a value that originated here, and a private copy of the string in each
// of those packages is a spelling that can drift from the row it must equal.
const (
	MuxerMPEGTS = "mpegts"
	MuxerMP4    = "mp4"
	MuxerHLS    = "hls"
)

// FormatForContentType returns the FormatInfo for a content type, or ok=false if
// castor cannot produce it. Callers read the muxer, extension, and delivery off
// the returned FormatInfo.
func FormatForContentType(ct string) (FormatInfo, bool) {
	f, ok := formatRegistry[ct]
	return f, ok
}

// ProducibleFormats yields every container castor can produce. It exists so the
// couplings a producible format carries can be asserted over the whole registry
// rather than over a list a test would have to remember to grow: every row needs
// a declared framing, and every row's muxer needs a containerTuning entry in the
// ffmpeg package. Order is unspecified, since every consumer is a per-row check.
func ProducibleFormats() iter.Seq[FormatInfo] { return maps.Values(formatRegistry) }

var extensionMap = map[string]string{
	".mp4":  MP4,
	".mkv":  MKV,
	".webm": WebM,
	".avi":  AVI,
	".mov":  MOV,
	".m3u8": HLS,
}

// DetectFromExtension returns a content type based on the URL's file extension,
// or empty string if unrecognized.
func DetectFromExtension(u *url.URL) string {
	return extensionMap[strings.ToLower(path.Ext(u.Path))]
}

// mimeContentTypes maps a server-confirmed MIME type to a content type. Every
// value here is a container named elsewhere in this file; mp2t and x-msvideo were
// missing rows rather than deliberate omissions, so a raw MPEG-TS or AVI served
// with its correct MIME reported "unknown" while the same file under its own
// extension resolved fine.
var mimeContentTypes = map[string]string{
	"video/mp4":                     MP4,
	"video/webm":                    WebM,
	"video/x-matroska":              MKV,
	"video/mp2t":                    MPEGTS,
	"video/x-msvideo":               AVI,
	"audio/mpegurl":                 HLS,
	"audio/x-mpegurl":               HLS,
	"application/x-mpegurl":         HLS,
	"application/vnd.apple.mpegurl": HLS,
}

// DetectFromMIME returns a content type based on a confirmed MIME type,
// or empty string if unrecognized.
func DetectFromMIME(mime string) string {
	return mimeContentTypes[strings.ToLower(mime)]
}

// HeaderArgs renders h as the ffmpeg/ffprobe -headers flag pair, or nil when h is
// empty. ffprobe and ffmpeg both want every request header in one CRLF-joined blob.
func HeaderArgs(h http.Header) []string {
	if len(h) == 0 {
		return nil
	}
	var b strings.Builder
	for key, values := range h {
		for _, v := range values {
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	return []string{"-headers", b.String()}
}

// NormalizeStreamHeaders returns a copy of browser-captured headers ready to
// replay to the puller. It drops headers that break a re-issued fetch and, when
// a Referer is present without an Origin, derives the Origin from it: a
// cross-origin browser GET sends only a Referer, but CDNs commonly gate segment
// delivery on Origin too, and the derived pair is what a site's own player proxy
// sends. The input is not mutated.
func NormalizeStreamHeaders(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		return nil
	}
	// A stale Range fetches a byte slice instead of the whole resource; a
	// br/zstd Accept-Encoding yields a body ffmpeg can't decode ("Invalid data
	// found when processing input").
	out.Del("Range")
	out.Del("Accept-Encoding")
	if out.Get("Origin") == "" {
		if origin := originOf(out.Get("Referer")); origin != "" {
			out.Set("Origin", origin)
		}
	}
	return out
}

// originOf returns the scheme://host origin of an absolute URL, or "" if s is
// not one.
func originOf(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// FormatToContentType maps an ffprobe format_name to a content type. ffprobe
// reports a comma-joined list of every demuxer that claimed the input, so the
// first recognised name wins.
//
// An unrecognised format is NOT an error. Resolution's only use for the answer is
// to decide whether the source needs HLS's relaxed input flags and whether the
// renderer might be handed the URL, and a container castor has no name for
// answers "no" to both, which is exactly what the unknown content type produces.
// Refusing here used to abort the cast before any stage ran, so a raw MPEG-TS
// stream (format_name "mpegts") or an FLV could not be
// cast at all even though castor muxes MPEG-TS itself.
func FormatToContentType(format string) string {
	for f := range strings.SplitSeq(format, ",") {
		switch strings.TrimSpace(f) {
		case "hls", "applehttp":
			return HLS
		// "mp4" is checked but "mov" deliberately is not. ffprobe 8.1.2 reports the
		// same joined list, "mov,mp4,m4a,3gp,3g2,mj2", for a genuine .mov and for a
		// plain .mp4, and "mov" comes first in it. A mov row would
		// therefore reclassify every mp4 source castor reads as video/quicktime, and
		// a renderer advertising only video/mp4 would stop accepting sources it plays
		// today. The joined list already resolves to MP4 for both, which is the
		// answer both callers of this want: they are the same container family and
		// MP4 is the one castor produces.
		case "mp4":
			return MP4
		case "matroska":
			return MKV
		case "webm":
			return WebM
		case "avi":
			return AVI
		case "mpegts":
			return MPEGTS
		case "flv":
			return FLV
		}
	}
	return ""
}
