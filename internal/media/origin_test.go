package media

import (
	"net/url"
	"testing"
	"time"
)

func rendition(path string, bitrate Bitrate, height int) Rendition {
	return Rendition{URL: &url.URL{Path: path}, Bitrate: bitrate, Height: height}
}

// TestOriginSole pins the fact the whole type was added for: whether the source gave
// castor anything to choose from. A media playlist and a single-rendition master both
// answer "no", and they must, because the synthetic single-variant entry that
// normalises the first shape is exactly what used to make them indistinguishable.
func TestOriginSole(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Origin
		want bool
	}{
		{"a document nobody read offered nothing", Origin{}, true},
		{
			"a media playlist normalises to one rendition, which is not a choice",
			Origin{Renditions: []Rendition{rendition("/media.m3u8", 0, 0)}},
			true,
		},
		{
			"the observed failure: one 4K rendition and no alternative",
			Origin{Renditions: []Rendition{rendition("/sole.m3u8", 17_000_000, 1600)}},
			true,
		},
		{
			"a real ladder is a choice",
			Origin{Renditions: []Rendition{rendition("/480.m3u8", 1_400_000, 480), rendition("/1080.m3u8", 6_200_000, 1080)}},
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.Sole(); got != tc.want {
				t.Errorf("Sole() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOriginLighter covers the ordering a degrade reads off the head, and the one
// exclusion that keeps a degrade from moving UP the ladder: a rung whose bitrate the
// source never declared is arithmetically below every ceiling and is evidence of
// nothing.
func TestOriginLighter(t *testing.T) {
	o := Origin{Renditions: []Rendition{
		rendition("/480", 1_400_000, 480),
		rendition("/undeclared", 0, 720),
		rendition("/1080", 6_200_000, 1080),
		rendition("/2160", 17_000_000, 2160),
	}}

	got := o.Lighter(6_200_000)
	if len(got) != 1 || got[0].URL.Path != "/480" {
		t.Fatalf("Lighter(6.2 Mbit/s) = %v, want only /480: the ceiling is exclusive and an undeclared rate is not evidence of being lighter", got)
	}

	got = o.Lighter(17_000_000)
	want := []string{"/1080", "/480"}
	if len(got) != len(want) {
		t.Fatalf("Lighter(17 Mbit/s) returned %d rungs, want %d", len(got), len(want))
	}
	for i, path := range want {
		if got[i].URL.Path != path {
			t.Errorf("rung %d = %q, want %q (heaviest first, so a degrade takes the head)", i, got[i].URL.Path, path)
		}
	}

	if got := o.Lighter(1_400_000); len(got) != 0 {
		t.Errorf("Lighter(the cheapest rung) = %v, want nothing: there is no rung below the bottom one", got)
	}
}

// TestOriginProjectedRuntime is the arithmetic that makes a starving cast
// actionable, plus the two shapes it must refuse rather than answer: a source that
// published no runtime, and a speed of zero, which projects everything to forever.
func TestOriginProjectedRuntime(t *testing.T) {
	feature := Origin{Duration: 2 * time.Hour}

	// 0.109x is the speed a real run reported: two hours of picture at eleven minutes
	// per hour of waiting.
	got, ok := feature.ProjectedRuntime(0.109)
	if !ok {
		t.Fatal("a known duration at a known speed must project")
	}
	if want := 18 * time.Hour; got < want || got > want+45*time.Minute {
		t.Errorf("ProjectedRuntime(0.109) = %s, want a little over %s", got, want)
	}

	if _, ok := feature.ProjectedRuntime(0); ok {
		t.Error("a speed of zero must refuse to project rather than answer forever")
	}
	if _, ok := (Origin{}).ProjectedRuntime(1); ok {
		t.Error("a source that published no duration has no runtime to project")
	}
	// Carries a duration deliberately: a sliding window whose EXTINF sum reached the
	// Origin describes the window, not the program, so liveness has to be refused on
	// its own account and not left to a zero duration to catch.
	if _, ok := (Origin{Live: true, Duration: 2 * time.Hour}).ProjectedRuntime(2); ok {
		t.Error("a live edge has no runtime to project, whatever duration reached it")
	}
}
