package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// The join that makes an Outcome, over hand-built landings: no ffmpeg, no fixtures and no
// renderer, which is the point of the outcome being a value the legs report rather than an
// error one of them returns.

// TestTheReadsOwnErrorIsWhatTheCastReports is the mislabelling this layer exists to end.
// The reader's exit status travelled out through the spool's write side, the encoder's
// stdin and the delivery's teardown, gathering a prefix at each, and reached a user as
// "encoder: spool producer failed: upstream pull: exit status 183": the encoder was the
// only party named and the only one that had done nothing wrong.
func TestTheReadsOwnErrorIsWhatTheCastReports(t *testing.T) {
	readErr := errors.New("upstream pull: exit status 183")
	noticed := fmt.Errorf("encoder: spool producer failed: %w", readErr)

	out := landing{reached: attempt.PhaseReading, err: noticed, readErr: readErr}.outcome(t.Context())

	if out.Err.Error() != readErr.Error() {
		t.Errorf("the cast reports %q, want the read's own %q: the encoder merely noticed", out.Err, readErr)
	}
	if !errors.Is(out.Err, readErr) {
		t.Error("the read's error is no longer findable in the cast's result")
	}
	if out.Evidence.ReadErr != readErr {
		t.Error("the evidence does not carry the read's own terminal error, so no rule can be keyed on it")
	}
}

// TestADeliveryWithSomethingOfItsOwnToSayIsHeardToo covers the other arm: an encoder that
// exited on its own account, or printed a line meaning its output is not playable, is not
// merely repeating the read's failure, and dropping it is how a cast came to exit 0 having
// cast nothing.
func TestADeliveryWithSomethingOfItsOwnToSayIsHeardToo(t *testing.T) {
	readErr := errors.New("upstream pull: exit status 183")
	ownFault := errors.New("encoder: an audio repack was applied toward a container that frames its streams in band")

	out := landing{reached: attempt.PhaseReading, err: ownFault, readErr: readErr}.outcome(t.Context())

	for _, want := range []error{readErr, ownFault} {
		if !errors.Is(out.Err, want) {
			t.Errorf("the cast's result %q loses %q", out.Err, want)
		}
	}
	if !strings.HasPrefix(out.Err.Error(), readErr.Error()) {
		t.Errorf("the cast's result %q does not lead with the read that failed first", out.Err)
	}
}

// TestAVerdictKeepsItsOwnAccount pins that attribution stops where a judgement has already
// made one: a watch fault names the party it blames and carries that party's error inside
// it, so replacing it with the reader's bare error would trade a sentence a user can act on
// ("the source read reached a terminal error before playback could start", with the
// measurements) for an exit status.
func TestAVerdictKeepsItsOwnAccount(t *testing.T) {
	readErr := errors.New("upstream pull: exit status 183")
	verdict := &watch.Fault{
		Kind:    watch.Dead,
		Why:     "the source read reached a terminal error before playback could start",
		Window:  watch.BeforePlay,
		Subject: "playback gate",
		Health:  watch.Health{Landed: 33088, Speed: 0.159, Headroom: 2, Samples: 4},
		Err:     readErr,
	}

	out := landing{reached: attempt.PhaseReading, err: verdict, readErr: readErr}.outcome(t.Context())

	var got *watch.Fault
	if !errors.As(out.Err, &got) {
		t.Fatalf("the cast reports %q, which is no longer the verdict that ended it", out.Err)
	}
	if !errors.Is(out.Err, readErr) {
		t.Error("the verdict no longer unwraps to the read's own error")
	}
	if out.Evidence.Verdict != watch.Dead || out.Evidence.Health.Speed != 0.159 {
		t.Errorf("evidence = %+v, want the verdict and the measurements it was reached on", out.Evidence)
	}
}

// TestHowFarACastGotIsTheLaterOfWhatEachPartySaw is what makes the recovery gate honest. The
// leg reports the stages it drove and the Play the delivery told it about; a verdict reports
// the window it was reached in, which is the only account there is of an artifact gate inside
// the delivery driver. Taking the leg's answer alone would walk a cast reached past the read
// back to it, and the loop would offer to start it over.
func TestHowFarACastGotIsTheLaterOfWhatEachPartySaw(t *testing.T) {
	cases := []struct {
		name string
		leg  attempt.Phase
		win  watch.Window
		want attempt.Phase
	}{
		{"a verdict on a renderer that holds a URL outranks a leg that only knows it was reading",
			attempt.PhaseReading, watch.Playing, attempt.PhasePlaying},
		{"a verdict on the artifact a renderer will be pointed at is past the read",
			attempt.PhaseReading, watch.Opening, attempt.PhaseOpening},
		{"a verdict before playback does not walk a delivered cast backwards",
			attempt.PhaseDelivered, watch.BeforePlay, attempt.PhaseDelivered},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			l := landing{reached: tt.leg, err: &watch.Fault{Kind: watch.Stalled, Window: tt.win}}
			if got := l.outcome(t.Context()).Reached(); got != tt.want {
				t.Errorf("reached %s, want %s", got, tt.want)
			}
		})
	}
}

