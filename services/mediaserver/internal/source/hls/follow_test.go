package hls

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source/sourcetest"
	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// input is the one input of an HLS media playlist, live or ended.
func input(t *testing.T, live bool) media.Input {
	t.Helper()
	return media.Input{
		ID: media.PrimaryInputID, URL: sourcetest.URL(t, "https://cdn.example/live/index.m3u8"),
		ContentType: media.HLS, Fetch: media.Fetch{Segmented: true, Live: live},
	}
}

func TestAPlaylistThatEndedHasNoTimelineToFollow(t *testing.T) {
	if follow := (Format{}).Timeline(&sourcetest.Document{Body: mediaPlaylist, Status: http.StatusOK}, input(t, false), media.TrackVideo); follow != nil {
		t.Errorf("an ended playlist is followed by %T, want it read directly", follow)
	}
}

func TestARefusedReloadCarriesTheOriginsStatus(t *testing.T) {
	follow := Format{}.Timeline(&sourcetest.Document{Status: http.StatusGone}, input(t, true), media.TrackVideo)
	_, err := follow.Window(t.Context())
	if f, ok := errors.AsType[*timeline.Failure](err); !ok || f.Status != http.StatusGone {
		t.Errorf("a 410 reload failed with %v, want the origin's status carried", err)
	}
}
