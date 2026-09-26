package dash

import (
	"net/http"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/sourcetest"
)

const mpdOpen = `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" `

// at is the presentation clock this many seconds after availabilityStartTime.
func at(seconds int) time.Time { return time.Date(2026, 1, 1, 0, 0, seconds, 0, time.UTC) }

func TestAnIdIsTheRepresentationOfTheKindTheInputReads(t *testing.T) {
	const body = mpdOpen + `type="static">
  <Period id="ad" duration="PT1S"><AdaptationSet contentType="video"><SegmentTemplate media="ad/$RepresentationID$/$Number$.m4s" duration="1"/>
    <Representation id="1" height="720"/></AdaptationSet></Period>
  <Period id="film" duration="PT2S">
    <AdaptationSet contentType="video"><SegmentTemplate media="f/$RepresentationID$/$Number$.m4s" duration="1"/><Representation id="0" height="360"/></AdaptationSet>
    <AdaptationSet contentType="audio"><SegmentTemplate media="f/$RepresentationID$/$Number$.m4s" duration="1"/><Representation id="1" codecs="mp4a.40.2"/></AdaptationSet>
  </Period></MPD>`
	w := window(t, map[string]string{"/live/manifest.mpd": body}, media.TrackAudio, "1", time.Time{})
	same(t, uris(w), []string{"https://cdn.example/live/f/1/1.m4s", "https://cdn.example/live/f/1/2.m4s"})
}

func TestALiveTimelineListsOnlySegmentsThatExist(t *testing.T) {
	const body = mpdOpen + `type="dynamic" availabilityStartTime="2026-01-01T00:00:00Z" timeShiftBufferDepth="PT60S">
  <Period id="past" start="PT0S" duration="PT10S"><AdaptationSet contentType="video">
    <SegmentTemplate timescale="1" media="a$Time$.m4s"><SegmentTimeline><S t="0" d="2" r="-1"/></SegmentTimeline></SegmentTemplate>
    <Representation id="v" height="360"/></AdaptationSet></Period>
  <Period id="now" start="PT10S"><AdaptationSet contentType="video">
    <SegmentTemplate timescale="1" media="b$Time$.m4s"><SegmentTimeline><S t="0" d="2" r="-1"/></SegmentTimeline></SegmentTemplate>
    <Representation id="v" height="360"/></AdaptationSet></Period></MPD>`
	// Fifteen seconds in, the ended Period holds five segments and the running one two finished and one in production.
	w := window(t, map[string]string{"/live/manifest.mpd": body}, media.TrackVideo, "v", at(15))
	same(t, uris(w), []string{
		"https://cdn.example/live/a0.m4s", "https://cdn.example/live/a2.m4s", "https://cdn.example/live/a4.m4s",
		"https://cdn.example/live/a6.m4s", "https://cdn.example/live/a8.m4s",
		"https://cdn.example/live/b0.m4s", "https://cdn.example/live/b2.m4s",
	})
}

func TestALiveDurationTemplateForgetsWhatTheBufferNoLongerKeeps(t *testing.T) {
	const body = mpdOpen + `type="dynamic" availabilityStartTime="2026-01-01T00:00:00Z" timeShiftBufferDepth="PT6S">
  <Period id="old" start="PT0S" duration="PT6S"><AdaptationSet contentType="video">
    <SegmentTemplate timescale="1" duration="2" media="old$Number$.m4s"/><Representation id="v" height="360"/></AdaptationSet></Period>
  <Period id="now" start="PT600S"><AdaptationSet contentType="video">
    <SegmentTemplate timescale="1" duration="2" media="now$Number$.m4s"/><Representation id="v" height="360"/></AdaptationSet></Period></MPD>`
	w := window(t, map[string]string{"/live/manifest.mpd": body}, media.TrackVideo, "v", at(605))
	same(t, uris(w), []string{"https://cdn.example/live/now1.m4s", "https://cdn.example/live/now2.m4s"})
}

