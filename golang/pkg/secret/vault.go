// Package secret is documented in ref.go.
package secret

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	ci "github.com/megalomania428/go-lib-ci"
	"github.com/megalomania428/mega-doub/pkg/retry"
)

// defaultVaultTimeout limits one attempt of a request when VaultOptions has no limit, as
// the default of vault.timeout does.
const defaultVaultTimeout = 30 * time.Second

// maxVaultResponse is the largest size of the body of a Vault response in bytes.
const maxVaultResponse = 1 << 20

// maxCAFileSize is the largest size of the file of VaultOptions.CAFile in bytes.
const maxCAFileSize = 1 << 20

// maxPEMType is the largest number of the characters of the type of a PEM block that an
// error quotes before %q escapes them: the type is the text of the file, which may be as
// long as the file. An escaped character takes up to 10 bytes, so the quoted type is
// never longer than 642 bytes.
const maxPEMType = 64

var (
	// errVaultNotConfigured is the error of a vault: reference without the Vault address.
	errVaultNotConfigured = errors.New("vault is not configured")
	// errVaultResponseTooLarge is the error of a response body above maxVaultResponse.
	errVaultResponseTooLarge = errors.New("vault response is too large")
	// errVaultResponseInvalid is the error of a response body that encoding/json does not
	// decode into a KV version 2 secret; its error would quote a character or a number
	// of the body.
	errVaultResponseInvalid = errors.New("vault response is not a valid KV version 2 secret")
	// errVaultTokenEmpty is the error of a token whose value is empty.
	errVaultTokenEmpty = errors.New("vault token is empty")
	// errVaultTokenVault is the error of a token that is a vault: reference itself.
	errVaultTokenVault = errors.New("vault token must not be a vault: reference")
	// errVaultTokenInvalid is the error of a token with a byte outside 0x21..0x7e.
	errVaultTokenInvalid = errors.New("vault token contains invalid characters")
	// errCAFileEmpty is the error of a ca_file without a PEM block.
	errCAFileEmpty = errors.New("ca_file: no certificates found")
)

// VaultOptions holds the settings of the Vault client of one run. The zero value means
// that Vault is not configured, a vault: reference fails then.
type VaultOptions struct {
	// Address is the address of the Vault server.
	Address string
	// Token is the reference of the token: env:, file:, plain: or a value without a prefix.
	Token Ref
	// Timeout limits one attempt of a request: the connection, TLS, the response headers
	// and the body; zero or below selects 30 seconds.
	Timeout time.Duration
	// CAFile is the PEM file of the certificates that the client trusts besides the system
	// ones for an https address; an http address does not use it.
	CAFile string
	// Retry holds the options of the retries of the requests, not applied by the client.
	Retry retry.Options
	// Logger receives the records of the client, nil discards them.
	Logger *slog.Logger
}

// StatusError is the error of a Vault response outside 2xx. It holds neither the URL nor
// the body of the response.
type StatusError struct {
	// Code is the HTTP status code of the response.
	Code int
}

// Error returns the text with the status code.
func (e *StatusError) Error() string {
	return fmt.Sprintf("vault returned %d", e.Code)
}

// TokenError is the error of a vault: reference when the token of Vault does not resolve:
// its reference fails or is a vault: reference, its value is empty or has a byte outside
// 0x21..0x7e. Err is the cause, which never holds the value of the token.
type TokenError struct {
	Err error
}

// Error returns the short text without the cause.
func (e *TokenError) Error() string {
	return "vault token is unresolved"
}

// Unwrap returns the cause.
func (e *TokenError) Unwrap() error {
	return e.Err
}

