package playlist

import "strings"

const streamInf = "#EXT-X-STREAM-INF:"

type attribute struct{ key, value string }

// attributes splits an HLS attribute list on the commas outside quoted values.
func attributes(list string) []attribute {
	var as []attribute
	pair := func(s string) attribute {
		key, value, _ := strings.Cut(s, "=")
		return attribute{key: key, value: value}
	}
	quoted, start := false, 0
	for i := range len(list) {
		switch {
		case list[i] == '"':
			quoted = !quoted
		case list[i] == ',' && !quoted:
			as = append(as, pair(list[start:i]))
			start = i + 1
		}
	}
	return append(as, pair(list[start:]))
}

func joined(as []attribute) string {
	pairs := make([]string, len(as))
	for i, a := range as {
		pairs[i] = a.key + "=" + a.value
	}
	return strings.Join(pairs, ",")
}
