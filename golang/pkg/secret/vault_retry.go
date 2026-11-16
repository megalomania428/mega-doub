// cspell:ignore retryable

// Package secret is documented in ref.go.
package secret

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"

	ci "github.com/megalomania428/go-lib-ci"
	"github.com/megalomania428/mega-doub/pkg/retry"
)

// defaultOperation names the operation of a request whose context carries no name.
const defaultOperation = "vault read"

// operationKey is the key of the context value with the name of the operation of a
// request, which retryTransport writes to the records of the attempts.
type operationKey struct{}

// withOperation returns ctx with the name of the read of ref: vault read <mount>/<path>,
// or vault read <mount> when the path is empty. The name holds neither the token nor a
// value.
func withOperation(ctx context.Context, ref ci.VaultSecretRef) context.Context {
	name := defaultOperation + " " + ref.Mount
	if ref.Path != "" {
		name += "/" + ref.Path
	}
	return context.WithValue(ctx, operationKey{}, name)
}

// exhaustedError is the error of a request whose last attempt failed with a retryable
// error when the budget of the retries ran out or the retries are disabled: a failed
// connection, a timeout of the attempt, 429 or 5xx. Err is the error of the last attempt.
type exhaustedError struct {
	Err error
}

// Error returns the text of the error of the last attempt.
func (e *exhaustedError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the error of the last attempt.
func (e *exhaustedError) Unwrap() error {
	return e.Err
}

// retryTransport repeats the attempts of next, the transport of one attempt, with the
// schedule of opts while they fail with a retryable error, and logs every such failure.
// The lines of ci.Retry are discarded, so no text passes the logger by.
type retryTransport struct {
	next http.RoundTripper
	// base is the transport of the connections, its idle connections are closed before
	// the next attempt.
	base   *http.Transport
	opts   retry.Options
	logger *slog.Logger
}

// RoundTrip implements http.RoundTripper. A result that is not retryable is returned as
// it is; the error of the last attempt that ran out of the retries is *exhaustedError; a
// cancellation of the context of req is returned as the error of the context, even when
// the last attempt failed for another reason.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	name, ok := ctx.Value(operationKey{}).(string)
	if !ok {
		name = defaultOperation
	}
	opts := t.opts.CI(name)
	opts.Stderr = io.Discard
	opts.IdleCloser = t.base
	var (
		resp    *http.Response
		final   error
		attempt int
	)
	err := ci.Retry(ctx, opts, func(context.Context) error {
		attempt++
		r, err := t.next.RoundTrip(req)
		if err == nil || !retryable(ctx, err) {
			// nil stops ci.Retry, the result is kept apart.
			resp, final = r, err
			return nil
		}
		t.logger.Warn("vault request failed", "operation", name, "attempt", attempt,
			"error", err.Error())
		return err
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, ctxErr
	}
	if err != nil {
		return nil, &exhaustedError{Err: err}
	}
	return resp, final
}

// retryable reports whether err, the error of one attempt of a request with the context
// ctx, is worth another attempt: a response of 429 or 5xx and an error of the
// connection, a timeout of the attempt and of the TLS handshake included. A canceled
// ctx, another response outside 2xx, a failed verification of the certificate, a TLS
// record that is not TLS, such as an https address on a port of plain HTTP, and a body
// above maxVaultResponse are final.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code/100 == 5
	}
	var cve *tls.CertificateVerificationError
	var rhe tls.RecordHeaderError
	return !errors.As(err, &cve) && !errors.As(err, &rhe) &&
		!errors.Is(err, errVaultResponseTooLarge)
}

// unavailableErr returns the remembered error of a request that ran out of its retries,
// nil without it.
func (v *vault) unavailableErr() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.unavailable
}

// rememberUnavailable remembers err, the error of a request that ran out of its retries,
// for the run: an exception from the rule of the resolver that a failure is not cached,
// otherwise every reference to an unavailable Vault would spend the budget again.
func (v *vault) rememberUnavailable(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.unavailable = err
}
