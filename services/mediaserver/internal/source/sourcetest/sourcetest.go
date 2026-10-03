// Package sourcetest has fixture origins and program assertions for source tests.
package sourcetest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// Playlist serves one fixture document to every fetch; like web.Client, anything but a 2xx fails.
type Playlist struct {
	Body   string
	Status int
}

func (*Playlist) Replay(_ *url.URL, h http.Header) http.Header { return h }

func (p *Playlist) Read(ctx context.Context, u *url.URL, h http.Header, r timeline.Range) (io.ReadCloser, error) {
	return read(ctx, p, u, h, r)
}

func (p *Playlist) Fetch(_ context.Context, u *url.URL, _ http.Header) (string, *url.URL, int, error) {
	if p.Status < 200 || p.Status >= 300 {
		return "", u, p.Status, fmt.Errorf("fetching document: HTTP %d", p.Status)
	}
	return p.Body, u, p.Status, nil
}

// Playlists serves documents of one origin by path, 404s every other, and records every path asked for.
type Playlists struct {
	Documents map[string]string

	mu    sync.Mutex
	asked []string
}

// Testdata serves every file in dir at its own name under the origin root.
func Testdata(t *testing.T, dir string) *Playlists {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	docs := make(map[string]string, len(entries))
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		docs["/"+e.Name()] = string(body)
	}
	return &Playlists{Documents: docs}
}

func (*Playlists) Replay(_ *url.URL, h http.Header) http.Header { return h }

func (p *Playlists) Fetch(_ context.Context, u *url.URL, _ http.Header) (string, *url.URL, int, error) {
	p.mu.Lock()
	p.asked = append(p.asked, u.Path)
	p.mu.Unlock()
	body, ok := p.Documents[u.Path]
	if !ok {
		return "", u, http.StatusNotFound, fmt.Errorf("fetching document: HTTP %d", http.StatusNotFound)
	}
	return body, u, http.StatusOK, nil
}

func (p *Playlists) Read(ctx context.Context, u *url.URL, h http.Header, r timeline.Range) (io.ReadCloser, error) {
	return read(ctx, p, u, h, r)
}

type fetcher interface {
	Fetch(ctx context.Context, u *url.URL, h http.Header) (string, *url.URL, int, error)
}

// read serves the part of what f fetches that r names, all of it for a range with no length, failing as an origin's status would.
func read(ctx context.Context, f fetcher, u *url.URL, h http.Header, r timeline.Range) (io.ReadCloser, error) {
	body, _, status, err := f.Fetch(ctx, u, h)
	if err != nil {
		return nil, &timeline.Failure{Status: status, Err: err}
	}
	if r.Length > 0 {
		body = body[min(r.Offset, int64(len(body))):min(r.Offset+r.Length, int64(len(body)))]
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// Asked is every path fetched so far, in order.
func (p *Playlists) Asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.asked)
}

// URL parses raw, failing the test when it does not.
func URL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// RequireInput is the program's input with this ID, failing the test when absent.
func RequireInput(t *testing.T, program media.Program, id media.InputID) media.Input {
	t.Helper()
	input, ok := program.LookupInput(id)
	if !ok {
		t.Fatalf("program has no %q input: %+v", id, program.Inputs)
	}
	return input
}

// PrimaryInput is the input that owns the program's clock, failing the test when absent.
func PrimaryInput(t *testing.T, program media.Program) media.Input {
	t.Helper()
	input, ok := program.PrimaryInput()
	if !ok {
		t.Fatalf("program has no primary input: its clock names %q, and it carries %+v", program.ClockInput, program.Inputs)
	}
	return input
}
