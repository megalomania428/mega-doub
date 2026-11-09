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
	"strconv"

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
	Root  *yaml.Node
	index *index
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

// sourceHead is the type of a source, read before its decoder is known.
type sourceHead struct {
	Type yaml.Node `yaml:"type"`
}

// loader decodes one file and collects its problems.
type loader struct {
	opts   []yaml.Option
	issues []issue
	// fatal reports an error that stops decoding.
	fatal bool
	// undecoded are the paths the library has not decoded: the targets or the sources
	// after a fatal error and the sources without a decoder.
	undecoded map[string]bool
	// stops are the fatal errors, the library has not decoded the rest of their element.
	stops []issue
}

// issue is a problem with the data to find its path.
type issue struct {
	Problem
	// scope is the path of the element the library decoded, the path of the problem
	// is looked up by its position among the nodes of the element.
	scope string
	// lookup reports a problem of the library to find the path of, the others have
	// their path already or have none.
	lookup bool
	kind   keyKind
	// field is the name of the field the message names.
	field string
	// null marks a null key, it is a problem only where the library constructs the
	// mapping that holds it.
	null bool
	// seen is the position in the walk where the library sees the null key.
	seen int
}

// Load reads and decodes the configuration file at path. Every problem of the file is
// reported at once in the returned *Error.
func Load(path string) (*Loaded, error) {
	if path == "" {
		return nil, newError("", []Problem{{Message: "configuration path is empty"}})
	}
	// The file is read by the absolute path the problems name: a link followed by ..
	// would lead elsewhere.
	file, err := filepath.Abs(path)
	if err != nil {
		return nil, newError(path, []Problem{{Message: err.Error()}})
	}
	data, err := readFile(file)
	if err != nil {
		return nil, newError(file, []Problem{{Message: err.Error()}})
	}
	loaded, problems := newLoader().load(data)
	if len(problems) > 0 {
		return nil, newError(file, problems)
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

// newLoader returns a loader with the strict options of the library.
func newLoader() *loader {
	return &loader{undecoded: map[string]bool{}, opts: []yaml.Option{
		yaml.WithKnownFields(),
		yaml.WithUniqueKeys(),
		yaml.WithPlugin(loaderlimits.New(loaderlimits.DepthValue(maxDepth))),
	}}
}

// add keeps a problem of the element at scope that has its path already.
func (l *loader) add(scope string, p Problem) {
	l.issues = append(l.issues, issue{Problem: p, scope: scope})
}

// addError adds the problems of a library error found while decoding the element at
// scope and reports whether there were any. A single *yaml.LoadError is fatal: it stops
// decoding and has no path.
func (l *loader) addError(scope string, err error) bool {
	_, fatal := err.(*yaml.LoadError)
	l.fatal = l.fatal || fatal
	for _, p := range yamlProblems(err) {
		kind, field := kindOf(p.Message)
		is := issue{Problem: p, scope: scope, lookup: !fatal, kind: kind, field: field}
		l.issues = append(l.issues, is)
		if fatal {
			l.stops = append(l.stops, is)
		}
	}
	return err != nil
}

// load decodes the document and returns its problems, the configuration is nil when
// there are any.
func (l *loader) load(data []byte) (*Loaded, []Problem) {
	dec, err := yaml.NewLoader(bytes.NewReader(data), l.opts...)
	if err != nil {
		return nil, yamlProblems(err)
	}
	var doc yaml.Node
	if err := dec.Load(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, []Problem{{Message: "configuration is empty"}}
		}
		return nil, yamlProblems(err)
	}
	var next yaml.Node
	switch err := dec.Load(&next); {
	case err == nil:
		return nil, []Problem{{Line: next.Line, Column: next.Column,
			Message: "configuration must be a single document"}}
	case !errors.Is(err, io.EOF):
		return nil, yamlProblems(err)
	}
	if len(doc.Content) == 0 || isNull(doc.Content[0]) {
		return nil, []Problem{{Message: "configuration is empty"}}
	}
	return l.loadDocument(&doc)
}

// loadDocument decodes a document node that is not empty and indexes it.
func (l *loader) loadDocument(doc *yaml.Node) (*Loaded, []Problem) {
	cfg := l.decode(doc)
	x := newIndex(doc.Content[0])
	loaded := &Loaded{Root: doc, index: x}
	stops := make([]int, len(l.stops))
	for i, s := range l.stops {
		stops[i] = x.stopAt(s.scope, s.Line, s.Column)
	}
	for _, n := range x.nulls {
		// The library has not seen a null key after the place a fatal error stops at.
		seen := !underAny(n.Path, l.undecoded)
		for k, s := range l.stops {
			if inScope(s.scope, n.Path) && n.order >= stops[k] {
				seen = false
			}
		}
		if seen {
			l.issues = append(l.issues, issue{Problem: n.Problem, scope: scopeOf(n.Path),
				null: true, seen: n.order})
		}
	}
	// An incomplete index drops the null keys, then only its own problem is left.
	if problems := loaded.index.assign(l.issues); len(problems) > 0 {
		return nil, problems
	}
	// An incomplete index would report a present key as missing.
	if !loaded.index.complete {
		return nil, []Problem{{Line: 1, Column: 1,
			Message: "configuration is too large to index"}}
	}
	if !loaded.HasValue("global.copy_dir") {
		cfg.Global.CopyDir = defaultCopyDir + cfg.Global.Instance
	}
	if !loaded.HasValue("global.state_dir") {
		cfg.Global.StateDir = defaultStateDir + cfg.Global.Instance
	}
	loaded.Config = cfg
	return loaded, nil
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
	// A fatal error may stop the library before it reads the targets at all.
	if l.addError("", doc.Load(&root, l.opts...)) && l.fatal {
		l.undecoded["targets"] = true
	}
	cfg.Templates = root.Templates
	// An explicit null clears a list, so the default is restored.
	if cfg.Prune.Keep == nil {
		cfg.Prune.Keep = []string{}
	}
	cfg.Targets = make([]Target, len(root.Targets))
	for i := range root.Targets {
		path := "targets[" + strconv.Itoa(i) + "]"
		if l.fatal {
			l.undecoded[path] = true
			continue
		}
		cfg.Targets[i] = l.target(path, &root.Targets[i])
	}
	return cfg
}

// target decodes the target at path.
func (l *loader) target(path string, node *yaml.Node) Target {
	item := targetItem{TargetSettings: defaultTargetSettings()}
	if l.addError(path, node.Load(&item, l.opts...)) && l.fatal {
		l.undecoded[path+".sources"] = true
	}
	if item.Auth == nil {
		item.Auth = defaultAuth()
	}
	t := Target{TargetSettings: item.TargetSettings, Sources: []SourceConfig{}}
	for i := range item.Sources {
		scope := path + ".sources[" + strconv.Itoa(i) + "]"
		if l.fatal {
			l.undecoded[scope] = true
			continue
		}
		if src := l.source(scope, &item.Sources[i]); src != nil {
			t.Sources = append(t.Sources, src)
		}
	}
	return t
}

// source decodes the source at path strictly with the decoder of its type.
func (l *loader) source(path string, node *yaml.Node) SourceConfig {
	dec := l.decoder(path, node)
	if dec == nil {
		l.undecoded[path] = true
		return nil
	}
	src, err := dec(node, l.opts...)
	l.addError(path, err)
	if err != nil {
		return nil
	}
	return src
}

// decoder reads the type of the source at path loosely and returns its decoder, nil
// when the type has a problem.
func (l *loader) decoder(path string, node *yaml.Node) Decoder {
	var head sourceHead
	// The strict decoding reports duplicate keys, only a fatal error or a source that
	// is not a mapping stops here.
	if l.addError(path, node.Load(&head, yaml.WithUniqueKeys(false))) {
		return nil
	}
	if !isGiven(&head.Type) {
		at := resolve(node)
		l.add(path, Problem{Line: at.Line, Column: at.Column, Path: path,
			Message: "source type is required"})
		return nil
	}
	var sourceType string
	if l.addError(path, head.Type.Load(&sourceType, l.opts...)) {
		return nil
	}
	// An explicit '' is a type no decoder has.
	dec, ok := decoders[sourceType]
	if !ok {
		at := resolve(&head.Type)
		l.add(path, Problem{Line: at.Line, Column: at.Column, Path: path + ".type",
			Message: "unknown source type"})
		return nil
	}
	return dec
}

// isGiven reports that the key is present with a value other than null.
func isGiven(node *yaml.Node) bool {
	return node.Kind != 0 && !isNull(node)
}

func isNull(node *yaml.Node) bool {
	return resolve(node).ShortTag() == "!!null"
}

// resolve returns the node an alias points to.
func resolve(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}
