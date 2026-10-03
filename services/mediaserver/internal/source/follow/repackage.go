package follow

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/source/index"
)

// Repackage copies one fMP4 fragment, its init section ahead of it, into MPEG-TS.
type Repackage func(ctx context.Context, fmp4 io.Reader, ts io.Writer) error

// Repackager copies one fMP4 fragment, its init section ahead of it, into MPEG-TS, where a picture's parameters travel in-band.
func Repackager(ffmpegPath string) Repackage {
	args := slices.Concat([]string{
		"-hide_banner", "-nostats", "-v", "error",
		"-f", ffmpeg.FormatMP4, "-i", ffmpeg.StdinPipe,
		"-map", "0:v?", "-map", "0:a?", "-c", "copy",
		// The fragment's own decode times, so consecutive fragments stay one timeline.
		"-copyts",
		// One fixed lift for every fragment: a per-fragment shift of priming samples below zero would overlap its neighbour.
		"-avoid_negative_ts", "disabled", "-output_ts_offset", "10",
	}, ffmpeg.MPEGTSResetArgs, []string{"-f", ffmpeg.FormatMPEGTS, ffmpeg.StdoutPipe})
	return func(ctx context.Context, fmp4 io.Reader, ts io.Writer) error {
		if said, err := ffmpeg.Run(ctx, ffmpegPath, args, fmp4, ts); err != nil {
			return fmt.Errorf("repackaging to MPEG-TS: %w: %s", err, said)
		}
		return nil
	}
}

// carried is every sample entry MPEG-TS carries; an encrypted entry (encv, enca) is not among them.
var carried = []string{"avc1", "avc3", "hvc1", "hev1", "mp4a", "ac-3", "ec-3", "Opus", ".mp3"}

// tsCarries reports an init section whose every track MPEG-TS can carry.
func tsCarries(init []byte) bool {
	entries := index.SampleEntries(init)
	return len(entries) > 0 && !slices.ContainsFunc(entries, func(e string) bool { return !slices.Contains(carried, e) })
}

// repackagedExtension names a segment castor serves as MPEG-TS, so the reader probes it as one.
const repackagedExtension = ".ts"
