package e2e

import (
	"bytes"
	"context"
	"io"
	"iter"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// The subject is one question: given an input stream, does castor produce a
// stream that plays.
//
// Nothing here knows what a Chromecast is. Delivery, discovery and device
// protocols are decisions made elsewhere and covered elsewhere; what reaches this
// file is the only part of them that changes the bytes, which is the container
// castor was asked to write and the codecs the far end can decode. Both are plain
// data, so a cell needs no renderer, no server and no subprocess to describe.
//
// What is real: the input is a live stream from a real ffmpeg over real HTTP, the
// decisions are castor's own resolvers, the arguments are castor's own, the
// encoder is a real ffmpeg, and the result is read back with a real ffprobe.

const (
	// produceFor is how long each cell lets the encoder run. A live source has no
	// end, so production is stopped rather than waited out.
	//
	// It has to outlast the coarsest thing the encode does, which is the segmented
	// output's four-second fragments: an artifact interrupted before its first
	// fragment closes is not empty, it is unreadable, and ffprobe fails on it rather
	// than reporting zero packets. Three fragments leaves margin for a cell that
	// also re-encodes audio and so reads slower than wall clock.
	produceFor = 14 * time.Second

	// probeWithin bounds each source probe, standing in for the resolver's
	// configured probe timeout. Every input here is served from a local httptest
	// server, so this is a deadlock guard rather than a real allowance.
	probeWithin = 30 * time.Second
)

// ---------------------------------------------------------------- the matrix

// input is one row of the input axis: a live stream shape castor has to read, and
// what should survive reading it.
type input struct {
	name string
	live liveSource
	// channels asserts the produced channel count for a multichannel input, so a
	// silent downmix fails rather than passes. 0 skips the check.
	channels int
	// silent marks an input with no audio track at all. There is nothing to assert
	// about audio then, and the point of the row is that the absence is survivable
	// rather than fatal.
	silent bool
}

// inputs is the input axis. The rows disagree with each other on purpose: the
// same playlist extension serves AAC framed two incompatible ways, one row only
// opens if the reader relaxes its checks, and one carries audio no downmix should
// touch.
var inputs = []input{{
	// The ordinary case: TS segments, so the AAC arrives framed in band, with a
	// header ahead of every frame.
	name: "ts-aac",
	live: liveSource{},
}, {
	// The embed-CDN disguise: identical media, but the segments are served under a
	// .jpg extension so the origin labels them image/jpeg. Reading it at all takes
	// relaxed extension checks.
	name: "ts-aac-disguised-as-jpeg",
	live: liveSource{SegmentExt: ".jpg"},
}, {
	// fMP4 segments, so the AAC is already out of band. This is the direction that
	// must NOT be repacked, and getting it wrong is the failure that does not
	// announce itself: ffmpeg exits cleanly having discarded almost every packet.
	name: "fmp4-aac",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s"},
}, {
	// 5.1 AC-3, which the far end below decodes, so it should survive at full
	// channel count rather than being folded to stereo.
	name:     "ts-ac3-surround",
	live:     liveSource{AudioCodec: "ac3", AudioChannels: "6"},
	channels: 6,
}, {
	// HEVC, which is ordinary for anything 4K and is the one codec that lands in an
	// MP4 tagged hev1 when players conventionally want hvc1.
	name: "ts-hevc-aac",
	live: liveSource{VideoCodec: "libx265"},
}, {
	// FLAC, which MPEG-TS has no stream type for. It does not refuse it: it writes
	// the track as private data and exits cleanly, so the only correct handling is
	// to re-encode into something the container can carry. Producing this input at
	// all is what makes that claim testable.
	name: "fmp4-flac",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s", AudioCodec: "flac"},
}, {
	// Opus, which MPEG-TS does carry, with proper stream registration rather than
	// as private data. It is here to hold the other side of that line: an absent
	// adaptation must mean "nothing to do", not "never looked at".
	name: "fmp4-opus",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s", AudioCodec: "libopus"},
}, {
	// VP9, the video half of the same problem: MPEG-TS writes it as private data at
	// a clean exit, while the MP4 family carries it fine.
	name: "fmp4-vp9",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s", VideoCodec: "libvpx-vp9"},
}, {
	// No audio at all. A pinned stream map turns that into an argument-parse
	// failure before a byte is read, which is a source refused for being unusual.
	name:   "ts-video-only",
	live:   liveSource{NoAudio: true},
	silent: true,
}, {
	// Tracks published as separate renditions, the shape an HLS master with an
	// audio group resolves to. Neither is playable alone, so the program only
	// exists if both are read and muxed back together.
	name: "demuxed-renditions",
	live: liveSource{Demuxed: true},
}}

// decodes is what the far end can play. It is deliberately generous: this suite
// is about producing a valid stream, not about capability negotiation, so a
// codec is re-encoded here only when the container genuinely cannot carry it.
var decodes = media.Renderer{
	Video: []media.VideoSupport{{Codec: media.CodecH264}, {Codec: media.CodecHEVC}},
	Audio: []media.AudioSupport{
		{Codec: media.CodecAAC, MaxChannels: 8},
		{Codec: media.CodecAC3},
		{Codec: media.CodecEAC3},
	},
}

// output is one row of the output axis: a container to produce, and whether it is
// produced straight off the network or out of the read-once spool.
type output struct {
	name        string
	contentType string
	// spooled routes the input through castor's MPEG-TS spool first, the way a
	// cast to a player that cannot fetch for itself does. It is not a detail: the
	// spool re-frames everything through it, so an input whose AAC arrived out of
	// band comes back off the spool in band and needs the opposite handling.
	spooled bool
}

// outputs is the output axis. MPEG-TS repeats decoder configuration in band,
// ahead of every frame; the MP4 family declares it once, up front. A bitstream
// that is correct for one is rejected or silently gutted by the other.
var outputs = []output{
	{name: "mpegts", contentType: media.MPEGTS},
	{name: "mp4", contentType: media.MP4},
	{name: "hls-fmp4", contentType: media.HLS},
	{name: "spool-to-mpegts", contentType: media.MPEGTS, spooled: true},
	{name: "spool-to-mp4", contentType: media.MP4, spooled: true},
}

// cells yields every pair. The cross-product is the test: handling that is right
// for one container is destructive for another, so an input shape is only proven
// by producing every container from it.
func cells() iter.Seq2[input, output] {
	return func(yield func(input, output) bool) {
		for _, in := range inputs {
			for _, out := range outputs {
				if !yield(in, out) {
					return
				}
			}
		}
	}
}

// TestProducesPlayableStream reads every live input shape and produces every
// container from it, then asserts on the packets that came out.
//
// Cells run sequentially: each drives a real-time encoder on both ends, and
// running them concurrently would have them competing for the CPU the pacing
// depends on.
func TestProducesPlayableStream(t *testing.T) {
	for in, out := range cells() {
		t.Run(in.name+"/"+out.name, func(t *testing.T) {
			got := produce(t, in, out)

			got.carriesVideo()
			if !in.silent {
				got.carriesAudio()
			}
			if in.channels > 0 {
				got.hasChannels(in.channels)
			}
		})
	}
}

// ---------------------------------------------------------------- production

// produce runs castor's real stream-production chain for one cell: probe the
// input, let castor's own resolvers decide what to copy and what to re-encode,
// build castor's own argument list from that, and run it. Nothing about the
// arguments is written here, which is the point: what is under test is what
// castor decides, not what a test can talk ffmpeg into doing.
func produce(t *testing.T, in input, out output) result {
	t.Helper()
	tl := newTools(t)
	origin := startLive(t, tl, in.live)
	if !origin.isLive(t, tl) {
		t.Fatal("fixture reports a duration, so it is not exercising the live path")
	}

	format, ok := media.FormatForContentType(out.contentType)
	if !ok {
		t.Fatalf("castor cannot produce %q", out.contentType)
	}

	stream := &media.Stream{URL: mustURL(t, origin.PlaylistURL), ContentType: media.HLS, Live: true}
	if origin.AudioURL != "" {
		stream.AudioURL = mustURL(t, origin.AudioURL)
	}
	// The live fixture is read on the terms the read table gives a live segmented
	// source, chosen exactly as production chooses them (from what the source
	// published, not from the URL), so this matrix exercises the flags a real cast
	// sends rather than a set assembled here.
	policy, err := read.For(read.ShapeOf(media.Origin{Segmented: true, Live: true}), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	source := ffmpeg.NewNetworkSource(stream, policy)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	var fromSpool string
	opts := ffmpeg.EncodeOptions{Format: format}
	if out.spooled {
		opts.PipeFormat = ffmpeg.SpoolFormat
		fromSpool, opts.Probe = spool(t, ctx, tl, source)
	} else {
		opts.Source = source
		probe, err := probeSource(ctx, tl, source)
		if err != nil {
			t.Fatalf("probing the input: %v", err)
		}
		opts.Probe = probe
	}

	opts.Audio = core.DecideAudio(ctx, core.AudioInputs{Caps: decodes, Probe: opts.Probe, Into: format})
	opts.Video = core.DecideVideo(ctx, core.VideoInputs{
		Caps:       decodes,
		Probe:      opts.Probe,
		Into:       format,
		Policy:     core.CopyWhatFits,
		MaxHeight:  1080,
		FFmpegPath: tl.ffmpeg,
	})
	t.Logf("input %s/%dch, producing %s (video=%s audio=%s)",
		opts.Probe.VideoCodec, opts.Probe.AudioChannels, out.contentType,
		opts.Video.Name(), opts.Audio.Name())

	return run(t, ctx, tl, opts, format, fromSpool)
}

// spool reproduces the read-once path's first leg: castor pulls the input into an
// append-only MPEG-TS file, and everything downstream reads that rather than the
// original. Probing the spool rather than the input is the whole reason this leg
// is modelled, because the two do not agree about framing.
func spool(t *testing.T, ctx context.Context, tl tools, source ffmpeg.NetworkSource) (string, media.ProbeInfo) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "spool.ts")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	pullCtx, stop := context.WithTimeout(ctx, produceFor)
	defer stop()

	// What the spool can carry is not a given. MPEG-TS has no stream type for
	// several codecs and does not refuse them: it writes the track as private data
	// and exits cleanly, so a bare copy would hand everything downstream a spool
	// that was already destroyed. Castor answers that at runtime, by watching the
	// muxer's own complaint and restarting the pull with the affected axis
	// re-encoded; asking its carriage tables up front reaches the same pull options
	// without reimplementing the detection.
	var axes carriage.Axes
	if probe, err := probeSource(ctx, tl, source); err == nil {
		axes = carriage.Known(probe, ffmpeg.SpoolFormat)
	}
	if axes.Any() {
		t.Logf("the spool cannot carry this input as-is; re-encoding video=%v audio=%v", axes.Video, axes.Audio)
	}

	pullOpts := ffmpeg.PullOptions{Source: source, Reencode: axes}
	proc, err := ffmpeg.Start(pullCtx, tl.ffmpeg,
		ffmpeg.PullArgs(pullOpts), ffmpeg.WithExtraPipes(pullOpts.ExtraPipes()))
	if err != nil {
		t.Fatalf("starting the pull: %v", err)
	}

	// The pull reports on itself on an extra pipe, and that feed has to be read: ffmpeg
	// writes it with a blocking write, so an unread one stops the download once the
	// pipe buffer fills. Reading it here also states what the read achieved, which for
	// a live source is the only measure of it that means anything (speed= is media
	// seconds per wall-clock second, so a healthy live read sits at 1x).
	var last media.Progress
	done := make(chan struct{})
	go func() {
		defer close(done)
		ffmpeg.WatchProgress(proc.ProgressFeed(), func(s media.Progress) { last = s })
	}()

	if _, err := io.Copy(out, proc.Stdout); err != nil && pullCtx.Err() == nil {
		t.Fatalf("spooling: %v", err)
	}
	_ = proc.Wait()
	<-done
	t.Logf("the pull delivered %s of media at %vx", last.Position.Round(time.Millisecond), last.Speed)

	// Only emptiness is a failure here. A byte threshold would be a guess about
	// bitrate, and these inputs deliberately span an order of magnitude of it; what
	// the spool has to contain is judged by the packet counts downstream.
	if fileSize(path) == 0 {
		t.Fatal("the pull spooled nothing")
	}
	probe, err := ffmpeg.FileProbe(tl.ffprobe, path).Probe(ctx)
	if err != nil {
		t.Fatalf("probing the spool: %v", err)
	}
	return path, probe
}

