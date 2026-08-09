package watch

import (
	"slices"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// TestVerdicts pins every rule in the table over Health values built by hand. Each row
// is a state a cast really reaches, and most of them were unreachable from a test at
// all: they lived inside poll loops that needed a running ffmpeg, a spool, a whisper
// model or a renderer on the network.
func TestVerdicts(t *testing.T) {
	tests := []struct {
		name   string
		window Window
		health Health
		want   Kind
	}{{
		name:   "any landed byte opens a subtitle-less cast",
		window: BeforePlay,
		health: Health{Landed: 1},
		want:   Ready,
	}, {
		// A source with no readable media at all: the read settled without producing
		// bytes, and there is nothing left to wait for. The caller reads the read's own
		// error immediately after.
		name:   "a finished read opens the gate even empty",
		window: BeforePlay,
		health: Health{Ended: true},
		want:   Ready,
	}, {
		name:   "an empty buffer from a live read waits",
		window: BeforePlay,
		health: Health{},
		want:   Starting,
	}, {
		// The "subs never show" bug as a rule: the burn-in encoder must never start ahead
		// of the transcription's committed frontier, so bytes alone do not open this gate.
		name:   "a burn-in waits for the transcription lead",
		window: BeforePlay,
		health: Health{Landed: 4 << 20, Subtitles: true, Lead: transcriptionLeadSeconds - 1},
		want:   Starting,
	}, {
		name:   "the transcription lead opens the gate",
		window: BeforePlay,
		health: Health{Landed: 1, Subtitles: true, Lead: transcriptionLeadSeconds},
		want:   Ready,
	}, {
		// A source shorter than the lead never builds one, so a finished transcription is
		// the other way past this rule.
		name:   "a finished transcription opens the gate with no lead",
		window: BeforePlay,
		health: Health{Landed: 1, Subtitles: true, LeadDone: true},
		want:   Ready,
	}, {
		// A read that dies instantly flips the transcription's Done (its PCM hits EOF)
		// before the read's own error lands. Opening here would cast an empty buffer.
		name:   "a finished transcription over an empty buffer does not open",
		window: BeforePlay,
		health: Health{Subtitles: true, LeadDone: true},
		want:   Starting,
	}, {
		name:   "silence past the stall window is a stall",
		window: BeforePlay,
		health: Health{SinceGrowth: StallWindow + time.Second},
		want:   Stalled,
	}, {
		name:   "silence within the stall window is not",
		window: BeforePlay,
		health: Health{SinceGrowth: StallWindow - time.Second},
		want:   Starting,
	}, {
		// A completed read's buffer never grows again, and reporting that as a stall would
		// fail every short source.
		name:   "a finished read never stalls",
		window: BeforePlay,
		health: Health{Ended: true, SinceGrowth: 10 * StallWindow},
		want:   Ready,
	}, {
		name:   "a read that failed before playback is dead",
		window: BeforePlay,
		health: Health{Landed: 1 << 20, Ended: true, Failed: true},
		want:   Dead,
	}, {
		// The observed failure, as its numbers: 33 KB and one second of media in thirty
		// seconds against a reader allowed twice realtime, and the deficit continuous from
		// the first sample for longer than the backoff ceiling the reader was handed.
		name:   "a read delivering 0.0627x against an allowed 2x is undeliverable",
		window: BeforePlay,
		health: Health{Landed: 33088, Position: time.Second, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples, SinceDeficit: deficitWindow + time.Second},
		want:   Undeliverable,
	}, {
		// The other measured failures: 0.109x, 0.159x and 0.39x all sat on the same side
		// of the floor as each other and nowhere near the 2.100x a healthy cast reported.
		name:   "a read delivering 0.109x against an allowed 2x is undeliverable",
		window: BeforePlay,
		health: Health{Landed: 1 << 20, Speed: 0.109, Headroom: 2, Samples: minSpeedSamples, SinceDeficit: deficitWindow + time.Second},
		want:   Undeliverable,
	}, {
		// The blocker this arm shipped with: it convicted on a single sample while its
		// in-flight twin waited for the same deficit to persist. A whisper model loading on
		// the goroutine draining the PCM tee, or one 429 waited out inside the ceiling
		// ffmpeg was handed for it, is a dip and not a link that cannot carry the title, and
		// the refusal then walked and burned every other admitted link for it.
		name:   "a fresh deficit before playback is not yet acted on",
		window: BeforePlay,
		health: Health{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples, SinceDeficit: time.Second},
		want:   Starting,
	}, {
		// And that hold is a hold, not an opening: the deliverability question is answerable
		// only before a renderer holds a URL, because a revision is what rescues a starving
		// cast and no revision exists once someone is watching. Opening here on the 33 KB in
		// the buffer is the original failure, with the arm above made unreachable.
		name:   "a fresh deficit holds the gate rather than opening it",
		window: BeforePlay,
		health: Health{Landed: 8 << 20, Speed: 0.39, Headroom: 2, Samples: 100, SinceDeficit: 30 * time.Second},
		want:   Starting,
	}, {
		// A read whose pace was withheld answers no deliverability question at all, however
		// long its numbers stay under playback rate. It is the zero-value convention behind
		// Health.Headroom, and what a read teeing PCM to a transcription or producing a
		// track through castor's own encoder is given (see pipeline's pull.judgedPace):
		// the pace was an allowance on the link, and the link is not what is slow.
		name:   "a read whose pace was withheld is never undeliverable",
		window: BeforePlay,
		health: Health{Landed: 1 << 20, Speed: 0.0627, Headroom: 0, Samples: 100, SinceDeficit: 10 * StallWindow},
		want:   Ready,
	}, {
		// A live edge is paced at exactly 1.0, so it cannot be outrun and 0.98 says
		// nothing about the link. Convicting here would fail every live cast castor makes.
		name:   "a live edge delivering 0.98x is not undeliverable",
		window: BeforePlay,
		health: Health{Landed: 1 << 20, Speed: 0.98, Headroom: 1, Samples: 100},
		want:   Ready,
	}, {
		// The healthy calibration: a real cast measured 2.100 against an allowed 2.0,
		// which is the reader running AHEAD of its pace and throttling itself.
		name:   "a read delivering 2.1x against an allowed 2x is ready",
		window: BeforePlay,
		health: Health{Landed: 1 << 20, Speed: 2.1, Headroom: 2, Samples: minSpeedSamples},
		want:   Ready,
	}, {
		// The deliberate cost of the hold: a cast with bytes in the buffer waits until the
		// reader has stated a speed enough times to be judged on it. One slow first block
		// cannot convict a link, and it cannot open a gate either.
		name:   "the gate holds until the read has stated a speed enough times",
		window: BeforePlay,
		health: Health{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples - 1},
		want:   Starting,
	}, {
		// The same hold in literal counts, because every row above says minSpeedSamples and
		// would keep passing if the confidence window were one block. ffmpeg's speed is
		// cumulative from process start, so a single block carries the whole connection cost
		// (a healthy cast logged a 2.473s lag before its first packet) in its denominator: at
		// one sample 0.4x is arithmetic about a TLS handshake and says nothing about a link.
		name:   "one block of stated speed convicts nothing",
		window: BeforePlay,
		health: Health{Landed: 1 << 20, Speed: 0.4, Headroom: 2, Samples: 1, SinceDeficit: 10 * StallWindow},
		want:   Starting,
	}, {
		// And the observed failure's own first two blocks, which are the same shape: a run
		// that really was starving is convicted on its sustained figures further down this
		// table, not on the two that are still mostly startup.
		name:   "the observed run's first two blocks convict nothing",
		window: BeforePlay,
		health: Health{Landed: 33088, Speed: 0.159, Headroom: 2, Samples: 2, SinceDeficit: 10 * StallWindow},
		want:   Starting,
	}, {
		// A whisper cast reads PCM off the same 2x read and reports the same 2x speed, so
		// the clause that convicts a starving link passes a burn-in untouched.
		name:   "a burn-in over a healthy 2x read opens once it leads",
		window: BeforePlay,
		health: Health{Landed: 4 << 20, Speed: 2, Headroom: 2, Samples: minSpeedSamples, Subtitles: true, Lead: transcriptionLeadSeconds},
		want:   Ready,
	}, {
		// A read that has finished delivered the whole program; its average rate is a fact
		// about the past and not a prediction, and a source shorter than the confidence
		// window would otherwise be convicted by the arithmetic of its own startup cost.
		name:   "a finished read is never undeliverable",
		window: BeforePlay,
		health: Health{Landed: 1 << 20, Speed: 0.3, Headroom: 2, Samples: minSpeedSamples, Ended: true},
		want:   Ready,
	}, {
		name:   "the artifact appearing opens a delivery",
		window: Opening,
		health: Health{Landed: 1},
		want:   Ready,
	}, {
		name:   "a delivery whose artifact has not appeared waits",
		window: Opening,
		health: Health{},
		want:   Starting,
	}, {
		// A slow upstream can legitimately take this long to yield a first byte, and the
		// delivery is better off proceeding and letting the renderer's own buffering wait.
		name:   "a delivery past its own patience proceeds without the artifact",
		window: Opening,
		health: Health{Overdue: true},
		want:   Ready,
	}, {
		// If nothing came out of the producer, nothing ever will: an ADTS AAC copy into the
		// mp4 muxer exits 255 having written audio:0KiB, and pointing a renderer at that is
		// the "exited 0 having cast nothing" shape.
		name:   "a producer that ended with no artifact is dead",
		window: Opening,
		health: Health{Ended: true},
		want:   Dead,
	}, {
		// The producer may finalise the artifact on its way out, so the two facts are read
		// at the same instant rather than the exit being taken as the answer.
		name:   "a producer that ended having written the artifact is ready",
		window: Opening,
		health: Health{Ended: true, Landed: 64},
		want:   Ready,
	}, {
		name:   "a playing cast with nothing against it is healthy",
		window: Playing,
		health: Health{Landed: 1 << 20, Handed: 1 << 20, Speed: 2, Headroom: 2, Samples: 100},
		want:   Healthy,
	}, {
		// The renderer accepted Play and was handed no byte of what was made for it. Every other
		// fact about this cast looks like a delivered one, which is why it was reported as
		// success. A renderer that ASKED and took nothing is this same reading, which is the
		// whole of why the row measures what got through instead of counting requests: it used
		// to be a different reading, and the different one was answered Healthy.
		name:   "a renderer handed no byte of the program is unfetched",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 0, SinceFetch: fetchWindow + time.Second},
		want:   Unfetched,
	}, {
		name:   "a renderer given less than the fetch window is not yet unfetched",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 0, SinceFetch: fetchWindow - time.Second},
		want:   Healthy,
	}, {
		// A renderer that fetched and then went quiet is NOT judged here, however long the
		// silence and whatever it has in hand. A viewer who paused, a viewer who walked away and
		// a renderer that crashed all produce exactly this evidence, so a verdict over it states
		// something castor cannot know; the delivery's write deadline is what answers a quiet
		// renderer, and whether it ever took what was made for it is arithmetic done once the
		// cast has ended (see core.Undelivered).
		name:   "a renderer that fetched and stopped is not a verdict",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 4 << 20, SinceFetch: StallWindow + time.Second},
		want:   Healthy,
	}, {
		// The same silence with media still in hand, which is a paused viewer to the byte. Twenty
		// minutes are fetchable and four of them can have been played: ending the cast here took
		// the film away at two and a half minutes of somebody standing up.
		name:   "a renderer that stopped fetching with media still in hand is not a verdict",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 4 << 20, SinceFetch: StallWindow + time.Second, Delivered: 20 * time.Minute, SincePlay: 4 * time.Minute},
		want:   Healthy,
	}, {
		// And the same silence with the buffer apparently played out, which is the reading a
		// verdict used to fire on. It cannot happen on the one supervised composition (the
		// encoder fills the delivery from the spool for the rest of the title, so this figure
		// only grows), and where it can be constructed it still describes a viewer who might sit
		// back down.
		name:   "a renderer that stopped fetching having played everything out is not a verdict either",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 4 << 20, SinceFetch: StallWindow + time.Second, Delivered: 4 * time.Minute, SincePlay: 5 * time.Minute},
		want:   Healthy,
	}, {
		// A renderer that never came for the bytes at all is convicted however much is waiting
		// for it, and this row is why the buffer is not a term of that rule: there is no viewer
		// to protect, the whole title is in hand, and nobody has ever fetched a byte of it.
		name:   "a renderer that never fetched is unfetched however much is buffered",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 0, SinceFetch: fetchWindow + time.Second, Delivered: 30 * time.Minute},
		want:   Unfetched,
	}, {
		// The producer's silence, over a renderer that still has media. The read is allowed
		// twice realtime, so at the one-hour mark it is typically half an hour ahead of the
		// viewer: an upstream that goes quiet there has cost the ending and not the film, and
		// tearing the encoder down on the spot took thirty watchable minutes away from somebody
		// who was watching them and then blamed an expired playlist for it.
		name:   "a producer that stopped while the renderer still has media is not stalled",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 4 << 20, SinceGrowth: StallWindow + time.Second, Delivered: 45 * time.Minute, SincePlay: 15 * time.Minute},
		want:   Healthy,
	}, {
		// Once that lead is played out the cast really is over, and it is named with the same
		// verdict and the same reasoning as before: the likeliest cause is a signed playlist
		// whose segments have expired.
		name:   "a producer that stopped once the renderer has played it all out is stalled",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 4 << 20, SinceGrowth: StallWindow + time.Second, Delivered: 15 * time.Minute, SincePlay: 16 * time.Minute},
		want:   Stalled,
	}, {
		// Once the producer is done the renderer is draining a finished buffer, and the
		// sink's own Wait is what ends that. Convicting here would fail the tail of every
		// completed cast.
		name:   "a quiet renderer over a finished producer is not a verdict",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 4 << 20, SinceFetch: StallWindow + time.Second, Ended: true},
		want:   Healthy,
	}, {
		// In flight the deficit must outlast two reconnect ceilings, for the same reason
		// the stall window does: a rate-limited CDN goes quiet for a full ceiling and then
		// lands its retry.
		name:   "a fresh in-flight deficit is not yet acted on",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 1 << 20, Speed: 0.3, Headroom: 2, Samples: 100, SinceDeficit: time.Second},
		want:   Healthy,
	}, {
		name:   "an in-flight deficit past two reconnect ceilings is undeliverable",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 1 << 20, Speed: 0.3, Headroom: 2, Samples: 100, SinceDeficit: StallWindow + time.Second},
		want:   Undeliverable,
	}, {
		// The withheld pace again, in the window where the verdict ends a cast someone is
		// watching instead of revising an attempt nobody is. A read castor itself throttles or
		// encodes is measured on its own work in both windows, because the pace is one fact
		// about the read rather than one per window (see pipeline's pull.judgedPace), so the
		// numbers that convict the link above acquit here however slow and however sustained.
		// The same read genuinely going quiet is still named, by the stall row above, which
		// reads no pace and blames no link.
		name:   "a read whose pace was withheld is never undeliverable in flight either",
		window: Playing,
		health: Health{Landed: 8 << 20, Handed: 1 << 20, Speed: 0.0627, Headroom: 0, Samples: 100, SinceDeficit: 10 * StallWindow},
		want:   Healthy,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, _, err := judge(tt.window, tt.health)
			if err != nil {
				t.Fatalf("judge: %v", err)
			}
			if rule.Kind != tt.want {
				t.Errorf("verdict = %s (rule %q), want %s", rule.Kind, rule.Name, tt.want)
			}
		})
	}
}

