// cspell:ignore ECONNREFUSED httptest retryable
package secret

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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
	// testAttemptTimeout is vault.timeout of the tests of the retries.
	testAttemptTimeout = 50 * time.Millisecond
	// testLongTimeout is vault.timeout of the tests that count the attempts exactly: on a
	// loaded machine a connection, a TLS handshake or a response may take longer than
	// testAttemptTimeout, and the timeout of an attempt is retried.
	testLongTimeout = time.Second
	// testOperation is the name of the operation of the reads of testVaultRef and of
	// vaultKeyRef.
	testOperation = "vault read depot/mega-doub/prod"
)

// fastRetry returns the schedule of the tests of the retries: the sleeps of 5, 10 and
// 15 ms three times each, then of 20 ms, 190 ms in 14 sleeps between 15 attempts within
// the budget of 200 ms. Every field is set: go-lib-ci replaces a backoff value of 0 or
// below with a default of seconds.
func fastRetry() retry.Options {
	return retry.Options{MaxRetriesTime: 200 * time.Millisecond,
		BackoffInitial: 5 * time.Millisecond, BackoffStep: 5 * time.Millisecond,
		BackoffStepAttempts: 3, BackoffCap: 20 * time.Millisecond}
}

// shortRetry returns fastRetry with the budget of 20 ms: 3 sleeps of 5 ms between 4
// attempts, for the tests whose every attempt waits for testAttemptTimeout.
func shortRetry() retry.Options {
	opts := fastRetry()
	opts.MaxRetriesTime = 20 * time.Millisecond
	return opts
}

// noRetry returns the schedule without retries.
func noRetry() retry.Options {
	return retry.Options{MaxRetriesTime: -1}
}

// recordLog keeps the records of a JSON logger of the debug level.
type recordLog struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	logger *slog.Logger
}

// newRecordLog returns an empty log with its logger.
func newRecordLog() *recordLog {
	l := &recordLog{}
	l.logger = slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l
}

// Write implements io.Writer.
func (l *recordLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// attempts returns the records of the log and fails the test on a record other than
// vault request failed of the warn level.
func (l *recordLog) attempts(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	text := l.buf.String()
	l.mu.Unlock()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %q: %v", line, err)
		}
		if record["level"] != "WARN" || record["msg"] != "vault request failed" {
			t.Fatalf("record %q, want level WARN and msg vault request failed", line)
		}
		records = append(records, record)
	}
	return records
}

// checkAttempts fails the test unless the records number the attempts from 1 in their
// order, name the operation testOperation and hold an error without the token and the
// URL of the server at address.
func checkAttempts(t *testing.T, records []map[string]any, address string) {
	t.Helper()
	for i, record := range records {
		// encoding/json decodes a number to float64.
		if got, ok := record["attempt"].(float64); !ok || got != float64(i+1) {
			t.Errorf("record %d: attempt = %v, want %d", i, record["attempt"], i+1)
		}
		if got := record["operation"]; got != testOperation {
			t.Errorf("record %d: operation = %v, want %q", i, got, testOperation)
		}
		text, ok := record["error"].(string)
		if !ok || text == "" {
			t.Errorf("record %d: error = %v, want a text", i, record["error"])
		}
		for _, s := range []string{testVaultToken, address, "http://", "https://", "/v1/"} {
			if strings.Contains(text, s) {
				t.Errorf("record %d: error = %q, holds %q", i, text, s)
			}
		}
	}
}

// statusSequence returns a handler that answers the n-th request with the n-th status
// and the requests after them with the last one: 200 with vaultSecret, another status
// with testVaultBody.
func statusSequence(statuses ...int) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		status := statuses[min(int(n.Add(1)), len(statuses))-1]
		if status == http.StatusOK {
			vaultSecret(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, testVaultBody)
	}
}

// thenSecret returns a handler that serves the first request with first and the other
// ones with vaultSecret.
func thenSecret(first http.HandlerFunc) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			first(w, r)
			return
		}
		vaultSecret(w, r)
	}
}

// vaultData returns a handler that answers with the KV version 2 secret of data.
func vaultData(data string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeVaultData(w, data)
	}
}

