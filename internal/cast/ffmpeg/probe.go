package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/stupside/castor/internal/media"
)

// probeStderrTail bounds how much of ffprobe's complaint is kept. A probe of a dead
// playlist prints three warning lines per segment and a feature title has thousands
// of them, all saying the same thing; the last few dozen lines carry every distinct
// fact in the whole stream, and the alternative is holding megabytes of repetition to
// print one of them.
const probeStderrTail = 8 << 10

// tailWriter keeps the last probeStderrTail bytes written to it and discards the rest
// as they are overtaken.
type tailWriter struct{ buf []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if excess := len(w.buf) - probeStderrTail; excess > 0 {
		w.buf = append(w.buf[:0], w.buf[excess:]...)
	}
	return len(p), nil
}

// evidence renders what ffprobe said as a suffix for the error, or nothing when it
// said nothing. An empty tail is itself a finding: ffprobe was reached, produced no
// complaint, and still did not answer.
func (w *tailWriter) evidence() string {
	if said := strings.TrimSpace(string(w.buf)); said != "" {
		return "\n" + said
	}
	return ""
}

// FileProbe binds a local path to the ffprobe that measures it. It is safe to point at a
// still-growing spool: ffprobe reads from the start, analyses the leading packets, and
// returns.
func FileProbe(ffprobePath, path string) FileProber {
	return FileProber{ffprobePath: ffprobePath, path: path}
}

// FileProber is a measurement of a local file, held as a value so the party that owns the
// budget and the "nothing is known against this subject" rule can drive it without knowing
// that a subprocess is involved (see core.Prober).
type FileProber struct{ ffprobePath, path string }

func (p FileProber) Probe(ctx context.Context) (media.ProbeInfo, error) {
	return probe(ctx, p.ffprobePath, p.path, nil)
}

// SourceProbe binds an upstream to the ffprobe that measures it. It opens the source
// exactly as the reader that follows it will: the same request headers, the same
// container-specific input flags, and the same read policy (its mid-read deadline and its
// reconnect terms, see NetworkSource.Read), so it cannot fail for a reason the read would
// not also meet. That matters for a playlist whose segments are served under a disguised
// extension, where without the relaxed extension checks the probe reports an unreadable
// stream while the remux plays it fine, and it matters just as much for the terms: a probe
// that gave up on the first 429 while the reader would have waited it out convicted a link
// the cast could have read.
//
// ONE reason is castor's own and is not the read's: the caller's deadline is deliberately
// shorter than the patience the read is granted (see core.probeBudget against
// read.BackoffMax), so a source that only answers slowly is measured as nothing rather than
// as dead. That is why the failure below names the deadline as castor's, and why nothing
// downstream may read a failed measurement as a verdict on the link.
func SourceProbe(ffprobePath string, src NetworkSource) SourceProber {
	return SourceProber{ffprobePath: ffprobePath, src: src}
}

// SourceProber is a measurement of one upstream program, held as a value for the same
// reason FileProber is.
//
// The deadline is the CALLER'S, and there has to be one: ffmpeg's HLS demuxer answers a
// playlist whose segments all 403 by walking the whole playlist, skipping each segment
// after it has "failed too many times", which for a feature title is thousands of round
// trips producing no output and no exit (199 seconds on a real one, printing nothing at
// all). The read policy's segment retry budget multiplies those round trips, since each
// skip now costs every re-fetch the reader was granted, which changes nothing about the
// bound: this deadline is what ends that walk, and it ended it before the budget existed.
// It is owned by whoever also owns what a failed measurement means, so the two cannot
// disagree, and it bounds the whole program rather than each rendition, so a demuxed
// program cannot take twice as long as the caller allowed.
type SourceProber struct {
	ffprobePath string
	src         NetworkSource
}

