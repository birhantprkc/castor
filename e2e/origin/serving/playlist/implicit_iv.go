package playlist

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
)

// ImplicitIV drops the IV from every key, leaving each segment's media sequence number to stand in for it, as the specification allows.
type ImplicitIV struct{}

func (ImplicitIV) Name() string { return "implicit-iv" }

var ivAttribute = regexp.MustCompile(`,IV=0[xX][0-9a-fA-F]+`)

func (ImplicitIV) Wrap(next http.Handler, _ origin.Published) http.Handler {
	return serving.RewritesDocs(".m3u8", next, func(playlist string) (string, bool, error) {
		// A media playlist with no IV to drop would test nothing.
		if strings.Contains(playlist, "#EXTINF") && !ivAttribute.MatchString(playlist) {
			return "", false, errors.New("implicit-iv: the playlist names no IV to leave implicit")
		}
		return ivAttribute.ReplaceAllString(playlist, ""), true, nil
	})
}
