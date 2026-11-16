// cspell:ignore doub testsource

package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/internal/testsource"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "password", raw: "sftp://u:p@h.example.invalid/x",
			want: "sftp://u:***@h.example.invalid/x"},
		{name: "user only", raw: "sftp://user@host//abs", want: "sftp://user@host//abs"},
		{name: "region of s3", raw: "s3://region@endpoint/bucket",
			want: "s3://region@endpoint/bucket"},
		{name: "no user information", raw: "b2://bucket/prefix", want: "b2://bucket/prefix"},
		{name: "empty", raw: "", want: ""},
		{name: "escaped password", raw: "sftp://u:p%40ss@h/x", want: "sftp://u:***@h/x"},
		{name: "@ in the password", raw: "sftp://u:p@ss@h/x", want: "sftp://u:***@h/x"},
		{name: "space in the path", raw: "sftp://u:p@h/a b", want: "sftp://u:***@h/a b"},
		{name: "escaped # and ? in the path", raw: "sftp://u:p@h/a%23b%3Fc",
			want: "sftp://u:***@h/a%23b%3Fc"},
		{name: "unparsable", raw: "%zz", want: "***"},
		{name: "unparsable with a password", raw: "sftp://u:fake-canary@h:bad/",
			want: "***"},
		{name: "opaque", raw: "scheme:user:pass@host", want: "scheme:***"},
		{name: "scheme case kept", raw: "SFTP:u:p@h", want: "SFTP:***"},
		{name: "authority only after the scheme", raw: "file:/a?u=http://x:p@h/",
			want: "file:/a?u=http://x:p@h/"},
		{name: "empty password", raw: "sftp://u:@h/x", want: "sftp://u:***@h/x"},
		{name: "query and fragment not masked", raw: "sftp://u:p@h/x?pw=q#f",
			want: "sftp://u:***@h/x?pw=q#f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.SanitizeURL(tt.raw)
			if got != tt.want {
				t.Errorf("SanitizeURL() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "%2A") {
				t.Errorf("SanitizeURL() = %q escapes the mask", got)
			}
		})
	}
}

// secretConfig returns a configuration with every secret field set to ref: an ssh
// target with a source of the test type and a local target with the defaults of the
// ssh fields after loading.
func secretConfig(ref secret.Ref) *config.Config {
	var keys map[string]secret.Ref
	if ref != "" {
		keys = map[string]secret.Ref{"b2_id": ref, "b2_key": ref}
	}
	timeout := config.Duration(10 * time.Second)
	return &config.Config{
		Templates: yaml.Node{Value: "template"},
		Vault:     config.Vault{Address: "https://vault.example.invalid", Token: ref},
		Storage: config.Storage{
			URL:      "sftp://u:fake-url-password@h.example.invalid/x",
			Password: ref,
			Keys:     keys,
		},
		Targets: []config.Target{
			{
				TargetSettings: config.TargetSettings{
					Name:           "db",
					Transport:      "ssh",
					Host:           "db.example.invalid",
					Port:           22,
					User:           "backup",
					Auth:           []string{"key"},
					Key:            ref,
					KeyPassphrase:  ref,
					Password:       ref,
					KnownHosts:     "/etc/ssh/known_hosts",
					ConnectTimeout: timeout,
				},
				Sources: []config.SourceConfig{testsource.Source{
					SourceBase: config.SourceBase{Type: testsource.Type, ID: "main"},
					Path:       "/srv/data",
					Token:      ref,
				}},
			},
			{
				TargetSettings: config.TargetSettings{
					Name:           "local",
					Transport:      "local",
					Port:           22,
					Auth:           []string{"agent"},
					ConnectTimeout: timeout,
				},
			},
		},
	}
}

// refs returns the secret references of the configuration by their paths.
func refs(c *config.Config) map[string]secret.Ref {
	got := map[string]secret.Ref{}
	config.WalkRefs(c, func(path string, ref *secret.Ref) {
		got[path] = *ref
	})
	return got
}