func TestAnOffsetAvailabilityListsTheSegmentStillInProduction(t *testing.T) {
	const body = mpdOpen + `type="dynamic" availabilityStartTime="2026-01-01T00:00:00Z" timeShiftBufferDepth="PT60S">
  <Period start="PT0S"><AdaptationSet contentType="video">
    <SegmentTemplate timescale="1" duration="2" media="$Number$.m4s" availabilityTimeOffset="INF"/><Representation id="v" height="360"/></AdaptationSet></Period></MPD>`
	same(t, uris(window(t, map[string]string{"/live/manifest.mpd": body}, media.TrackVideo, "v", at(5))),
		[]string{"https://cdn.example/live/1.m4s", "https://cdn.example/live/2.m4s", "https://cdn.example/live/3.m4s"})
}

func TestALivePresentationWithNoStartIsRefused(t *testing.T) {
	const body = mpdOpen + `type="dynamic"><Period><AdaptationSet contentType="video">
    <SegmentTemplate timescale="1" duration="2" media="$Number$.m4s"/><Representation id="v" height="360"/></AdaptationSet></Period></MPD>`
	in := media.Input{ID: media.PrimaryInputID, URL: sourcetest.URL(t, "https://cdn.example/live/manifest.mpd"), Representation: "v"}
	f := Format{}.Timeline(source.Env{Client: &sourcetest.Playlist{Body: body, Status: http.StatusOK}}, in, media.TrackVideo)
	if _, err := f.Window(t.Context()); err == nil {
		t.Error("placed a live edge with no availabilityStartTime")
	}
}

func TestAListInheritsFromItsSetAttributeByAttribute(t *testing.T) {
	const body = mpdOpen + `type="static"><Period><AdaptationSet contentType="video">
  <SegmentList timescale="10" duration="20"><Initialization sourceURL="init.mp4"/></SegmentList>
  <Representation id="v" height="360"><SegmentList><SegmentURL media="1.m4s"/><SegmentURL media="2.m4s"/></SegmentList></Representation>
  </AdaptationSet></Period></MPD>`
	w := window(t, map[string]string{"/live/manifest.mpd": body}, media.TrackVideo, "v", time.Time{})
	if len(w.Segments) != 2 || w.Segments[1].Duration != 2*time.Second || w.Segments[1].Map == nil || w.Segments[1].Map.URI != "https://cdn.example/live/init.mp4" {
		t.Errorf("segments = %+v, want two 2s segments under the set's init", w.Segments)
	}
}

func TestATemplatesInitializationElementNamesItsInit(t *testing.T) {
	const body = mpdOpen + `type="static"><Period duration="PT2S"><AdaptationSet contentType="video">
  <SegmentTemplate media="$Number$.m4s" duration="1"><Initialization sourceURL="head.mp4"/></SegmentTemplate>
  <Representation id="v" height="360"/></AdaptationSet></Period></MPD>`
	w := window(t, map[string]string{"/live/manifest.mpd": body}, media.TrackVideo, "v", time.Time{})
	if len(w.Segments) != 2 || w.Segments[0].Map == nil || w.Segments[0].Map.URI != "https://cdn.example/live/head.mp4" {
		t.Errorf("segments = %+v, want the Initialization element's init", w.Segments)
	}
}

func TestAnIndexWithNoStatedInitTakesWhatPrecedesIt(t *testing.T) {
	index := sidx(0, 1000)
	film := make([]byte, 700)
	copy(film[600:], index)
	docs := map[string]string{
		"/live/manifest.mpd": mpdOpen + `type="static"><Period><AdaptationSet contentType="video" mimeType="video/mp4">
  <Representation id="v" height="360"><BaseURL>film.mp4</BaseURL><SegmentBase indexRange="600-651"/></Representation>
  </AdaptationSet></Period></MPD>`,
		"/live/film.mp4": string(film),
	}
	w := window(t, docs, media.TrackVideo, "v", time.Time{})
	if len(w.Segments) != 1 || w.Segments[0].Map == nil || w.Segments[0].Map.Range.Length != 600 {
		t.Errorf("segments = %+v, want the 600 bytes ahead of the index as the init", w.Segments)
	}
}

func TestARepresentationNamingNothingIsRefused(t *testing.T) {
	const body = mpdOpen + `type="static"><Period duration="PT2S"><AdaptationSet contentType="video"><Representation id="v" height="360"/></AdaptationSet></Period></MPD>`
	in := media.Input{ID: media.PrimaryInputID, URL: sourcetest.URL(t, "https://cdn.example/live/manifest.mpd"), Representation: "v"}
	f := Format{}.Timeline(source.Env{Client: &sourcetest.Playlist{Body: body, Status: http.StatusOK}}, in, media.TrackVideo)
	if _, err := f.Window(t.Context()); err == nil {
		t.Error("read the presentation itself as the representation's media")
	}
}

