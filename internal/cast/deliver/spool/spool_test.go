package spool

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

func newTestSpool(t *testing.T) *Spool {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTailBlocksUntilWritten is the property the whole type exists for: a reader
// at end-of-data waits for the producer instead of reporting EOF.
func TestTailBlocksUntilWritten(t *testing.T) {
	s := newTestSpool(t)

	tail, err := s.Tail(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()

	read := make(chan string, 1)
	go func() {
		buf := make([]byte, 4)
		n, _ := io.ReadFull(tail, buf)
		read <- string(buf[:n])
	}()

	select {
	case got := <-read:
		t.Fatalf("a tail on an empty spool returned %q instead of waiting", got)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := s.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-read:
		if got != "data" {
			t.Errorf("tail read %q, want %q", got, "data")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the tail never woke on a write")
	}
	s.CloseWrite(nil)
}

// TestATailFromAnOffsetContinuesWhereAConsumerStopped is what lets a severed HTTP client be
// served its position instead of the start of the film: the buffer is append-only, so byte N is
// the same byte for the life of the spool and a consumer holding the prefix can be handed the
// rest. An offset the producer has not reached yet blocks like any other tail at end-of-data,
// which is the difference between a consumer that waits and one told the stream ended.
func TestATailFromAnOffsetContinuesWhereAConsumerStopped(t *testing.T) {
	s := newTestSpool(t)
	if _, err := s.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}

	tail, err := s.TailAt(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()

	read := make(chan string, 1)
	go func() {
		buf := make([]byte, 6)
		n, _ := io.ReadFull(tail, buf)
		read <- string(buf[:n])
	}()

	select {
	case got := <-read:
		t.Fatalf("a tail at the end of the written bytes returned %q instead of waiting for them", got)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := s.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-read:
		if got != "second" {
			t.Errorf("a tail from byte 5 read %q, want %q: a consumer served from the wrong offset is handed media it cannot decode", got, "second")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the tail never woke on a write")
	}
	s.CloseWrite(nil)
}

// TestClosingATailTerminatesAReadParkedInIt is the property a teardown that returns depends on.
//
// A tail is what feeds ffmpeg's stdin, and os/exec's Wait also waits for the goroutine copying
// that reader. So when a fault ends a cast, the encoder is killed while its tail is parked at
// end-of-data on a buffer whose producer will never write again: killing the process does not
// wake that read, and closing the file underneath it does not either, because a reader inside a
// cond wait is not looking at the file. Nothing was left to free it, and the cast's own context,
// which would have, is cancelled by a defer that cannot run until the delivery returns. The wait
// never ended.
//
// It reports an error rather than EOF because the producer has NOT finished: a consumer told EOF
// by a buffer that is still growing would mux a truncated file and call it complete.
func TestClosingATailTerminatesAReadParkedInIt(t *testing.T) {
	s := newTestSpool(t)
	t.Cleanup(func() { s.CloseWrite(nil) })
	if _, err := s.Write([]byte("the head of a program")); err != nil {
		t.Fatal(err)
	}

	tail, err := s.Tail(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// Drained past what has been written, so the read below is parked exactly where an encoder's
	// stdin copier parks: at the end of a spool nobody is going to grow.
	if _, err := io.ReadAll(io.LimitReader(tail, 21)); err != nil {
		t.Fatal(err)
	}
	parked := make(chan error, 1)
	go func() {
		_, err := tail.Read(make([]byte, 4))
		parked <- err
	}()
	select {
	case err := <-parked:
		t.Fatalf("the read returned %v instead of waiting at the end of a spool that is still open", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := tail.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-parked:
		if !errors.Is(err, fs.ErrClosed) {
			t.Errorf("a read parked in a closed tail returned %v, want the closure; EOF would tell a consumer the producer had finished", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing a tail left a read parked in it forever, which is the teardown that never returns")
	}
}

// TestTailReportsTheProducerError makes the write side's terminal error the
// reader's error too, so a truncated download cannot look like a clean end.
func TestTailReportsTheProducerError(t *testing.T) {
	s := newTestSpool(t)
	tail, err := s.Tail(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()

	want := errors.New("upstream died")
	s.CloseWrite(want)

	if _, err := io.ReadAll(tail); !errors.Is(err, want) {
		t.Errorf("tail error = %v, want %v", err, want)
	}
}
