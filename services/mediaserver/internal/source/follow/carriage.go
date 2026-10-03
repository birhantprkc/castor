package follow

import (
	"slices"

	"github.com/stupside/castor/services/mediaserver/internal/source/index"
)

// carried is every sample entry MPEG-TS carries; an encrypted entry (encv, enca) is not among them.
var carried = []string{"avc1", "avc3", "hvc1", "hev1", "mp4a", "ac-3", "ec-3", "Opus", ".mp3"}

// tsCarries reports an init section whose every track MPEG-TS can carry.
func tsCarries(init []byte) bool {
	entries := index.SampleEntries(init)
	return len(entries) > 0 && !slices.ContainsFunc(entries, func(e string) bool { return !slices.Contains(carried, e) })
}

// repackagedExtension names a segment castor serves as MPEG-TS, so the reader probes it as one.
const repackagedExtension = ".ts"
