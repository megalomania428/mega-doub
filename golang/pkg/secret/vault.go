// Package secret is documented in ref.go.
package secret

import (
	"bytes"
	"context"
	"encoding/json"
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
	// CAFile is the file of the trusted certificates of the server, not applied by the
	// client.
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

// vault reads the vault: references of one run with one client, created at the first
// reference after the token resolves.
type vault struct {
	opts VaultOptions
	// resolver resolves the token, the resolver of the run by default.
	resolver Resolver
	// newClient creates the client, ci.NewVaultClient by default.
	newClient func(opts ci.VaultOptions) (*ci.VaultClient, error)
	// once creates the client, client and err are its result.
	once   sync.Once
	client *ci.VaultClient
	err    error
	// mu guards the state of the client: token is the value of the token once it resolves.
	mu    sync.Mutex
	token string
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
	return &vault{opts: opts, resolver: r, newClient: ci.NewVaultClient}
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
// the token resolves included, starts nothing and wins over the error of the token.
func (v *vault) connect(ctx context.Context) (*ci.VaultClient, error) {
	token, err := v.bootstrap(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}
	v.once.Do(func() { v.client, v.err = v.create(token) })
	return v.client, v.err
}

// bootstrap returns the value of the token. It resolves the token with the resolver of
// the run and keeps the value, so the other references do not read its source again.
func (v *vault) bootstrap(ctx context.Context) (string, error) {
	v.mu.Lock()
	token := v.token
	v.mu.Unlock()
	if token != "" {
		return token, nil
	}
	if strings.HasPrefix(string(v.opts.Token), "vault:") {
		return "", errVaultTokenVault
	}
	value, err := v.resolver.Resolve(ctx, v.opts.Token)
	if err != nil {
		return "", err
	}
	if token = value.Reveal(); token == "" {
		return "", errVaultTokenEmpty
	}
	v.mu.Lock()
	v.token = token
	v.mu.Unlock()
	return token, nil
}

// create returns the client with the token. Its HTTP client limits every attempt by
// opts.Timeout instead of a limit of the whole call and follows no redirect: Go keeps the
// token header on a redirect to another host.
func (v *vault) create(token string) (*ci.VaultClient, error) {
	client := ci.NewHTTPClient(v.opts.Timeout)
	client.Transport = &statusTransport{next: client.Transport, timeout: v.opts.Timeout}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return v.newClient(ci.VaultOptions{Address: v.opts.Address, Token: token,
		HTTPClient: client})
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
