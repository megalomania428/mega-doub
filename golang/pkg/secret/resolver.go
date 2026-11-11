// cspell:ignore NONBLOCK

// Package secret is documented in ref.go.
package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
)

// maxFileSize is the largest size of the file of a file: reference in bytes.
const maxFileSize = 64 << 10

// ErrUnsupported is the error of a vault: reference, which is not resolved yet.
var ErrUnsupported = errors.New("vault references are not supported yet")

// Resolver turns a Ref into its value. Values are cached for the lifetime
// of one run.
type Resolver interface {
	Resolve(ctx context.Context, ref Ref) (Value, error)
}

// New returns a Resolver of the plain:, env: and file: references with an empty cache,
// every run creates its own. A vault: reference gives ErrUnsupported.
func New() Resolver {
	return &resolver{entries: map[Ref]*entry{}, readFile: readFile}
}

// resolver caches the values of the references for one run.
type resolver struct {
	mu      sync.Mutex
	entries map[Ref]*entry
	// readFile reads the value of a file: reference, readFile of the package by default.
	readFile func(path string) (string, error)
}

// entry is the cache slot of one reference. Its own lock makes the concurrent requests
// of the reference wait for one read, while the other references do not wait.
type entry struct {
	mu    sync.Mutex
	ready bool
	value Value
}

// Resolve returns the value of ref, from the cache when it is resolved already. A failure
// leaves the entry not ready, so the next request reads the source again.
func (r *resolver) Resolve(ctx context.Context, ref Ref) (Value, error) {
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	r.mu.Lock()
	e, ok := r.entries[ref]
	if !ok {
		e = &entry{}
		r.entries[ref] = e
	}
	r.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.ready {
		value, err := r.lookup(ref)
		if err != nil {
			return Value{}, err
		}
		e.value, e.ready = NewValue(value), true
	}
	return e.value, nil
}

// lookup reads the value of ref from its source. The errors name the kind of the
// reference and the variable or the path, never the value.
func (r *resolver) lookup(ref Ref) (string, error) {
	parsed, err := Parse(ref)
	if err != nil {
		return "", err
	}
	switch parsed.Kind {
	case KindEnv:
		value, ok := os.LookupEnv(parsed.Body)
		if !ok {
			return "", fmt.Errorf("env reference: variable %s is not set", parsed.Body)
		}
		if value == "" {
			return "", fmt.Errorf("env reference: variable %s is empty", parsed.Body)
		}
		return value, nil
	case KindFile:
		return r.readFile(parsed.Body)
	case KindVault:
		return "", ErrUnsupported
	}
	return parsed.Body, nil
}

// readFile returns the content of the regular file at path without a leading UTF-8 byte
// order mark and the surrounding whitespace. The file is opened without blocking: the
// open of a named pipe without a writer would block forever.
func readFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("file reference: %w", err)
	}
	data, err := readRegular(f, path)
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("file reference: %w", closeErr)
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
	if value == "" {
		return "", fmt.Errorf("file reference: %s is empty", path)
	}
	return value, nil
}

// readRegular reads the open file f at path, which must be a regular file of at most
// maxFileSize bytes; the file may grow after Stat, so the read is limited too.
func readRegular(f *os.File, path string) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("file reference: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file reference: %s is not a regular file", path)
	}
	tooLarge := fmt.Errorf("file reference: %s is larger than %d bytes", path, maxFileSize)
	if info.Size() > maxFileSize {
		return nil, tooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("file reference: %w", err)
	}
	if len(data) > maxFileSize {
		return nil, tooLarge
	}
	return data, nil
}