// vault reads the vault: references of one run with one client, created at the first
// reference after the token resolves. A failure to create the client (the token does
// not resolve, ca_file is broken, the system pool fails) is remembered for the run: the
// other references get it without a new attempt. This is an exception from the rule of
// the resolver that a failure is not cached, otherwise every reference would repeat the
// same failure.
type vault struct {
	opts VaultOptions
	// resolver resolves the token, the resolver of the run by default.
	resolver Resolver
	// newClient creates the client, ci.NewVaultClient by default.
	newClient func(opts ci.VaultOptions) (*ci.VaultClient, error)
	// systemPool returns the system certificate pool, x509.SystemCertPool by default.
	systemPool func() (*x509.CertPool, error)
	// once creates the client, client and err are its result.
	once   sync.Once
	client *ci.VaultClient
	err    error
	// mu guards the state of the client only while it is read or written, never during a
	// request or a call of the resolver: token and tokenErr are the first outcome of the
	// token, its value or its failure, at most one of them is set.
	mu       sync.Mutex
	token    string
	tokenErr error
}

// newVault returns the reader of the vault: references of r with the defaults for the
// zero fields of opts.
func newVault(opts VaultOptions, r Resolver) *vault {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultVaultTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &vault{opts: opts, resolver: r, newClient: ci.NewVaultClient,
		systemPool: x509.SystemCertPool}
}

// read returns the value of the key of the KV version 2 secret at path, the body of a
// vault: reference. A cancellation of ctx is returned as the error of ctx.
func (v *vault) read(ctx context.Context, path string) (string, error) {
	if v.opts.Address == "" {
		return "", errVaultNotConfigured
	}
	client, err := v.connect(ctx)
	if err != nil {
		return "", err
	}
	ref, err := ci.ParseVaultKVv2Path(path)
	if err != nil {
		return "", err
	}
	value, err := client.ReadVaultKVv2(ctx, ref)
	if err == nil {
		return value, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return "", ctxErr
	}
	return "", readError(ref, err)
}

// connect returns the client of the run, created with the token at the first call. The
// token resolves before the client is created, so an error of ctx, one that comes while
// the token resolves included, starts nothing, wins over the error of the token and is
// not remembered. The first outcome of the token and the result of the creation are
// remembered for the run.
func (v *vault) connect(ctx context.Context) (*ci.VaultClient, error) {
	token, err := v.bootstrap(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if token, err = v.settle(token, err); err != nil {
		return nil, err
	}
	v.once.Do(func() { v.client, v.err = v.create(token) })
	return v.client, v.err
}

// bootstrap returns the remembered outcome of the token or, without it, the outcome of
// a new resolution of the token with the resolver of the run, which connect remembers.
// A failure is *TokenError.
func (v *vault) bootstrap(ctx context.Context) (string, error) {
	v.mu.Lock()
	token, tokenErr := v.token, v.tokenErr
	v.mu.Unlock()
	switch {
	case token != "":
		return token, nil
	case tokenErr != nil:
		return "", tokenErr
	case strings.HasPrefix(string(v.opts.Token), "vault:"):
		return "", &TokenError{Err: errVaultTokenVault}
	}
	value, err := v.resolver.Resolve(ctx, v.opts.Token)
	if err != nil {
		return "", &TokenError{Err: err}
	}
	if token = value.Reveal(); token == "" {
		return "", &TokenError{Err: errVaultTokenEmpty}
	}
	// net/http rejects a control byte in the header before the connection, with an error
	// that a retry would repeat, and a space passes it; neither is in a Vault token.
	if !validToken(token) {
		return "", &TokenError{Err: errVaultTokenInvalid}
	}
	return token, nil
}

// settle remembers the outcome of the token, the value token or the failure err, unless
// an outcome is remembered already, and returns the remembered one: when two references
// resolve the token at once, both get the outcome that came first.
func (v *vault) settle(token string, err error) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token == "" && v.tokenErr == nil {
		v.token, v.tokenErr = token, err
	}
	return v.token, v.tokenErr
}

// validToken reports whether every byte of token is from 0x21 to 0x7e.
func validToken(token string) bool {
	for i := range len(token) {
		if token[i] < 0x21 || token[i] > 0x7e {
			return false
		}
	}
	return true
}

