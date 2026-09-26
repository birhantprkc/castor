package access

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case path.Ext(r.URL.Path) == ".m3u8":
			// Each fetch signs anew, so a ranged or conditional read of the file on disk would lie.
			whole := r.Clone(r.Context())
			for _, h := range []string{"Range", "If-Range", "If-Modified-Since", "If-None-Match"} {
				whole.Header.Del(h)
			}
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, whole)
			maps.Copy(w.Header(), rec.Header())
			for _, h := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "Etag"} {
				w.Header().Del(h)
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(sign(rec.Body.String(), p.SegmentExt, time.Now().Add(s.TTL)))
		case p.IsSegment(r):
			exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
			if err != nil || exp < time.Now().UnixMilli() {
				http.Error(w, "signature expired", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// sign appends the expiry to every segment a playlist lists; renditions a master lists are signed when fetched.
func sign(playlist, segmentExt string, until time.Time) []byte {
	var out bytes.Buffer
	for line := range strings.Lines(playlist) {
		uri := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(uri, "#") || path.Ext(uri) != segmentExt {
			out.WriteString(line)
			continue
		}
		fmt.Fprintf(&out, "%s?exp=%d%s", uri, until.UnixMilli(), line[len(uri):])
	}
	return out.Bytes()
}