// TestACancelledCastIsReadFromTheContext keeps that fact off the error text. Everything
// castor kills reports a broken pipe, an exit status or a severed client, each worded by
// whichever library got there first, and a cast that recognised its own cancellation by
// matching prose would answer Ctrl+C by classifying a stall.
func TestACancelledCastIsReadFromTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	l := landing{reached: attempt.PhaseReading, err: errors.New("signal: killed"), readErr: errors.New("signal: killed")}
	if !l.outcome(ctx).Evidence.Cancelled {
		t.Error("a cast killed by its own context does not report itself cancelled")
	}
	if l.outcome(t.Context()).Evidence.Cancelled {
		t.Error("a cast whose context is live reports itself cancelled on the strength of an error's wording")
	}
}

// TestARendererThatRefusedTheURLIsReportedAsSuch covers the one leg that asks the renderer
// to play with no delivery driver in between: castor read nothing and produced nothing
// there, so the refusal is the renderer's own and has to travel as a fact rather than as
// a sentence somebody has to parse.
func TestARendererThatRefusedTheURLIsReportedAsSuch(t *testing.T) {
	refused := errors.New("SOAP SetAVTransportURI: 714")
	out := landing{playErr: refused, err: fmt.Errorf("starting playback: %w", refused)}.outcome(t.Context())

	if out.Evidence.PlayErr != refused {
		t.Errorf("evidence carries PlayErr %v, want the renderer's own refusal", out.Evidence.PlayErr)
	}
	if out.Reached() != attempt.PhaseUnstarted {
		t.Errorf("reached %s, want %s: nothing was read and nothing was produced", out.Reached(), attempt.PhaseUnstarted)
	}
}

// TestTheExecutorBlamesARendererThatWouldNotPlay is the WIRING behind the fact above: the
// pass-through leg is the one that asks the renderer to play with no delivery driver in
// between, so it is the only party that can report a refusal, and it drives no ffmpeg at
// all. Without this the renderer's own refusal reaches the classifier as an unrecognised
// failure and no recovery can ever be aimed at it.
func TestTheExecutorBlamesARendererThatWouldNotPlay(t *testing.T) {
	refused := errors.New("SOAP SetAVTransportURI: 714")
	// A renderer that fetches for itself and accepts the source container, over a source
	// that needs nothing but its URL: the one shape that reaches Play without an encoder.
	dev := &fakeDevice{caps: chromecastLike(media.MP4), refuse: refused}
	source := &media.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie.mp4"}, ContentType: media.MP4}

	cfg := castConfig(device.TypeChromecast, "", "")
	out := NewExecutor(cfg, connectTo(dev), "127.0.0.1").Run(t.Context(), attempt.Attempt{Try: 1, Source: source})

	if out.Err == nil {
		t.Fatal("the cast reported success though the renderer refused the URL")
	}
	if !errors.Is(out.Evidence.PlayErr, refused) {
		t.Errorf("evidence carries PlayErr %v, want the renderer's own refusal %v", out.Evidence.PlayErr, refused)
	}
}

// TestADeliveryNobodyTookTheStreamFromReachesTheEvidence is the last link of the chain that
// keeps "exited 0 having cast nothing" from being reported as a delivered cast. The statement
// is the DELIVERY's, made after its sink's Wait had already ended cleanly, so it arrives on the
// cast's error and nowhere else; read off it here, a classification can be keyed on it
// structurally instead of on what the message happens to say.
func TestADeliveryNobodyTookTheStreamFromReachesTheEvidence(t *testing.T) {
	short := &core.Undelivered{Handed: 2 * time.Minute, Produced: 2 * time.Hour}
	l := landing{reached: attempt.PhasePlaying, err: fmt.Errorf("delivering the stream: %w", short)}

	out := l.outcome(t.Context())

	if out.Err == nil {
		t.Fatal("a cast whose renderer took two minutes of a two hour film reported success")
	}
	if out.Evidence.Undelivered == nil {
		t.Fatal("the evidence does not carry the delivery's own account, so no rule can be keyed on it and the failure is classified as one nobody recognises")
	}
	if out.Reached() != attempt.PhasePlaying {
		t.Errorf("reached %s, want %s: a renderer held the URL, so nothing may be started over", out.Reached(), attempt.PhasePlaying)
	}
}