// all is every row a cast can be judged by: the ordered rows plus each window's total row.
// The guarantees in this package (a name, a citation of the wiring that reaches it, an
// action in every window it answers) are about all of them, and a total row held apart from
// the ordered slice is exactly the kind of row that stops being checked.
func all() []Rule { return append(slices.Clone(rules), starting, healthy) }

// TestEveryWindowEndsInATotalRow is the totality guarantee in the form the walk now relies
// on. judge answers with the window's total row whenever no ordered row claimed the state,
// so a window whose total row does not answer it, or a total row carrying a predicate that
// could decline, is a cast being judged by nothing at all. A cast about which nothing is
// known must resolve to Starting or Healthy in every window, because the alternative is a
// supervisor that refuses to judge.
func TestEveryWindowEndsInATotalRow(t *testing.T) {
	for _, w := range []Window{BeforePlay, Opening, Playing} {
		r := total(w)
		if r.When != nil {
			t.Errorf("the %s window's total row %q carries a predicate the walk never asks: a row that can decline belongs in rules, where the walk reads it", w, r.Name)
		}
		if !slices.Contains(r.Windows, w) {
			t.Errorf("the %s window is answered by %q, which does not claim to answer it", w, r.Name)
		}
		if _, _, err := judge(w, Health{}); err != nil {
			t.Errorf("a zero Health in the %s window: %v", w, err)
		}
	}
	for _, r := range rules {
		if r.When == nil {
			t.Errorf("ordered rule %q carries no predicate, so it answers every state and shadows every row below it", r.Name)
		}
	}
}

