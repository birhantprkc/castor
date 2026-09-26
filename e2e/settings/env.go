package settings

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// Env carries the document as CASTOR_SECTION__FIELD variables with no file anywhere, as users configure containers (#36, #64).
type Env struct{}

func (Env) Name() string { return "env" }

func (Env) Carry(t *testing.T, doc map[string]any) (Launch, error) {
	env, err := flatten("CASTOR", doc)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Env: env, Dir: t.TempDir()}, nil
}

// flatten names each leaf by its path, sections joined by a double underscore, in a stable order.
func flatten(prefix string, doc map[string]any) ([]string, error) {
	var env []string
	for _, key := range slices.Sorted(maps.Keys(doc)) {
		name := prefix + "_" + strings.ToUpper(key)
		switch value := doc[key].(type) {
		case map[string]any:
			nested, err := flatten(name+"_", value)
			if err != nil {
				return nil, err
			}
			env = append(env, nested...)
		case []any:
			return nil, fmt.Errorf("%s is a list, which castor's environment cannot carry: use config file", name)
		default:
			env = append(env, fmt.Sprintf("%s=%v", name, value))
		}
	}
	return env, nil
}