// create returns the client with the token. Its HTTP client limits every attempt by
// opts.Timeout instead of a limit of the whole call and follows no redirect: Go keeps the
// token header on a redirect to another host.
func (v *vault) create(token string) (*ci.VaultClient, error) {
	client := ci.NewHTTPClient(v.opts.Timeout)
	if err := v.trust(client.Transport); err != nil {
		return nil, err
	}
	client.Transport = &statusTransport{next: client.Transport, timeout: v.opts.Timeout}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return v.newClient(ci.VaultOptions{Address: v.opts.Address, Token: token,
		HTTPClient: client})
}

// trust sets the TLS configuration of rt, the transport of ci.NewHTTPClient, for an
// https address: the protocol version 1.2 at least and the system pool, with the
// certificates of opts.CAFile besides it when the file is set. The configuration is new,
// so nothing that http.DefaultTransport holds turns the verification of TLS off. With an
// http address it reads nothing and changes nothing.
func (v *vault) trust(rt http.RoundTripper) error {
	if u, err := url.Parse(v.opts.Address); err == nil && u.Scheme == "http" {
		return nil
	}
	base, ok := rt.(*http.Transport)
	if !ok {
		return fmt.Errorf("vault transport is %T, want *http.Transport", rt)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12} // DevSkim: ignore DS112852
	if v.opts.CAFile != "" {
		pool, err := v.systemPool()
		if err != nil {
			return fmt.Errorf("vault system certificate pool: %w", err)
		}
		data, err := readRegularFile(v.opts.CAFile, maxCAFileSize, "ca_file")
		if err != nil {
			return err
		}
		if err := appendCerts(pool, data); err != nil {
			return err
		}
		config.RootCAs = pool
	}
	base.TLSClientConfig = config
	return nil
}

// appendCerts adds the certificates of data, the content of ca_file, to pool. Every PEM
// block is a CERTIFICATE without headers that parses, and only whitespace follows the
// last block; one leading UTF-8 byte order mark of data is dropped, as pem.Decode would
// take it for text before the first block and miss it. x509.CertPool.AppendCertsFromPEM
// is not used: it skips other blocks silently. pem.Decode skips without an error
// everything it does not decode as a block, so only the tail after the last block is
// checked. The errors name the block by its number from 1 that counts only the decoded
// blocks and never quote its base64 body; the error of another type quotes at most
// maxPEMType characters of the type with the escapes of %q, and the error of a
// certificate that does not parse wraps the error of crypto/x509, which may quote a
// field of it.
func appendCerts(pool *x509.CertPool, data []byte) error {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	blocks := 0
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			if blocks > 0 && len(bytes.TrimSpace(rest)) > 0 {
				return fmt.Errorf("ca_file: trailing data after block %d", blocks)
			}
			break
		}
		data = rest
		blocks++
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("ca_file: block %d: unexpected PEM type %.*q", blocks, maxPEMType,
				block.Type)
		}
		if len(block.Headers) > 0 {
			return fmt.Errorf("ca_file: block %d: unexpected PEM headers", blocks)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("ca_file: block %d: parse certificate: %w", blocks, err)
		}
		pool.AddCert(cert)
	}
	if blocks == 0 {
		return errCAFileEmpty
	}
	return nil
}

// statusTransport makes one attempt of a request to Vault under a context limited by
// timeout. A response outside 2xx, a redirect included, becomes *StatusError with the
// body closed unread; the body of a 2xx response of at most maxVaultResponse bytes is
// read within the attempt and handed over from memory.
type statusTransport struct {
	next    http.RoundTripper
	timeout time.Duration
}

// RoundTrip implements http.RoundTripper.
func (t *statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), t.timeout)
	defer cancel()
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		return nil, &StatusError{Code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVaultResponse+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > maxVaultResponse {
		return nil, errVaultResponseTooLarge
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// readError returns the error of the read of ref: a 404 response names the secret, the
// error of a request loses the URL that http.Client adds, a body that encoding/json does
// not decode loses its text, the others stay as they are.
func readError(ref ci.VaultSecretRef, err error) error {
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return fmt.Errorf("vault secret %s/%s not found: %w", ref.Mount, ref.Path, se)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("vault request: %w", ue.Err)
	}
	var syn *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.As(err, &syn) || errors.As(err, &te) {
		return errVaultResponseInvalid
	}
	return err
}
