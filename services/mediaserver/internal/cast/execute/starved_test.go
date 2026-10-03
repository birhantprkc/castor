package execute

import (
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// TestADeviceThatEndsHavingTakenEverythingTheUnfinishedReadMadeIsStarved is told apart from one that stopped with media still to come.
func TestADeviceThatEndsHavingTakenEverythingTheUnfinishedReadMadeIsStarved(t *testing.T) {
	sp, err := deliver.NewSpool(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 64<<10)
	if _, err := sp.Write(body); err != nil {
		t.Fatal(err)
	}
	srv, err := deliver.OpenSpooledStream(t.Context(), deliver.Opening{
		Format:        container.Format{ContentType: media.MPEGTS, Extension: ".ts"},
		Listeners:     loopback{},
		WriteDeadline: time.Minute,
	}, sp, make(chan struct{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close(); sp.CloseWrite(nil) })

	resp, err := http.Get(srv.URL().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if _, err := io.ReadFull(resp.Body, make([]byte, len(body))); err != nil {
		t.Fatal(err)
	}

	read := &pull{done: make(chan struct{})}
	feedOf := func(r *pull) feed { return feed{buffered: &buffered{reader: r}} }
	madeBytes := int64(len(body))
	d := delivery{sink: streamedSink{Stream: srv, made: func() media.Progress { return media.Progress{Position: time.Minute, Bytes: madeBytes} }}}

	if !starved(feedOf(read), d) {
		t.Error("a device that took all the read had made, while the read still ran, was not starved")
	}

	madeBytes = int64(len(body)) * 2
	if starved(feedOf(read), d) {
		t.Error("a device that stopped with media still waiting for it was called starved")
	}

	madeBytes = int64(len(body))
	close(read.done)
	if starved(feedOf(read), d) {
		t.Error("a device that took everything from a read that had finished was called starved")
	}
}
