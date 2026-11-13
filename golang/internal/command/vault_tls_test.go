// cspell:ignore httptest
package command

import (
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// failedRecord is the start of the pattern of the record of a failed secret reference.
const failedRecord = `level=ERROR msg="secret resolution failed" `

// tlsTestToken is the value of the token of the Vault tests with TLS.
const tlsTestToken = "fake-tls-vault-token-4e8b"

// countingVault is a test Vault server with TLS that counts its requests.
type countingVault struct {
	*httptest.Server
	requests atomic.Int32
}

// newCountingVault starts a test Vault server with TLS that answers every request with
// 403, closed after the test; the errors of the handshakes are discarded.
func newCountingVault(t *testing.T) *countingVault {
	t.Helper()
	v := &countingVault{}
	v.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter,
		_ *http.Request) {
		v.requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	v.Config.ErrorLog = log.New(io.Discard, "", 0)
	v.StartTLS()
	t.Cleanup(v.Close)
	return v
}

// writeTLSConfig writes to a new temporary file the configuration with the Vault server
// at address, the token from MEGA_DOUB_TEST_VAULT_TOKEN, the ca_file and three vault:
// references in storage and returns its path. The value of vault.token is at the line 4,
// column 10; the references are at 9:13, 11:13 and 12:12.
func writeTLSConfig(t *testing.T, address, caFile string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.yaml")
	writeFiles(t, map[string]string{path: "---\nvault:\n  address: '" + address + "'\n" +
		"  token: 'env:MEGA_DOUB_TEST_VAULT_TOKEN'\n  ca_file: '" + caFile + "'\n" +
		"storage:\n  url: 'b2://tls-bucket/prefix'\n  snapshot_id: tls-main\n" +
		"  password: 'vault:depot/mega-doub/tls/password'\n  keys:\n" +
		"    b2_key: 'vault:depot/mega-doub/tls/b2-key'\n" +
		"    b2_id: 'vault:depot/mega-doub/tls/b2-id'\n" +
		"targets:\n  - name: local\n    transport: local\n" +
		"    sources: [{type: test, id: data, path: /srv/data}]\n"})
	return path
}

// refRecords returns the patterns of the records of the three vault: references of
// writeTLSConfig in the file with the error.
func refRecords(file, err string) []string {
	return []string{
		failedRecord + `path=storage\.password kind=vault file=` + file +
			` line=9 column=13 error=` + err,
		failedRecord + `path=storage\.keys\.b2_key kind=vault file=` + file +
			` line=11 column=13 error=` + err,
		failedRecord + `path=storage\.keys\.b2_id kind=vault file=` + file +
			` line=12 column=12 error=` + err,
	}
}

// checkVaultRun runs config validate --resolve on the configuration at path and checks
// the exit code 2, the records, an empty stdout, no request to srv and no token in
// stderr.
func checkVaultRun(t *testing.T, srv *countingVault, path string, records []string) {
	t.Helper()
	got, stdout, stderr := execute(t, Deps{}, "config", "validate", "--resolve", "-c", path)
	if got != 2 {
		t.Errorf("cli.Execute() = %v, want 2: %s", got, stderr)
	}
	if stdout != "" {
		t.Errorf("cli.Execute() stdout = %q, want none", stdout)
	}
	matchRecords(t, textRecords(stderr), records)
	if strings.Contains(stderr, tlsTestToken) {
		t.Errorf("cli.Execute() stderr = %q, holds the token", stderr)
	}
	if n := srv.requests.Load(); n != 0 {
		t.Errorf("server got %d requests, want 0", n)
	}
}

func Test_handle_vaultTokenRecords(t *testing.T) {
	setRetryEnv(t, nil)
	tests := []struct {
		name string
		// env is the value of MEGA_DOUB_TEST_VAULT_TOKEN, unset when empty.
		env string
		// cause is the pattern of the error of the record of vault.token.
		cause string
	}{
		{name: "unresolved token",
			cause: `"env reference: variable MEGA_DOUB_TEST_VAULT_TOKEN is not set"`},
		{name: "newline in the token", env: tlsTestToken + "\n",
			cause: `"vault token contains invalid characters"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env == "" {
				unsetEnv(t, "MEGA_DOUB_TEST_VAULT_TOKEN")
			} else {
				t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", tt.env)
			}
			srv := newCountingVault(t)
			caFile := filepath.Join(t.TempDir(), "ca.pem")
			certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE",
				Bytes: srv.Certificate().Raw})
			if err := os.WriteFile(caFile, certificate, 0o600); err != nil {
				t.Fatal(err)
			}
			path := writeTLSConfig(t, srv.URL, caFile)
			file := slogValue(t, path)
			records := append([]string{failedRecord + `path=vault\.token kind=env file=` +
				file + ` line=4 column=10 error=` + tt.cause},
				refRecords(file, `"vault token is unresolved"`)...)
			checkVaultRun(t, srv, path, records)
		})
	}
}

func Test_handle_vaultCAFileRecords(t *testing.T) {
	setRetryEnv(t, nil)
	t.Setenv("MEGA_DOUB_TEST_VAULT_TOKEN", tlsTestToken)
	tests := []struct {
		name string
		// content is the content of ca_file, which is missing when nil.
		content []byte
		// cause returns the text of the error of the creation of the client.
		cause func(caFile string) string
	}{
		{name: "missing ca_file", cause: func(caFile string) string {
			return "ca_file: open " + caFile + ": no such file or directory"
		}},
		{name: "ca_file without PEM", content: []byte("fake-not-pem\n"),
			cause: func(string) string { return "ca_file: no certificates found" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCountingVault(t)
			caFile := filepath.Join(t.TempDir(), "ca.pem")
			if tt.content != nil {
				if err := os.WriteFile(caFile, tt.content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := writeTLSConfig(t, srv.URL, caFile)
			// Every reference gets the full text, no record of vault.token is written.
			checkVaultRun(t, srv, path, refRecords(slogValue(t, path),
				slogValue(t, tt.cause(caFile))))
		})
	}
}
