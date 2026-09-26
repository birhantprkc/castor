// Package strategy binds the names a case is written in to the strategies a composition root lists.
package strategy

import (
	"fmt"
	"slices"
	"strings"
)

// Named is a strategy a case can pick by name.
type Named interface{ Name() string }

// Registry is an ordered set of strategies; order is precedence where more than one applies.
type Registry[T Named] []T

// Lookup returns the strategy called name, or an error listing the names there are.
func (r Registry[T]) Lookup(name string) (T, error) {
	if i := slices.IndexFunc(r, func(s T) bool { return s.Name() == name }); i >= 0 {
		return r[i], nil
	}
	var zero T
	names := make([]string, len(r))
	for i, s := range r {
		names[i] = s.Name()
	}
	return zero, fmt.Errorf("%q is none of %s", name, strings.Join(names, ", "))
}

// First returns the first strategy match accepts, in registry order.
func (r Registry[T]) First(match func(T) bool) (T, bool) {
	i := slices.IndexFunc(r, match)
	if i < 0 {
		var zero T
		return zero, false
	}
	return r[i], true
}
