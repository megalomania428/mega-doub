// Package secret is documented in ref.go.
package secret

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Kind is the kind of a secret reference, the name of its prefix.
type Kind string

// The kinds of the references.
const (
	KindPlain Kind = "plain"
	KindEnv   Kind = "env"
	KindFile  Kind = "file"
	KindVault Kind = "vault"
)

// Parsed is a reference split into its kind and body.
type Parsed struct {
	Kind Kind
	// Body is the reference without the prefix, the whole string without a prefix.
	Body string
	// Prefixed reports an explicit prefix.
	Prefixed bool
}

// kinds are the kinds of the known prefixes.
var kinds = []Kind{KindPlain, KindEnv, KindFile, KindVault}

// envName matches the name of an environment variable.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Parse splits the reference into its kind and body and checks the body. The prefixes
// are case-sensitive, a string without a known prefix is a plain value. The errors do
// not hold the body.
func Parse(ref Ref) (Parsed, error) {
	if ref == "" {
		return Parsed{}, errors.New("reference is empty")
	}
	kind, body, ok := split(string(ref))
	if !ok {
		return Parsed{Kind: KindPlain, Body: string(ref)}, nil
	}
	var err error
	switch kind {
	case KindPlain:
		if body == "" {
			err = errors.New("plain reference has an empty value")
		}
	case KindEnv:
		if !envName.MatchString(body) {
			err = errors.New("env reference must name a variable of letters, digits and _, " +
				"not starting with a digit")
		}
	case KindFile:
		if !filepath.IsAbs(body) || filepath.Clean(body) != body {
			err = errors.New("file reference must be a clean absolute path")
		}
	case KindVault:
		err = checkVault(body)
	}
	if err != nil {
		return Parsed{}, err
	}
	return Parsed{Kind: kind, Body: body, Prefixed: true}, nil
}

// split returns the kind and the body of a reference with a known prefix.
func split(s string) (Kind, string, bool) {
	for _, kind := range kinds {
		if body, ok := strings.CutPrefix(s, string(kind)+":"); ok {
			return kind, body, true
		}
	}
	return "", "", false
}

// checkVault checks the mount/path/key body of a vault reference. It is stricter than
// the path check of go-lib-ci on purpose: the path is joined into the URL of a request
// that carries the token.
func checkVault(body string) error {
	segments := strings.Split(body, "/")
	if len(segments) < 3 {
		return errors.New("vault reference must be mount/path/key")
	}
	for _, segment := range segments {
		switch {
		case segment == "":
			return errors.New("vault reference has an empty segment")
		case segment == "." || segment == "..":
			return errors.New("vault reference has a . or .. segment")
		case strings.ContainsFunc(segment, invalidRune):
			return errors.New("vault reference has a space, a control character, ?, #, % " +
				"or \\ in a segment")
		}
	}
	return nil
}

// invalidRune reports a rune a segment of a vault reference must not have.
func invalidRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`?#%\`, r)
}