// Probe measures a demuxed program as a program rather than as a URL: its audio lives in
// the companion rendition, so probing only the video one would report a silent source and
// cost it its stream copy.
//
// When the audio rendition cannot be probed, the returned ProbeInfo's audio half is ZEROED
// rather than left holding the video rendition's own audio. That is not tidiness. The
// encode maps 1:a:0 on this shape, so the video rendition's audio track is not the track
// being encoded, and a decision taken from it is a decision about the wrong stream. Before
// the copy adaptations existed that only mis-chose copy-vs-encode; now it also chooses a
// bitstream filter, and both wrong answers are severe: the wrong codec is exit 234 at
// filter init with the output never opened, and the wrong direction is exit 0 with
// destroyed audio. A zero audio half matches no adaptation and reads as "re-encode" to the
// resolver, which is the honest answer to "castor could not measure this track".
func (p SourceProber) Probe(ctx context.Context) (media.ProbeInfo, error) {
	ffprobePath, src := p.ffprobePath, p.src

	inputArgs := readArgs(src.Read)
	inputArgs = append(inputArgs, media.HeaderArgs(src.Headers)...)
	inputArgs = append(inputArgs, containerInputArgs(src.ContentType, src.Read)...)

	info, err := probe(ctx, ffprobePath, src.URL.String(), inputArgs)
	if err != nil || src.AudioURL == nil {
		return info, err
	}

	audio, err := probe(ctx, ffprobePath, src.AudioURL.String(), inputArgs)
	if err != nil {
		// The video half still decides the video axis. The audio half is cleared
		// because it describes input 0 while the encode maps input 1.
		info.AudioCodec, info.AudioChannels = "", 0
		return info, fmt.Errorf("probing audio rendition: %w", err)
	}
	info.AudioCodec, info.AudioChannels = audio.AudioCodec, audio.AudioChannels
	return info, nil
}

// probe runs ffprobe against input and hands its JSON to the one decoder both layers that
// measure read (media.DecodeProbe). inputArgs are the flags an input needs to be opened at
// all (headers, container leniency); what is asked for and what it means are media's, so a
// copy decision here and a candidate's admission there cannot disagree about what a video
// track is.
func probe(ctx context.Context, ffprobePath, input string, inputArgs []string) (media.ProbeInfo, error) {
	args := []string{
		// Warning, not error, for the same reason the puller runs at warning: the lines
		// that describe a dead playlist are warnings, so at -v error a probe walking one
		// prints nothing at all. Measured against a playlist whose every segment 404s,
		// -v error yields two lines that name no status ("Error when loading first
		// segment", "Invalid data found when processing input") while -v warning yields
		// "HTTP error 404 File not found", "Failed to open segment 0 of playlist 0" and
		// "Segment 0 of playlist 0 failed too many times, skipping". A probe castor kills
		// at its own deadline is killed in the middle of exactly that walk, so at error
		// level the one path with a diagnosis to offer offers nothing. The JSON is on
		// stdout, so nothing here reaches the parser.
		"-v", "warning",
		"-print_format", "json",
		"-show_entries", media.ProbeEntries,
	}
	args = append(args, inputArgs...)
	args = append(args, input)

	// Stderr is captured rather than left to Output's ExitError, because the failure
	// worth explaining is the one where there IS no exit status: ctx expiring kills
	// ffprobe, and the evidence for why it deserved to be killed is whatever it had
	// printed by then. It is kept as a bounded tail for the same reason the process
	// runner keeps one: the walk above prints three lines per segment, and a feature
	// title has thousands of them.
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	var stdout bytes.Buffer
	tail := &tailWriter{}
	cmd.Stdout = &stdout
	cmd.Stderr = tail
	if err := cmd.Run(); err != nil {
		// The deadline is named as castor's own. It is the one probe failure that
		// establishes nothing about the source (see the 199 second walk above), and
		// reporting it as "signal: killed" invites the reader to blame ffprobe for
		// obeying.
		if ctx.Err() != nil {
			return media.ProbeInfo{}, fmt.Errorf("ffprobe hit castor's probe deadline: %w%s", ctx.Err(), tail.evidence())
		}
		return media.ProbeInfo{}, fmt.Errorf("ffprobe: %w%s", err, tail.evidence())
	}
	return media.DecodeProbe(stdout.Bytes())
}
