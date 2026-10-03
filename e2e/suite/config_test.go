package suite

import (
	"maps"
	"testing"

	"go.yaml.in/yaml/v3"
)

// configDoc is the case's castor section plus what the command needs, with the device pinned to the receiver.
func configDoc(t *testing.T, section yaml.Node, needs map[string]any, deviceType, host string) map[string]any {
	t.Helper()
	doc := map[string]any{}
	if err := decodeSection(section, &doc); err != nil {
		t.Fatalf("castor section: %v", err)
	}
	return with(t, with(t, doc, needs), map[string]any{"device": map[string]any{"type": deviceType, "host": host}})
}

// with is doc with sections merged into it; a key the case already set fails the case rather than being overridden.
func with(t *testing.T, doc, sections map[string]any) map[string]any {
	t.Helper()
	return merge(t, "castor", doc, sections)
}

func merge(t *testing.T, path string, doc, over map[string]any) map[string]any {
	t.Helper()
	out := maps.Clone(doc)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range over {
		at := path + "." + key
		have, taken := out[key]
		if !taken {
			out[key] = value
			continue
		}
		haveSection, ok := have.(map[string]any)
		section, isSection := value.(map[string]any)
		if !ok || !isSection {
			t.Fatalf("%s is set by the suite for the receiver, command or topology: remove it from the castor section", at)
		}
		out[key] = merge(t, at, haveSection, section)
	}
	return out
}

// decodeSection reads castor's section as castor would, unknown keys included; an absent section leaves v as it is.
func decodeSection(section yaml.Node, v any) error {
	if section.Kind == 0 {
		return nil
	}
	return section.Decode(v)
}
