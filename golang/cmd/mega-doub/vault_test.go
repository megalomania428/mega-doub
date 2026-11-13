// cspell:ignore doub

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// unavailableConfig is a configuration with two vault: references and a Vault address
// whose connections are refused at once.
const unavailableConfig = "---\nglobal:\n  log_format: json\nvault:\n" +
	"  address: 'https://127.0.0.1:1'\n  token: 'env:VAULT_TOKEN'\n  timeout: 2s\n" +
	"storage:\n  url: 'b2://bucket/prefix'\n  snapshot_id: main\n" +
	"  password: 'vault:depot/mega-doub/prod/storage-password'\n  keys:\n" +
	"    b2_key: 'vault:depot/mega-doub/prod/b2-key'\n"

func Test_run_vaultUnavailable(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "fake-token")
	// The budget of 1s with the default schedule gives two attempts with one sleep of 1s;
	// the default budget of 111s would make the test too slow.
	t.Setenv("MAX_RETRIES_TIME", "1s")
	for _, name := range []string{"BACKOFF_INITIAL", "BACKOFF_STEP", "BACKOFF_STEP_ATTEMPTS",
		"BACKOFF_CAP"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(t.TempDir(), "unavailable.yaml")
	if err := os.WriteFile(path, []byte(unavailableConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	start := time.Now()
	got := run(t.Context(), []string{"config", "validate", "--resolve", "-c", path}, stdout,
		stderr)
	elapsed := time.Since(start)
	if got != 2 {
		t.Errorf("run() = %v, want 2: %s", got, stderr)
	}
	var attempts []float64
	failures := 0
	for _, line := range strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("run() stderr line %q is not a JSON record: %v", line, err)
		}
		switch record["msg"] {
		case "vault request failed":
			// encoding/json decodes a number to float64.
			attempt, _ := record["attempt"].(float64)
			attempts = append(attempts, attempt)
		case "secret resolution failed":
			failures++
			if text, _ := record["error"].(string); !strings.Contains(text,
				"connection refused") {
				t.Errorf("run() record %q, want connection refused in error", line)
			}
		}
	}
	// The second reference gets the remembered error without a request.
	if !reflect.DeepEqual(attempts, []float64{1, 2}) {
		t.Errorf("run() records vault request failed have attempts %v, want [1 2]", attempts)
	}
	if failures != 2 {
		t.Errorf("run() wrote %d records secret resolution failed, want 2", failures)
	}
	for _, out := range []string{stdout.String(), stderr.String()} {
		if strings.Contains(out, "fake-token") {
			t.Errorf("run() output contains the token: %s", out)
		}
	}
	if elapsed < time.Second || elapsed > 5*time.Second {
		t.Errorf("run() took %v, want from 1s to 5s", elapsed)
	}
}
