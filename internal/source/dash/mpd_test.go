package dash

import (
	"testing"

	"github.com/stupside/castor/internal/source"
)

// Real ffmpeg manifest: one adaptation set per video, separate audio.
const ffmpegPresentation = `<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" profiles="urn:mpeg:dash:profile:isoff-live:2011"
     type="static" mediaPresentationDuration="PT1H30M5.5S" minBufferTime="PT4.0S">
  <Period id="0" start="PT0.0S">
    <AdaptationSet id="0" contentType="video" maxWidth="1920" maxHeight="1080">
      <Representation id="0" mimeType="video/mp4" codecs="avc1.640028" bandwidth="6941000" width="1920" height="1080"/>
    </AdaptationSet>
    <AdaptationSet id="1" contentType="video" maxWidth="1280" maxHeight="720">
      <Representation id="1" mimeType="video/mp4" codecs="avc1.64001f" bandwidth="2400000" width="1280" height="720"/>
    </AdaptationSet>
    <AdaptationSet id="2" contentType="audio">
      <Representation id="2" mimeType="audio/mp4" codecs="mp4a.40.2" bandwidth="128000"/>
    </AdaptationSet>
  </Period>
</MPD>`

// Measured height outranks declared; a count mismatch pairs nothing.
func TestDeclaredRatesPairWithMeasuredHeightsOrNotAtAll(t *testing.T) {
	measured := []source.Rendition{{Index: 0, Height: 1080}, {Index: 1, Height: 720}}
	declared := []source.Rendition{{Index: 0, Height: 1088, Bitrate: 6941000}, {Index: 1, Height: 720, Bitrate: 2400000}}

	got := mergeDeclared(measured, declared)
	if got[0].Height != 1080 || got[0].Bitrate != 6941000 || got[1].Bitrate != 2400000 {
		t.Errorf("merged = %+v, want measured heights with declared rates", got)
	}
	if short := mergeDeclared(measured, declared[:1]); short[0].Bitrate != 0 {
		t.Error("a representation count disagreeing with the probed streams was paired anyway")
	}
}
