// cspell:ignore httptest
package secret

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// brokenBase64 is a CERTIFICATE block whose body is not base64.
const brokenBase64 = "-----BEGIN CERTIFICATE-----\n!!!!\n-----END CERTIFICATE-----\n"

// testTLSVault is a test Vault server with TLS that counts its connections and requests.
type testTLSVault struct {
	*httptest.Server
	conns, requests atomic.Int32
}

// newTestTLSVault starts a test Vault server with TLS that answers with vaultSecret,
// closed after the test. The hook of the connections is set before the start, so
// httptest keeps its own accounting; the errors of the handshakes are discarded.
func newTestTLSVault(t *testing.T) *testTLSVault {
	t.Helper()
	v := &testTLSVault{}
	v.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		v.requests.Add(1)
		vaultSecret(w, r)
	}))
	v.Config.ErrorLog = log.New(io.Discard, "", 0)
	v.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			v.conns.Add(1)
		}
	}
	v.StartTLS()
	t.Cleanup(v.Close)
	return v
}

// checkNoContact fails the test when srv got a connection or a request.
func checkNoContact(t *testing.T, srv *testTLSVault) {
	t.Helper()
	if n := srv.conns.Load(); n != 0 {
		t.Errorf("server got %d connections, want 0", n)
	}
	if n := srv.requests.Load(); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}

// certPEM returns the CERTIFICATE block of the certificate of srv.
func certPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// writeCAFile writes the parts one after another to a new temporary file and returns
// its path.
func writeCAFile(t *testing.T, parts ...[]byte) string {
	t.Helper()
	return writeSecret(t, string(bytes.Join(parts, nil)))
}

// vaultKeyRef returns the reference to the key key-<i> of the secret mega-doub/prod.
func vaultKeyRef(i int) Ref {
	return Ref("vault:depot/mega-doub/prod/key-" + strconv.Itoa(i))
}

func Test_resolver_Resolve_vaultCAFile(t *testing.T) {
	srv := newTestTLSVault(t)
	r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: time.Second, CAFile: writeCAFile(t, certPEM(srv.Server))})
	got, err := r.Resolve(t.Context(), testVaultRef)
	if err != nil || got.Reveal() != testVaultValue {
		t.Errorf("resolver.Resolve() = %q, %v, want %q", got.Reveal(), err, testVaultValue)
	}
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("server got %d requests, want 1", n)
	}
}

func Test_resolver_Resolve_vaultUntrusted(t *testing.T) {
	srv := newTestTLSVault(t)
	r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: time.Second})
	_, err := r.Resolve(t.Context(), testVaultRef)
	var ve *tls.CertificateVerificationError
	if !errors.As(err, &ve) {
		t.Errorf("resolver.Resolve() error = %v, want *tls.CertificateVerificationError", err)
	}
	if n := srv.conns.Load(); n != 1 {
		t.Errorf("server got %d connections, want 1", n)
	}
	if n := srv.requests.Load(); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}

