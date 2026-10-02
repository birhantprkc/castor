package deliver

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

// Spool is an append-only buffer with blocking tails, safe for concurrent access.
type Spool struct {
	path string
	w    *os.File

	mu     sync.Mutex
	cond   *sync.Cond
	size   int64
	closed bool  // no more writes are coming
	err    error // terminal write-side error, if any
}

func NewSpool(path string) (*Spool, error) {
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

// CloseWrite marks write finished; err records early stop reason, drained by tails.
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

// Path returns backing file path; append-only guarantee lets readers see consistent prefixes.
func (s *Spool) Path() string { return s.path }

// TailAt returns tail from byte offset with resume support (append-only guarantees immutability).
func (s *Spool) TailAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("opening spool for tail: %w", err)
	}
	t := &tailReader{spool: s, f: f, ctx: ctx, offset: offset}
	// Register cond wake for ctx cancellation; cleanup via Close.
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
	// done: owner closed reader; guarded by spool mutex for wait observation.
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
		// Return error not EOF to distinguish close from producer finish.
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

// Close terminates parked Read with broadcast to prevent encoder deadlock.
func (t *tailReader) Close() error {
	s := t.spool
	s.mu.Lock()
	t.done = true
	s.cond.Broadcast()
	s.mu.Unlock()
	t.stop()
	return t.f.Close()
}