// TestEveryRuleHasAnActionInEveryWindowItAnswers pins the coupling between the two
// tables. A row given a window the action table was never told about is a verdict nobody
// decided anything about, which is a cast being watched by something with no idea what to
// do; it would surface only on the run that reached it.
func TestEveryRuleHasAnActionInEveryWindowItAnswers(t *testing.T) {
	for _, r := range all() {
		if len(r.Windows) == 0 {
			t.Errorf("rule %q answers no window, so it can never fire", r.Name)
		}
		for _, w := range r.Windows {
			if _, ok := actions[verdict{Kind: r.Kind, Window: w}]; !ok {
				t.Errorf("rule %q reaches %s in the %s window, which has no action", r.Name, r.Kind, w)
			}
		}
	}
}

// TestNoPlayingVerdictCanRevise is the safety property of the whole layer, and it is
// structural rather than remembered. A revision restarts the read from scratch on a fresh
// buffer, and nothing rewinds: doing that while a renderer holds a URL would replay a film
// from the beginning at minute forty, which is worse than a clear error.
func TestNoPlayingVerdictCanRevise(t *testing.T) {
	for v, act := range actions {
		if v.Window == Playing && act == revise {
			t.Errorf("a %s verdict in the playing window asks for a revision, which would restart a cast someone is watching", v.Kind)
		}
	}
}