func Test_appendCerts(t *testing.T) {
	srv := newTestTLSVault(t)
	cert := certPEM(srv.Server)
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY",
		Bytes: []byte("fake-key-body")})
	garbage := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE",
		Bytes: []byte("not a certificate")})
	headers := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: srv.Certificate().Raw})
	// want is the pool with the certificate of srv only.
	want := x509.NewCertPool()
	want.AddCert(srv.Certificate())
	bom := []byte("\xEF\xBB\xBF")
	type args struct {
		data []byte
	}
	tests := []struct {
		name string
		args args
		// wantErr is the text of the error, errPrefix is its start when the end varies.
		wantErr   string
		errPrefix string
	}{
		{name: "two certificates with a whitespace tail",
			args: args{data: bytes.Join([][]byte{cert, cert, []byte("\n \t\n")}, nil)}},
		{name: "BOM before a certificate",
			args: args{data: bytes.Join([][]byte{bom, cert}, nil)}},
		{name: "BOM and a line break before a certificate",
			args: args{data: bytes.Join([][]byte{bom, []byte("\n"), cert}, nil)}},
		{name: "BOM before the second certificate",
			args:    args{data: bytes.Join([][]byte{cert, bom, cert}, nil)},
			wantErr: "ca_file: trailing data after block 1"},
		{name: "broken DER after a certificate",
			args:      args{data: bytes.Join([][]byte{cert, garbage}, nil)},
			errPrefix: "ca_file: block 2: parse certificate: "},
		{name: "broken base64 after a certificate",
			args:    args{data: bytes.Join([][]byte{cert, []byte(brokenBase64)}, nil)},
			wantErr: "ca_file: trailing data after block 1"},
		// pem.Decode skips the broken block silently, a known limit.
		{name: "broken base64 before a certificate",
			args: args{data: bytes.Join([][]byte{[]byte(brokenBase64), cert}, nil)}},
		{name: "private key", args: args{data: key},
			wantErr: `ca_file: block 1: unexpected PEM type "PRIVATE KEY"`},
		{name: "long type", args: args{data: pem.EncodeToMemory(&pem.Block{
			Type: strings.Repeat("T", 300), Bytes: []byte("fake-long-body")})},
			wantErr: `ca_file: block 1: unexpected PEM type "` + strings.Repeat("T", 64) + `"`},
		{name: "long type of control bytes", args: args{data: pem.EncodeToMemory(&pem.Block{
			Type: strings.Repeat("\x01", 300), Bytes: []byte("fake-long-body")})},
			wantErr: `ca_file: block 1: unexpected PEM type "` + strings.Repeat(`\x01`, 64) + `"`},
		{name: "private key after a certificate",
			args:    args{data: bytes.Join([][]byte{cert, key}, nil)},
			wantErr: `ca_file: block 2: unexpected PEM type "PRIVATE KEY"`},
		{name: "data after the last block",
			args:    args{data: bytes.Join([][]byte{cert, []byte("fake-tail\n")}, nil)},
			wantErr: "ca_file: trailing data after block 1"},
		{name: "headers", args: args{data: headers},
			wantErr: "ca_file: block 1: unexpected PEM headers"},
		{name: "no PEM", args: args{data: []byte("fake-not-pem\n")},
			wantErr: "ca_file: no certificates found"},
		{name: "empty", args: args{}, wantErr: "ca_file: no certificates found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := x509.NewCertPool()
			err := appendCerts(pool, tt.args.data)
			switch {
			case tt.errPrefix != "":
				if err == nil || !strings.HasPrefix(err.Error(), tt.errPrefix) {
					t.Errorf("appendCerts() error = %v, want the prefix %q", err, tt.errPrefix)
				}
			case tt.wantErr != "":
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("appendCerts() error = %v, want %q", err, tt.wantErr)
				}
			case err != nil:
				t.Errorf("appendCerts() error = %v, want nil", err)
			case !pool.Equal(want):
				t.Error("appendCerts() pool differs from the pool with one certificate")
			}
			// The errors never quote the content of a block.
			for _, s := range []string{"fake-key-body", // gitleaks:allow
				"ZmFrZS1rZXktYm9keQ", "not a certificate", "fake-tail", "fake-not-pem",
				"fake-long-body", "!!!!"} {
				if err != nil && strings.Contains(err.Error(), s) {
					t.Errorf("appendCerts() error = %v, holds %q", err, s)
				}
			}
		})
	}
}

