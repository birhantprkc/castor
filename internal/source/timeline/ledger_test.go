package timeline

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// listed is a window of one-second segments named after their origin sequence, as an origin numbering from first publishes them.
func listed(prefix string, first, last int64) Window {
	var w Window
	for n := first; n <= last; n++ {
		w.Segments = append(w.Segments, Segment{
			URI:      fmt.Sprintf("https://origin.example/%s%03d.ts", prefix, n),
			Duration: time.Second,
			Place:    Place{Start: n, End: n + 1},
		})
	}
	return w
}

// playlist is what the ledger renders, one line per URI or tag.
func playlist(l *Ledger) []string {
	return strings.Split(strings.TrimSpace(string(l.Render(nil))), "\n")
}

func count(lines []string, prefix string) int {
	n := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

func TestAnEncoderThatNumbersFromZeroAgainKeepsTheTimelineMovingForward(t *testing.T) {
	var l Ledger
	l.Merge(listed("a", 0, 9))
	restarted := listed("b", 0, 2)
	if got := l.Merge(restarted); got.Restarts != 1 {
		t.Fatalf("merge after a restart = %+v, want one restart", got)
	}
	lines := playlist(&l)
	if got := count(lines, "#EXT-X-MEDIA-SEQUENCE:3"); got != 1 {
		t.Errorf("the sequence did not move forward past the restart:\n%s", l.Render(nil))
	}
	if got := count(lines, "#EXT-X-DISCONTINUITY"); got != 1 {
		t.Errorf("marked %d seams, want the one restart:\n%s", got, l.Render(nil))
	}
	if !strings.Contains(string(l.Render(nil)), "#EXT-X-DISCONTINUITY\n#EXTINF:1.000000,\nhttps://origin.example/b000.ts") {
		t.Errorf("the seam is not in front of the restarted encoder's first segment:\n%s", l.Render(nil))
	}
}

func TestAStaleEdgeServingAnOlderWindowAddsNothing(t *testing.T) {
	var l Ledger
	l.Merge(listed("a", 0, 9))
	if got := l.Merge(listed("a", 4, 7)); got != (Merged{}) {
		t.Errorf("a stale window merged as %+v, want nothing", got)
	}
	if got := count(playlist(&l), "#EXT-X-DISCONTINUITY"); got != 0 {
		t.Errorf("a stale window opened %d seams", got)
	}
}

func TestSegmentsTheOriginSkippedAreASeamNotASilence(t *testing.T) {
	var l Ledger
	l.Merge(listed("a", 0, 3))
	if got := l.Merge(listed("a", 7, 9)); got.Gaps != 1 {
		t.Errorf("merge past lost segments = %+v, want one gap", got)
	}
	if got := count(playlist(&l), "#EXT-X-DISCONTINUITY"); got != 1 {
		t.Errorf("marked %d seams at the loss, want 1", got)
	}
}

func TestASlidingWindowOnlyAppendsWhatIsNew(t *testing.T) {
	var l Ledger
	for first := int64(0); first < 20; first += 2 {
		l.Merge(listed("a", first, first+5))
	}
	lines := playlist(&l)
	if got := count(lines, "#EXT-X-DISCONTINUITY"); got != 0 {
		t.Errorf("a window sliding forward opened %d seams", got)
	}
	if lines[len(lines)-1] != "https://origin.example/a023.ts" {
		t.Errorf("the playlist ends at %q, want the origin's edge", lines[len(lines)-1])
	}
}

func TestTheLedgerKeepsAsMuchAsTheOriginsLongestWindow(t *testing.T) {
	var l Ledger
	l.Merge(listed("a", 0, 9))
	l.Merge(listed("b", 0, 0))
	for n := int64(1); n <= 12; n++ {
		l.Merge(listed("b", n, n))
	}
	lines := playlist(&l)
	if got := count(lines, "#EXTINF"); got != 10 {
		t.Errorf("kept %d segments, want the origin's longest window of 10", got)
	}
	if !strings.Contains(string(l.Render(nil)), "#EXT-X-DISCONTINUITY-SEQUENCE:1\n") {
		t.Errorf("the trimmed seam is not counted in EXT-X-DISCONTINUITY-SEQUENCE:\n%s", l.Render(nil))
	}
	if !strings.Contains(string(l.Render(nil)), "#EXT-X-MEDIA-SEQUENCE:13\n") {
		t.Errorf("the sequence did not move forward with the trim:\n%s", l.Render(nil))
	}
}

func TestAStartMeasuredFromTheOriginsFirstSegmentIsMovedOntoTheLedgers(t *testing.T) {
	var l Ledger
	l.Merge(listed("a", 0, 9))
	w := listed("a", 3, 8)
	w.Start = &Start{Offset: 2 * time.Second, Precise: true}
	l.Merge(w)
	if !strings.Contains(string(l.Render(nil)), "#EXT-X-START:TIME-OFFSET=5.000000,PRECISE=YES\n") {
		t.Errorf("the start was not rebased by the three segments the ledger holds before the origin's first:\n%s", l.Render(nil))
	}
	w.Start = &Start{Offset: 0}
	l.Merge(w)
	if !strings.Contains(string(l.Render(nil)), "#EXT-X-START:TIME-OFFSET=3.000000\n") {
		t.Errorf("a start at the origin's first segment was lost:\n%s", l.Render(nil))
	}
}

func TestAClosedOriginEndsThePlaylistAndMergesNoMore(t *testing.T) {
	var l Ledger
	w := listed("a", 0, 3)
	w.Closed = true
	l.Merge(w)
	before := string(l.Render(nil))
	l.Merge(listed("a", 4, 6))
	if after := string(l.Render(nil)); after != before {
		t.Errorf("merged segments after the origin closed:\n%s", after)
	}
	if lines := playlist(&l); lines[len(lines)-1] != "#EXT-X-ENDLIST" {
		t.Errorf("a closed origin's playlist does not end:\n%s", l.Render(nil))
	}
}

func TestKeysAndInitSectionsAreWrittenWhereTheyChange(t *testing.T) {
	var l Ledger
	w := listed("a", 0, 3)
	for i := range w.Segments {
		w.Segments[i].Map = &Map{URI: "https://origin.example/init.mp4", Range: Range{Offset: 0, Length: 720}}
		w.Segments[i].Range = Range{Offset: int64(720 + i*1000), Length: 1000}
		w.Segments[i].Key = Key{Method: "AES-128", URI: "https://origin.example/k", IV: fmt.Sprintf("0x%032x", i)}
	}
	w.Segments[3].Key = Key{}
	l.Merge(w)
	lines := playlist(&l)
	if got := count(lines, "#EXT-X-MAP"); got != 1 {
		t.Errorf("wrote %d init sections for one unchanged init", got)
	}
	if got := count(lines, "#EXT-X-KEY:METHOD=AES-128"); got != 3 {
		t.Errorf("wrote %d keys, want one per distinct IV", got)
	}
	for _, want := range []string{
		`#EXT-X-MAP:URI="https://origin.example/init.mp4",BYTERANGE="720@0"`,
		"#EXT-X-BYTERANGE:1000@1720",
		`#EXT-X-KEY:METHOD=AES-128,URI="https://origin.example/k",IV=0x00000000000000000000000000000002`,
		"#EXT-X-KEY:METHOD=NONE",
	} {
		if count(lines, want) != 1 {
			t.Errorf("missing %q in:\n%s", want, l.Render(nil))
		}
	}
}

func TestPeriodsAreSeamsAndOnesAlreadyPlayedAreNotReplayed(t *testing.T) {
	period := func(name string, from, to int64) []Segment {
		w := listed(name, from, to)
		for i := range w.Segments {
			w.Segments[i].Place.Period = name
		}
		return w.Segments
	}
	var l Ledger
	l.Merge(Window{Segments: append(period("film", 0, 3), period("ad", 0, 1)...)})
	// A live MPD that dropped the film Period and lists the ad and what follows.
	l.Merge(Window{Segments: append(period("ad", 0, 1), period("film2", 0, 2)...)})
	lines := playlist(&l)
	if got := count(lines, "https://origin.example/ad000.ts"); got != 1 {
		t.Errorf("the ad Period was published %d times, want once:\n%s", got, l.Render(nil))
	}
	if lines[len(lines)-1] != "https://origin.example/film2002.ts" {
		t.Errorf("the playlist does not end at the next Period's edge:\n%s", l.Render(nil))
	}
	if got := count(lines, "#EXT-X-DISCONTINUITY"); got != 2 {
		t.Errorf("marked %d seams, want one per Period boundary", got)
	}
}

func TestAStaleWindowFromAPeriodAlreadyPlayedIsNotReplayed(t *testing.T) {
	var l Ledger
	film, ad := listed("film", 0, 1), listed("ad", 0, 1)
	for i := range 2 {
		film.Segments[i].Place.Period, ad.Segments[i].Place.Period = "film", "ad"
	}
	l.Merge(film)
	l.Merge(ad)
	// An edge still listing the film after castor trimmed it.
	before := string(l.Render(nil))
	l.Merge(Window{Segments: film.Segments[1:]})
	if after := string(l.Render(nil)); after != before {
		t.Errorf("replayed segments of a Period already played:\n%s", after)
	}
}

func TestAGapTheOriginDeclaredIsASeamButNotALoss(t *testing.T) {
	var l Ledger
	l.Merge(listed("a", 0, 3))
	after := listed("a", 5, 7)
	after.Segments[0].Seam = true
	if got := l.Merge(after); got.Gaps != 0 {
		t.Errorf("a declared gap counted as %d lost, want none", got.Gaps)
	}
	if got := count(playlist(&l), "#EXT-X-DISCONTINUITY"); got != 1 {
		t.Errorf("marked %d seams, want the declared one", got)
	}
}

func TestTheTargetDurationNeverShrinks(t *testing.T) {
	var l Ledger
	long := listed("a", 0, 0)
	long.Segments[0].Duration = 6 * time.Second
	l.Merge(long)
	for n := int64(1); n <= 3; n++ {
		l.Merge(listed("a", n, n))
	}
	if !strings.Contains(string(l.Render(nil)), "#EXT-X-TARGETDURATION:6\n") {
		t.Errorf("the target shrank once the long segment was trimmed:\n%s", l.Render(nil))
	}
}
