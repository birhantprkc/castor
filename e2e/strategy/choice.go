package strategy

import (
	"bytes"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Choice is a case's pick of one strategy: a bare name, or a one-key map from the name to its settings.
type Choice struct {
	Name     string
	Settings yaml.Node
}

func (c *Choice) UnmarshalYAML(n *yaml.Node) error {
	switch {
	case n.Kind == yaml.ScalarNode:
		c.Name = n.Value
		return nil
	case n.Kind == yaml.MappingNode && len(n.Content) == 2:
		c.Name, c.Settings = n.Content[0].Value, *n.Content[1]
		return nil
	}
	return fmt.Errorf("line %d: want a strategy name, or one name mapped to its settings", n.Line)
}

// Choices is a map of picks, each key naming a strategy and its value that strategy's settings.
type Choices []Choice

func (cs *Choices) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: want a map of strategy names to their settings", n.Line)
	}
	for pair := range slices.Chunk(n.Content, 2) {
		*cs = append(*cs, Choice{Name: pair[0].Value, Settings: *pair[1]})
	}
	return nil
}

// Decode reads settings into v, refusing keys v does not declare; absent settings leave v as it is.
func Decode(settings yaml.Node, v any) error {
	if settings.Kind == 0 {
		return nil
	}
	raw, err := yaml.Marshal(&settings)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(v)
}
