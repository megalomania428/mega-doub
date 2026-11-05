// Package config is documented in config.go.
package config

import (
	"fmt"

	"go.yaml.in/yaml/v4"
)

// Decoder builds a source configuration from its YAML node.
type Decoder func(node *yaml.Node, opts ...yaml.Option) (SourceConfig, error)

// SourceConfig is the common part every source configuration carries.
type SourceConfig interface {
	SourceID() string
	SourceType() string
	Validate() []Problem
}

// SourceBase holds the fields every source has, source types embed it inline.
type SourceBase struct {
	Type string `yaml:"type"`
	ID   string `yaml:"id"`
}

// SourceType returns the source type.
func (b SourceBase) SourceType() string {
	return b.Type
}

// SourceID returns the source identifier.
func (b SourceBase) SourceID() string {
	return b.ID
}

var decoders = map[string]Decoder{}

// Register adds a decoder for one source type. It panics on a duplicate
// type, which can only happen at init time.
func Register(sourceType string, dec Decoder) {
	if _, ok := decoders[sourceType]; ok {
		panic(fmt.Sprintf("config: source type %q is already registered", sourceType))
	}
	decoders[sourceType] = dec
}
