package wire

import (
	"log/slog"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Line is r on the wire, its attributes flattened to text under dotted group keys.
func Line(r slog.Record, attrs []slog.Attr) *castorv1.LogLine {
	var flat []*castorv1.LogLine_Attr
	add := func(a slog.Attr) bool { flat = flatten(flat, "", a); return true }
	for _, a := range attrs {
		add(a)
	}
	r.Attrs(add)
	return &castorv1.LogLine{Level: Level(r.Level), Message: r.Message, Attrs: flat}
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