// probeSource measures an upstream under this suite's own budget. The budget is the
// caller's everywhere in castor, because whoever owns what a failed measurement means owns
// how long it may take to fail (see core.Measure).
func probeSource(ctx context.Context, tl tools, source ffmpeg.NetworkSource) (media.ProbeInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, probeWithin)
	defer cancel()
	return ffmpeg.SourceProbe(tl.ffprobe, source).Probe(ctx)
}

// run executes castor's argument list with a real ffmpeg and collects what it
// produced.
//
// It drives the process itself rather than through ffmpeg.Start, for one reason:
// the stop signal. A live source never ends, so production has to be interrupted,
// and SIGKILL is the wrong way to do it. A fragmented MP4 killed mid-fragment
// loses whatever the muxer had not flushed, which for a Dolby track is every
// audio sample it was holding, so the result looks exactly like the silent
// corruption this suite exists to detect. SIGINT is what a user pressing Ctrl+C
// sends and what ffmpeg unwinds cleanly on. The arguments are still castor's; only
// the process handling is local.
func run(t *testing.T, ctx context.Context, tl tools, opts ffmpeg.EncodeOptions, format media.FormatInfo, fromSpool string) result {
	t.Helper()

	args, err := ffmpeg.EncodeArgs(opts)
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	t.Logf("ffmpeg %s", strings.Join(args, " "))

	workDir := t.TempDir()
	runCtx, stop := context.WithTimeout(ctx, produceFor)
	defer stop()

	cmd := exec.CommandContext(runCtx, tl.ffmpeg, args...)
	cmd.Dir = workDir // a segmented format writes its playlist and segments here
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 15 * time.Second

	var stderr strings.Builder
	cmd.Stderr = &stderr

	// Every castor encode reports on itself on its first extra fd, so this local
	// process handling has to provide that fd and read it: an encode whose -progress
	// URL points at an fd the parent never opened does not start at all ("Failed to
	// open progress URL pipe:3: Bad file descriptor"), and one nobody reads stops once
	// the pipe buffer fills.
	progressR, progressW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer progressR.Close()
	cmd.ExtraFiles = []*os.File{progressW}

	var last media.Progress
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		ffmpeg.WatchProgress(progressR, func(s media.Progress) { last = s })
	}()

	if fromSpool != "" {
		spooled, err := os.Open(fromSpool)
		if err != nil {
			t.Fatal(err)
		}
		defer spooled.Close()
		cmd.Stdin = spooled
	}

	produced := filepath.Join(workDir, "produced"+format.Extension)
	if format.Delivery == media.DeliverSegmented {
		produced = filepath.Join(workDir, media.HLSPlaylistName)
	} else {
		out, err := os.Create(produced)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		cmd.Stdout = out
	}

	// Interrupted production exits non-zero by definition, so the status says
	// nothing here; whether what landed plays is the whole question.
	_ = cmd.Run()

	// Closing the parent's write end is what ends the feed: the child is gone, and
	// this is the last handle holding the pipe open.
	_ = progressW.Close()
	<-drained
	t.Logf("the encode produced %s of media at %vx", last.Position.Round(time.Millisecond), last.Speed)

	if format.Delivery == media.DeliverSegmented {
		closePlaylist(t, produced)
	}
	// An encode that produced no artifact at all is a failure of the run, not a
	// result to assert on, and the only useful thing to say about it is what ffmpeg
	// said. Reporting it here keeps that diagnosis out of the assertions, which
	// would otherwise fail on an unreadable path with nothing to explain it.
	if fileSize(produced) == 0 {
		t.Fatalf("the encoder produced no %s at %s\n%s", format.ContentType, produced, stderr.String())
	}
	return result{t: t, ffprobe: tl.ffprobe, path: produced, stderr: stderr.String()}
}