// TestNoActionIsOfferedForAWindowNoRuleAnswers keeps the action table from accumulating
// entries for verdicts that cannot happen, which is how a table stops describing the
// program it is supposed to be.
func TestNoActionIsOfferedForAWindowNoRuleAnswers(t *testing.T) {
	reachable := map[verdict]bool{}
	for _, r := range all() {
		for _, w := range r.Windows {
			reachable[verdict{Kind: r.Kind, Window: w}] = true
		}
	}
	for v := range actions {
		if !reachable[v] {
			t.Errorf("the action table answers a %s verdict in the %s window, which no rule reaches", v.Kind, v.Window)
		}
	}
}

// TestNothingBoundsAFragileReadButThisRow is the obligation the read table took on when it
// stopped handing an fMP4 source a mid-read deadline. Without -rw_timeout, ffmpeg will wait
// on a socket that was accepted and then went quiet for as long as the peer keeps it open,
// which is forever on the tarpit shape, so the ONLY thing that ends such a read is a verdict
// reached here.
//
// It reads the policy out of the read table rather than asserting a duration, so the two
// halves of that trade cannot drift apart: re-arming the deadline breaks the premise below,
// and removing this row's window or making its buffer term bind before playback breaks the
// verdict. The pre-playback case is the one that has to be checked, because the in-flight arm
// deliberately waits for the renderer's buffer to be played out first and there is no such
// buffer before a renderer holds anything.
func TestNothingBoundsAFragileReadButThisRow(t *testing.T) {
	fragile := read.For(read.Shape{Segmented: true, Framing: media.FramingOutOfBand}, 30*time.Second)
	if fragile.Deadline != 0 {
		t.Fatalf("the %q read carries a %s mid-read deadline, so this test is measuring the wrong thing: ffmpeg bounds that read itself",
			fragile.Name, fragile.Deadline)
	}

	// The read as this layer sees it after the silence: still running, nothing landed, and
	// nobody yet pointed at anything (Delivered and SincePlay are zero in every window before
	// Play, which is what leaves the buffer term vacuous here).
	quiet := Health{SinceGrowth: StallWindow + time.Second}
	rule, act, err := judge(BeforePlay, quiet)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if rule.Kind != Stalled {
		t.Errorf("a %q read that landed nothing for %s is judged %s (rule %q), want %s: nothing else will ever end it",
			fragile.Name, quiet.SinceGrowth, rule.Kind, rule.Name, Stalled)
	}
	if act != revise {
		t.Error("that verdict does not ask for a revision; before playback the answer is to change the attempt, which is what makes reaching this verdict better than the deadline it replaced")
	}

	// And one poll earlier it is still waiting, so the bound is this window and not the first
	// reading of an empty buffer.
	if rule, _, err := judge(BeforePlay, Health{SinceGrowth: StallWindow - time.Second}); err != nil {
		t.Fatal(err)
	} else if rule.Kind == Stalled {
		t.Errorf("a read that has been quiet for %s is already stalled (rule %q); the bound is meant to outlast the backoff ceiling the reader was handed",
			StallWindow-time.Second, rule.Name)
	}
}