// closedAddress returns the http address of a closed port of the loopback.
func closedAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "http://" + l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// newPlainVault starts a test Vault server without TLS that answers with vaultSecret and
// counts its connections and requests, closed after the test.
func newPlainVault(t *testing.T) *testTLSVault {
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
	v.Start()
	t.Cleanup(v.Close)
	return v
}

// silentListener is a listener of the loopback that accepts connections and keeps them
// without a byte until the end of the test.
type silentListener struct {
	net.Listener
	accepted atomic.Int32
}

// newSilentListener starts a silent listener, closed with its connections after the
// test.
func newSilentListener(t *testing.T) *silentListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &silentListener{Listener: l}
	var mu sync.Mutex
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return s
}

// newRetryChain returns the transports that create builds for the schedule and the
// logger with the attempt timeout testLongTimeout, without TLS settings.
func newRetryChain(t *testing.T, schedule retry.Options,
	logger *slog.Logger) *retryTransport {
	t.Helper()
	base, ok := ci.NewHTTPClient(testLongTimeout).Transport.(*http.Transport)
	if !ok {
		t.Fatal("ci.NewHTTPClient() transport is not *http.Transport")
	}
	return &retryTransport{next: &statusTransport{next: base, timeout: testLongTimeout},
		base: base, opts: schedule, logger: logger}
}

// storeMax raises peak to n when n is above it.
func storeMax(peak *atomic.Int32, n int32) {
	for {
		p := peak.Load()
		if n <= p || peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func Test_withOperation(t *testing.T) {
	tests := []struct {
		name string
		ref  ci.VaultSecretRef
		want string
	}{
		{name: "mount and path", ref: ci.VaultSecretRef{Mount: "depot",
			Path: "mega-doub/prod", Key: "storage-password"}, want: testOperation},
		{name: "mount only", ref: ci.VaultSecretRef{Mount: "depot", Key: "token"},
			want: "vault read depot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withOperation(t.Context(), tt.ref).Value(operationKey{}); got != tt.want {
				t.Errorf("withOperation() value = %v, want %q", got, tt.want)
			}
		})
	}
}

func Test_exhaustedError_Error(t *testing.T) {
	e := &exhaustedError{Err: &StatusError{Code: http.StatusServiceUnavailable}}
	if got, want := e.Error(), "vault returned 503"; got != want {
		t.Errorf("exhaustedError.Error() = %q, want %q", got, want)
	}
}

func Test_exhaustedError_Unwrap(t *testing.T) {
	cause := &StatusError{Code: http.StatusTooManyRequests}
	e := &exhaustedError{Err: cause}
	if got := e.Unwrap(); got != cause {
		t.Errorf("exhaustedError.Unwrap() = %v, want %v", got, cause)
	}
}

