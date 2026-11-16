// cspell:ignore loaderlimits

// Package config is documented in config.go.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v4"
	"go.yaml.in/yaml/v4/plugin/loaderlimits"
)

const (
	// maxFileSize is the largest configuration file in bytes.
	maxFileSize = 1 << 20
	// maxDepth is the deepest nesting of collections in the file.
	maxDepth = 64
)

// Loaded is a loaded configuration with the data the later checks need.
type Loaded struct {
	Config *Config
	// Path is the absolute path of the configuration file.
	Path string
	// Root is the document node of the file.
	Root *yaml.Node
	// LogLevelSet and LogFormatSet report that global.log_level and global.log_format
	// are given with a value other than null.
	LogLevelSet  bool
	LogFormatSet bool
}

// configFile is the root of the file, targets are decoded one by one. The sections
// point to a configuration filled with the defaults.
type configFile struct {
	Templates yaml.Node   `yaml:"templates"`
	Global    *Global     `yaml:"global"`
	Vault     *Vault      `yaml:"vault"`
	Storage   *Storage    `yaml:"storage"`
	Prune     *Prune      `yaml:"prune"`
	Check     *Check      `yaml:"check"`
	Targets   []yaml.Node `yaml:"targets"`
}

// targetItem is one element of targets, sources are decoded one by one.
type targetItem struct {
	TargetSettings `yaml:",inline"`
	Sources        []yaml.Node `yaml:"sources"`
}

// givenKeys captures the keys whose defaults depend on their presence.
type givenKeys struct {
	Global struct {
		LogLevel  yaml.Node `yaml:"log_level"`
		LogFormat yaml.Node `yaml:"log_format"`
		CopyDir   yaml.Node `yaml:"copy_dir"`
		StateDir  yaml.Node `yaml:"state_dir"`
	} `yaml:"global"`
}

// sourceHead is the type of a source, read before its decoder is known.
type sourceHead struct {
	Type yaml.Node `yaml:"type"`
}

// loader decodes one file and collects its problems.
type loader struct {
	opts     []yaml.Option
	problems []Problem
	seen     map[Problem]bool
	// fatal reports an error that stops decoding.
	fatal bool
}

// Load reads and decodes the configuration file at path. Every problem of the file is
// reported at once in the returned *Error.
func Load(path string) (*Loaded, error) {
	file, err := filepath.Abs(path)
	if err != nil {
		return nil, newError(path, []Problem{{Message: err.Error()}})
	}
	data, err := readFile(file)
	if err != nil {
		return nil, newError(file, []Problem{{Message: err.Error()}})
	}
	l := &loader{seen: map[Problem]bool{}, opts: []yaml.Option{
		yaml.WithKnownFields(),
		yaml.WithUniqueKeys(),
		yaml.WithPlugin(loaderlimits.New(loaderlimits.DepthValue(maxDepth))),
	}}
	loaded := l.load(data)
	if len(l.problems) > 0 {
		return nil, newError(file, l.problems)
	}
	loaded.Path = file
	return loaded, nil
}

// readFile reads at most maxFileSize bytes and fails on a larger file.
func readFile(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, pathError(err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, pathError(err)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("file is larger than %d bytes", maxFileSize)
	}
	return data, nil
}

// pathError drops the path from err, the problem carries it already.
func pathError(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
	}
	return err
}

// add keeps the problems not seen before, a template used many times repeats them.
func (l *loader) add(problems ...Problem) {
	for _, p := range problems {
		if !l.seen[p] {
			l.seen[p] = true
			l.problems = append(l.problems, p)
		}
	}
}

// addError adds the problems of a library error and reports whether there were any.
// A single *yaml.LoadError is fatal and stops decoding.
func (l *loader) addError(err error) bool {
	if _, ok := err.(*yaml.LoadError); ok {
		l.fatal = true
	}
	l.add(yamlProblems(err)...)
	return err != nil
}

