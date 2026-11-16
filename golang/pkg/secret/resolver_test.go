// cspell:ignore CRLF

package secret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeSecret writes a file with the content in a new temporary directory and returns
// its path.
func writeSecret(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// countReads makes r count its file reads.
func countReads(r *resolver) *atomic.Int32 {
	reads := &atomic.Int32{}
	read := r.readFile
	r.readFile = func(path string) (string, error) {
		reads.Add(1)
		return read(path)
	}
	return reads
}

func TestNew(t *testing.T) {
	r, ok := New(VaultOptions{}).(*resolver)
	if !ok {
		t.Fatalf("New() = %T, want *resolver", New(VaultOptions{}))
	}
	if len(r.entries) != 0 || r.readFile == nil {
		t.Errorf("New() = %+v, want an empty cache and a file reader", r)
	}
}

func Test_resolver_Resolve(t *testing.T) {
	type args struct {
		ref Ref
	}
	t.Setenv("MEGA_DOUB_RESOLVE_SET", canary)
	t.Setenv("MEGA_DOUB_RESOLVE_SPACES", " "+canary+" \n")
	t.Setenv("MEGA_DOUB_RESOLVE_EMPTY", "")
	// t.Setenv restores the variable after the test, which runs without it.
	t.Setenv("MEGA_DOUB_RESOLVE_UNSET", "")
	if err := os.Unsetenv("MEGA_DOUB_RESOLVE_UNSET"); err != nil {
		t.Fatal(err)
	}
	spaces := writeSecret(t, " \t"+canary+" \n")
	empty := writeSecret(t, "")
	blank := writeSecret(t, " \t\r\n")
	marked := writeSecret(t, "\xEF\xBB\xBF"+canary+"\n")
	onlyMark := writeSecret(t, "\xEF\xBB\xBF")
	crlf := writeSecret(t, canary+"\r\n")
	limit := strings.Repeat("a", maxFileSize)
	exact := writeSecret(t, limit)
	larger := writeSecret(t, limit+"a")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(spaces, link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
		// is is the error that the error wraps.
		is error
		// errHas is the text that the error holds, such as the variable or the path.
		errHas string
	}{
		{name: "plain", args: args{ref: "plain: " + canary + " "}, want: " " + canary + " "},
		{name: "no prefix", args: args{ref: canary}, want: canary},
		{name: "env", args: args{ref: "env:MEGA_DOUB_RESOLVE_SET"}, want: canary},
		{
			name: "env with spaces kept",
			args: args{ref: "env:MEGA_DOUB_RESOLVE_SPACES"},
			want: " " + canary + " \n",
		},
		{
			name:    "env not set",
			args:    args{ref: "env:MEGA_DOUB_RESOLVE_UNSET"},
			wantErr: true,
			errHas:  "MEGA_DOUB_RESOLVE_UNSET",
		},
		{
			name:    "env empty",
			args:    args{ref: "env:MEGA_DOUB_RESOLVE_EMPTY"},
			wantErr: true,
			errHas:  "MEGA_DOUB_RESOLVE_EMPTY",
		},
		{name: "file with spaces", args: args{ref: Ref("file:" + spaces)}, want: canary},
		{name: "empty file", args: args{ref: Ref("file:" + empty)}, wantErr: true,
			errHas: empty},
		{name: "blank file", args: args{ref: Ref("file:" + blank)}, wantErr: true,
			errHas: blank},
		{
			name: "file with a byte order mark",
			args: args{ref: Ref("file:" + marked)},
			want: canary,
		},
		{
			name:    "file of a byte order mark only",
			args:    args{ref: Ref("file:" + onlyMark)},
			wantErr: true,
			errHas:  onlyMark,
		},
		{name: "file with CRLF", args: args{ref: Ref("file:" + crlf)}, want: canary},
		{name: "file of the limit", args: args{ref: Ref("file:" + exact)}, want: limit},
		{
			name:    "file over the limit",
			args:    args{ref: Ref("file:" + larger)},
			wantErr: true,
			errHas:  larger,
		},
		{name: "symlink to a file", args: args{ref: Ref("file:" + link)}, want: canary},
		{
			name:    "directory",
			args:    args{ref: Ref("file:" + t.TempDir())},
			wantErr: true,
			errHas:  "is not a regular file",
		},
		{
			name:    "missing file",
			args:    args{ref: Ref("file:" + missing)},
			wantErr: true,
			is:      os.ErrNotExist,
			errHas:  missing,
		},
		{
			name:    "vault not configured",
			args:    args{ref: "vault:depot/fake/key"},
			wantErr: true,
			is:      errVaultNotConfigured,
			errHas:  "vault is not configured",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New(VaultOptions{}).Resolve(t.Context(), tt.args.ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolver.Resolve() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got.Reveal() != tt.want {
				t.Errorf("resolver.Resolve() = %q, want %q", got.Reveal(), tt.want)
			}
			if err == nil {
				return
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("resolver.Resolve() error = %v, want %v", err, tt.is)
			}
			if !strings.Contains(err.Error(), tt.errHas) {
				t.Errorf("resolver.Resolve() error = %v, want %q in it", err, tt.errHas)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("resolver.Resolve() error = %v, holds the value", err)
			}
		})
	}
}

func Test_resolver_Resolve_cache(t *testing.T) {
	path := writeSecret(t, "fake-first")
	ref := Ref("file:" + path)
	r, ok := New(VaultOptions{}).(*resolver)
	if !ok {
		t.Fatalf("New() = %T, want *resolver", New(VaultOptions{}))
	}
	reads := countReads(r)
	if got, err := r.Resolve(t.Context(), ref); err != nil || got.Reveal() != "fake-first" {
		t.Fatalf("resolver.Resolve() = %q, %v, want fake-first", got.Reveal(), err)
	}
	if err := os.WriteFile(path, []byte("fake-second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Resolve(t.Context(), ref); err != nil || got.Reveal() != "fake-first" {
		t.Errorf("resolver.Resolve() again = %q, %v, want the cached fake-first",
			got.Reveal(), err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("resolver.Resolve() read the file %d times, want 1", n)
	}
	got, err := New(VaultOptions{}).Resolve(t.Context(), ref)
	if err != nil || got.Reveal() != "fake-second" {
		t.Errorf("new resolver.Resolve() = %q, %v, want fake-second", got.Reveal(), err)
	}
}

func Test_resolver_Resolve_failureNotCached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late")
	ref := Ref("file:" + path)
	r := New(VaultOptions{})
	if _, err := r.Resolve(t.Context(), ref); err == nil {
		t.Fatal("resolver.Resolve() of a missing file error = nil, want an error")
	}
	if err := os.WriteFile(path, []byte("fake-late"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Resolve(t.Context(), ref); err != nil || got.Reveal() != "fake-late" {
		t.Errorf("resolver.Resolve() after the failure = %q, %v, want fake-late",
			got.Reveal(), err)
	}
}

func Test_resolver_Resolve_canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r, ok := New(VaultOptions{}).(*resolver)
	if !ok {
		t.Fatalf("New() = %T, want *resolver", New(VaultOptions{}))
	}
	reads := countReads(r)
	path := writeSecret(t, canary)
	if _, err := r.Resolve(ctx, Ref("file:"+path)); !errors.Is(err, context.Canceled) {
		t.Errorf("resolver.Resolve() error = %v, want %v", err, context.Canceled)
	}
	if n := reads.Load(); n != 0 {
		t.Errorf("resolver.Resolve() read the file %d times, want 0", n)
	}
}

func Test_resolver_Resolve_concurrent(t *testing.T) {
	const goroutines = 32
	shared := writeSecret(t, "fake-shared")
	r, ok := New(VaultOptions{}).(*resolver)
	if !ok {
		t.Fatalf("New() = %T, want *resolver", New(VaultOptions{}))
	}
	reads := countReads(r)
	var wg sync.WaitGroup
	got := make([]string, goroutines)
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			// Every goroutine asks for the shared file and for a reference of its own.
			value, err := r.Resolve(t.Context(), Ref("file:"+shared))
			if err != nil {
				errs[i] = err
				return
			}
			own, err := r.Resolve(t.Context(), Ref("plain:fake-"+strings.Repeat("x", i+1)))
			if err != nil {
				errs[i] = err
				return
			}
			got[i] = value.Reveal() + " " + own.Reveal()
		})
	}
	wg.Wait()
	for i := range goroutines {
		want := "fake-shared fake-" + strings.Repeat("x", i+1)
		if errs[i] != nil || got[i] != want {
			t.Errorf("goroutine %d got %q, %v, want %q", i, got[i], errs[i], want)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("resolver.Resolve() read the shared file %d times, want 1", n)
	}
	t.Run("a blocked read does not delay other references", func(t *testing.T) {
		const wait = 5 * time.Second
		slow := writeSecret(t, "fake-slow")
		other := writeSecret(t, "fake-other")
		r, ok := New(VaultOptions{}).(*resolver)
		if !ok {
			t.Fatalf("New() = %T, want *resolver", New(VaultOptions{}))
		}
		barrier := make(chan struct{})
		var open sync.Once
		release := func() { open.Do(func() { close(barrier) }) }
		t.Cleanup(release)
		started := make(chan struct{})
		var slowReads atomic.Int32
		read := r.readFile
		r.readFile = func(path string) (string, error) {
			if path == slow {
				if slowReads.Add(1) == 1 {
					close(started)
				}
				<-barrier
			}
			return read(path)
		}
		type result struct {
			value string
			err   error
		}
		slowDone := make(chan result, 1)
		go func() {
			value, err := r.Resolve(t.Context(), Ref("file:"+slow))
			slowDone <- result{value.Reveal(), err}
		}()
		select {
		case <-started:
		case <-time.After(wait):
			t.Fatal("resolver.Resolve() did not start reading the slow file")
		}
		otherDone := make(chan result, 1)
		go func() {
			file, err := r.Resolve(t.Context(), Ref("file:"+other))
			if err != nil {
				otherDone <- result{err: err}
				return
			}
			plain, err := r.Resolve(t.Context(), Ref("plain:fake-plain"))
			otherDone <- result{file.Reveal() + " " + plain.Reveal(), err}
		}()
		select {
		case res := <-otherDone:
			if want := "fake-other fake-plain"; res.err != nil || res.value != want {
				t.Errorf("other references = %q, %v, want %q", res.value, res.err, want)
			}
		case <-time.After(wait):
			t.Fatal("resolver.Resolve() of other references waits for a blocked read")
		}
		select {
		case res := <-slowDone:
			t.Fatalf("resolver.Resolve() of the slow file ended early: %q, %v",
				res.value, res.err)
		default:
		}
		release()
		select {
		case res := <-slowDone:
			if res.err != nil || res.value != "fake-slow" {
				t.Errorf("slow file = %q, %v, want fake-slow", res.value, res.err)
			}
		case <-time.After(wait):
			t.Fatal("resolver.Resolve() of the slow file hangs after the barrier opens")
		}
		if n := slowReads.Load(); n != 1 {
			t.Errorf("resolver.Resolve() read the slow file %d times, want 1", n)
		}
	})
}