func Test_retryable(t *testing.T) {
	certErr := &tls.CertificateVerificationError{Err: errors.New("fake unknown authority")}
	tests := []struct {
		name string
		err  error
		// canceled cancels the context of the request.
		canceled bool
		want     bool
	}{
		{name: "429", err: &StatusError{Code: http.StatusTooManyRequests}, want: true},
		{name: "500", err: &StatusError{Code: http.StatusInternalServerError}, want: true},
		{name: "503", err: &StatusError{Code: http.StatusServiceUnavailable}, want: true},
		{name: "599", err: &StatusError{Code: 599}, want: true},
		{name: "wrapped 503", err: fmt.Errorf("fake: %w",
			&StatusError{Code: http.StatusServiceUnavailable}), want: true},
		{name: "307", err: &StatusError{Code: http.StatusTemporaryRedirect}},
		{name: "400", err: &StatusError{Code: http.StatusBadRequest}},
		{name: "401", err: &StatusError{Code: http.StatusUnauthorized}},
		{name: "403", err: &StatusError{Code: http.StatusForbidden}},
		{name: "404", err: &StatusError{Code: http.StatusNotFound}},
		{name: "405", err: &StatusError{Code: http.StatusMethodNotAllowed}},
		{name: "408", err: &StatusError{Code: http.StatusRequestTimeout}},
		{name: "425", err: &StatusError{Code: http.StatusTooEarly}},
		{name: "600", err: &StatusError{Code: 600}},
		{name: "connection refused", err: syscall.ECONNREFUSED, want: true},
		{name: "timeout of the attempt", err: context.DeadlineExceeded, want: true},
		{name: "certificate", err: certErr},
		{name: "wrapped certificate", err: fmt.Errorf("fake: %w", certErr)},
		{name: "record header", err: tls.RecordHeaderError{Msg: "fake record"}},
		{name: "too large", err: errVaultResponseTooLarge},
		{name: "canceled", err: &StatusError{Code: http.StatusServiceUnavailable},
			canceled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			if got := retryable(ctx, tt.err); got != tt.want {
				t.Errorf("retryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_retryTransport_RoundTrip(t *testing.T) {
	t.Run("https to plain HTTP", func(t *testing.T) {
		srv := newPlainVault(t)
		records := newRecordLog()
		rt := newRetryChain(t, fastRetry(), records.logger)
		ctx := withOperation(t.Context(), ci.VaultSecretRef{Mount: "depot",
			Path: "mega-doub/prod"})
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://"+srv.Listener.Addr().String()+testVaultPath, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		var rhe tls.RecordHeaderError
		if !errors.As(err, &rhe) || string(rhe.RecordHeader[:]) != "HTTP/" {
			t.Errorf("retryTransport.RoundTrip() error = %v, want tls.RecordHeaderError of "+
				"HTTP/", err)
		}
		var ee *exhaustedError
		if errors.As(err, &ee) {
			t.Errorf("retryTransport.RoundTrip() error = %v, want no *exhaustedError", err)
		}
		if n := srv.conns.Load(); n != 1 {
			t.Errorf("server got %d connections, want 1", n)
		}
		if got := records.attempts(t); len(got) != 0 {
			t.Errorf("retryTransport.RoundTrip() wrote %d records, want 0", len(got))
		}
	})
	t.Run("request without an operation", func(t *testing.T) {
		srv := newTestVault(t, statusSequence(http.StatusServiceUnavailable))
		records := newRecordLog()
		rt := newRetryChain(t, noRetry(), records.logger)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			srv.URL+testVaultPath, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		var ee *exhaustedError
		var se *StatusError
		if !errors.As(err, &ee) || !errors.As(err, &se) ||
			se.Code != http.StatusServiceUnavailable {
			t.Errorf("retryTransport.RoundTrip() error = %v, want *exhaustedError of 503", err)
		}
		got := records.attempts(t)
		if len(got) != 1 || got[0]["operation"] != defaultOperation {
			t.Errorf("retryTransport.RoundTrip() records = %v, want one with operation %q",
				got, defaultOperation)
		}
		if n := srv.requests.Load(); n != 1 {
			t.Errorf("server got %d requests, want 1", n)
		}
	})
}

func Test_resolver_Resolve_vaultRetry(t *testing.T) {
	// slow answers after the timeout of the attempt, when the client has given up.
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(4 * testAttemptTimeout):
		}
		vaultSecret(w, r)
	}
	// dropped closes the connection without a response.
	dropped := func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}
	// cut closes the connection in the middle of the body of a 200 response.
	cut := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":`)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	retryAfter := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	large := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", maxVaultResponse+1))
	}
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		schedule retry.Options
		// errHas is the text that the error holds, empty for the value.
		errHas string
		// wantRequests is the number of the requests of the first reference, 0 for at
		// least two.
		wantRequests int32
		// exhausted tells that the error is *exhaustedError and is remembered.
		exhausted bool
		// minElapsed is the lower bound of the time of the first reference.
		minElapsed time.Duration
		// timeout is vault.timeout, testLongTimeout when 0.
		timeout time.Duration
	}{
		{name: "503 twice then 200", schedule: fastRetry(),
			handler: statusSequence(http.StatusServiceUnavailable,
				http.StatusServiceUnavailable, http.StatusOK), wantRequests: 3,
			minElapsed: 10 * time.Millisecond},
		{name: "429 then 200", schedule: fastRetry(),
			handler: statusSequence(http.StatusTooManyRequests,
				http.StatusOK), wantRequests: 2, minElapsed: 5 * time.Millisecond},
		{name: "dropped connection then 200", handler: thenSecret(dropped),
			schedule: fastRetry(), wantRequests: 2},
		{name: "timeout of the attempt then 200", handler: thenSecret(slow),
			schedule: fastRetry(), minElapsed: testAttemptTimeout,
			timeout: testAttemptTimeout},
		{name: "body cut then 200", handler: thenSecret(cut), schedule: fastRetry(),
			wantRequests: 2},
		{name: "persistent 503", handler: statusSequence(http.StatusServiceUnavailable),
			schedule: fastRetry(), errHas: "vault returned 503", exhausted: true,
			minElapsed: 190 * time.Millisecond},
		{name: "503 with Retry-After", handler: retryAfter, schedule: fastRetry(),
			errHas: "vault returned 503", exhausted: true,
			minElapsed: 190 * time.Millisecond},
		{name: "disabled retries", handler: statusSequence(http.StatusServiceUnavailable),
			schedule: noRetry(), errHas: "vault returned 503", wantRequests: 1,
			exhausted: true},
		{name: "403", handler: statusSequence(http.StatusForbidden), schedule: fastRetry(),
			errHas: "vault returned 403", wantRequests: 1},
		{name: "404", handler: statusSequence(http.StatusNotFound), schedule: fastRetry(),
			errHas: "vault returned 404", wantRequests: 1},
		{name: "307", handler: statusSequence(http.StatusTemporaryRedirect),
			schedule: fastRetry(), errHas: "vault returned 307", wantRequests: 1},
		{name: "body too large", handler: large, schedule: fastRetry(),
			errHas: "vault response is too large", wantRequests: 1},
		{name: "not JSON", handler: vaultData("fake-not-json"), schedule: fastRetry(),
			errHas: testVaultInvalidText, wantRequests: 1},
		{name: "missing key", handler: vaultData(`{"other":"fake-other"}`),
			schedule: fastRetry(), errHas: "has no key", wantRequests: 1},
		{name: "not a string", handler: vaultData(`{"storage-password":7,"key-1":7}`),
			schedule: fastRetry(), errHas: "want string", wantRequests: 1},
		{name: "empty value", handler: vaultData(`{"storage-password":"","key-1":""}`),
			schedule: fastRetry(), errHas: "is empty", wantRequests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestVault(t, tt.handler)
			records := newRecordLog()
			timeout := tt.timeout
			if timeout == 0 {
				timeout = testLongTimeout
			}
			r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
				Timeout: timeout, Retry: tt.schedule, Logger: records.logger})
			start := time.Now()
			got, err := r.Resolve(t.Context(), testVaultRef)
			elapsed := time.Since(start)
			requests := srv.requests.Load()
			if tt.wantRequests == 0 && requests < 2 ||
				tt.wantRequests != 0 && requests != tt.wantRequests {
				t.Errorf("server got %d requests, want %d (0 for at least 2)", requests,
					tt.wantRequests)
			}
			if elapsed < tt.minElapsed || elapsed > 2*time.Second {
				t.Errorf("resolver.Resolve() took %v, want from %v to 2s", elapsed,
					tt.minElapsed)
			}
			// Every failed attempt that is retried, the last one included, is logged.
			failed := 0
			switch {
			case tt.errHas == "":
				if err != nil || got.Reveal() != testVaultValue {
					t.Fatalf("resolver.Resolve() = %q, %v, want %q", got.Reveal(), err,
						testVaultValue)
				}
				failed = int(requests) - 1
			case err == nil || !strings.Contains(err.Error(), tt.errHas):
				t.Fatalf("resolver.Resolve() error = %v, want %q in it", err, tt.errHas)
			case tt.exhausted:
				failed = int(requests)
			}
			attempts := records.attempts(t)
			if len(attempts) != failed {
				t.Errorf("resolver.Resolve() wrote %d records, want %d", len(attempts), failed)
			}
			checkAttempts(t, attempts, srv.URL)
			if tt.errHas == "" {
				return
			}
			checkNoLeak(t, err, srv.URL)
			var ee *exhaustedError
			if errors.As(err, &ee) != tt.exhausted {
				t.Errorf("resolver.Resolve() error = %v, *exhaustedError %v, want %v", err,
					!tt.exhausted, tt.exhausted)
			}
			// The next reference gets a remembered error without a request, another
			// error is not remembered.
			_, next := r.Resolve(t.Context(), vaultKeyRef(1))
			want := requests + 1
			if tt.exhausted {
				want = requests
				if next == nil || next.Error() != err.Error() {
					t.Errorf("next resolver.Resolve() error = %v, want %v", next, err)
				}
			}
			if n := srv.requests.Load(); n != want {
				t.Errorf("server got %d requests after the next reference, want %d", n, want)
			}
		})
	}
}

