package ffmpeg

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Binary is what the ffmpeg castor runs understands beyond what every version castor supports shares.
type Binary struct {
	// Catchup is a binary (ffmpeg 8 on) that holds a read fallen behind to its readrate unless told a catch-up rate; older ones catch up unbounded.
	Catchup bool
}

var inspected sync.Map

// Inspect reads what ffmpegPath understands, once per binary.
func Inspect(ffmpegPath string) Binary {
	if b, ok := inspected.Load(ffmpegPath); ok {
		return b.(Binary)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	help, _ := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-h", "long").Output()
	b := Binary{Catchup: strings.Contains(string(help), "-readrate_catchup")}
	inspected.Store(ffmpegPath, b)
	return b
}
