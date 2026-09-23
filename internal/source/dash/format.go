package dash

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// Format is DASH as a source.Format: the other source shape that publishes a choice.
type Format struct{}

var _ source.Format = Format{}

func (Format) Name() string { return "dash" }

func (Format) Identity() source.Identity {
	return source.Identity{ContentType: media.DASH, Extensions: []string{".mpd"}, MIMETypes: []string{"application/dash+xml"}}
}

// Resolve reads the manifest, then narrows the opaque graph to the representation the ceiling admits.
func (Format) Resolve(ctx context.Context, env source.Env, s source.Subject) (source.Resolution, error) {
	stream, origin := s.Stream, s.Origin
	rungs := representations(stream.Probe)
	if doc, ok := readPresentation(ctx, env.Playlists, stream); ok {
		origin.Live = doc.Live
		// A manifest publishing no runtime states an absence, which must not erase a measured one.
		if doc.Duration > 0 {
			origin.Duration = doc.Duration
		}
		origin.Encrypted = doc.Encrypted
		rungs = mergeDeclared(rungs, doc.Renditions)
	}
	resolved, err := source.Opaque(stream, origin)
	if err != nil {
		return resolved, err
	}
	if len(rungs) == 0 {
		reportNoRendition(ctx, stream, env.MaxHeight)
		return resolved, nil
	}
	resolved.Origin.Renditions = rungs
	chosen := resolved.Origin.Choose(env.MaxHeight, byHeight)
	resolved.Rendition = chosen
	source.ReportRendition(ctx, chosen, resolved.Origin, env.MaxHeight)

	program, err := source.Narrow(resolved.Program, chosen, resolved.Program)
	if err != nil {
		return resolved, fmt.Errorf("binding the chosen representation: %w", err)
	}
	resolved.Program = program
	return resolved, nil
}

// signature is the root element every presentation is written under.
const signature = "<MPD"

// Recognize reads a presentation written as a document whose root element is a required signature.
func (Format) Recognize(body string) (source.Ladder, []string) {
	if !strings.Contains(body, signature) {
		return source.LadderUnknown, nil
	}
	return source.LadderMultivariant, references(body)
}

var (
	referenceAttribute = regexp.MustCompile(`(?:media|initialization|sourceURL)="([^"$]*)"`)
	referenceBase      = regexp.MustCompile(`<BaseURL[^>]*>([^<]+)</BaseURL>`)
)

func references(body string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{referenceAttribute, referenceBase} {
		for _, match := range re.FindAllStringSubmatch(body, -1) {
			if ref := strings.TrimSpace(match[1]); ref != "" {
				out = append(out, ref)
			}
		}
	}
	return out
}
