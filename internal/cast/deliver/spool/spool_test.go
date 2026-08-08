package spool

import (
	"errors"
	"io"
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
