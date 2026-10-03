package ffmpeg

import (
	"bytes"
	"context"
	"time"
)

// Binary is what the ffmpeg castor runs understands beyond what every version castor supports shares.
type Binary struct {
	// Catchup is a binary (ffmpeg 8 on) that holds a read fallen behind to its readrate unless told a catch-up rate; older ones catch up unbounded.
	Catchup bool
}

// Inspect reads what ffmpegPath understands.
func Inspect(ffmpegPath string) Binary {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var help bytes.Buffer
	_, _ = Run(ctx, ffmpegPath, []string{"-hide_banner", "-h", "long"}, nil, &help)
	return Binary{Catchup: bytes.Contains(help.Bytes(), []byte("-readrate_catchup"))}
}