func Test_resolver_Resolve_vaultRetryNilLogger(t *testing.T) {
	srv := newTestVault(t, statusSequence(http.StatusServiceUnavailable))
	r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: testAttemptTimeout, Retry: fastRetry()})
	_, err := r.Resolve(t.Context(), testVaultRef)
	var ee *exhaustedError
	if !errors.As(err, &ee) {
		t.Errorf("resolver.Resolve() error = %v, want *exhaustedError", err)
	}
	if n := srv.requests.Load(); n < 2 {
		t.Errorf("server got %d requests, want at least 2", n)
	}
}

func Test_resolver_Resolve_vaultRetryTLS(t *testing.T) {
	tests := []struct {
		name  string
		start func(t *testing.T) *testTLSVault
		// https replaces the scheme of the address of the server with https.
		https bool
		// want tells whether the error is the expected one.
		want func(err error) bool
	}{
		{name: "untrusted certificate", start: newTestTLSVault,
			want: func(err error) bool {
				var cve *tls.CertificateVerificationError
				return errors.As(err, &cve)
			}},
		// http.Client replaces tls.RecordHeaderError of HTTP/ with http.ErrSchemeMismatch.
		{name: "https to plain HTTP", start: newPlainVault, https: true,
			want: func(err error) bool { return errors.Is(err, http.ErrSchemeMismatch) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := tt.start(t)
			address := srv.URL
			if tt.https {
				address = "https://" + srv.Listener.Addr().String()
			}
			records := newRecordLog()
			r := New(VaultOptions{Address: address, Token: "plain:" + testVaultToken,
				Timeout: testLongTimeout, Retry: fastRetry(), Logger: records.logger})
			_, err := r.Resolve(t.Context(), testVaultRef)
			if !tt.want(err) {
				t.Errorf("resolver.Resolve() error = %v, want the error of %s", err, tt.name)
			}
			var ee *exhaustedError
			if errors.As(err, &ee) {
				t.Errorf("resolver.Resolve() error = %v, want no *exhaustedError", err)
			}
			if n := srv.conns.Load(); n != 1 {
				t.Errorf("server got %d connections, want 1", n)
			}
			// The error is not remembered: the next reference connects again.
			if _, err := r.Resolve(t.Context(), vaultKeyRef(1)); !tt.want(err) {
				t.Errorf("next resolver.Resolve() error = %v, want the error of %s", err,
					tt.name)
			}
			if n := srv.conns.Load(); n != 2 {
				t.Errorf("server got %d connections after the next reference, want 2", n)
			}
			if got := records.attempts(t); len(got) != 0 {
				t.Errorf("resolver.Resolve() wrote %d records, want 0", len(got))
			}
			if n := srv.requests.Load(); n != 0 {
				t.Errorf("server got %d requests, want 0", n)
			}
		})
	}
}

