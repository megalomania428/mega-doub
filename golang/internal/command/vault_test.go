// cspell:ignore httptest
package command

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// vaultCanaryToken is the token that the test Vault server of the command tests expects.
const vaultCanaryToken = "fake-canary-vault-token-7f3a"

// newCanaryVault starts a test Vault server, closed after the test. It answers a request
// for a secret path that ends with /forbidden with 403 and the body, any other request
// with the token with the KV version 2 secret that holds the value at the key password.
func newCanaryVault(t *testing.T, value, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		switch {
		case r.Header.Get("X-Vault-Token") != vaultCanaryToken:
			w.WriteHeader(http.StatusBadRequest)
		case strings.HasSuffix(r.URL.Path, "/forbidden"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, body)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"data":{"password":"`+value+`"}}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func Test_vaultOptions(t *testing.T) {
	setRetryEnv(t, nil)
	t.Setenv("MAX_RETRIES_TIME", "7s")
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", vaultCanaryToken)
	srv := newCanaryVault(t, "fake-options-value", "fake-options-body")
	path := filepath.Join(t.TempDir(), "vault.yaml")
	writeFiles(t, map[string]string{path: "---\nvault:\n  address: '" + srv.URL + "'\n" +
		"  token: 'env:MEGA_DOUB_TEST_VAULT_TOKEN'\n  timeout: 3s\n" +
		"  ca_file: /etc/ssl/certs/fake-vault-ca.pem\n"})
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	type args struct {
		loaded *config.Loaded
		logger *slog.Logger
	}
	tests := []struct {
		name string
		args args
		want secret.VaultOptions
	}{
		{name: "vault section", args: args{loaded: loaded, logger: logger},
			want: secret.VaultOptions{Address: srv.URL,
				Token: "env:MEGA_DOUB_TEST_VAULT_TOKEN", Timeout: 3 * time.Second,
				CAFile: "/etc/ssl/certs/fake-vault-ca.pem", Retry: loaded.Retry,
				Logger: logger}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vaultOptions(tt.args.loaded, tt.args.logger)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("vaultOptions() = %v, want %v", got, tt.want)
			}
			if got.Retry.MaxRetriesTime != 7*time.Second {
				t.Errorf("vaultOptions().Retry.MaxRetriesTime = %v, want 7s from the "+
					"environment", got.Retry.MaxRetriesTime)
			}
			// The options reach the server: a 403 response is *secret.StatusError.
			ref := secret.Ref("vault:depot/mega-doub/forbidden/password")
			_, resolveErr := secret.New(got).Resolve(t.Context(), ref)
			var se *secret.StatusError
			if !errors.As(resolveErr, &se) || se.Code != http.StatusForbidden {
				t.Errorf("secret.New().Resolve() error = %v, want *secret.StatusError with 403",
					resolveErr)
			}
		})
	}
}

func Test_handle_vaultExample(t *testing.T) {
	setRetryEnv(t, nil)
	const example = "../../testdata/example-vault.yaml"
	created := 0
	deps := Deps{NewResolver: func(opts secret.VaultOptions) secret.Resolver {
		created++
		return secret.New(opts)
	}}
	got, stdout, stderr := execute(t, deps, "config", "validate", "-c", example)
	if got != 0 {
		t.Errorf("cli.Execute() = %v, want 0: %s", got, stderr)
	}
	if stdout != "" {
		t.Errorf("cli.Execute() stdout = %q, want none", stdout)
	}
	if !strings.Contains(stderr, `msg="configuration is valid"`) {
		t.Errorf("cli.Execute() stderr = %q, want configuration is valid", stderr)
	}
	// Without --resolve no resolver is created, so no request leaves the process.
	if created != 0 {
		t.Errorf("cli.Execute() created %d resolvers, want 0", created)
	}
}

func Test_handle_vaultCanary(t *testing.T) {
	setRetryEnv(t, nil)
	const (
		valueCanary = "fake-canary-vault-value-2b7e"
		bodyCanary  = "fake-canary-vault-body-9c4f"
	)
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", vaultCanaryToken)
	srv := newCanaryVault(t, valueCanary, bodyCanary)
	dir := t.TempDir()
	resolved := filepath.Join(dir, "resolved.yaml")
	forbidden := filepath.Join(dir, "forbidden.yaml")
	// canaryConfig returns a configuration with the debug level, the test Vault server and
	// the password from it.
	canaryConfig := func(password string) string {
		return "---\nglobal:\n  log_level: debug\nvault:\n  address: '" + srv.URL + "'\n" +
			"  token: 'env:MEGA_DOUB_TEST_VAULT_TOKEN'\nstorage:\n" +
			"  url: 'b2://canary-bucket/prefix'\n  snapshot_id: canary-main\n" +
			"  password: '" + password + "'\ntargets:\n  - name: local\n" +
			"    transport: local\n    sources: [{type: test, id: data, path: /srv/data}]\n"
	}
	writeFiles(t, map[string]string{
		resolved:  canaryConfig("vault:depot/mega-doub/canary/password"),
		forbidden: canaryConfig("vault:depot/mega-doub/forbidden/password"),
	})
	tests := []struct {
		name string
		path string
		want int
		// wantStderr is the text that stderr holds.
		wantStderr []string
	}{
		{name: "resolved", path: resolved, want: 0, wantStderr: []string{"secrets resolved"}},
		{
			name:       "forbidden",
			path:       forbidden,
			want:       2,
			wantStderr: []string{"secret resolution failed", "vault returned 403"},
		},
	}
	canaries := []string{vaultCanaryToken, valueCanary, bodyCanary}
	commands := [][]string{{"config", "validate", "--resolve"}, {"backup", "--dry-run"}}
	for _, tt := range tests {
		for _, format := range []string{"text", "json"} {
			for _, command := range commands {
				name := tt.name + "/" + format + "/" + strings.Join(command, " ")
				t.Run(name, func(t *testing.T) {
					args := append([]string{"--log-format", format, "-c", tt.path}, command...)
					got, stdout, stderr := execute(t, Deps{}, args...)
					if got != tt.want {
						t.Errorf("cli.Execute() = %v, want %v: %s", got, tt.want, stderr)
					}
					for _, text := range tt.wantStderr {
						if !strings.Contains(stderr, text) {
							t.Errorf("cli.Execute() stderr = %q, want %q in it", stderr, text)
						}
					}
					for _, canary := range canaries {
						if strings.Contains(stdout, canary) || strings.Contains(stderr, canary) {
							t.Errorf("cli.Execute() output contains %q", canary)
						}
					}
					// The records name neither the address of the server nor a request path.
					for _, url := range []string{srv.URL, "/v1/"} {
						if strings.Contains(stderr, url) {
							t.Errorf("cli.Execute() stderr contains %q", url)
						}
					}
				})
			}
		}
	}
}
