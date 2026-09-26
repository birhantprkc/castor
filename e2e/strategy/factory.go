package strategy

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Factory builds a strategy from its settings in a case.
type Factory[T any] interface {
	Named
	Build(settings yaml.Node) (T, error)
}

// Build resolves a choice against a registry of factories.
func Build[T any, F Factory[T]](r Registry[F], c Choice) (T, error) {
	f, err := r.Lookup(c.Name)
	if err != nil {
		var zero T
		return zero, err
	}
	return f.Build(c.Settings)
}

// Fixed is the factory of a strategy that takes no settings.
type Fixed[T Named] struct{ Strategy T }

func (f Fixed[T]) Name() string { return f.Strategy.Name() }

func (f Fixed[T]) Build(settings yaml.Node) (T, error) {
	return f.Strategy, Decode(settings, &struct{}{})
}

// OneOf is a factory whose setting names one of its strategies, as in `delivery: served`.
type OneOf[T Named] struct {
	Called string
	Of     Registry[T]
}

func (o OneOf[T]) Name() string { return o.Called }

func (o OneOf[T]) Build(settings yaml.Node) (T, error) {
	var name string
	if err := settings.Decode(&name); err != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", o.Called, err)
	}
	return o.Of.Lookup(name)
}
