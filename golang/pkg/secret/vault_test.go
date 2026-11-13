// cspell:ignore ECONNREFUSED httptest
package secret

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	ci "github.com/megalomania428/go-lib-ci"
	"github.com/megalomania428/mega-doub/pkg/retry"
)

const (
	// testVaultToken is the token that the test Vault server expects.
	testVaultToken = "fake-vault-token-3c9d"
	// testVaultValue is the value of the secrets of the test Vault server.
	testVaultValue = "fake-vault-value-8e1f"
	// testVaultBody is the body of the responses outside 2xx of the test Vault server.
	testVaultBody = "fake-vault-body-5d2a"
	// testVaultInvalidText is the exact text of the error of an invalid response.
	testVaultInvalidText = "vault response is not a valid KV version 2 secret"
	// testVaultRef is a reference to the key storage-password of the secret mega-doub/prod
	// of the mount depot.
	testVaultRef = Ref("vault:depot/mega-doub/prod/storage-password")
	// testVaultPath is the request path of testVaultRef.
	testVaultPath = "/v1/depot/data/mega-doub/prod"
)

// testVault is a test Vault server that counts its requests.
type testVault struct {
	*httptest.Server
	requests atomic.Int32
}

// newTestVault starts a test Vault server with the handler, closed after the test.
func newTestVault(t *testing.T, handler http.HandlerFunc) *testVault {
	t.Helper()
	v := &testVault{}
	count := func(w http.ResponseWriter, r *http.Request) {
		v.requests.Add(1)
		handler(w, r)
	}
	v.Server = httptest.NewServer(http.HandlerFunc(count))
	t.Cleanup(v.Close)
	return v
}