func TestThumbnailsAndTrickPlayNeverJoinTheLadder(t *testing.T) {
	const body = mpdOpen + `type="static" mediaPresentationDuration="PT1H"><Period>
  <AdaptationSet contentType="video"><Representation id="film" codecs="avc1.64001f" bandwidth="800000" height="360"/></AdaptationSet>
  <AdaptationSet contentType="image" mimeType="image/jpeg"><Representation id="tiles" bandwidth="1000" width="1600" height="900"/></AdaptationSet>
  <AdaptationSet contentType="video"><EssentialProperty schemeIdUri="http://dashif.org/guidelines/trickmode" value="1"/>
    <Representation id="trick" codecs="avc1.64001f" bandwidth="100000" height="720"/></AdaptationSet>
</Period></MPD>`
	resolved := resolveWith(t, body, source.Rendition{}, 1080)
	if len(resolved.Origin.Renditions) != 1 || resolved.Rendition.Representation != "film" {
		t.Errorf("ladder = %+v, want the film alone", resolved.Origin.Renditions)
	}
}

func TestALiveLadderIsTheOpenPeriods(t *testing.T) {
	const body = mpdOpen + `type="dynamic" availabilityStartTime="2026-01-01T00:00:00Z"><Period id="ad" start="PT0S" duration="PT600S">
  <AdaptationSet contentType="video"><Representation id="ad" codecs="avc1.64001f" bandwidth="4000000" height="1080"/></AdaptationSet></Period>
  <Period id="now" start="PT600S"><AdaptationSet contentType="video"><Representation id="now" codecs="avc1.64001f" bandwidth="1000000" height="720"/></AdaptationSet></Period></MPD>`
	if got := resolveWith(t, body, source.Rendition{}, 720).Rendition; got.Representation != "now" {
		t.Errorf("chosen %+v, want the running Period's 720p rung, not the finished ad's", got)
	}
}

func TestCalendarUnitsWrittenAsZerosStillReadAsADuration(t *testing.T) {
	runtime := func(stated string) time.Duration {
		body := mpdOpen + `type="static" mediaPresentationDuration="` + stated + `"><Period>
  <AdaptationSet contentType="video"><Representation id="v" codecs="avc1.64001f" bandwidth="800000" height="360"/></AdaptationSet></Period></MPD>`
		return resolveWith(t, body, source.Rendition{}, 1080).Origin.Duration
	}
	if got := runtime("P0Y0M0DT0H3M30S"); got != 210*time.Second {
		t.Errorf("P0Y0M0DT0H3M30S = %v, want 3m30s", got)
	}
	if got := runtime("P1M"); got != 0 {
		t.Errorf("P1M = %v, want nothing: a month has no fixed length", got)
	}
}

func TestAnIdReusedAcrossPeriodsIsReadAsTheOneChosen(t *testing.T) {
	const body = mpdOpen + `type="static">
  <Period id="ad" duration="PT1S"><AdaptationSet contentType="video"><SegmentTemplate media="ad/$RepresentationID$/$Number$.m4s" duration="1"/>
    <Representation id="0" height="1080"/></AdaptationSet></Period>
  <Period id="film" duration="PT4S"><AdaptationSet contentType="video"><SegmentTemplate media="f/$RepresentationID$/$Number$.m4s" duration="4"/>
    <Representation id="0" height="360"/><Representation id="1" height="720"/></AdaptationSet></Period>
  <Period id="ad2" duration="PT1S"><AdaptationSet contentType="video"><SegmentTemplate media="ad2/$RepresentationID$/$Number$.m4s" duration="1"/>
    <Representation id="x" height="1080"/><Representation id="y" height="360"/></AdaptationSet></Period>
</MPD>`
	w := window(t, map[string]string{"/live/manifest.mpd": body}, media.TrackVideo, "0", time.Time{})
	same(t, uris(w), []string{"https://cdn.example/live/ad/0/1.m4s", "https://cdn.example/live/f/0/1.m4s", "https://cdn.example/live/ad2/y/1.m4s"})
}
