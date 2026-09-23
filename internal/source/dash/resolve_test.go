package dash

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/sourcetest"
)

func resolve(t *testing.T, playlists source.Playlists, stream *source.Candidate) source.Resolution {
	t.Helper()
	resolver := source.NewResolver(source.Config{MaxHeight: 1080}, playlists, source.Formats{Format{}})
	resolved, err := resolver.RefetchProgram(t.Context(), stream, source.Rendition{})
	if err != nil {
		t.Fatalf("RefetchProgram: %v", err)
	}
	return resolved
}

// manifest is the candidate a ranked MPD arrives as, with the heights its probe measured.
func manifest(t *testing.T, heights ...int) *source.Candidate {
	t.Helper()
	stream := &source.Candidate{URL: sourcetest.URL(t, "https://origin.example/manifest.mpd"), ContentType: media.DASH}
	if heights != nil {
		stream.Probe = &media.ProbeInfo{
			ContentType: media.DASH, VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC,
			VideoHeight: heights[0], VideoHeights: heights,
		}
	}
	return stream
}

func TestDASHBindsTheCeilingToTheRepresentationItReads(t *testing.T) {
	resolved := resolve(t, &sourcetest.Playlist{}, manifest(t, 480, 2160, 1080))
	if got := resolved.Rendition; got.Index != 2 || got.Height != 1080 {
		t.Errorf("chosen rendition = %+v, want the tallest rung under the 1080 cap (index 2)", got)
	}
	if video, ok := resolved.Program.Track(media.TrackVideo); !ok || video.Index != 2 {
		t.Errorf("video track = %+v, want the chosen representation", video)
	}
	if len(resolved.Origin.Renditions) != 3 {
		t.Errorf("published ladder = %+v, want the three rungs offered", resolved.Origin.Renditions)
	}
	// The probe described another representation, so it must not travel.
	if _, measured := resolved.Program.Measurement(); measured {
		t.Error("the narrowed program kept codec facts measured from another representation")
	}
}

// laddered has two rungs; the top declares HEVC Main 10, which fixes no bit depth.
const laddered = `<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT1H">
  <Period>
    <AdaptationSet contentType="video">
      <Representation mimeType="video/mp4" codecs="hvc1.2.4.L150.90" bandwidth="17000000" width="3840" height="2160"/>
      <Representation mimeType="video/mp4" codecs="avc1.640028" bandwidth="6941000" width="1920" height="1080"/>
    </AdaptationSet>
  </Period>
</MPD>`

func TestDASHNarrowedToARepresentationCarriesWhatTheManifestDeclared(t *testing.T) {
	resolved := resolve(t, &sourcetest.Playlist{Body: laddered, Status: http.StatusOK}, manifest(t, 2160, 1080))
	if got := resolved.Rendition; got.Index != 1 || got.Height != 1080 {
		t.Fatalf("chosen rendition = %+v, want the 1080 rung (index 1)", got)
	}
	got, measured := resolved.Program.Measurement()
	want := media.ProbeInfo{VideoCodec: media.CodecH264, VideoProfile: "High", VideoHeight: 1080, VideoBitDepth: 8}
	if !measured || !reflect.DeepEqual(got, want) {
		t.Errorf("measurement = %+v, want the chosen representation's declaration %+v", got, want)
	}
}

func TestDASHLivenessComesFromTheManifestType(t *testing.T) {
	const dynamic = `<MPD type="dynamic" xmlns="urn:mpeg:dash:schema:mpd:2011"><Period>
  <AdaptationSet contentType="video"><Representation bandwidth="800000" width="640" height="360"/></AdaptationSet>
</Period></MPD>`
	for _, tc := range []struct {
		body     string
		wantLive bool
	}{{dynamic, true}, {ffmpegPresentation, false}} {
		// A probe with no runtime, which alone would read as live.
		stream := manifest(t)
		stream.Probe = &media.ProbeInfo{ContentType: media.DASH}
		resolved := resolve(t, &sourcetest.Playlist{Body: tc.body, Status: http.StatusOK}, stream)
		if resolved.Origin.Live != tc.wantLive || sourcetest.PrimaryInput(t, resolved.Program).Fetching().Live != tc.wantLive {
			t.Errorf("live = %v (input %v), want %v", resolved.Origin.Live, sourcetest.PrimaryInput(t, resolved.Program).Fetching().Live, tc.wantLive)
		}
	}
}

func TestPickRepresentationTakesTheTallestAdmittedRung(t *testing.T) {
	for _, tc := range []struct {
		name    string
		heights []int
		ceiling media.HeightCap
		want    int
	}{
		{"the tallest under the cap", []int{360, 1080, 720}, 1080, 1},
		{"nothing under the cap takes the shortest", []int{2160, 1440, 4320}, 720, 1},
		{"an unknown-height slot is skipped without renumbering", []int{0, 720}, 1080, 1},
	} {
		rungs := representations(&media.ProbeInfo{VideoHeights: tc.heights})
		if got := (source.Origin{Renditions: rungs}).Choose(tc.ceiling, byHeight); got.Index != tc.want {
			t.Errorf("%s: Choose(%v, cap=%d) = index %d, want %d", tc.name, tc.heights, int(tc.ceiling), got.Index, tc.want)
		}
	}
}
