// cspell:ignore NONBLOCK

// Package secret is documented in ref.go.
package secret

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
)

// maxFileSize is the largest size of the file of a file: reference in bytes.
const maxFileSize = 64 << 10

// Resolver turns a Ref into its value. Values are cached for the lifetime
// of one run.
type Resolver interface {
	Resolve(ctx context.Context, ref Ref) (Value, error)
}

// New returns a Resolver of the plain:, env:, file: and vault: references with an empty
// cache, every run creates its own. The vault: references are read with opts: the zero
// VaultOptions makes them fail with the error vault is not configured.
func New(opts VaultOptions) Resolver {
	r := &resolver{entries: map[Ref]*entry{}, readFile: readFile}
	r.vault = newVault(opts, r)
	return r
}

// resolver caches the values of the references for one run.
type resolver struct {
	mu      sync.Mutex
	entries map[Ref]*entry
	// readFile reads the value of a file: reference, readFile of the package by default.
	readFile func(path string) (string, error)
	// vault reads the values of the vault: references.
	vault *vault
}

// entry is the cache slot of one reference. Its own lock makes the concurrent requests
// of the reference wait for one read, while the other references do not wait.
type entry struct {
	mu    sync.Mutex
	ready bool
	value Value
}

// Resolve returns the value of ref, from the cache when it is resolved already. A failure
// leaves the entry not ready, so the next request reads the source again; the Vault
// client is the exception, it remembers its own failure to be created and a request that
// ran out of its retries. A cancellation of ctx, one that comes while the request waits
// for the entry included, is returned as the error of ctx.
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
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	if !e.ready {
		value, err := r.lookup(ctx, ref)
		if err != nil {
			return Value{}, err
		}
		e.value, e.ready = NewValue(value), true
	}
	return e.value, nil
}

// lookup reads the value of ref from its source. The errors name the kind of the
// reference and the variable or the path, never the value. It runs under the lock of the
// entry of ref only, so a vault: reference resolves the token of Vault with r.
func (r *resolver) lookup(ctx context.Context, ref Ref) (string, error) {
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
		return r.vault.read(ctx, parsed.Body)
	}
	return parsed.Body, nil
}

// readFile returns the content of the regular file at path without a leading UTF-8 byte
// order mark and the surrounding whitespace.
func readFile(path string) (string, error) {
	data, err := readRegularFile(path, maxFileSize, "file reference")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
	if value == "" {
		return "", fmt.Errorf("file reference: %s is empty", path)
	}
	return value, nil
}

// readRegularFile returns the content of the regular file at path of at most limit bytes,
// the errors start with prefix. The file is opened without blocking: the open of a named
// pipe without a writer would block forever.
func readRegularFile(path string, limit int64, prefix string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", prefix, err)
	}
	data, err := readRegular(f, path, limit, prefix)
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("%s: %w", prefix, closeErr)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// readRegular reads the open file f at path, which must be a regular file of at most
// limit bytes; the file may grow after Stat, so the read is limited too. The errors start
// with prefix.
func readRegular(f *os.File, path string, limit int64, prefix string) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", prefix, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s is not a regular file", prefix, path)
	}
	tooLarge := fmt.Errorf("%s: %s is larger than %d bytes", prefix, path, limit)
	if info.Size() > limit {
		return nil, tooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", prefix, err)
	}
	if int64(len(data)) > limit {
		return nil, tooLarge
	}
	return data, nil
}