func Test_resolver_Resolve_vaultRetryHang(t *testing.T) {
	tests := []struct {
		name string
		// start starts the server and returns its address and its counter of the
		// requests or connections.
		start func(t *testing.T) (string, *atomic.Int32)
	}{
		{name: "body of 200 hangs", start: func(t *testing.T) (string, *atomic.Int32) {
			release := make(chan struct{})
			srv := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			})
			// The cleanups run in reverse order: the handler returns before the server
			// closes.
			t.Cleanup(func() { close(release) })
			return srv.URL, &srv.requests
		}},
		{name: "TLS handshake hangs", start: func(t *testing.T) (string, *atomic.Int32) {
			l := newSilentListener(t)
			return "https://" + l.Addr().String(), &l.accepted
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			address, count := tt.start(t)
			records := newRecordLog()
			r := New(VaultOptions{Address: address, Token: "plain:" + testVaultToken,
				Timeout: testAttemptTimeout, Retry: shortRetry(), Logger: records.logger})
			_, err := r.Resolve(t.Context(), testVaultRef)
			var ee *exhaustedError
			if !errors.As(err, &ee) {
				t.Errorf("resolver.Resolve() error = %v, want *exhaustedError", err)
			}
			attempts := records.attempts(t)
			if len(attempts) < 2 {
				t.Errorf("resolver.Resolve() wrote %d records, want at least 2", len(attempts))
			}
			checkAttempts(t, attempts, address)
			contacts := count.Load()
			if contacts < 2 {
				t.Errorf("server got %d contacts, want at least 2", contacts)
			}
			// The error is remembered: the next reference makes no attempt.
			if _, next := r.Resolve(t.Context(), vaultKeyRef(1)); next == nil ||
				err == nil || next.Error() != err.Error() {
				t.Errorf("next resolver.Resolve() error = %v, want %v", next, err)
			}
			if n := len(records.attempts(t)); n != len(attempts) {
				t.Errorf("next resolver.Resolve() wrote %d records, want none",
					n-len(attempts))
			}
			if n := count.Load(); n != contacts {
				t.Errorf("server got %d contacts after the next reference, want %d", n,
					contacts)
			}
		})
	}
}

