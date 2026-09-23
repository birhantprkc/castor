// Package sourcetest has scripted measurement and fixture origins for source tests.
package sourcetest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/stupside/castor/internal/media"
)

// Measurer answers from a script keyed by URL and records what it was asked.
type Measurer struct {
	Answers map[string]Answer

	mu       sync.Mutex
	measured []string
}

// Answer is one scripted measurement (absent Info with Err = production shape for unopened link).
type Answer struct {
	Info  *media.ProbeInfo
	Reach media.Reach
	Err   error
}

// Probe binds the script to one link (test binds it to candidate's URL to build source.Probes).
func (m *Measurer) Probe(u *url.URL) media.Prober { return scripted{answers: m, url: u} }

// Asked is every URL measured so far, in order.
func (m *Measurer) Asked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.measured)
}

type scripted struct {
	answers *Measurer
	url     *url.URL
}

func (p scripted) Probe(context.Context) (media.ProbeInfo, media.Reach, error) {
	m := p.answers
	m.mu.Lock()
	defer m.mu.Unlock()
	m.measured = append(m.measured, p.url.String())
	a, ok := m.Answers[p.url.String()]
	if !ok {
		return media.ProbeInfo{}, media.ReachUnproven, fmt.Errorf("no scripted measurement for %s", p.url)
	}
	// Scripted failure carries no info (production shape for unopened link).
	if a.Info == nil {
		return media.ProbeInfo{}, a.Reach, a.Err
	}
	return *a.Info, a.Reach, a.Err
}

// Playlist serves one fixture document to every fetch; like web.Playlists, anything but a 2xx fails.
type Playlist struct {
	Body   string
	Status int
}

func (p *Playlist) Fetch(_ context.Context, u *url.URL, _ http.Header) (string, *url.URL, int, error) {
	if p.Status < 200 || p.Status >= 300 {
		return "", u, p.Status, fmt.Errorf("fetching playlist: HTTP %d", p.Status)
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

func (p *Playlists) Fetch(_ context.Context, u *url.URL, _ http.Header) (string, *url.URL, int, error) {
	p.mu.Lock()
	p.asked = append(p.asked, u.Path)
	p.mu.Unlock()
	body, ok := p.Documents[u.Path]
	if !ok {
		return "", u, http.StatusNotFound, fmt.Errorf("fetching playlist: HTTP %d", http.StatusNotFound)
	}
	return body, u, http.StatusOK, nil
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
