package cast

import (
	"log/slog"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Lines is which of a cast's own lines its watch asks for.
type Lines int

const (
	// LinesNone asks for none: servers in this process already write them.
	LinesNone Lines = iota
	LinesWarn
	LinesDebug
)

func (l Lines) level() *castorv1.LogLevel {
	switch l {
	case LinesWarn:
		return new(castorv1.LogLevel_LOG_LEVEL_WARN)
	case LinesDebug:
		return new(castorv1.LogLevel_LOG_LEVEL_DEBUG)
	}
	return nil
}

// FromServer marks the lines a server wrote, apart from the command's own.
func FromServer(h slog.Handler) slog.Handler {
	return h.WithAttrs([]slog.Attr{slog.String("from", "server")})
}

// record is a cast's line, re-logged here as it arrived.
func record(l *castorv1.LogLine) slog.Record {
	r := slog.NewRecord(time.Now(), level(l.GetLevel()), l.GetMessage(), 0)
	for _, a := range l.GetAttrs() {
		r.AddAttrs(slog.String(a.GetKey(), a.GetValue()))
	}
	return r
}

func level(l castorv1.LogLevel) slog.Level {
	switch l {
	case castorv1.LogLevel_LOG_LEVEL_DEBUG:
		return slog.LevelDebug
	case castorv1.LogLevel_LOG_LEVEL_INFO:
		return slog.LevelInfo
	case castorv1.LogLevel_LOG_LEVEL_WARN:
		return slog.LevelWarn
	}
	return slog.LevelError
}
