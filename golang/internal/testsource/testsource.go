// cspell:ignore testsource

// Package testsource registers the source type the tests use. Only test files import
// it, so the registry of the binary stays empty.
package testsource

import (
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// Type is the name of the source type.
const Type = "test"

// Source is the configuration of the source type.
type Source struct {
	config.SourceBase `yaml:",inline"`
	Path              string     `yaml:"path"`
	Token             secret.Ref `yaml:"token,omitempty"`
}

// Validate reports a missing id and a relative path, problems about the whole source
// and about one of its keys.
func (s Source) Validate() []config.Problem {
	var problems []config.Problem
	if s.ID == "" {
		problems = append(problems, config.Problem{Message: "id is required"})
	}
	if !strings.HasPrefix(s.Path, "/") {
		problems = append(problems,
			config.Problem{Path: "path", Message: "path must be absolute"})
	}
	return problems
}

var once sync.Once

// Register registers the source type, the calls after the first one do nothing:
// config.Register panics on a repeated type.
func Register() {
	once.Do(func() {
		config.Register(Type, decode)
	})
}

// decode builds the source from its node with the options of the loader.
func decode(node *yaml.Node, opts ...yaml.Option) (config.SourceConfig, error) {
	var s Source
	if err := node.Load(&s, opts...); err != nil {
		return nil, err
	}
	return s, nil
}