// closePlaylist marks the produced window as ended, if the encoder did not
// already do it.
//
// What castor writes is a live playlist, deliberately without EXT-X-ENDLIST: a
// player is meant to keep asking for the next segment. That is correct for a cast
// and useless to a reader that wants to reach the end, which every assertion here
// is. Production was stopped mid-stream, so declaring the stream ends there is not
// a fiction.
//
// The tag may already be present: interrupted rather than killed, the hls muxer
// finalizes its own playlist on the way out. Appending a second one produces a
// playlist no reader accepts, which is a failure of this file that looks exactly
// like the empty artifact the suite exists to catch.
func closePlaylist(t *testing.T, path string) {
	t.Helper()
	playlist, err := os.ReadFile(path)
	if err != nil {
		return // the encoder never got as far as a playlist; run reports that
	}
	const endlist = "#EXT-X-ENDLIST"
	if bytes.Contains(playlist, []byte(endlist)) {
		return
	}
	if err := os.WriteFile(path, append(playlist, endlist+"\n"...), 0o600); err != nil {
		t.Fatalf("closing the produced playlist: %v", err)
	}
}

// ---------------------------------------------------------------- assertions

// result is what castor produced, and the questions worth asking about it. Every
// one is about packets actually present, never about declared tracks: the
// failures this suite exists to catch produce a stream that declares everything
// and carries nothing.
type result struct {
	t       *testing.T
	ffprobe string
	path    string
	stderr  string
}