func Test_resolver_Resolve_vaultCreateRemembered(t *testing.T) {
	poolErr := errors.New("fake system pool error")
	tests := []struct {
		name string
		// prepare makes the file at path, ca_file, before the first reference.
		prepare func(t *testing.T, path string, srv *testTLSVault)
		// poolFails makes the system pool fail until the repair.
		poolFails bool
		// check reports whether err is the failure of the creation.
		check func(err error) bool
	}{
		{name: "missing ca_file", prepare: func(*testing.T, string, *testTLSVault) {},
			check: func(err error) bool {
				return errors.Is(err, fs.ErrNotExist) &&
					strings.HasPrefix(err.Error(), "ca_file: open ")
			}},
		{name: "ca_file without PEM",
			prepare: func(t *testing.T, path string, _ *testTLSVault) {
				if err := os.WriteFile(path, []byte("fake-not-pem\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			check: func(err error) bool { return errors.Is(err, errCAFileEmpty) }},
		{name: "directory as ca_file",
			prepare: func(t *testing.T, path string, _ *testTLSVault) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			check: func(err error) bool {
				return strings.HasPrefix(err.Error(), "ca_file: ") &&
					strings.HasSuffix(err.Error(), " is not a regular file")
			}},
		{name: "system pool error", poolFails: true,
			prepare: func(t *testing.T, path string, srv *testTLSVault) {
				if err := os.WriteFile(path, certPEM(srv.Server), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			check: func(err error) bool {
				return errors.Is(err, poolErr) &&
					err.Error() == "vault system certificate pool: fake system pool error"
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestTLSVault(t)
			path := filepath.Join(t.TempDir(), "ca.pem")
			tt.prepare(t, path, srv)
			r := newVaultResolver(t, VaultOptions{Address: srv.URL,
				Token: "plain:" + testVaultToken, Timeout: time.Second, CAFile: path})
			clients := countClients(r)
			var poolCalls atomic.Int32
			var healthy atomic.Bool
			r.vault.systemPool = func() (*x509.CertPool, error) {
				poolCalls.Add(1)
				if tt.poolFails && !healthy.Load() {
					return nil, poolErr
				}
				return x509.SystemCertPool()
			}
			_, first := r.Resolve(t.Context(), vaultKeyRef(0))
			if first == nil || !tt.check(first) {
				t.Fatalf("resolver.Resolve() error = %v, want the failure of the creation", first)
			}
			// The repair does not help: the other references get the first failure.
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, certPEM(srv.Server), 0o600); err != nil {
				t.Fatal(err)
			}
			healthy.Store(true)
			for i := 1; i < 3; i++ {
				if _, err := r.Resolve(t.Context(), vaultKeyRef(i)); err != first {
					t.Errorf("resolver.Resolve(%v) error = %v, want %v", vaultKeyRef(i), err, first)
				}
			}
			var te *TokenError
			if errors.As(first, &te) {
				t.Errorf("resolver.Resolve() error = %v, want no *TokenError", first)
			}
			if n := clients.Load(); n != 0 {
				t.Errorf("resolver.Resolve() created %d clients, want 0", n)
			}
			if n := poolCalls.Load(); n != 1 {
				t.Errorf("the system pool was read %d times, want 1", n)
			}
			checkNoContact(t, srv)
		})
	}
}

func Test_resolver_Resolve_vaultCreateConcurrent(t *testing.T) {
	const goroutines = 32
	srv := newTestTLSVault(t)
	r := newVaultResolver(t, VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: time.Second, CAFile: writeCAFile(t, []byte("fake-not-pem\n"))})
	clients := countClients(r)
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() { _, errs[i] = r.Resolve(t.Context(), vaultKeyRef(i)) })
	}
	wg.Wait()
	for i, err := range errs {
		if err == nil || err != errs[0] {
			t.Errorf("goroutine %d: error = %v, want %v", i, err, errs[0])
		}
	}
	if n := clients.Load(); n != 0 {
		t.Errorf("resolver.Resolve() created %d clients, want 0", n)
	}
	checkNoContact(t, srv)
}

func Test_resolver_Resolve_vaultTokenRemembered(t *testing.T) {
	unsetVaultEnv(t, "MEGA_DOUB_TEST_VAULT_TOKEN")
	srv := newTestTLSVault(t)
	r := newVaultResolver(t, VaultOptions{Address: srv.URL,
		Token: "env:MEGA_DOUB_TEST_VAULT_TOKEN", Timeout: time.Second,
		CAFile: writeCAFile(t, certPEM(srv.Server))})
	clients := countClients(r)
	var tokenReads atomic.Int32
	r.vault.resolver = tokenStub(func(ctx context.Context, ref Ref) (Value, error) {
		tokenReads.Add(1)
		return r.Resolve(ctx, ref)
	})
	_, first := r.Resolve(t.Context(), vaultKeyRef(0))
	var te *TokenError
	if !errors.As(first, &te) || first.Error() != "vault token is unresolved" {
		t.Fatalf("resolver.Resolve() error = %v, want *TokenError", first)
	}
	want := "env reference: variable MEGA_DOUB_TEST_VAULT_TOKEN is not set"
	if cause := errors.Unwrap(first); cause == nil || cause.Error() != want {
		t.Errorf("errors.Unwrap() = %v, want %q", cause, want)
	}
	// The token becomes valid, the other references get the first failure anyway.
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", testVaultToken)
	for i := 1; i < 3; i++ {
		if _, err := r.Resolve(t.Context(), vaultKeyRef(i)); err != first {
			t.Errorf("resolver.Resolve(%v) error = %v, want %v", vaultKeyRef(i), err, first)
		}
	}
	if n := tokenReads.Load(); n != 1 {
		t.Errorf("the token was resolved %d times, want 1", n)
	}
	if n := clients.Load(); n != 0 {
		t.Errorf("resolver.Resolve() created %d clients, want 0", n)
	}
	checkNoContact(t, srv)
}

func Test_resolver_Resolve_vaultTokenSettled(t *testing.T) {
	tokenErr := errors.New("fake token failure")
	// refs are two vault: references with the key of vaultSecret.
	refs := []Ref{testVaultRef, "vault:depot/mega-doub/stage/storage-password"}
	tests := []struct {
		name string
		// slowErr is the outcome of the read of the token that starts first and ends last,
		// fastErr is the outcome of the other read; nil is the valid token.
		slowErr, fastErr error
		// wantErr is the failure of both references, nil is the value of both.
		wantErr error
		// wantClients and wantRequests are the numbers of clients and requests.
		wantClients, wantRequests int32
	}{
		{name: "failure before the value", fastErr: tokenErr, wantErr: tokenErr},
		{name: "value before the failure", slowErr: tokenErr, wantClients: 1,
			wantRequests: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestTLSVault(t)
			r := newVaultResolver(t, VaultOptions{Address: srv.URL,
				Token: "plain:" + testVaultToken, Timeout: time.Second,
				CAFile: writeCAFile(t, certPEM(srv.Server))})
			clients := countClients(r)
			started, release := make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			r.vault.resolver = tokenStub(func(context.Context, Ref) (Value, error) {
				err := tt.fastErr
				if reads.Add(1) == 1 {
					close(started)
					<-release
					err = tt.slowErr
				}
				if err != nil {
					return Value{}, err
				}
				return NewValue(testVaultToken), nil
			})
			errs := make([]error, len(refs))
			var wg sync.WaitGroup
			wg.Go(func() { _, errs[0] = r.Resolve(t.Context(), refs[0]) })
			<-started
			_, errs[1] = r.Resolve(t.Context(), refs[1])
			close(release)
			wg.Wait()
			for i, err := range errs {
				// errors.Is with the nil target reports whether err is nil.
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("resolver.Resolve(%v) error = %v, want %v", refs[i], err, tt.wantErr)
				}
			}
			if errs[0] != errs[1] {
				t.Errorf("resolver.Resolve() errors = %v and %v, want the same", errs[0], errs[1])
			}
			if n := clients.Load(); n != tt.wantClients {
				t.Errorf("resolver.Resolve() created %d clients, want %d", n, tt.wantClients)
			}
			if n := srv.requests.Load(); n != tt.wantRequests {
				t.Errorf("server got %d requests, want %d", n, tt.wantRequests)
			}
		})
	}
}

