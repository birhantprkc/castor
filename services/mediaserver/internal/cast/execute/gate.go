package execute

import (
	"context"

	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
)

func readMonitor(reader *pull, m health.Monitor) health.Monitor {
	m.Producer = reader
	m.Telemetry = reader
	m.Landed = reader.spool.Size
	m.Headroom = reader.judgedPace()
	return m
}

// gate holds a buffered cast until its read has proven it can deliver, and its transcription leads.
func gate(ctx context.Context, buf *buffered) error {
	return health.Watch(ctx, readMonitor(buf.reader, health.Monitor{
		Subject: "playback gate",
		Phase:   health.Reading,
		Lead:    buf.burn,
	}))
}