// TestStallWindowOutlivesTheReconnectCeiling pins the one thing about the stall deadline
// that is not a free choice. Every upstream fetch is given the read policy's backoff
// ceiling, read.BackoffMax, and a rate-limited CDN really does answer 429 and go quiet for
// all of it before the retry that lands. A judgement whose deadline is inside that window
// can only ever fire on a retry still owed its chance, and it then reports castor's own
// impatience as an expired playlist. The margin must cover a failed wait, a successful one,
// and the bytes that follow.
//
// It reads the ceiling from the read policy and not from the argument builder, which is the
// direction that keeps this bound honest: the number to outlast is the one whoever decided
// how long to keep retrying chose, and a flag renderer is not that party.
func TestStallWindowOutlivesTheReconnectCeiling(t *testing.T) {
	if StallWindow <= 2*read.BackoffMax {
		t.Errorf("StallWindow = %s, which does not outlast two reconnect backoffs of %s", StallWindow, read.BackoffMax)
	}
	// Every shipped policy is given that ceiling, so the bound derived from it covers every
	// shape of source a cast can be waiting on rather than one of them.
	for _, shape := range []read.Shape{{}, {Segmented: true}, {Segmented: true, Live: true}} {
		policy := read.For(shape, 30*time.Second)
		if policy.Backoff > read.BackoffMax {
			t.Errorf("a %s source backs off for %s, which outlasts the ceiling this bound was derived from", shape, policy.Backoff)
		}
	}
}

