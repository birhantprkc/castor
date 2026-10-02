package wire

import (
	"log/slog"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// LogLevel is level on the wire.
func LogLevel(level slog.Level) castorv1.LogLevel {
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

// FromLogLevel is the level the wire names.
func FromLogLevel(l castorv1.LogLevel) slog.Level {
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

// Log is r on the wire, its attributes flattened to text under dotted group keys.
func Log(r slog.Record, attrs []slog.Attr) *castorv1.LogLine {
	var flat []*castorv1.LogLine_Attr
	add := func(a slog.Attr) bool { flat = flatten(flat, "", a); return true }
	for _, a := range attrs {
		add(a)
	}
	r.Attrs(add)
	return &castorv1.LogLine{Level: LogLevel(r.Level), Message: r.Message, Attrs: flat}
}

func flatten(into []*castorv1.LogLine_Attr, prefix string, a slog.Attr) []*castorv1.LogLine_Attr {
	v := a.Value.Resolve()
	key := prefix + a.Key
	if v.Kind() != slog.KindGroup {
		return append(into, &castorv1.LogLine_Attr{Key: key, Value: v.String()})
	}
	for _, g := range v.Group() {
		into = flatten(into, key+".", g)
	}
	return into
}

// FromLog is the record a watcher re-logs, stamped when it arrived.
func FromLog(l *castorv1.LogLine) slog.Record {
	level := FromLogLevel(l.GetLevel())
	r := slog.NewRecord(time.Now(), level, l.GetMessage(), 0)
	for _, a := range l.GetAttrs() {
		r.AddAttrs(slog.String(a.GetKey(), a.GetValue()))
	}
	return r
}