func (r result) carriesVideo() {
	r.t.Helper()
	if n := r.packets("v"); n == 0 {
		r.t.Errorf("the produced stream carries no video packets\n%s", r.stderr)
	} else {
		r.t.Logf("produced %d video packets", n)
	}
}

func (r result) carriesAudio() {
	r.t.Helper()
	if n := r.packets("a"); n == 0 {
		r.t.Errorf("the produced stream carries no audio packets: a track can be declared and still be empty, which is exactly how a bad repack fails\n%s", r.stderr)
	} else {
		r.t.Logf("produced %d audio packets", n)
	}
}

func (r result) hasChannels(want int) {
	r.t.Helper()
	if got := r.probe("-select_streams", "a:0", "-show_entries", "stream=channels"); got != want {
		r.t.Errorf("produced %d audio channels, want %d: a codec the far end decodes should not be downmixed", got, want)
	}
}

func (r result) packets(kind string) int {
	r.t.Helper()
	return r.probe("-select_streams", kind+":0", "-count_packets", "-show_entries", "stream=nb_read_packets")
}

// probe runs ffprobe and reads the number it reports.
//
// Two things make that less obvious than it sounds, and both once turned a
// healthy stream into a fabricated bug report. An MPEG-TS file carries its
// streams inside a program, so ffprobe prints the same value twice, once per
// section. And an AC-3 track in MP4 prints a trailing empty CSV field, so the
// whole line does not parse as a number even though the number is right there.
//
// A value it cannot parse is therefore a fault in this function, never an answer,
// and it says so rather than returning the zero that means "this stream is
// empty". That distinction is the entire point: an empty stream is what this
// suite exists to catch, so nothing else may be allowed to look like one.
func (r result) probe(args ...string) int {
	r.t.Helper()
	full := append([]string{"-v", "error"}, media.HLSInputArgs...)
	full = append(append(full, args...), "-of", "csv=p=0", r.path)

	// Bounded, because reading a produced stream is not obviously terminating: a
	// playlist that still looks live has a reader waiting for a segment that will
	// never arrive, and an unbounded probe hangs the suite instead of failing a cell.
	ctx, cancel := context.WithTimeout(r.t.Context(), 60*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, r.ffprobe, full...).Output()
	if err != nil {
		r.t.Fatalf("probing %s: %v", r.path, err)
	}

	reported := strings.TrimSpace(string(out))
	for line := range strings.Lines(reported) {
		for field := range strings.SplitSeq(line, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(field)); err == nil {
				return n
			}
		}
	}
	// Nothing, or "N/A", is ffprobe's answer for a stream it cannot read at all,
	// which is a real zero. Anything else means this function failed to read a
	// reply it was given.
	if reported != "" && !strings.Contains(reported, "N/A") {
		r.t.Fatalf("could not read a number out of ffprobe's reply %q for %v", reported, args)
	}
	return 0
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