// load decodes the document, it returns nil when the problems stop decoding.
func (l *loader) load(data []byte) *Loaded {
	dec, err := yaml.NewLoader(bytes.NewReader(data), l.opts...)
	if err != nil {
		l.add(yamlProblems(err)...)
		return nil
	}
	var doc yaml.Node
	if err := dec.Load(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			l.add(Problem{Message: "configuration is empty"})
		} else {
			l.add(yamlProblems(err)...)
		}
		return nil
	}
	var next yaml.Node
	switch err := dec.Load(&next); {
	case err == nil:
		l.add(Problem{Line: next.Line, Column: next.Column,
			Message: "configuration must be a single document"})
		return nil
	case !errors.Is(err, io.EOF):
		l.add(yamlProblems(err)...)
		return nil
	}
	if len(doc.Content) == 0 || isNull(doc.Content[0]) {
		l.add(Problem{Message: "configuration is empty"})
		return nil
	}
	cfg := l.decode(&doc)
	if len(l.problems) > 0 {
		return nil
	}
	var given givenKeys
	// The strict decoding has expanded every alias under the limit already. This one
	// constructs fewer values, so the alias ratio of the library is not checked again.
	presence := yaml.WithPlugin(loaderlimits.New(loaderlimits.DepthValue(maxDepth),
		loaderlimits.AliasNone()))
	if l.addError(doc.Load(&given, yaml.WithUniqueKeys(false), presence)) {
		return nil
	}
	if !isGiven(&given.Global.CopyDir) {
		cfg.Global.CopyDir = defaultCopyDir + cfg.Global.Instance
	}
	if !isGiven(&given.Global.StateDir) {
		cfg.Global.StateDir = defaultStateDir + cfg.Global.Instance
	}
	return &Loaded{
		Config:       cfg,
		Root:         &doc,
		LogLevelSet:  isGiven(&given.Global.LogLevel),
		LogFormatSet: isGiven(&given.Global.LogFormat),
	}
}

// decode fills the defaults and decodes the document over them.
func (l *loader) decode(doc *yaml.Node) *Config {
	cfg := defaultConfig()
	root := configFile{
		Global:  &cfg.Global,
		Vault:   &cfg.Vault,
		Storage: &cfg.Storage,
		Prune:   &cfg.Prune,
		Check:   &cfg.Check,
	}
	l.addError(doc.Load(&root, l.opts...))
	cfg.Templates = root.Templates
	// An explicit null clears a list, so the default is restored.
	if cfg.Prune.Keep == nil {
		cfg.Prune.Keep = []string{}
	}
	cfg.Targets = make([]Target, len(root.Targets))
	for i := range root.Targets {
		if l.fatal {
			break
		}
		cfg.Targets[i] = l.target(&root.Targets[i])
	}
	return cfg
}

func (l *loader) target(node *yaml.Node) Target {
	item := targetItem{TargetSettings: defaultTargetSettings()}
	l.addError(node.Load(&item, l.opts...))
	if item.Auth == nil {
		item.Auth = defaultAuth()
	}
	t := Target{TargetSettings: item.TargetSettings, Sources: []SourceConfig{}}
	for i := range item.Sources {
		if l.fatal {
			break
		}
		if src := l.source(&item.Sources[i]); src != nil {
			t.Sources = append(t.Sources, src)
		}
	}
	return t
}

// source reads the type of a source loosely and decodes the source strictly with the
// decoder of the type.
func (l *loader) source(node *yaml.Node) SourceConfig {
	var head sourceHead
	// The strict decoding reports duplicate keys, only a fatal error or a source that
	// is not a mapping stops here.
	if l.addError(node.Load(&head, yaml.WithUniqueKeys(false))) {
		return nil
	}
	var sourceType string
	if isGiven(&head.Type) && l.addError(head.Type.Load(&sourceType, l.opts...)) {
		return nil
	}
	if sourceType == "" {
		l.add(Problem{Line: node.Line, Column: node.Column,
			Message: "source type is required"})
		return nil
	}
	dec, ok := decoders[sourceType]
	if !ok {
		l.add(Problem{Line: node.Line, Column: node.Column,
			Message: "unknown source type"})
		return nil
	}
	src, err := dec(node, l.opts...)
	l.addError(err)
	if err != nil {
		return nil
	}
	return src
}

// isGiven reports that the key is present with a value other than null.
func isGiven(node *yaml.Node) bool {
	return node.Kind != 0 && !isNull(node)
}

func isNull(node *yaml.Node) bool {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node.ShortTag() == "!!null"
}
