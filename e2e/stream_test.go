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

	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
)

const (
	// produceFor is how long each cell lets the encoder run before stopping it; a live source has no end.
	produceFor = 6 * time.Second

	probeWithin = 30 * time.Second
)

type input struct {
	name string
	live liveSource
	// channels asserts the produced channel count, so a silent downmix fails. 0 skips.
	channels int
	// silent marks an input with no audio track at all.
	silent    bool
	opensOnly bool
}

// inputs is the input axis.
var inputs = []input{{
	name: "ts-aac",
	live: liveSource{},
}, {
	name:      "ts-aac-disguised-as-jpeg",
	live:      liveSource{SegmentExt: ".jpg"},
	opensOnly: true,
}, {
	// AAC already out of band, the direction that must NOT be repacked.
	name: "fmp4-aac",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s"},
}, {
	name:     "ts-ac3-surround",
	live:     liveSource{AudioCodec: "ac3", AudioChannels: "6"},
	channels: 6,
}, {
	// HEVC, the one codec that lands in an MP4 tagged hev1 when players want hvc1.
	name: "ts-hevc-aac",
	live: liveSource{VideoCodec: "libx265"},
}, {
	name: "fmp4-flac",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s", AudioCodec: "flac"},
}, {
	name: "fmp4-opus",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s", AudioCodec: "libopus"},
}, {
	name: "fmp4-vp9",
	live: liveSource{SegmentType: "fmp4", SegmentExt: ".m4s", VideoCodec: "libvpx-vp9"},
}, {
	// No audio at all: a pinned stream map turns that into an argument-parse failure before a byte is read.
	name:   "ts-video-only",
	live:   liveSource{NoAudio: true},
	silent: true,
}, {
	// Tracks as separate renditions, the shape an HLS master with an audio group resolves to.
	name: "demuxed-renditions",
	live: liveSource{Demuxed: true},
}}

var decodes = media.Capabilities{
	Video: []media.VideoSupport{{Codec: media.CodecH264}, {Codec: media.CodecHEVC}},
	Audio: []media.AudioSupport{
		{Codec: media.CodecAAC, MaxChannels: 8},
		{Codec: media.CodecAC3},
		{Codec: media.CodecEAC3},
	},
}

type output struct {
	name        string
	contentType string
	spooled     bool
}

// outputs is the output axis.
var outputs = []output{
	{name: "mpegts", contentType: media.MPEGTS},
	{name: "mp4", contentType: media.MP4},
	{name: "hls-fmp4", contentType: media.HLS},
	{name: "spool-to-mpegts", contentType: media.MPEGTS, spooled: true},
	{name: "spool-to-mp4", contentType: media.MP4, spooled: true},
}

func cells() iter.Seq2[input, output] {
	return func(yield func(input, output) bool) {
		for _, in := range inputs {
			for _, out := range outputs {
				if in.opensOnly && out.contentType != media.MPEGTS {
					continue
				}
				if !yield(in, out) {
					return
				}
			}
		}
	}
}

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

