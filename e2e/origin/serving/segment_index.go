package serving

import (
	"path"
	"strconv"
	"strings"
)

// SegmentIndex is the trailing number of a segment's name, as in seg_010.ts or seg_1_010.ts.
func SegmentIndex(p string) (int, bool) {
	base := strings.TrimSuffix(path.Base(p), path.Ext(p))
	start := strings.LastIndexFunc(base, func(r rune) bool { return r < '0' || r > '9' }) + 1
	n, err := strconv.Atoi(base[start:])
	return n, err == nil
}