func Test_resolver_Resolve_vaultUnavailable(t *testing.T) {
	tests := []struct {
		name string
		// status is the status of every response, 0 for a closed port.
		status int
	}{
		{name: "503", status: http.StatusServiceUnavailable},
		{name: "429", status: http.StatusTooManyRequests},
		{name: "closed port"},
	}
	for _, tt := range tests {
		for _, parallel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parallel %t", tt.name, parallel), func(t *testing.T) {
				var address string
				var requests *atomic.Int32
				if tt.status == 0 {
					address = closedAddress(t)
				} else {
					srv := newTestVault(t, statusSequence(tt.status))
					address, requests = srv.URL, &srv.requests
				}
				records := newRecordLog()
				r := New(VaultOptions{Address: address, Token: "plain:" + testVaultToken,
					Timeout: testLongTimeout, Retry: fastRetry(), Logger: records.logger})
				errs := make([]error, 3)
				if parallel {
					var wg sync.WaitGroup
					for i := range errs {
						wg.Go(func() { _, errs[i] = r.Resolve(t.Context(), vaultKeyRef(i)) })
					}
					wg.Wait()
				} else {
					_, errs[0] = r.Resolve(t.Context(), vaultKeyRef(0))
					first, before := len(records.attempts(t)), int32(0)
					if requests != nil {
						before = requests.Load()
					}
					for i := 1; i < len(errs); i++ {
						_, errs[i] = r.Resolve(t.Context(), vaultKeyRef(i))
						if n := len(records.attempts(t)); n != first {
							t.Errorf("reference %d wrote %d records, want none", i, n-first)
						}
						if requests != nil && requests.Load() != before {
							t.Errorf("reference %d made %d requests, want none", i,
								requests.Load()-before)
						}
					}
				}
				// The budget is spent once: the attempts make one sequence from 1.
				attempts := records.attempts(t)
				if len(attempts) < 2 {
					t.Errorf("resolver.Resolve() wrote %d records, want at least 2",
						len(attempts))
				}
				checkAttempts(t, attempts, address)
				if requests != nil && int(requests.Load()) != len(attempts) {
					t.Errorf("server got %d requests, want %d", requests.Load(), len(attempts))
				}
				for i, err := range errs {
					var ee *exhaustedError
					if !errors.As(err, &ee) {
						t.Errorf("reference %d: error = %v, want *exhaustedError", i, err)
					}
					if err == nil || errs[0] == nil || err.Error() != errs[0].Error() {
						t.Errorf("reference %d: error = %v, want %v", i, err, errs[0])
					}
				}
				if tt.status == 0 && !errors.Is(errs[0], syscall.ECONNREFUSED) {
					t.Errorf("resolver.Resolve() error = %v, want %v", errs[0],
						syscall.ECONNREFUSED)
				}
			})
		}
	}
}

