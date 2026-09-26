package hls

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/follow"
	"github.com/stupside/castor/internal/source/sourcetest"
	"github.com/stupside/castor/internal/source/timeline"
)

// program is one input of an HLS media playlist, live or ended.
func program(t *testing.T, live bool) media.Program {
	t.Helper()
	p, err := media.NewProgram(media.Program{
		Inputs: []media.Input{{
			ID: media.PrimaryInputID, URL: sourcetest.URL(t, "https://cdn.example/live/index.m3u8"),
			ContentType: media.HLS, Fetch: media.Fetch{Segmented: true, Live: live},
		}},
		Tracks:     []media.TrackRef{{Input: media.PrimaryInputID, Kind: media.TrackVideo}},
		ClockInput: media.PrimaryInputID,
		EndPolicy:  media.EndAtLongest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func republish(t *testing.T, playlists source.Client, p media.Program) media.Program {
	t.Helper()
	followed, stop, err := follow.New(playlists, source.Formats{Format{}}, 5*time.Second, nil).Republish(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop() })
	return followed
}

func TestAPlaylistThatEndedIsReadDirectly(t *testing.T) {
	p := program(t, false)
	followed := republish(t, &sourcetest.Playlist{Body: mediaPlaylist, Status: http.StatusOK}, p)
	if in, _ := followed.PrimaryInput(); in.URL.String() != "https://cdn.example/live/index.m3u8" {
		t.Errorf("an ended playlist reads %s, want the origin's own", in.URL)
	}
}

func TestALivePlaylistCastorCannotReadStaysFfmpegsToRead(t *testing.T) {
	followed := republish(t, &sourcetest.Playlist{Status: http.StatusForbidden}, program(t, true))
	if in, _ := followed.PrimaryInput(); in.URL.String() != "https://cdn.example/live/index.m3u8" {
		t.Errorf("an unreadable playlist reads %s, want the origin's own", in.URL)
	}
}

func TestARefusedReloadCarriesTheOriginsStatus(t *testing.T) {
	in, _ := program(t, true).PrimaryInput()
	follow := Format{}.Timeline(source.Env{Client: &sourcetest.Playlist{Status: http.StatusGone}}, in, media.TrackVideo)
	_, err := follow.Window(t.Context())
	if f, ok := errors.AsType[*timeline.Failure](err); !ok || f.Status != http.StatusGone {
		t.Errorf("a 410 reload failed with %v, want the origin's status carried", err)
	}
}