// TestTheConfidenceWindowOutlastsTheStartupLagItCarries pins the VALUE of the one dial
// decision 3 named, which was pinned by nothing: every row of the table above expresses its
// sample count as minSpeedSamples, so the constant could be set to one and the whole suite
// stayed green while the rule convicted on ffmpeg's first cumulative figure.
//
// The property is arithmetic and not taste. speed= is cumulative from process start, so
// every stated figure carries the connection's own cost in its denominator: DNS, the TLS
// handshake, the first HTTP round trip and the demuxer's probe, measured at 2.473s on a
// healthy cast. A window shorter than that lag is a judgement about the handshake, and it
// convicts a link that is delivering exactly what it was allowed. The window is counted in
// the reader's own report blocks, whose period is the read policy's (see read.StatsPeriod,
// which is what the pull renders as -stats_period), so it is those two that have to be
// multiplied rather than a number in an argument builder.
func TestTheConfidenceWindowOutlastsTheStartupLagItCarries(t *testing.T) {
	window := time.Duration(minSpeedSamples) * read.StatsPeriod
	if window <= startupLag {
		t.Errorf("the confidence window is %s (%d blocks of %s), which does not outlast the %s a read spends before it can state a speed at all: the figure that convicts is the connection's cost, not the link's rate",
			window, minSpeedSamples, read.StatsPeriod, startupLag)
	}
	// And it stays a hold rather than becoming a timeout: a window that outlasted the stall
	// bound would mean no read could ever be judged before something else gave up on it.
	if window >= StallWindow {
		t.Errorf("the confidence window is %s, which outlasts the %s stall bound: the gate would be judged by whatever gives up first", window, StallWindow)
	}
}