// TestADeliveredCastReportsNothingAgainstItself is the ordinary path, and the one shape a
// join must never invent a failure for.
func TestADeliveredCastReportsNothingAgainstItself(t *testing.T) {
	out := landing{reached: attempt.PhaseDelivered}.outcome(t.Context())
	if out.Err != nil {
		t.Errorf("a delivered cast reports %v", out.Err)
	}
	if out.Evidence.Verdict != watch.Starting {
		t.Errorf("a delivered cast carries verdict %s, want the one that means nothing was judged", out.Evidence.Verdict)
	}
}

// TestTheReadsExitStatusAndCopiesReachTheEvidence is what makes a broken copy nameable at
// all. Neither term is inferable from the error: "upstream pull: exit status 183" is a
// string a rule may not read, and nothing about the source's codecs says whether this read
// was passing them through or producing them.
func TestTheReadsExitStatusAndCopiesReachTheEvidence(t *testing.T) {
	readErr := errors.New("upstream pull: exit status 183")
	l := landing{
		reached:    attempt.PhaseReading,
		err:        fmt.Errorf("encoder: spool producer failed: %w", readErr),
		readErr:    readErr,
		readExit:   183,
		readCopied: carriage.Axes{Video: true, Audio: true},
	}

	out := l.outcome(t.Context())
	if out.Evidence.ReadExit != 183 {
		t.Errorf("evidence exit status = %d, want the reader's own 183: a killed reader has none and must not be confused with this",
			out.Evidence.ReadExit)
	}
	if got := out.Evidence.Copied; !got.Video || !got.Audio {
		t.Errorf("evidence copied = %s, want both halves: only a copied axis can be blamed for a bitstream it was handed", got)
	}
}

// TestABufferReEncodesWhatTheContainerRefusesAndWhatAlreadyBroke covers the union the read
// is told to produce, which arrives from two facts with nothing in common.
//
// What the buffer's container will not carry is a property of the codecs, decided from a
// measurement before anything runs. What a previous attempt's copy broke on cannot be
// measured at all: the same packets copy cleanly when they arrive whole, and only having
// watched a reader exit on them is evidence. A leg that read one and not the other would
// either lose a track for the whole title or repeat the exit that killed the last attempt.
func TestABufferReEncodesWhatTheContainerRefusesAndWhatAlreadyBroke(t *testing.T) {
	// VP9 is written into MPEG-TS as unreadable private data at exit 0, so the carriage
	// table refuses the video copy on the codec alone.
	refused := core.Facts{Measured: true, Probe: media.ProbeInfo{VideoCodec: media.CodecVP9, AudioCodec: media.CodecAAC}}
	carriable := core.Facts{Measured: true, Probe: media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC}}

	if got := bufferCarriage(t.Context(), carriable, carriage.Axes{}); got.Any() {
		t.Errorf("a carriable source with nothing against it re-encodes %s, want a plain copy", got)
	}
	if got := bufferCarriage(t.Context(), refused, carriage.Axes{}); !got.Video || got.Audio {
		t.Errorf("re-encoding %s, want the video alone: MPEG-TS has no stream type for VP9 and carries the AAC as it is", got)
	}
	if got := bufferCarriage(t.Context(), carriable, carriage.Axes{Audio: true}); got.Video || !got.Audio {
		t.Errorf("re-encoding %s, want the audio alone: nothing is known against the video and the audio already killed a reader", got)
	}
	if got := bufferCarriage(t.Context(), refused, carriage.Axes{Audio: true}); !got.Video || !got.Audio {
		t.Errorf("re-encoding %s, want both: one half the container refuses, the other a copy already broke on", got)
	}
	// A source castor could not measure is a source with nothing known against it, which is
	// the same answer an unmeasured codec gets from the carriage table itself. The attempt's
	// own evidence still stands: it was established by a reader, not by a probe.
	if got := bufferCarriage(t.Context(), core.Facts{}, carriage.Axes{Video: true}); !got.Video || got.Audio {
		t.Errorf("an unmeasured source re-encodes %s, want the axis the last reader died copying and nothing else", got)
	}
}