func produce(t *testing.T, in input, out output) result {
	t.Helper()
	tl := newTools(t)
	origin := startLive(t, tl, in.live)
	if !origin.isLive(t) {
		t.Fatal("fixture reports a duration, so it is not exercising the live path")
	}

	format, ok := container.FormatForContentType(out.contentType)
	if !ok {
		t.Fatalf("castor cannot produce %q", out.contentType)
	}

	inputs := []media.Input{{
		ID: media.PrimaryInputID, URL: mustURL(t, origin.PlaylistURL), ContentType: media.HLS,
		Fetch: media.Fetch{Segmented: true, Live: true},
	}}
	tracks := []media.TrackRef{
		{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
		{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
	}
	end := media.EndAtLongest
	if origin.AudioURL != "" {
		inputs = append(inputs, media.Input{
			ID: media.AudioInputID, URL: mustURL(t, origin.AudioURL), ContentType: media.HLS,
			Fetch: media.Fetch{Segmented: true, Live: true},
		})
		tracks[1].Input = media.AudioInputID
		end = media.EndAtShortest
	}
	policy := read.For(media.Fetch{Segmented: true, Live: true}, 30*time.Second)
	program, err := media.NewProgram(media.Program{
		Inputs:     inputs,
		Tracks:     tracks,
		ClockInput: media.PrimaryInputID,
		EndPolicy:  end,
	})
	if err != nil {
		t.Fatalf("normalizing the fixture: %v", err)
	}
	policies := make(map[media.InputID]read.Policy, len(program.Inputs))
	for _, input := range program.Inputs {
		policies[input.ID] = policy
	}
	source, err := ffmpeg.NewProgramSource(program, policies)
	if err != nil {
		t.Fatalf("binding the fixture's read policy: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	var fromSpool string
	opts := ffmpeg.EncodeOptions{Format: format}
	ceiling := read.Ceiling(format.Delivery == container.DeliverSegmented, false)
	if out.spooled {
		opts.Input = ffmpeg.FromPipe(ffmpeg.SpoolFormat, ceiling)
		fromSpool, opts.Probe = spool(t, ctx, tl, program, source)
	} else {
		encoding, err := ffmpeg.NewProgramSource(program, read.Plan(policies).Encoding(program, ceiling))
		if err != nil {
			t.Fatalf("binding the encode's read policy: %v", err)
		}
		opts.Input = ffmpeg.FromSource(encoding)
		probe, err := probeSource(ctx, tl, program, source)
		if err != nil {
			t.Fatalf("probing the input: %v", err)
		}
		opts.Probe = probe
	}

	decided, err := plan.PlanMedia(ctx, plan.Inputs{
		Caps:      decodes,
		Probe:     opts.Probe,
		Into:      format,
		MaxHeight: 1080,
		Encoders:  ffmpeg.Encoders(tl.ffmpeg),
	})
	if err != nil {
		t.Fatalf("planning media: %v", err)
	}
	opts.Video, opts.Audio = decided.Video, decided.Audio
	t.Logf("input %s/%dch, producing %s (video=%s audio=%s)",
		opts.Probe.VideoCodec, opts.Probe.AudioChannels, out.contentType,
		opts.Video.Name(), opts.Audio.Name())

	return run(t, ctx, tl, opts, format, fromSpool)
}

func spool(t *testing.T, ctx context.Context, tl tools, program media.Program, source ffmpeg.ProgramSource) (string, media.ProbeInfo) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "spool.ts")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	pullCtx, stop := context.WithTimeout(ctx, produceFor)
	defer stop()

	sourceProbe, err := probeSource(ctx, tl, program, source)
	if err != nil {
		t.Logf("the source could not be measured (%v); nothing is known against its packets, so both halves are copied", err)
	}
	floor, err := plan.Floor(ctx, plan.Inputs{
		Probe:     sourceProbe,
		Into:      ffmpeg.SpoolFormat,
		MaxHeight: 1080,
		Encoders:  ffmpeg.Encoders(tl.ffmpeg),
	})
	if err != nil {
		t.Fatalf("planning the read's floor: %v", err)
	}
	if produced := floor.Encoded(); produced.Any() {
		t.Logf("the spool cannot carry this input as-is; producing %s (video=%s audio=%s)",
			produced, floor.Video.Name(), floor.Audio.Name())
	}

	var last media.Progress
	pull, err := ffmpeg.PullArgs(ffmpeg.PullOptions{Source: source, Probe: sourceProbe, Video: floor.Video, Audio: floor.Audio})
	if err != nil {
		t.Fatalf("building the pull: %v", err)
	}
	proc, err := ffmpeg.Start(pullCtx, tl.ffmpeg, pull,
		ffmpeg.WithProgress(func(s media.Progress) { last = s }))
	if err != nil {
		t.Fatalf("starting the pull: %v", err)
	}

	if _, err := io.Copy(out, proc.Stdout); err != nil && pullCtx.Err() == nil {
		t.Fatalf("spooling: %v", err)
	}
	// Reaping joins the drain, so ffmpeg's last block is in hand when this returns.
	_ = proc.Wait()
	t.Logf("the pull delivered %s of media at %vx", last.Position.Round(time.Millisecond), last.Speed)

	if fileSize(path) == 0 {
		t.Fatal("the pull spooled nothing")
	}
	info, _, err := probe.FFprobe(tl.ffprobe).File(path).Probe(ctx)
	if err != nil {
		t.Fatalf("probing the spool: %v", err)
	}
	return path, info
}

func probeSource(ctx context.Context, tl tools, program media.Program, source ffmpeg.ProgramSource) (media.ProbeInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, probeWithin)
	defer cancel()
	info, _, err := probe.FFprobe(tl.ffprobe).Source(program, source.ProbeInputs()).Probe(ctx)
	return info, err
}

// run executes castor's argument list with a real ffmpeg and collects what it produced.
func run(t *testing.T, ctx context.Context, tl tools, opts ffmpeg.EncodeOptions, format container.FormatInfo, fromSpool string) result {
	t.Helper()

	encode, err := ffmpeg.EncodeArgs(opts)
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	args := encode.Args
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
	if format.Delivery == container.DeliverSegmented {
		produced = filepath.Join(workDir, container.HLSPlaylistName)
	} else {
		out, err := os.Create(produced)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		cmd.Stdout = out
	}

	_ = cmd.Run()

	// The parent's write end is the last handle holding the pipe open, so closing it is what ends the feed.
	_ = progressW.Close()
	<-drained
	t.Logf("the encode produced %s of media at %vx", last.Position.Round(time.Millisecond), last.Speed)

	if format.Delivery == container.DeliverSegmented {
		closePlaylist(t, produced)
	}
	if fileSize(produced) == 0 {
		t.Fatalf("the encoder produced no %s at %s\n%s", format.ContentType, produced, stderr.String())
	}
	return result{t: t, ffprobe: tl.ffprobe, path: produced, stderr: stderr.String()}
}

// closePlaylist marks the produced window as ended if the encoder did not.
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

// result is what castor produced.
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
func (r result) probe(args ...string) int {
	r.t.Helper()
	full := append([]string{"-v", "error"}, media.AdaptiveInputArgs(media.HLS, 0)...)
	full = append(append(full, args...), "-of", "csv=p=0", r.path)

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
	// Nothing, or "N/A", is ffprobe's answer for a stream it cannot read at all, which is a real zero.
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