// TestNeitherDeliverabilityArmFiresInsideTheBackoffItHandedTheReader is the same bound the
// stall window has, on the arm that shipped without it. ffmpeg is given read.BackoffMax to
// wait out a 429, and it delivers nothing for all of it, so a cumulative speed measured
// across that window is under playback rate by construction. A pre-playback arm that
// convicts inside it refuses a cast for the backoff castor itself granted, and then walks
// and burns every other admitted link (single-use signed URLs among them) for a fault that
// was never about any link.
//
// One ceiling before playback against two in flight, because what the verdict DOES differs:
// before playback it revises the attempt, in flight it ends a cast someone is watching.
func TestNeitherDeliverabilityArmFiresInsideTheBackoffItHandedTheReader(t *testing.T) {
	if deficitWindow < read.BackoffMax {
		t.Errorf("the pre-playback deficit window is %s, inside the %s reconnect ceiling the read policy hands every fetch", deficitWindow, read.BackoffMax)
	}
	if StallWindow <= deficitWindow {
		t.Errorf("the in-flight deficit window is %s and the pre-playback one %s; the arm that ends a cast someone is watching must be the patient one", StallWindow, deficitWindow)
	}
	// The hold is what makes the wait legitimate: a deficit inside the ceiling must neither
	// convict nor open, because opening it hands a renderer a stream castor has measured as
	// unwatchable and makes the arm above unreachable.
	fresh := Health{Landed: 8 << 20, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples, SinceDeficit: deficitWindow - time.Second}
	rule, act, err := judge(BeforePlay, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if act != keepWatching {
		t.Errorf("a deficit of %s is answered by rule %q with %v, want the gate held", fresh.SinceDeficit, rule.Name, act)
	}
}

// TestTheDeliverabilityFloorSitsBetweenTheMeasuredCasts is the calibration, as an
// assertion. Every failing cast castor has observed measured between 0.0627 and 0.39
// against a readrate of 2.0; the one healthy cast measured 2.100 against the same 2.0. A
// floor that convicted the healthy one would break every working cast, and one that
// acquitted the failing ones would leave a sixteen hour download looking like a slow start.
func TestTheDeliverabilityFloorSitsBetweenTheMeasuredCasts(t *testing.T) {
	const allowed = 2.0
	for _, measured := range []float64{0.0627, 0.109, 0.159, 0.39} {
		h := Health{Landed: 1 << 20, Speed: media.Speed(measured), Headroom: allowed, Samples: minSpeedSamples}
		if !h.starving() {
			t.Errorf("a read measured at %gx against an allowed %gx is not named as starving", measured, allowed)
		}
	}
	healthy := Health{Landed: 1 << 20, Speed: 2.100, Headroom: allowed, Samples: minSpeedSamples}
	if healthy.starving() {
		t.Error("the cast that played fine, measured at 2.100x against an allowed 2.0x, is named as starving")
	}
}

// TestTheBufferIsWhatTheRendererCannotYetHavePlayed pins the measurement the in-flight stall
// rule holds a cast open on, and pins it as a LOWER bound in the one direction that matters.
// Playback runs at exactly playbackRate and a renderer cannot play media that was never
// produced for it, so what is left is everything delivered less the most a viewer can have
// consumed. Every term it leaves out (the seconds a renderer spends buffering before it
// starts, and the pauses that are the whole point of the measurement) makes the real figure
// larger, which is why a cast may be abandoned on this being gone and never on it being
// small.
func TestTheBufferIsWhatTheRendererCannotYetHavePlayed(t *testing.T) {
	for _, tt := range []struct {
		name   string
		health Health
		want   time.Duration
		inHand bool
	}{{
		name:   "a renderer four minutes into twenty has sixteen left",
		health: Health{Delivered: 20 * time.Minute, SincePlay: 4 * time.Minute},
		want:   16 * time.Minute,
		inHand: true,
	}, {
		// Parity is not media in hand: a renderer that has had exactly as long as was ever
		// produced for it may be at the end of it, and the rules only ever hold a cast open on
		// what they can prove is still there.
		name:   "a renderer that has had as long as was produced has nothing proven left",
		health: Health{Delivered: 10 * time.Minute, SincePlay: 10 * time.Minute},
		want:   0,
		inHand: false,
	}, {
		// A delivery that reports nothing (a rolling window, or a leg that supervises without
		// one) leaves both terms zero, and this is what keeps the pre-playback stall exactly
		// what it was: nothing has been delivered to anybody, so the second term of those rules
		// is vacuous there and a dead playlist is still named on silence alone.
		name:   "an unmeasured delivery holds nothing open",
		health: Health{Landed: 8 << 20, Handed: 4 << 20},
		want:   0,
		inHand: false,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.health.buffer(); got != tt.want {
				t.Errorf("buffer = %s, want %s", got, tt.want)
			}
			if got := tt.health.buffered(); got != tt.inHand {
				t.Errorf("buffered = %v, want %v", got, tt.inHand)
			}
		})
	}
}
