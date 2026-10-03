package execute

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/codec"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
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
	buf := gateFixture(t, 0)
	buf.burn = &fakeBurn{lead: &fakeLead{latest: 1}}
	if _, err := buf.reader.spool.Write(make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		opened <- gate(ctx, buf)
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
			p.floor = codec.Plan{Video: codec.Encode(codec.VideoEncode{}), Audio: codec.CopyAudio()}
		},
		want: 0,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			buf := gateFixture(t, granted)
			tt.load(buf.reader)

			// A read that died is the fastest verdict the pre-playback window reaches.
			buf.reader.err = errors.New("upstream pull: exit status 1")
			close(buf.reader.done)
			before := paceBehindTheVerdict(t, func(ctx context.Context) error { return gate(ctx, buf) })

			never := stoppedDevice{last: time.Now().Add(-health.StallWindow - time.Second)}
			inFlight := paceBehindTheVerdict(t, func(ctx context.Context) error {
				return health.Watch(ctx, playingMonitor(feed{buffered: buf}, delivery{}, never))
			})

			if before != inFlight {
				t.Errorf("the gate judged this read against %gx and the supervisor against %gx", before, inFlight)
			}
			if before != tt.want {
				t.Errorf("the pace this read is judged against = %gx, want %gx", before, tt.want)
			}
		})
	}
}

// paceBehindTheVerdict runs one watch to its verdict and reports the pace the read was judged against.
func paceBehindTheVerdict(t *testing.T, watching func(context.Context) error) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := watching(ctx)
	fault, ok := errors.AsType[*health.Fault](err)
	if !ok {
		t.Fatalf("the watch ended with %v rather than a verdict carrying the numbers it was reached on", err)
	}
	return fault.Vitals.Headroom
}

// stoppedDevice took nothing, last asked at last.
type stoppedDevice struct {
	last     time.Time
	buffered time.Duration
}

func (s stoppedDevice) Handed() (int64, time.Time) { return 0, s.last }
func (s stoppedDevice) Buffered() time.Duration    { return s.buffered }

// gateFixture is a buffered read over an empty spool, granted pace.
func gateFixture(t *testing.T, pace float64) *buffered {
	t.Helper()
	sp, err := deliver.NewSpool(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.CloseWrite(nil) })

	return &buffered{reader: &pull{
		proc:  finished(t),
		spool: sp,
		done:  make(chan struct{}),
		pace:  pace,
	}}
}
