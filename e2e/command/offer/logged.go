package offer

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/command"
	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Logged only prints the stream's link to the console, never requesting it, as in `stream: {logged: '[hls] attaching source %s'}`.
type Logged struct{}

func (Logged) Name() string { return "logged" }

func (Logged) Build(settings yaml.Node) (command.Offer, error) {
	var line string
	if err := strategy.Decode(settings, &line); err != nil {
		return nil, fmt.Errorf("logged: %w", err)
	}
	if strings.Count(line, "%s") != 1 {
		return nil, fmt.Errorf("logged: %q: want the console line with one %%s where the stream's link goes", line)
	}
	return logged{line: line}, nil
}

type logged struct{ line string }

func (logged) Name() string { return "logged" }

func (l logged) Mount(_ *testing.T, _ *http.ServeMux, src *origin.Origin) (template.HTML, template.JS) {
	quoted, _ := json.Marshal(strings.Replace(l.line, "%s", src.URL, 1))
	return "", template.JS("console.log(" + string(quoted) + ");")
}
