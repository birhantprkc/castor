package playlist

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
	"github.com/stupside/castor/e2e/strategy"
)

// Event builds a behaviour typing media playlists as an EVENT that starts at an offset, as in `event: {offset: 0}`; list it before live, which strips the type.
type Event struct{}

func (Event) Name() string { return "event" }

func (Event) Build(settings yaml.Node) (origin.Behaviour, error) {
	var e event
	if err := strategy.Decode(settings, &e); err != nil {
		return nil, fmt.Errorf("event: %w", err)
	}
	if math.IsNaN(e.Offset) || math.IsInf(e.Offset, 0) {
		return nil, fmt.Errorf("event: want a finite offset in seconds, negative counting from the end")
	}
	return e, nil
}

type event struct {
	Offset float64 `yaml:"offset"`
}

func (event) Name() string { return "event" }

func (e event) Wrap(next http.Handler, _ origin.Published) http.Handler {
	return serving.RewritesPlaylists(next, func(playlist string) (string, bool) {
		if !strings.Contains(playlist, "#EXTINF") {
			return "", false
		}
		var out strings.Builder
		for line := range strings.Lines(playlist) {
			if strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE") || strings.HasPrefix(line, "#EXT-X-START") {
				continue
			}
			out.WriteString(line)
			if strings.HasPrefix(line, "#EXTM3U") {
				fmt.Fprintf(&out, "#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-START:TIME-OFFSET=%s,PRECISE=YES\n", strconv.FormatFloat(e.Offset, 'f', -1, 64))
			}
		}
		return out.String(), true
	})
}
