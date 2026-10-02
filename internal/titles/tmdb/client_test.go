package tmdb

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Search uses one url.Values for two requests; get must not mutate caller's map.
func TestGetTreatsCallerValuesAsReadOnly(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.URL.Query().Get("api_key"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"results":[{"id":1,"title":"Dune"}]}`))
	}))
	defer srv.Close()
	c := &Client{apiKey: "secret", base: srv.URL, http: srv.Client()}

	q := url.Values{"query": {"dune"}}
	var out struct{}
	if err := c.get(t.Context(), "/search/movie", q, &out); err != nil {
		t.Fatal(err)
	}
	if _, mutated := q["api_key"]; mutated {
		t.Fatal("get wrote api_key into the caller's values")
	}

	// Race detector proves Search goroutines don't collide on shared map.
	res, err := c.Search(t.Context(), "dune")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("Search returned %d rows, want one per media type", len(res))
	}
	mu.Lock()
	defer mu.Unlock()
	for i, k := range keys {
		if k != "secret" {
			t.Fatalf("request %d carried api_key %q", i, k)
		}
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

// Errors reach the TUI status line, so none may quote the keyed request URL.
func TestErrorsNeverCarryTheAPIKey(t *testing.T) {
	const key = "s3cr3t-api-key"
	c := New(key)
	c.http.Transport = failingTransport{}

	_, searchErr := c.Search(t.Context(), "dune")
	_, detailsErr := c.Details(t.Context(), MediaMovie, 42)
	_, posterErr := c.Poster(t.Context(), "/p.jpg")
	for name, err := range map[string]error{"search": searchErr, "details": detailsErr, "poster": posterErr} {
		if err == nil {
			t.Fatalf("%s: want an error from a failing transport", name)
		}
		if strings.Contains(err.Error(), key) {
			t.Errorf("%s error leaks the api key: %v", name, err)
		}
	}
}

func TestPosterStreamsTheImageAndRejectsMissingOnes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/w500/p.jpg" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("jpeg bytes"))
	}))
	defer srv.Close()
	c := New("secret")
	c.images = srv.URL + "/"

	body, err := c.Poster(t.Context(), "/p.jpg")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(got) != "jpeg bytes" {
		t.Fatalf("poster body = %q, %v", got, err)
	}

	if _, err := c.Poster(t.Context(), "/gone.jpg"); err == nil {
		t.Fatal("a 404 poster should be an error, not an empty image")
	}
}
