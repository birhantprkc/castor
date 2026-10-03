package access

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
	"github.com/stupside/castor/e2e/strategy"
)

// SignsSegments builds a behaviour listing segments under signatures that lapse, as in `signs-segments: {ttl: 30s}`.
type SignsSegments struct{}

func (SignsSegments) Name() string { return "signs-segments" }

func (SignsSegments) Build(settings yaml.Node) (origin.Behaviour, error) {
	var s signs
	if err := strategy.Decode(settings, &s); err != nil {
		return nil, fmt.Errorf("signs-segments: %w", err)
	}
	if s.TTL <= 0 {
		return nil, errors.New("signs-segments: want a positive ttl")
	}
	return s, nil
}

type signs struct {
	TTL time.Duration `yaml:"ttl"`
}

func (signs) Name() string { return "signs-segments" }

func (s signs) Wrap(next http.Handler, p origin.Published) http.Handler {
	// Each fetch signs anew, so the playlist is always rewritten whole.
	playlists := serving.RewritesPlaylists(next, func(playlist string) (string, bool) {
		return sign(playlist, p.SegmentExt, time.Now().Add(s.TTL)), true
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.IsSegment(r) {
			exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
			if err != nil || exp < time.Now().UnixMilli() {
				http.Error(w, "signature expired", http.StatusForbidden)
				return
			}
		}
		playlists.ServeHTTP(w, r)
	})
}

// sign appends the expiry to every segment a playlist lists; renditions a master lists are signed when fetched.
func sign(playlist, segmentExt string, until time.Time) string {
	var out strings.Builder
	for line := range strings.Lines(playlist) {
		uri := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(uri, "#") || path.Ext(uri) != segmentExt {
			out.WriteString(line)
			continue
		}
		fmt.Fprintf(&out, "%s?exp=%d%s", uri, until.UnixMilli(), line[len(uri):])
	}
	return out.String()
}
