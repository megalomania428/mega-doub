// cspell:ignore httptest
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megalomania428/mega-doub/internal/cli"
)

const (
	// retryVaultToken is the token of the configurations of the tests of the retries.
	retryVaultToken = "fake-retry-vault-token-8d2e"
	// retryVaultBody is the body of the 503 and 403 responses of the tests of the retries.
	retryVaultBody = "fake-retry-vault-body-6a1c"
	// retryVaultValue is the value of the secret that resolves after the retries.
	retryVaultValue = "fake-retry-vault-value-4e9d"
	// fastRetryYAML is the schedule of global.retry of the tests of the retries, the
	// budget of 200 ms with every field set.
	fastRetryYAML = "    max_retries_time: 200ms\n    backoff_initial: 5ms\n" +
		"    backoff_step: 5ms\n    backoff_step_attempts: 3\n    backoff_cap: 20ms\n"
	// slowRetryYAML is the schedule of global.retry whose first sleep takes 2 s.
	slowRetryYAML = "    max_retries_time: 10s\n    backoff_initial: 2s\n" +
		"    backoff_step: 5ms\n    backoff_step_attempts: 3\n    backoff_cap: 2s\n"
)

// retryVaultConfig returns a configuration with the debug level, the schedule of
// global.retry, the Vault server at address with the attempt timeout of 1 s, the
// password from the secret mega-doub/retry and the key b2_key from the secret
// mega-doub/forbidden.
func retryVaultConfig(address, schedule string) string {
	return "---\nglobal:\n  log_level: debug\n  retry:\n" + schedule + "vault:\n" +
		"  address: '" + address + "'\n  token: 'env:MEGA_DOUB_TEST_VAULT_TOKEN'\n" +
		"  timeout: 1s\nstorage:\n  url: 'b2://retry-bucket/prefix'\n" +
		"  snapshot_id: retry-main\n  password: 'vault:depot/mega-doub/retry/password'\n" +
		"  keys:\n    b2_key: 'vault:depot/mega-doub/forbidden/password'\n" +
		"targets:\n  - name: local\n    transport: local\n" +
		"    sources: [{type: test, id: data, path: /srv/data}]\n"
}

// newRetryVault starts a test Vault server, closed after the test. It answers the first
// two requests for the secret mega-doub/retry with 503 and the body, the next ones with
// the KV version 2 secret that holds retryVaultValue at the key password, and every
// request for the secret mega-doub/forbidden with 403 and the body.
func newRetryVault(t *testing.T) *httptest.Server {
	t.Helper()
	var retried atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/forbidden"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, retryVaultBody)
		case retried.Add(1) <= 2:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, retryVaultBody)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"data":{"password":"`+retryVaultValue+`"}}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// recordMessage returns the message of line, a record of the format, and fails the test
// when line is not a record of the logger.
func recordMessage(t *testing.T, format, line string) string {
	t.Helper()
	if format == "json" {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("stderr line %q is not a JSON record: %v", line, err)
		}
		msg, _ := record["msg"].(string)
		return msg
	}
	if !strings.HasPrefix(line, "time=") {
		t.Fatalf("stderr line %q is not a text record", line)
	}
	for _, msg := range []string{"vault request failed", "secret resolution failed"} {
		if strings.Contains(line, ` msg="`+msg+`" `) {
			return msg
		}
	}
	return ""
}

func Test_handle_vaultRetryRecords(t *testing.T) {
	setRetryEnv(t, nil)
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", retryVaultToken)
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			// Every format gets its own server: the server counts its requests.
			srv := newRetryVault(t)
			path := filepath.Join(t.TempDir(), "retry.yaml")
			writeFiles(t, map[string]string{path: retryVaultConfig(srv.URL, fastRetryYAML)})
			got, stdout, stderr := execute(t, Deps{}, "--log-format", format, "-c", path,
				"config", "validate", "--resolve")
			if got != 2 {
				t.Errorf("cli.Execute() = %v, want 2: %s", got, stderr)
			}
			if stdout != "" {
				t.Errorf("cli.Execute() stdout = %q, want none", stdout)
			}
			// Every line of stderr is a record of the logger, the lines of go-lib-ci that
			// start with ==> are discarded.
			attempts, failures := 0, 0
			for _, line := range strings.Split(stderr, "\n") {
				if line == "" {
					continue
				}
				if strings.HasPrefix(line, "==>") {
					t.Errorf("cli.Execute() stderr line %q passes the logger by", line)
				}
				switch recordMessage(t, format, line) {
				case "vault request failed":
					attempts++
				case "secret resolution failed":
					failures++
				}
			}
			// The password resolves after two attempts that failed with 503, the key b2_key
			// fails with 403 without retries.
			if attempts != 2 || failures != 1 {
				t.Errorf("cli.Execute() wrote %d records vault request failed and %d secret "+
					"resolution failed, want 2 and 1: %s", attempts, failures, stderr)
			}
			for _, text := range []string{"vault returned 503", "vault returned 403"} {
				if !strings.Contains(stderr, text) {
					t.Errorf("cli.Execute() stderr = %q, want %q in it", stderr, text)
				}
			}
			// The token, the value of the secret, the body, the address and the request
			// path stay out of the output, the records of the debug level included.
			for _, s := range []string{retryVaultToken, retryVaultValue, retryVaultBody,
				srv.URL, "/v1/"} {
				if strings.Contains(stdout, s) || strings.Contains(stderr, s) {
					t.Errorf("cli.Execute() output contains %q", s)
				}
			}
		})
	}
}

func Test_handle_vaultRetryCanceled(t *testing.T) {
	setRetryEnv(t, nil)
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", retryVaultToken)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var requests atomic.Int32
	// The first request cancels the run 20 ms later, within the sleep of 2 s.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		_ *http.Request) {
		if requests.Add(1) == 1 {
			time.AfterFunc(20*time.Millisecond, cancel)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "canceled.yaml")
	writeFiles(t, map[string]string{path: retryVaultConfig(srv.URL, slowRetryYAML)})
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	args := []string{"-c", path, "config", "validate", "--resolve"}
	if got := cli.Execute(ctx, args, stdout, stderr, cli.Build{}, New(Deps{})); got != 4 {
		t.Errorf("cli.Execute() = %v, want 4: %s", got, stderr)
	}
	interrupted := 0
	for _, record := range textRecords(stderr.String()) {
		switch {
		case strings.Contains(record, `msg="secret resolution failed"`):
			t.Errorf("cli.Execute() record %q, want no secret resolution failed", record)
		case strings.Contains(record, "msg=interrupted "):
			interrupted++
		case strings.Contains(record, "context canceled"):
			t.Errorf("cli.Execute() record %q, want no context canceled", record)
		}
	}
	if interrupted != 1 {
		t.Errorf("cli.Execute() wrote %d records interrupted, want 1: %s", interrupted,
			stderr)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("server got %d requests, want 1", n)
	}
}
