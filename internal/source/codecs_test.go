package source

import "testing"

func TestDeclaresVideoReadsTheSampleEntry(t *testing.T) {
	for _, tc := range []struct {
		codecs string
		want   bool
	}{
		{"avc1.640028,mp4a.40.2", true},
		{"mp4a.40.2, HVC1.1.6.L120.90", true},
		{"mp4a.40.2", false},
		{"avc1x.640028", false},
	} {
		if got := DeclaresVideo(tc.codecs); got != tc.want {
			t.Errorf("DeclaresVideo(%q) = %v, want %v", tc.codecs, got, tc.want)
		}
	}
}
