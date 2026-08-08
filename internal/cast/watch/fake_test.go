package watch

import (
	"sync"
	"time"

	"github.com/stupside/castor/internal/media"
)

// The fakes every test in this package runs on. They exist because the three ports are
// declared at this consumer for exactly that reason: the whole ruleset, both windows and
// the loop that walks them are exercisable with no ffmpeg, no whisper model, no network
// and no renderer.

// fakeLead stands in for the transcriber.
type fakeLead struct {
	latest float64
	done   bool
}

func (f fakeLead) LatestEnd() float64 { return f.latest }
func (f fakeLead) Done() bool         { return f.done }

// fakeProducer is a scripted read: a progress sample a test can advance, a terminal
// state it can reach, and lines it can have printed.
type fakeProducer struct {
	done     chan struct{}
	evidence []string

	mu       sync.Mutex
	progress media.Progress
	err      error
}

func newFakeProducer() *fakeProducer {
	return &fakeProducer{done: make(chan struct{})}
}

func (f *fakeProducer) Progress() media.Progress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.progress
}

func (f *fakeProducer) Done() <-chan struct{} { return f.done }

func (f *fakeProducer) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *fakeProducer) Evidence() []string { return f.evidence }

// report publishes one sample, which is how a test walks a read up or down the
// deliverability floor one block at a time.
func (f *fakeProducer) report(sample media.Progress) {
	f.mu.Lock()
	f.progress = sample
	f.mu.Unlock()
}

// settle ends the read with err, cleanly when err is nil.
func (f *fakeProducer) settle(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	close(f.done)
}

// fakeConsumer is a scripted renderer: how many times it has fetched and when it last
// did.
type fakeConsumer struct {
	mu       sync.Mutex
	requests int
	last     time.Time
}

func (f *fakeConsumer) Fetched() (int, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.last
}

func (f *fakeConsumer) fetch() {
	f.mu.Lock()
	f.requests++
	f.last = time.Now()
	f.mu.Unlock()
}
