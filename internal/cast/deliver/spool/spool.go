// Package spool provides an append-only on-disk buffer with blocking tails.
//
// A Spool decouples a producer from its consumers: the producer appends as
// fast as it can, and each Tail reads from byte 0, blocking at end-of-data
// until more bytes arrive instead of reporting EOF. Once the producer
// finishes, consumers drain the remainder and see EOF (or the producer's
// terminal error).
package spool

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

// Spool is the append-only buffer. Create one with New, feed it via Write,
// and finish with CloseWrite.
type Spool struct {
	path string
	w    *os.File

	mu     sync.Mutex
	cond   *sync.Cond
	size   int64
	closed bool  // no more writes are coming
	err    error // terminal write-side error, if any
}

func New(path string) (*Spool, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating spool file: %w", err)
	}
	s := &Spool{path: path, w: f}
	s.cond = sync.NewCond(&s.mu)
	return s, nil
}

// Write appends to the spool and wakes any blocked tails.
func (s *Spool) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.w.Write(p)
	s.size += int64(n)
	s.cond.Broadcast()
	return n, err
}

// CloseWrite marks the write side finished. err records why the producer
// stopped early (nil for a clean end); tails drain the remaining bytes and
// then see EOF (or the error).
func (s *Spool) CloseWrite(err error) {
	s.mu.Lock()
	s.closed = true
	s.err = err
	s.cond.Broadcast()
	s.mu.Unlock()
	_ = s.w.Close()
}

func (s *Spool) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Path returns the backing file path. A reader (e.g. ffprobe) may open it
// concurrently with the producer: the spool is append-only, so a read sees a
// consistent prefix of whatever has been written so far.
func (s *Spool) Path() string { return s.path }

// Tail returns a reader over the spool from byte 0 that blocks at end-of-data until the
// writer appends more or closes. A parked reader also unblocks when ctx is cancelled (with
// ctx.Err()) and when its owner closes it (see tailReader.Close), which are the two ways a
// consumer of a buffer that has stopped growing is ever let go.
func (s *Spool) Tail(ctx context.Context) (io.ReadCloser, error) { return s.TailAt(ctx, 0) }

// TailAt is Tail from a byte offset, for a consumer that already holds the
// prefix and asked to continue from where it stopped (an HTTP byte-range
// resume). offset must not be negative.
//
// It is sound only because this buffer is append-only: byte N is the same byte
// for the life of the spool, so a reader handed an offset can never be handed
// different media than the prefix it already has. An offset past the current end
// is not an error either, it blocks like any other tail at end-of-data, which is
// what a consumer asking for a byte the producer has not reached yet must do.
func (s *Spool) TailAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("opening spool for tail: %w", err)
	}
	t := &tailReader{spool: s, f: f, ctx: ctx, offset: offset}
	// Wake the cond loop when ctx dies so Read can observe cancellation. The stop
	// function is kept and called from Close: ctx is the whole cast's, so a
	// registration nobody cancels outlives the reader it was made for and is only
	// released when the cast ends.
	t.stop = context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	return t, nil
}

type tailReader struct {
	spool  *Spool
	f      *os.File
	ctx    context.Context
	stop   func() bool
	offset int64
	// done records that this reader's owner has closed it. It is guarded by the spool's mutex
	// because it is read inside the wait below: a reader parked at end-of-data has to observe
	// it, which is the whole reason it is a flag under that lock rather than a closed file.
	done bool
}

func (t *tailReader) Read(p []byte) (int, error) {
	s := t.spool
	s.mu.Lock()
	for t.offset >= s.size && !s.closed && !t.done && t.ctx.Err() == nil {
		s.cond.Wait()
	}
	size, closed, werr, gone := s.size, s.closed, s.err, t.done
	s.mu.Unlock()

	if err := t.ctx.Err(); err != nil {
		return 0, err
	}
	if gone {
		// The owner closed this reader, so there is nobody left to hand bytes to. Reported as an
		// error rather than as EOF because a consumer of a spool that is still growing must not
		// read "your owner gave up" as "the producer finished".
		return 0, fs.ErrClosed
	}
	if t.offset >= size {
		// closed and fully drained
		if werr != nil {
			return 0, fmt.Errorf("spool producer failed: %w", werr)
		}
		if closed {
			return 0, io.EOF
		}
	}

	n, err := t.f.ReadAt(p, t.offset)
	t.offset += int64(n)
	if err == io.EOF {
		// More data may arrive; the next Read blocks on the cond again.
		err = nil
		if n == 0 {
			return t.Read(p)
		}
	}
	return n, err
}

// Close ends this view of the spool, and TERMINATES a Read parked in it.
//
// The broadcast is what makes it terminate, and it is the difference between a teardown that
// returns and one that does not. A tail is what feeds ffmpeg's stdin, and os/exec's Wait also
// waits for the goroutine copying that reader, so an encoder killed while its tail is parked at
// end-of-data on a spool that will never grow again is reaped by nobody: the process is dead,
// the copy is asleep, and the wait behind it never ends. Waking the reader is the only thing
// that can free it, since nothing else about a closed file is visible from inside a cond wait.
func (t *tailReader) Close() error {
	s := t.spool
	s.mu.Lock()
	t.done = true
	s.cond.Broadcast()
	s.mu.Unlock()
	t.stop()
	return t.f.Close()
}
