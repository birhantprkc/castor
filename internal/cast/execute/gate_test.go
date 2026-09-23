package execute

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/cast/policy/watch"
	"github.com/stupside/castor/internal/media"
)

// fakeLead mocks transcriber for gate testing without whisper model.
type fakeLead struct {
	latest float64
	done   bool
}

func (f fakeLead) LatestEnd() float64 { return f.latest }
func (f fakeLead) Done() bool         { return f.done }

func TestWaitForPlayableHoldsUntilTheTranscriptionLeads(t *testing.T) {
	// Pace omitted: starving verdict would confuse the assertion.
	c := gateFixture(t, 0)
	c.burn = &fakeStage{lead: &fakeLead{latest: 1}}
	if _, err := c.spool.Write(make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		opened <- c.playable(ctx)
	}()

	select {
	case err := <-opened:
		t.Fatalf("the gate opened ahead of the transcription's committed frontier (err %v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	if err := <-opened; !errors.Is(err, context.Canceled) {
		t.Errorf("gate error = %v, want context.Canceled", err)
	}
}

// TestBothWindowsJudgeTheSameReadAgainstTheSamePace: a read castor throttles or encodes is not judged on realtime.
func TestBothWindowsJudgeTheSameReadAgainstTheSamePace(t *testing.T) {
	const granted = 2.0

	for _, tt := range []struct {
		name string
		load func(*pull)
		want float64
	}{{
		name: "a read copying both tracks off the link",
		load: func(*pull) {},
		want: granted,
	}, {
		name: "a read teeing PCM to a transcription",
		load: func(p *pull) { _, p.pcmOut = io.Pipe() },
		want: 0,
	}, {
		name: "a read producing a track rather than copying it",
		load: func(p *pull) {
			p.floor = plan.MediaPlan{Video: plan.EncodeVideo(plan.VideoEncode{}), Audio: plan.CopyAudio()}
		},
		want: 0,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			c := gateFixture(t, granted)
			tt.load(c.reader)

			// A read that died is the fastest verdict the pre-playback window reaches.
			c.reader.err = errors.New("upstream pull: exit status 1")
			close(c.reader.done)
			gate := paceBehindTheVerdict(t, c.playable)

			never := stoppedRenderer{last: time.Now().Add(-watch.StallWindow - time.Second)}
			inFlight := paceBehindTheVerdict(t, func(ctx context.Context) error {
				return watch.Watch(ctx, c.playingMonitor(never))
			})

			if gate != inFlight {
				t.Errorf("the gate judged this read against %gx and the supervisor against %gx", gate, inFlight)
			}
			if gate != tt.want {
				t.Errorf("the pace this read is judged against = %gx, want %gx", gate, tt.want)
			}
		})
	}
}

// paceBehindTheVerdict runs one watch to its verdict and reports the pace the read was judged against.
func paceBehindTheVerdict(t *testing.T, watching func(context.Context) error) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var fault *watch.Fault
	if err := watching(ctx); !errors.As(err, &fault) {
		t.Fatalf("the watch ended with %v rather than a verdict carrying the numbers it was reached on", err)
	}
	return fault.Health.Headroom
}

// stoppedRenderer took nothing, last asked at last.
type stoppedRenderer struct {
	last     time.Time
	buffered time.Duration
}

func (s stoppedRenderer) Handed() (int64, time.Time) { return 0, s.last }
func (s stoppedRenderer) Buffered() time.Duration    { return s.buffered }

func gateFixture(t *testing.T, pace float64) *cast {
	t.Helper()
	sp, err := deliver.NewSpool(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.CloseWrite(nil) })

	return &cast{
		row:   compose.Row{Kind: compose.ReadOnce},
		spool: sp,
		reader: &pull{
			spool:  sp,
			done:   make(chan struct{}),
			policy: read.Plan{media.PrimaryInputID: {Pace: read.Pace{Realtime: pace}}},
		},
	}
}
