package probe

import (
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

func TestClassifyReach(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		want   media.Reach
	}{
		{"signed.m3u8: Server returned 404 Not Found\n", media.ReachRefused},
		{"[http @ 0x14] HTTP error 429 Too Many Requests\n", media.ReachUnproven},
		{"signed.m3u8: Server returned 503 Service Unavailable\n", media.ReachUnproven},
	} {
		if got := classifyReach(tc.stderr); got != tc.want {
			t.Errorf("classifyReach(%q) = %s, want %s", tc.stderr, got, tc.want)
		}
	}
}
