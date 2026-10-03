package wire

import (
	"log/slog"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Level is level on the wire.
func Level(level slog.Level) castorv1.LogLevel {
	switch {
	case level <= slog.LevelDebug:
		return castorv1.LogLevel_LOG_LEVEL_DEBUG
	case level <= slog.LevelInfo:
		return castorv1.LogLevel_LOG_LEVEL_INFO
	case level <= slog.LevelWarn:
		return castorv1.LogLevel_LOG_LEVEL_WARN
	}
	return castorv1.LogLevel_LOG_LEVEL_ERROR
}

// FromLevel is the level the wire names.
func FromLevel(l castorv1.LogLevel) slog.Level {
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