// writeVaultData answers with the KV version 2 secret of the JSON object data.
func writeVaultData(w http.ResponseWriter, data string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"data":{"data":`+data+`}}`)
}

// vaultSecret answers with the secret that holds testVaultValue at the key
// storage-password.
func vaultSecret(w http.ResponseWriter, _ *http.Request) {
	writeVaultData(w, `{"storage-password":"`+testVaultValue+`"}`)
}

// newVaultResolver returns the resolver of the options.
func newVaultResolver(t *testing.T, opts VaultOptions) *resolver {
	t.Helper()
	r, ok := New(opts).(*resolver)
	if !ok {
		t.Fatalf("New() = %T, want *resolver", New(opts))
	}
	return r
}

// countClients makes r count the Vault clients that it creates.
func countClients(r *resolver) *atomic.Int32 {
	clients := &atomic.Int32{}
	create := r.vault.newClient
	r.vault.newClient = func(opts ci.VaultOptions) (*ci.VaultClient, error) {
		clients.Add(1)
		return create(opts)
	}
	return clients
}

// tokenStub is a Resolver of the token made of a function.
type tokenStub func(ctx context.Context, ref Ref) (Value, error)

// Resolve calls f.
func (f tokenStub) Resolve(ctx context.Context, ref Ref) (Value, error) {
	return f(ctx, ref)
}

// unsetVaultEnv removes the environment variable for the test.
func unsetVaultEnv(t *testing.T, name string) {
	t.Helper()
	// t.Setenv restores the variable after the test, which runs without it.
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

// checkNoLeak fails the test when the error text holds the token, a value, the body of
// a response, the address of the server or the URL of a request.
func checkNoLeak(t *testing.T, err error, address string) {
	t.Helper()
	for _, s := range []string{testVaultToken, testVaultValue, testVaultBody, address,
		"http://", "https://", "/v1/"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("resolver.Resolve() error = %v, holds %q", err, s)
		}
	}
}

func TestStatusError_Error(t *testing.T) {
	type fields struct {
		Code int
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{name: "forbidden", fields: fields{Code: http.StatusForbidden},
			want: "vault returned 403"},
		{name: "unavailable", fields: fields{Code: http.StatusServiceUnavailable},
			want: "vault returned 503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &StatusError{
				Code: tt.fields.Code,
			}
			if got := e.Error(); got != tt.want {
				t.Errorf("StatusError.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_newVault(t *testing.T) {
	r := New(VaultOptions{})
	// discard is the default logger, logger is a logger of the options.
	discard := slog.New(slog.DiscardHandler)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	set := VaultOptions{Address: "https://vault.example.invalid:8200",
		Token: "env:MEGA_DOUB_TEST_VAULT_TOKEN", Timeout: 5 * time.Second,
		CAFile: "/etc/ssl/certs/fake-vault-ca.pem", Logger: logger}
	type args struct {
		opts VaultOptions
		r    Resolver
	}
	tests := []struct {
		name string
		args args
		want *vault
	}{
		{name: "zero options", args: args{r: r}, want: &vault{
			opts: VaultOptions{Timeout: defaultVaultTimeout, Logger: discard}, resolver: r}},
		{name: "negative timeout", args: args{opts: VaultOptions{Timeout: -time.Second}, r: r},
			want: &vault{opts: VaultOptions{Timeout: defaultVaultTimeout, Logger: discard},
				resolver: r}},
		{name: "set options", args: args{opts: set, r: r},
			want: &vault{opts: set, resolver: r}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newVault(tt.args.opts, tt.args.r)
			// reflect.DeepEqual never matches a function and matches a channel only with
			// itself, so newClient, systemPool and requests are checked apart.
			if got.newClient == nil {
				t.Error("newVault().newClient = nil, want ci.NewVaultClient")
			}
			if got.systemPool == nil {
				t.Error("newVault().systemPool = nil, want x509.SystemCertPool")
			}
			if cap(got.requests) != 1 || len(got.requests) != 0 {
				t.Errorf("newVault().requests holds %d of %d, want an empty channel of 1",
					len(got.requests), cap(got.requests))
			}
			got.newClient, got.systemPool, got.requests = nil, nil, nil
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newVault() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_resolver_Resolve_vault(t *testing.T) {
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", testVaultToken)
	tokenFile := writeSecret(t, testVaultToken+"\n")
	srv := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testVaultPath || r.Header.Get("X-Vault-Token") != testVaultToken {
			t.Errorf("request of %s with the token %q, want %s with %q", r.URL.Path,
				r.Header.Get("X-Vault-Token"), testVaultPath, testVaultToken)
		}
		vaultSecret(w, r)
	})
	tests := []struct {
		name  string
		token Ref
	}{
		{name: "env token", token: "env:MEGA_DOUB_TEST_VAULT_TOKEN"},
		{name: "file token", token: Ref("file:" + tokenFile)},
		{name: "plain token", token: "plain:" + testVaultToken},
		{name: "token without a prefix", token: testVaultToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(VaultOptions{Address: srv.URL, Token: tt.token, Timeout: time.Second})
			got, err := r.Resolve(t.Context(), testVaultRef)
			if err != nil || got.Reveal() != testVaultValue {
				t.Errorf("resolver.Resolve() = %q, %v, want %q", got.Reveal(), err,
					testVaultValue)
			}
		})
	}
}

func Test_resolver_Resolve_vaultErrors(t *testing.T) {
	other := newTestVault(t, vaultSecret)
	tests := []struct {
		name     string
		status   int
		data     string
		body     string
		wantCode int
		errHas   string
		// errIs is the sentinel that the error must match and whose text it must equal.
		errIs error
	}{
		{name: "not found", status: http.StatusNotFound, wantCode: 404,
			errHas: "vault secret depot/mega-doub/prod not found: vault returned 404"},
		{name: "forbidden", status: http.StatusForbidden, wantCode: 403,
			errHas: "vault request: vault returned 403"},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantCode: 503,
			errHas: "vault request: vault returned 503"},
		{name: "redirect to another host", status: http.StatusTemporaryRedirect,
			wantCode: 307, errHas: "vault request: vault returned 307"},
		{name: "missing key", status: http.StatusOK, data: `{"other":"fake-other"}`,
			errHas: `has no key "storage-password"`},
		{name: "empty value", status: http.StatusOK, data: `{"storage-password":""}`,
			errHas: "is empty"},
		{name: "not a string", status: http.StatusOK, data: `{"storage-password":7}`,
			errHas: "want string"},
		{name: "not JSON", status: http.StatusOK, data: "fake-not-json",
			errHas: testVaultInvalidText, errIs: errVaultResponseInvalid},
		{name: "number out of range", status: http.StatusOK,
			data:   `{"storage-password":1e999}`,
			errHas: testVaultInvalidText, errIs: errVaultResponseInvalid},
		{name: "number out of range in another key", status: http.StatusOK,
			data:   `{"storage-password":"` + testVaultValue + `","other":1e999}`,
			errHas: testVaultInvalidText, errIs: errVaultResponseInvalid},
		{name: "data not an object", status: http.StatusOK,
			body:   `{"data":{"data":12345}}`,
			errHas: testVaultInvalidText, errIs: errVaultResponseInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
				switch tt.status {
				case http.StatusOK:
					if tt.body != "" {
						_, _ = io.WriteString(w, tt.body)
						return
					}
					writeVaultData(w, tt.data)
				case http.StatusTemporaryRedirect:
					http.Redirect(w, r, other.URL+r.URL.Path, tt.status)
				default:
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, testVaultBody)
				}
			})
			// 503 is retried by default, the retries are disabled for one request.
			r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
				Timeout: time.Second, Retry: retry.Options{MaxRetriesTime: -1}})
			_, err := r.Resolve(t.Context(), testVaultRef)
			if err == nil {
				t.Fatal("resolver.Resolve() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.errHas) {
				t.Errorf("resolver.Resolve() error = %v, want %q in it", err, tt.errHas)
			}
			if tt.errIs != nil {
				if !errors.Is(err, tt.errIs) {
					t.Errorf("resolver.Resolve() error = %v, want %v", err, tt.errIs)
				}
				if err.Error() != tt.errHas {
					t.Errorf("resolver.Resolve() error = %q, want %q", err, tt.errHas)
				}
				for _, s := range []string{"1e999", "12345", "fake-not-json", testVaultValue} {
					if strings.Contains(err.Error(), s) {
						t.Errorf("resolver.Resolve() error = %v, holds %q", err, s)
					}
				}
			}
			var se *StatusError
			if got := errors.As(err, &se); got != (tt.wantCode != 0) ||
				got && se.Code != tt.wantCode {
				t.Errorf("resolver.Resolve() error = %v, want *StatusError with code %d", err,
					tt.wantCode)
			}
			checkNoLeak(t, err, srv.URL)
			if n := srv.requests.Load(); n != 1 {
				t.Errorf("server got %d requests, want 1", n)
			}
		})
	}
	if n := other.requests.Load(); n != 0 {
		t.Errorf("the host of the redirect got %d requests, want 0", n)
	}
}

func Test_resolver_Resolve_vaultConnection(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "http://" + l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// A failed connection is retried by default, the retries are disabled for one attempt.
	r := New(VaultOptions{Address: address, Token: "plain:" + testVaultToken,
		Timeout: time.Second, Retry: retry.Options{MaxRetriesTime: -1}})
	_, err = r.Resolve(t.Context(), testVaultRef)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("resolver.Resolve() error = %v, want %v", err, syscall.ECONNREFUSED)
	}
	if !strings.HasPrefix(err.Error(), "vault request: ") {
		t.Errorf("resolver.Resolve() error = %v, want the prefix vault request", err)
	}
	checkNoLeak(t, err, address)
}

func Test_resolver_Resolve_vaultTimeout(t *testing.T) {
	const timeout = 200 * time.Millisecond
	release := make(chan struct{})
	srv := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	// The cleanups run in reverse order: the handler returns before the server closes.
	t.Cleanup(func() { close(release) })
	// A timeout of the attempt is retried by default, the retries are disabled for one
	// request.
	r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: timeout, Retry: retry.Options{MaxRetriesTime: -1}})
	start := time.Now()
	_, err := r.Resolve(t.Context(), testVaultRef)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolver.Resolve() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if elapsed < timeout || elapsed > timeout+time.Second {
		t.Errorf("resolver.Resolve() took %v, want from %v to %v", elapsed, timeout,
			timeout+time.Second)
	}
	checkNoLeak(t, err, srv.URL)
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("server got %d requests, want 1", n)
	}
}

func Test_resolver_Resolve_vaultTooLarge(t *testing.T) {
	srv := newTestVault(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", maxVaultResponse+1))
	})
	r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: time.Second})
	_, err := r.Resolve(t.Context(), testVaultRef)
	if !errors.Is(err, errVaultResponseTooLarge) {
		t.Fatalf("resolver.Resolve() error = %v, want %v", err, errVaultResponseTooLarge)
	}
	if want := "vault request: vault response is too large"; err.Error() != want {
		t.Errorf("resolver.Resolve() error = %v, want %q", err, want)
	}
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("server got %d requests, want 1", n)
	}
}

func Test_resolver_Resolve_vaultToken(t *testing.T) {
	unsetVaultEnv(t, "MEGA_DOUB_TEST_VAULT_UNSET")
	srv := newTestVault(t, vaultSecret)
	tests := []struct {
		name   string
		token  Ref
		stub   tokenStub
		errHas string
	}{
		{name: "vault token", token: "vault:depot/mega-doub/token/value",
			errHas: "vault token must not be a vault: reference"},
		{name: "unset token", token: "env:MEGA_DOUB_TEST_VAULT_UNSET",
			errHas: "env reference: variable MEGA_DOUB_TEST_VAULT_UNSET is not set"},
		{name: "empty token", token: "env:MEGA_DOUB_TEST_VAULT_UNSET",
			stub: func(context.Context, Ref) (Value, error) {
				return NewValue(""), nil
			},
			errHas: "vault token is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newVaultResolver(t, VaultOptions{Address: srv.URL, Token: tt.token,
				Timeout: time.Second})
			if tt.stub != nil {
				r.vault.resolver = tt.stub
			}
			clients := countClients(r)
			_, err := r.Resolve(t.Context(), testVaultRef)
			var te *TokenError
			if !errors.As(err, &te) || err.Error() != "vault token is unresolved" ||
				!strings.Contains(errors.Unwrap(err).Error(), tt.errHas) {
				t.Errorf("resolver.Resolve() error = %v, want *TokenError with %q in its cause",
					err, tt.errHas)
			}
			if n := clients.Load(); n != 0 {
				t.Errorf("resolver.Resolve() created %d clients, want 0", n)
			}
		})
	}
	if n := srv.requests.Load(); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}

func Test_resolver_Resolve_vaultNotConfigured(t *testing.T) {
	tests := []struct {
		name string
		opts VaultOptions
	}{
		{name: "zero options", opts: VaultOptions{}},
		{name: "empty address", opts: VaultOptions{Token: "plain:" + testVaultToken}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newVaultResolver(t, tt.opts)
			clients := countClients(r)
			_, err := r.Resolve(t.Context(), testVaultRef)
			if !errors.Is(err, errVaultNotConfigured) || err.Error() != "vault is not configured" {
				t.Errorf("resolver.Resolve() error = %v, want %v", err, errVaultNotConfigured)
			}
			if n := clients.Load(); n != 0 {
				t.Errorf("resolver.Resolve() created %d clients, want 0", n)
			}
		})
	}
}

func Test_resolver_Resolve_vaultCanceled(t *testing.T) {
	srv := newTestVault(t, vaultSecret)
	tests := []struct {
		name string
		// during cancels the context while the token resolves, not before the call.
		during bool
		// tokenErr is the error of the token that resolves while the context is canceled.
		tokenErr error
	}{
		{name: "before the call"},
		{name: "while the token resolves", during: true},
		{name: "while the token fails", during: true,
			tokenErr: errors.New("fake token source error")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := newVaultResolver(t, VaultOptions{Address: srv.URL,
				Token: "plain:" + testVaultToken, Timeout: time.Second})
			if tt.during {
				r.vault.resolver = tokenStub(func(context.Context, Ref) (Value, error) {
					cancel()
					return NewValue(testVaultToken), tt.tokenErr
				})
			} else {
				cancel()
			}
			clients := countClients(r)
			if _, err := r.Resolve(ctx, testVaultRef); !errors.Is(err, context.Canceled) {
				t.Errorf("resolver.Resolve() error = %v, want %v", err, context.Canceled)
			}
			if n := clients.Load(); n != 0 {
				t.Errorf("resolver.Resolve() created %d clients, want 0", n)
			}
		})
	}
	if n := srv.requests.Load(); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}

// cancelAfterFirstErr is a context that reports no cancellation on its first Err call
// and context.Canceled on the next ones.
type cancelAfterFirstErr struct {
	context.Context
	calls atomic.Int32
}

// Err returns nil at the first call and context.Canceled after it.
func (c *cancelAfterFirstErr) Err() error {
	if c.calls.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

func Test_resolver_Resolve_vaultCanceledWaiting(t *testing.T) {
	srv := newTestVault(t, vaultSecret)
	r := newVaultResolver(t, VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: time.Second})
	started, release := make(chan struct{}), make(chan struct{})
	r.vault.resolver = tokenStub(func(context.Context, Ref) (Value, error) {
		close(started)
		<-release
		return NewValue(testVaultToken), nil
	})
	var wg sync.WaitGroup
	var firstErr, waitErr error
	wg.Go(func() { _, firstErr = r.Resolve(t.Context(), testVaultRef) })
	<-started
	ctx := &cancelAfterFirstErr{Context: t.Context()}
	wg.Go(func() { _, waitErr = r.Resolve(ctx, testVaultRef) })
	// The second call is canceled while it waits for the entry that the first holds.
	deadline := time.Now().Add(10 * time.Second)
	for ctx.calls.Load() == 0 {
		if time.Since(deadline) > 0 {
			close(release)
			wg.Wait()
			t.Fatal("second resolver.Resolve() did not check the context in 10s")
		}
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if firstErr != nil {
		t.Errorf("first resolver.Resolve() error = %v, want nil", firstErr)
	}
	if !errors.Is(waitErr, context.Canceled) {
		t.Errorf("waiting resolver.Resolve() error = %v, want %v", waitErr, context.Canceled)
	}
}

func Test_resolver_Resolve_vaultOneClient(t *testing.T) {
	const (
		keys       = 4
		goroutines = 16
	)
	srv := newTestVault(t, func(w http.ResponseWriter, _ *http.Request) {
		writeVaultData(w, `{"key-0":"fake-0","key-1":"fake-1","key-2":"fake-2",`+
			`"key-3":"fake-3"}`)
	})
	r := newVaultResolver(t, VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: 5 * time.Second})
	clients := countClients(r)
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			n := strconv.Itoa(i % keys)
			value, err := r.Resolve(t.Context(), Ref("vault:depot/mega-doub/prod/key-"+n))
			if err == nil && value.Reveal() != "fake-"+n {
				err = errors.New("unexpected value of key-" + n)
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	if n := clients.Load(); n != 1 {
		t.Errorf("resolver.Resolve() created %d clients, want 1", n)
	}
	// Every reference is read once, the repeats come from the cache.
	if n := srv.requests.Load(); n != keys {
		t.Errorf("server got %d requests, want %d", n, keys)
	}
}

func Test_resolver_Resolve_vaultUnused(t *testing.T) {
	unsetVaultEnv(t, "MEGA_DOUB_TEST_VAULT_UNSET")
	srv := newTestVault(t, vaultSecret)
	r := newVaultResolver(t, VaultOptions{Address: srv.URL,
		Token: "env:MEGA_DOUB_TEST_VAULT_UNSET", Timeout: time.Second})
	clients := countClients(r)
	var tokenReads atomic.Int32
	r.vault.resolver = tokenStub(func(ctx context.Context, ref Ref) (Value, error) {
		tokenReads.Add(1)
		return r.Resolve(ctx, ref)
	})
	for _, ref := range []Ref{"plain:fake-plain", "fake-no-prefix"} {
		if _, err := r.Resolve(t.Context(), ref); err != nil {
			t.Errorf("resolver.Resolve(%v) error = %v, want nil", ref, err)
		}
	}
	if n := tokenReads.Load(); n != 0 {
		t.Errorf("the token was resolved %d times, want 0", n)
	}
	if n := clients.Load(); n != 0 {
		t.Errorf("resolver.Resolve() created %d clients, want 0", n)
	}
	if n := srv.requests.Load(); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}