func TestRedact(t *testing.T) {
	tests := []struct {
		name string
		ref  secret.Ref
		want secret.Ref
	}{
		{name: "plain", ref: "plain:fake-redact", want: "plain:***"},
		{name: "without a prefix", ref: "fake-redact", want: "plain:***"},
		{name: "unknown prefix", ref: "evn:FAKE_REDACT", want: "plain:***"},
		{name: "env", ref: "env:MEGA_DOUB_REDACT", want: "env:MEGA_DOUB_REDACT"},
		{name: "file", ref: "file:/run/secrets/fake-redact",
			want: "file:/run/secrets/fake-redact"},
		{name: "vault", ref: "vault:depot/redact/key", want: "vault:depot/redact/key"},
		{name: "empty", ref: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := secretConfig(tt.ref)
			got := config.Redact(c)
			if want := refs(secretConfig(tt.want)); !reflect.DeepEqual(refs(got), want) {
				t.Errorf("Redact() references = %v, want %v", refs(got), want)
			}
			if !reflect.DeepEqual(got.Templates, yaml.Node{}) {
				t.Errorf("Redact() templates = %v, want none", got.Templates)
			}
			wantLocal := config.TargetSettings{Name: "local", Transport: "local"}
			if local := got.Targets[1].TargetSettings; !reflect.DeepEqual(local, wantLocal) {
				t.Errorf("Redact() local target = %v, want %v", local, wantLocal)
			}
			if ssh := got.Targets[0]; ssh.Port != 22 || ssh.Host != "db.example.invalid" {
				t.Errorf("Redact() ssh target port %d and host %q", ssh.Port, ssh.Host)
			}
			if url := got.Storage.URL; url != "sftp://u:***@h.example.invalid/x" {
				t.Errorf("Redact() storage.url = %q", url)
			}
			// The copy shares nothing with the configuration.
			got.Targets[0].Auth[0] = "changed"
			got.Targets[0].Sources[0] = nil
			if !reflect.DeepEqual(c, secretConfig(tt.ref)) {
				t.Error("Redact() changed the configuration")
			}
		})
	}
}

// show loads and validates the configuration file and returns it with its Show output.
func show(t *testing.T, path string) (*config.Loaded, []byte) {
	t.Helper()
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if result := config.Validate(loaded); len(result.Problems) > 0 {
		t.Fatalf("config.Validate() problems = %v", result.Problems)
	}
	out, err := config.Show(loaded.Config)
	if err != nil {
		t.Fatalf("config.Show() error = %v", err)
	}
	return loaded, out
}

func TestShow(t *testing.T) {
	sshFields := []string{"host", "user", "port", "auth", "key", "key_passphrase",
		"password", "known_hosts", "connect_timeout"}
	tests := []struct {
		name string
		path string
	}{
		{name: "example", path: "../../testdata/example.yaml"},
		{name: "full", path: "../../testdata/full.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, want := show(t, tt.path)
			// The output is a fixed point: it loads, passes the validation and is shown
			// the same way again.
			file := filepath.Join(t.TempDir(), "show.yaml")
			if err := os.WriteFile(file, want, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, got := show(t, file)
			if !bytes.Equal(got, want) {
				t.Errorf("Show() of the output = %s, want %s", got, want)
			}
			for i, target := range loaded.Config.Targets {
				if target.Transport != "local" {
					continue
				}
				for _, field := range sshFields {
					if path := "targets[" + strconv.Itoa(i) + "]." + field; loaded.Has(path) {
						t.Errorf("Show() output has %s", path)
					}
				}
			}
		})
	}
}

func TestShow_sizeLimit(t *testing.T) {
	const limit = 1048576
	// sized returns a configuration whose target name is n bytes long.
	sized := func(n int) *config.Config {
		c := secretConfig("env:MEGA_DOUB_SECRET")
		c.Targets[0].Name = strings.Repeat("a", n)
		return c
	}
	base, err := config.Show(sized(1))
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	fit := limit - len(base) + 1
	tests := []struct {
		name    string
		n       int
		wantLen int
		wantErr bool
	}{
		{name: "small", n: 1, wantLen: len(base)},
		{name: "exactly the limit", n: fit, wantLen: limit},
		{name: "one byte over the limit", n: fit + 1, wantErr: true},
		{name: "far over the limit", n: 2 * limit, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.Show(sized(tt.n))
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "larger than 1048576 bytes") {
					t.Errorf("Show() error = %v, want a larger than 1048576 bytes error", err)
				}
				if got != nil {
					t.Errorf("Show() = %d bytes, want none", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("Show() error = %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("Show() = %d bytes, want %d", len(got), tt.wantLen)
			}
		})
	}
}
