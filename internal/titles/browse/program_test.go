package browse

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/stupside/castor/internal/titles/tmdb"
)

// shelf is a catalog that answers at once from a fixed shelf, so a run renders the same every time.
type shelf struct{}

var (
	matrix = tmdb.SearchResult{ID: 603, MediaType: tmdb.MediaMovie, Title: "The Matrix", ReleaseDate: "1999-03-31", Overview: "A hacker learns the truth about his reality.", VoteAverage: 8.2}
	office = tmdb.SearchResult{ID: 2316, MediaType: tmdb.MediaTV, Name: "The Office", FirstAirDate: "2005-03-24", Overview: "A mockumentary about office life.", VoteAverage: 8.6}
)

func (shelf) Search(context.Context, string) ([]tmdb.SearchResult, error) {
	return []tmdb.SearchResult{matrix, office}, nil
}

func (shelf) Trending(context.Context) ([]tmdb.SearchResult, error) {
	return []tmdb.SearchResult{matrix, office}, nil
}

func (shelf) Discover(context.Context, tmdb.DiscoverParams) (tmdb.Page, error) {
	return tmdb.Page{Results: []tmdb.SearchResult{matrix, office}, TotalPages: 1}, nil
}

func (shelf) Genres(context.Context) (tmdb.GenreCatalog, error) {
	return tmdb.GenreCatalog{Movie: []tmdb.Genre{{ID: 28, Name: "Action"}}, TV: []tmdb.Genre{{ID: 35, Name: "Comedy"}}}, nil
}

func (shelf) Details(_ context.Context, mediaType string, _ int) (*tmdb.Details, error) {
	if mediaType == tmdb.MediaTV {
		return &tmdb.Details{Tagline: "Paper is our business.", EpisodeRunTime: []int{22}}, nil
	}
	return &tmdb.Details{Tagline: "Welcome to the Real World.", Runtime: 136, Genres: []tmdb.Genre{{ID: 28, Name: "Action"}}}, nil
}

func (shelf) TV(context.Context, int) (*tmdb.TVDetails, error) {
	return &tmdb.TVDetails{Name: "The Office", Seasons: []tmdb.Season{
		{SeasonNumber: 1, Name: "Season 1", EpisodeCount: 6, AirDate: "2005-03-24"},
		{SeasonNumber: 2, Name: "Season 2", EpisodeCount: 22, AirDate: "2005-09-20"},
	}}, nil
}

func (shelf) Season(context.Context, int, int) (*tmdb.SeasonDetails, error) {
	return &tmdb.SeasonDetails{Episodes: []tmdb.Episode{
		{EpisodeNumber: 1, Name: "Pilot", AirDate: "2005-03-24"},
		{EpisodeNumber: 2, Name: "Diversity Day", AirDate: "2005-03-29"},
	}}, nil
}

func (shelf) Poster(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("the shelf has no posters")
}

// browsing runs the real program at a fixed size and colour profile, as a terminal would.
func browsing(t *testing.T) *teatest.TestModel {
	t.Helper()
	m := newModel(t.Context(), shelf{}, "Living room", "dlna")
	return teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(110, 32),
		teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.Ascii)),
	)
}

func showing(t *testing.T, tm *teatest.TestModel, text string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(text)) }, teatest.WithDuration(5*time.Second))
}

// lastSeen is what the operator last saw, without its colours, so the golden reads as the screen does.
func lastSeen(t *testing.T, tm *teatest.TestModel) []byte {
	t.Helper()
	return []byte(ansi.Strip(tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(model).View().Content))
}

func TestEnterOnATrendingMovieCastsIt(t *testing.T) {
	tm := browsing(t)
	showing(t, tm, "Welcome to the Real World.")

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	golden.RequireEqual(t, lastSeen(t, tm))
	if got, want := tm.FinalModel(t).(model).sel, (Selection{Kind: KindMovie, TMDBID: "603", Title: "The Matrix"}); got != want {
		t.Errorf("selected %+v, want %+v", got, want)
	}
}

func TestAShowIsDrilledIntoUntilAnEpisodeIsChosen(t *testing.T) {
	tm := browsing(t)
	showing(t, tm, "Welcome to the Real World.")

	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	showing(t, tm, "Paper is our business.")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	showing(t, tm, "Season 2")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	showing(t, tm, "Diversity Day")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	golden.RequireEqual(t, lastSeen(t, tm))
	want := Selection{Kind: KindEpisode, TMDBID: "2316", Title: "The Office · S01E02 · Diversity Day", Season: 1, Episode: 2}
	if got := tm.FinalModel(t).(model).sel; got != want {
		t.Errorf("selected %+v, want %+v", got, want)
	}
}