func Test_resolver_Resolve_vaultSerial(t *testing.T) {
	t.Run("one request at a time", func(t *testing.T) {
		var running, peak atomic.Int32
		srv := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
			storeMax(&peak, running.Add(1))
			defer running.Add(-1)
			select {
			case <-r.Context().Done():
			case <-time.After(50 * time.Millisecond):
			}
			w.WriteHeader(http.StatusForbidden)
		})
		r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
			Timeout: time.Second, Retry: noRetry()})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Go(func() { _, errs[i] = r.Resolve(t.Context(), vaultKeyRef(i)) })
		}
		wg.Wait()
		for i, err := range errs {
			if err == nil || !strings.Contains(err.Error(), "vault returned 403") {
				t.Errorf("reference %d: error = %v, want vault returned 403", i, err)
			}
		}
		if n := peak.Load(); n != 1 {
			t.Errorf("server handled %d requests at once, want 1", n)
		}
		// 403 is not remembered, so both references make their requests.
		if n := srv.requests.Load(); n != 2 {
			t.Errorf("server got %d requests, want 2", n)
		}
	})
	t.Run("canceled while waiting", func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		start := sync.OnceFunc(func() { close(started) })
		srv := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
			start()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			vaultSecret(w, r)
		})
		stop := sync.OnceFunc(func() { close(release) })
		// The cleanups run in reverse order: the handler returns before the server closes.
		t.Cleanup(stop)
		r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
			Timeout: 10 * time.Second, Retry: noRetry()})
		var wg sync.WaitGroup
		var firstErr error
		wg.Go(func() { _, firstErr = r.Resolve(t.Context(), testVaultRef) })
		<-started
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(20*time.Millisecond, cancel)
		begin := time.Now()
		_, err := r.Resolve(ctx, vaultKeyRef(1))
		elapsed := time.Since(begin)
		stop()
		wg.Wait()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiting resolver.Resolve() error = %v, want %v", err, context.Canceled)
		}
		if elapsed > time.Second {
			t.Errorf("waiting resolver.Resolve() took %v, want at most 1s", elapsed)
		}
		if firstErr != nil {
			t.Errorf("first resolver.Resolve() error = %v, want nil", firstErr)
		}
		if n := srv.requests.Load(); n != 1 {
			t.Errorf("server got %d requests, want 1", n)
		}
	})
	t.Run("canceled with Vault unavailable", func(t *testing.T) {
		r := newVaultResolver(t, VaultOptions{Address: closedAddress(t),
			Token: "plain:" + testVaultToken, Timeout: testAttemptTimeout, Retry: noRetry()})
		const path = "depot/mega-doub/prod/storage-password"
		_, err := r.vault.read(t.Context(), path)
		var ee *exhaustedError
		if !errors.As(err, &ee) {
			t.Fatalf("vault.read() error = %v, want *exhaustedError", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		// select takes the free turn instead of the cancellation about half of the time.
		for i := range 20 {
			if _, err := r.vault.read(ctx, path); !errors.Is(err, context.Canceled) {
				t.Fatalf("vault.read() %d with a canceled context: error = %v, want %v", i,
					err, context.Canceled)
			}
		}
	})
	t.Run("canceled after the retries ran out", func(t *testing.T) {
		srv := newTestVault(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		r := newVaultResolver(t, VaultOptions{Address: srv.URL,
			Token: "plain:" + testVaultToken, Timeout: testLongTimeout, Retry: noRetry()})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		create := r.vault.newClient
		r.vault.newClient = func(opts ci.VaultOptions) (*ci.VaultClient, error) {
			opts.HTTPClient.Transport = cancelAfter{next: opts.HTTPClient.Transport,
				cancel: cancel}
			return create(opts)
		}
		_, err := r.Resolve(ctx, testVaultRef)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resolver.Resolve() error = %v, want %v", err, context.Canceled)
		}
		var ee *exhaustedError
		if errors.As(err, &ee) {
			t.Errorf("resolver.Resolve() error = %v, want no *exhaustedError", err)
		}
		// The unavailability is not remembered: the next reference makes its request.
		_, err = r.Resolve(t.Context(), vaultKeyRef(1))
		if !errors.As(err, &ee) {
			t.Errorf("next resolver.Resolve() error = %v, want *exhaustedError", err)
		}
		if n := srv.requests.Load(); n != 2 {
			t.Errorf("server got %d requests, want 2", n)
		}
	})
	t.Run("canceled after a successful response", func(t *testing.T) {
		srv := newTestVault(t, vaultSecret)
		r := newVaultResolver(t, VaultOptions{Address: srv.URL,
			Token: "plain:" + testVaultToken, Timeout: testLongTimeout, Retry: noRetry()})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		create := r.vault.newClient
		r.vault.newClient = func(opts ci.VaultOptions) (*ci.VaultClient, error) {
			opts.HTTPClient.Transport = cancelAfter{next: opts.HTTPClient.Transport,
				cancel: cancel}
			return create(opts)
		}
		if _, err := r.Resolve(ctx, testVaultRef); !errors.Is(err, context.Canceled) {
			t.Fatalf("resolver.Resolve() error = %v, want %v", err, context.Canceled)
		}
		got, err := r.Resolve(t.Context(), testVaultRef)
		if err != nil || got.Reveal() != testVaultValue {
			t.Errorf("next resolver.Resolve() = %v, %v, want %q", got, err, testVaultValue)
		}
		if n := srv.requests.Load(); n != 2 {
			t.Errorf("server got %d requests, want 2", n)
		}
	})
}

// cancelAfter is a RoundTripper that cancels the context after next returns.
type cancelAfter struct {
	next   http.RoundTripper
	cancel context.CancelFunc
}

// RoundTrip calls next and cancels the context.
func (c cancelAfter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	c.cancel()
	return resp, err
}

// cancelAfterWrite is a Writer that cancels the context after next takes a record.
type cancelAfterWrite struct {
	next   io.Writer
	cancel context.CancelFunc
}

// Write writes to next and cancels the context.
func (c cancelAfterWrite) Write(p []byte) (int, error) {
	n, err := c.next.Write(p)
	c.cancel()
	return n, err
}

func Test_resolver_Resolve_vaultRetryCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := newTestVault(t, thenSecret(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	records := newRecordLog()
	// The record of the failed first attempt cancels the context, within the sleep of 2 s.
	logger := slog.New(slog.NewJSONHandler(cancelAfterWrite{next: records,
		cancel: cancel}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := New(VaultOptions{Address: srv.URL, Token: "plain:" + testVaultToken,
		Timeout: time.Second, Logger: logger,
		Retry: retry.Options{MaxRetriesTime: 10 * time.Second,
			BackoffInitial: 2 * time.Second, BackoffStep: 5 * time.Millisecond,
			BackoffStepAttempts: 3, BackoffCap: 2 * time.Second}})
	start := time.Now()
	_, err := r.Resolve(ctx, testVaultRef)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolver.Resolve() error = %v, want %v", err, context.Canceled)
	}
	// Without the cancellation the sleep alone would take 2 s.
	if elapsed >= time.Second {
		t.Errorf("resolver.Resolve() took %v, want less than 1s", elapsed)
	}
	var ee *exhaustedError
	if errors.As(err, &ee) {
		t.Errorf("resolver.Resolve() error = %v, want no *exhaustedError", err)
	}
	// The attempt before the cancellation stays logged.
	attempts := records.attempts(t)
	if len(attempts) != 1 {
		t.Errorf("resolver.Resolve() wrote %d records, want 1", len(attempts))
	}
	checkAttempts(t, attempts, srv.URL)
	// The cancellation is not remembered: the next call makes its request.
	got, err := r.Resolve(t.Context(), testVaultRef)
	if err != nil || got.Reveal() != testVaultValue {
		t.Errorf("next resolver.Resolve() = %q, %v, want %q", got.Reveal(), err,
			testVaultValue)
	}
	if n := srv.requests.Load(); n != 2 {
		t.Errorf("server got %d requests, want 2", n)
	}
}