func Test_resolver_Resolve_vaultTokenCanceled(t *testing.T) {
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", testVaultToken)
	srv := newTestTLSVault(t)
	r := newVaultResolver(t, VaultOptions{Address: srv.URL,
		Token: "env:MEGA_DOUB_TEST_VAULT_TOKEN", Timeout: time.Second,
		CAFile: writeCAFile(t, certPEM(srv.Server))})
	var tokenReads atomic.Int32
	r.vault.resolver = tokenStub(func(ctx context.Context, ref Ref) (Value, error) {
		tokenReads.Add(1)
		return r.Resolve(ctx, ref)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.vault.read(ctx, "depot/mega-doub/prod/storage-password")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("vault.read() error = %v, want %v", err, context.Canceled)
	}
	// The cancellation is not remembered: the next call reads the token again.
	got, err := r.Resolve(t.Context(), testVaultRef)
	if err != nil || got.Reveal() != testVaultValue {
		t.Errorf("resolver.Resolve() = %q, %v, want %q", got.Reveal(), err, testVaultValue)
	}
	if n := tokenReads.Load(); n != 2 {
		t.Errorf("the token was resolved %d times, want 2", n)
	}
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("server got %d requests, want 1", n)
	}
}

func Test_resolver_Resolve_vaultTokenInvalid(t *testing.T) {
	tests := []struct {
		name  string
		token Ref
		// env is the value of MEGA_DOUB_TEST_VAULT_TOKEN.
		env string
		// secret is the invalid value of the token.
		secret string
	}{
		{name: "newline at the end of env", token: "env:MEGA_DOUB_TEST_VAULT_TOKEN",
			env: testVaultToken + "\n", secret: testVaultToken},
		{name: "space inside", token: "plain:fake token", secret: "fake token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", tt.env)
			srv := newTestTLSVault(t)
			r := newVaultResolver(t, VaultOptions{Address: srv.URL, Token: tt.token,
				Timeout: time.Second, CAFile: writeCAFile(t, certPEM(srv.Server))})
			clients := countClients(r)
			_, err := r.Resolve(t.Context(), testVaultRef)
			var te *TokenError
			if !errors.As(err, &te) || err.Error() != "vault token is unresolved" {
				t.Fatalf("resolver.Resolve() error = %v, want *TokenError", err)
			}
			want := "vault token contains invalid characters"
			if cause := errors.Unwrap(err); cause == nil || cause.Error() != want {
				t.Errorf("errors.Unwrap() = %v, want %q", cause, want)
			}
			if strings.Contains(err.Error(), tt.secret) ||
				strings.Contains(errors.Unwrap(err).Error(), tt.secret) {
				t.Errorf("resolver.Resolve() error = %v, holds the token", err)
			}
			if n := clients.Load(); n != 0 {
				t.Errorf("resolver.Resolve() created %d clients, want 0", n)
			}
			checkNoContact(t, srv)
		})
	}
}
