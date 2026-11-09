package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/pkg/secret"
)

// testSource is the source type the tests register.
type testSource struct {
	SourceBase `yaml:",inline"`
	Path       string `yaml:"path"`
}

func (testSource) Validate() []Problem { return nil }

func init() {
	Register("test", func(node *yaml.Node, opts ...yaml.Option) (SourceConfig, error) {
		var s testSource
		if err := node.Load(&s, opts...); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func defaultTarget(name string) Target {
	return Target{
		TargetSettings: TargetSettings{
			Name:           name,
			Port:           22,
			Auth:           []string{"agent"},
			ConnectTimeout: Duration(10 * time.Second),
		},
		Sources: []SourceConfig{},
	}
}

// defaults returns the configuration of an empty mapping with the given targets.
func defaults(targets ...Target) *Config {
	return &Config{
		Global: Global{
			Instance:        "main",
			CopyDir:         "/var/lib/mega-doub/copy/main",
			StateDir:        "/var/lib/mega-doub/state/main",
			ParallelTargets: 4,
			ParallelSources: 2,
			LogLevel:        "info",
			LogFormat:       "text",
			Progress:        "auto",
			Timeout:         Duration(12 * time.Hour),
		},
		Vault:   Vault{Timeout: Duration(30 * time.Second)},
		Storage: Storage{Name: "default", Threads: 4, Tag: "mega-doub"},
		Prune:   Prune{Keep: []string{}, Threads: 4},
		Check:   Check{Every: Duration(168 * time.Hour), TestRestoreBytes: 256 << 20},
		Targets: append([]Target{}, targets...),
	}
}

func exampleConfig() *Config {
	cfg := defaults(
		Target{
			TargetSettings: TargetSettings{
				Name:           "db01",
				Transport:      "ssh",
				Host:           "db01.example.invalid",
				Port:           2222,
				User:           "root",
				Auth:           []string{"agent", "key"},
				Key:            "env:MEGA_DOUB_EXAMPLE_SSH_KEY",
				KnownHosts:     "/etc/ssh/ssh_known_hosts",
				ConnectTimeout: Duration(10 * time.Second),
			},
			Sources: []SourceConfig{},
		},
		defaultTarget("local"),
	)
	cfg.Global.Instance = "example"
	cfg.Global.CopyDir = "/var/lib/mega-doub/copy/example"
	cfg.Global.StateDir = "/var/lib/mega-doub/state/example"
	cfg.Targets[1].Transport = "local"
	cfg.Storage.URL = "b2://example-bucket/mega-doub"
	cfg.Storage.SnapshotID = "example-main"
	cfg.Storage.Password = "env:MEGA_DOUB_EXAMPLE_STORAGE_PASSWORD"
	cfg.Storage.Keys = map[string]secret.Ref{
		"b2_id":  "env:MEGA_DOUB_EXAMPLE_B2_ID",
		"b2_key": "env:MEGA_DOUB_EXAMPLE_B2_KEY",
	}
	return cfg
}

func fullConfig() *Config {
	return &Config{
		Global: Global{
			Instance:        "full",
			CopyDir:         "/srv/mega-doub/copy",
			StateDir:        "/srv/mega-doub/state",
			ParallelTargets: 8,
			ParallelSources: 3,
			LogLevel:        "debug",
			LogFormat:       "json",
			Progress:        "never",
			Timeout:         Duration(6*time.Hour + 30*time.Minute),
			Retry: Retry{
				MaxRetriesTime:      Duration(2 * time.Minute),
				BackoffInitial:      Duration(2 * time.Second),
				BackoffStep:         Duration(3 * time.Second),
				BackoffStepAttempts: 5,
				BackoffCap:          Duration(30 * time.Second),
			},
		},
		Vault: Vault{
			Address: "https://vault.example.invalid:8200",
			Token:   "file:/run/secrets/fake-vault-token",
			Timeout: Duration(45 * time.Second),
			CAFile:  "/usr/local/share/ca-certificates/fake-vault.crt",
		},
		Storage: Storage{
			Name:       "offsite",
			URL:        "b2://fake-bucket/full",
			SnapshotID: "full-main",
			Password:   "vault:depot/mega-doub/full/storage-password",
			Threads:    16,
			Tag:        "full mega-doub",
			Keys: map[string]secret.Ref{
				"b2_id":  "vault:depot/mega-doub/full/b2-id",
				"b2_key": "plain:fake-b2-key",
			},
		},
		Prune: Prune{Enabled: true, Keep: []string{"0:365", "7:30", "1:7"}, Threads: 2,
			Exclusive: true},
		Check: Check{Enabled: true, Every: Duration(24 * time.Hour), Chunks: true,
			Files: true, TestRestore: true, TestRestoreBytes: 1536 << 10},
		Targets: []Target{{
			TargetSettings: TargetSettings{
				Name:           "db02",
				Transport:      "ssh",
				Host:           "db02.example.invalid",
				Port:           2200,
				User:           "backup",
				Auth:           []string{"key", "password"},
				Key:            "file:/root/.ssh/fake_ed25519",
				KeyPassphrase:  "env:MEGA_DOUB_FULL_PASSPHRASE",
				Password:       "plain:fake-password",
				KnownHosts:     "/etc/ssh/fake_known_hosts",
				ConnectTimeout: Duration(15 * time.Second),
				Sudo:           true,
			},
			Sources: []SourceConfig{testSource{
				SourceBase: SourceBase{Type: "test", ID: "test-main"},
				Path:       "/srv/data",
			}},
		}},
	}
}

// sized returns a valid document of exactly n bytes.
func sized(n int) string {
	head := "---\n{}\n#"
	return head + strings.Repeat("x", n-len(head)-1) + "\n"
}

func TestLoad(t *testing.T) {
	zeros := defaults(Target{
		TargetSettings: TargetSettings{Auth: []string{}},
		Sources:        []SourceConfig{},
	})
	zeros.Global.Instance = ""
	zeros.Global.CopyDir = ""
	zeros.Global.StateDir = "/var/lib/mega-doub/state/"
	zeros.Global.ParallelTargets = 0
	zeros.Global.Timeout = 0
	zeros.Storage.Name = ""
	zeros.Storage.Threads = 0
	zeros.Check.TestRestoreBytes = 0
	merged := defaults()
	merged.Global.Instance = "other"
	merged.Global.CopyDir = "/srv/copy"
	merged.Global.StateDir = "/var/lib/mega-doub/state/other"
	aliased := defaults()
	aliased.Global.CopyDir = "/srv/custom"
	aliased.Global.StateDir = "/srv/state"
	aliased.Global.LogLevel = "error"
	aliased.Global.LogFormat = "json"
	aliased.Storage.Keys = map[string]secret.Ref{"k0": "env:FAKE_0"}
	listed := defaultTarget("x")
	listed.Host = "a.example.invalid"
	listed.Port = 1
	listed.User = "local"
	listed.KnownHosts = "/etc/ssh/b_known_hosts"
	type args struct {
		path string
	}
	tests := []struct {
		name string
		args args
		// content is written to a temporary file used instead of args.path.
		content       string
		want          *Config
		wantLevelSet  bool
		wantFormatSet bool
		// wantErr holds a fragment of each line of the error, {file} is the file path.
		wantErr []string
		// hidden must not appear in the error.
		hidden string
		quick  bool
	}{
		{
			name: "example",
			args: args{path: "../../testdata/example.yaml"},
			want: exampleConfig(),
		},
		{
			name:          "full",
			args:          args{path: "../../testdata/full.yaml"},
			want:          fullConfig(),
			wantLevelSet:  true,
			wantFormatSet: true,
		},
		{
			name:    "defaults",
			content: "---\ntargets:\n  - name: a\n  - name: b\n",
			want:    defaults(defaultTarget("a"), defaultTarget("b")),
		},
		{
			name: "explicit zero values",
			content: "---\nglobal:\n  instance: ''\n  copy_dir: ''\n  parallel_targets: 0\n" +
				"  timeout: 0\nstorage:\n  name: ''\n  threads: 0\nprune:\n  enabled: false\n" +
				"  keep: []\ncheck:\n  test_restore_bytes: 0\ntargets:\n  - name: ''\n" +
				"    port: 0\n    auth: []\n    connect_timeout: 0s\n    sudo: false\n" +
				"    sources: []\n",
			want: zeros,
		},
		{
			name: "explicit null values",
			content: "---\nglobal:\n  instance: null\n  copy_dir: null\n  state_dir:\n" +
				"  log_level: null\n  log_format: ~\n  timeout: null\nvault: null\n" +
				"prune:\n  keep: null\ncheck:\n  test_restore_bytes: null\ntargets:\n" +
				"  - name: a\n    port: null\n    auth: null\n    connect_timeout: null\n" +
				"    sources: null\n",
			want: defaults(defaultTarget("a")),
		},
		{
			name: "directory from a template",
			content: "---\ntemplates:\n  dirs: &dirs\n    copy_dir: /srv/copy\nglobal:\n" +
				"  <<: *dirs\n  instance: other\n",
			want: merged,
		},
		{
			// The local key wins, then the earlier template of the list. The repeated
			// template name is not detected.
			name: "merge key list",
			content: "---\ntemplates:\n  a: &a\n    host: a.example.invalid\n    port: 1\n" +
				"    user: t\n  b: &b\n    port: 2\n    known_hosts: /etc/ssh/b_known_hosts\n" +
				"  a: {port: 3}\ntargets:\n  - <<: [*a, *b]\n    name: x\n    user: local\n",
			want: defaults(listed),
		},
		{
			// The aliases pass the ratio check of the strict decoding only.
			name: "aliases in the global merge key",
			content: "---\ntemplates:\n  a: &a {progress: auto}\n  g: &g {copy_dir: " +
				"/srv/custom, state_dir: /srv/state, log_level: error, log_format: json}\n" +
				"storage: {keys: {k0: 'env:FAKE_0'}}\nglobal:\n  <<: [" +
				strings.Repeat("*a, ", 250) + "*g]\n",
			want:          aliased,
			wantLevelSet:  true,
			wantFormatSet: true,
		},
		{
			name:    "exactly the size limit",
			content: sized(1 << 20),
			want:    defaults(),
		},
		{
			name: "not yaml",
			args: args{path: "../../testdata/invalid/not-yaml.yaml"},
			wantErr: []string{
				"not-yaml.yaml:4:1: while parsing a flow sequence: did not find expected",
			},
		},
		{
			name:    "alias bomb",
			args:    args{path: "../../testdata/invalid/alias-bomb.yaml"},
			wantErr: []string{"alias-bomb.yaml:4:11: document contains excessive aliasing"},
			quick:   true,
		},
		{
			name: "unknown root key",
			args: args{path: "../../testdata/invalid/unknown-key-root.yaml"},
			wantErr: []string{
				"unknown-key-root.yaml:4:1: field bogus not found in type config.configFile",
			},
		},
		{
			name: "bad duration",
			args: args{path: "../../testdata/invalid/bad-duration.yaml"},
			wantErr: []string{
				"bad-duration.yaml:3:12: invalid duration: want a number with a unit, such as 30s",
			},
		},
		{
			name: "bad log level",
			args: args{path: "../../testdata/invalid/bad-log-level.yaml"},
			wantErr: []string{
				"bad-log-level.yaml:3:14: invalid log level: want one of debug, info, warn, error",
			},
		},
		{
			name: "values stay out of messages",
			content: "---\nglobal:\n  log_level: 'https://user:secret@example.com'\n" +
				"  log_format: 'https://user:secret@example.com'\n" +
				"  timeout: 'https://user:secret@example.com'\nstorage:\n  threads: secret\n" +
				"  keys: 'https://user:secret@example.com'\ncheck:\n" +
				"  test_restore_bytes: 'https://user:secret@example.com'\ntargets:\n" +
				"  - name: a\n    sources:\n      - type: 'https://user:secret@example.com'\n",
			wantErr: []string{
				"{file}:3:14: invalid log level: want one of debug, info, warn, error",
				"{file}:4:15: invalid log format: want one of text, json",
				"{file}:5:12: invalid duration: want a number with a unit, such as 30s",
				"{file}:7:12: cannot construct !!str into int",
				"{file}:8:9: cannot construct !!str into map[string]secret.Ref",
				"{file}:10:23: invalid size: want digits with an optional K, M, G or T suffix",
				"{file}:14:9: unknown source type",
			},
			hidden: "@example.com",
		},
		{
			name:    "tagged value stays out of the message",
			content: "---\nstorage:\n  password: !!int 'https://user:secret@example.com'\n",
			wantErr: []string{"{file}: cannot construct !!str as a !!int"},
			hidden:  "@example.com",
		},
		{
			name: "value after a tag with a space",
			content: "---\nstorage:\n  password: !<tag:example.invalid,2026:secret%20ref> " +
				"'fake-pw'\n",
			wantErr: []string{
				"{file}:3:13: cannot construct tag:example.invalid,2026:secret ref into secret.Ref",
			},
			hidden: "fake-pw",
		},
		{
			name: "value after a tag with a backtick",
			content: "---\nstorage:\n  password: !<tag:example.invalid,2026:a%20%60b> " +
				"'fake-pw'\n",
			wantErr: []string{"{file}:3:13: cannot construct tag:example.invalid,2026:a into"},
			hidden:  "fake-pw",
		},
		{
			name: "template key in two targets",
			content: "---\ntemplates:\n  tpl: &tpl\n    transport: ssh\n    bogus: 1\n" +
				"targets:\n  - <<: *tpl\n    name: a\n  - <<: *tpl\n    name: b\n",
			wantErr: []string{"{file}:5:5: field bogus not found in type config.targetItem"},
		},
		{
			name: "two merge keys",
			content: "---\ntemplates:\n  a: &a\n    port: 1\n  b: &b\n    user: u\n" +
				"targets:\n  - name: x\n    <<: *a\n    <<: *b\n",
			wantErr: []string{`{file}:10:5: mapping key "<<" already defined at line 9`},
		},
		{
			name: "fatal error stops decoding",
			content: "---\ntargets:\n  - name: a\n    extra: 1\n  - name: b\n    <<: 1\n" +
				"  - name: c\n    extra: 2\n",
			wantErr: []string{
				"{file}:4:5: field extra not found in type config.targetItem",
				"{file}:6:9: map merge requires map or sequence of maps as the value",
			},
		},
		{
			name: "fatal error in a source stops decoding",
			content: "---\ntargets:\n  - name: a\n    sources:\n      - type: test\n" +
				"        <<: 1\n      - type: bogus\n  - name: b\n    extra: 1\n",
			wantErr: []string{
				"{file}:6:13: map merge requires map or sequence of maps as the value",
			},
		},
		{
			name: "problems in file order",
			content: "---\ntargets:\n  - name: a\n    extra: 1\nglobal:\n  timeout: 30\n" +
				"  bogus: 1\n",
			wantErr: []string{
				"{file}:4:5: field extra not found",
				"{file}:6:12: invalid duration",
				"{file}:7:3: field bogus not found in type config.Global",
			},
		},
		{
			name: "source problems",
			content: "---\ntargets:\n  - name: a\n    sources:\n      - type: bogus\n" +
				"      - id: no-type\n      - type: test\n        extra: 1\n" +
				"      - type: [test]\n      - type: null\n      - type: test\n" +
				"        id: x\n        id: y\n      - type: ''\n      - [type]\n",
			wantErr: []string{
				"{file}:5:9: unknown source type",
				"{file}:6:9: source type is required",
				"{file}:8:9: field extra not found in type config.testSource",
				"{file}:9:15: cannot construct !!seq into string",
				"{file}:10:9: source type is required",
				`{file}:13:9: mapping key "id" already defined at line 12`,
				"{file}:14:9: source type is required",
				"{file}:15:9: cannot construct !!seq into config.sourceHead",
			},
		},
		{
			name:    "empty file",
			content: "",
			wantErr: []string{"{file}: configuration is empty"},
		},
		{
			name:    "only comments",
			content: "# nothing here\n",
			wantErr: []string{"{file}: configuration is empty"},
		},
		{
			name:    "null document",
			content: "---\nnull\n",
			wantErr: []string{"{file}: configuration is empty"},
		},
		{
			name:    "two documents",
			content: "---\nglobal: {}\n---\nglobal: {}\n",
			wantErr: []string{"{file}:3:1: configuration must be a single document"},
		},
		{
			name:    "over the size limit",
			content: sized(1<<20 + 1),
			wantErr: []string{"{file}: file is larger than 1048576 bytes"},
		},
		{
			name:    "missing file",
			args:    args{path: "/nonexistent/main.yaml"},
			wantErr: []string{"/nonexistent/main.yaml: open: no such file or directory"},
		},
		{
			name:    "directory",
			args:    args{path: "../../testdata"},
			wantErr: []string{"testdata: read: is a directory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.args.path
			if tt.content != "" || path == "" {
				path = filepath.Join(t.TempDir(), "main.yaml")
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			got, err := Load(path)
			if tt.quick && time.Since(start) > time.Second {
				t.Errorf("Load() took %v, want less than a second", time.Since(start))
			}
			if (err != nil) != (tt.wantErr != nil) {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				var cfgErr *Error
				if !errors.As(err, &cfgErr) {
					t.Fatalf("Load() error type = %T, want *Error", err)
				}
				file, _ := filepath.Abs(path)
				lines := strings.Split(err.Error(), "\n")
				if len(lines) != len(tt.wantErr) || len(cfgErr.Problems) != len(lines) {
					t.Fatalf("Load() error = %q, want %d problems", err, len(tt.wantErr))
				}
				if tt.hidden != "" && strings.Contains(err.Error(), tt.hidden) {
					t.Errorf("Load() error = %q, want no %q", err, tt.hidden)
				}
				for i, want := range tt.wantErr {
					want = strings.ReplaceAll(want, "{file}", file)
					if !strings.HasPrefix(lines[i], file) || !strings.Contains(lines[i], want) {
						t.Errorf("Load() error line %d = %q, want %q", i, lines[i], want)
					}
				}
				return
			}
			if file, _ := filepath.Abs(path); got.Path != file {
				t.Errorf("Load() Path = %q, want %q", got.Path, file)
			}
			if got.Root == nil || got.Root.Kind != yaml.DocumentNode {
				t.Errorf("Load() Root = %v, want a document node", got.Root)
			}
			if got.LogLevelSet != tt.wantLevelSet || got.LogFormatSet != tt.wantFormatSet {
				t.Errorf("Load() LogLevelSet, LogFormatSet = %v, %v, want %v, %v",
					got.LogLevelSet, got.LogFormatSet, tt.wantLevelSet, tt.wantFormatSet)
			}
			got.Config.Templates = yaml.Node{}
			if !reflect.DeepEqual(got.Config, tt.want) {
				t.Errorf("Load() Config = %+v, want %+v", got.Config, tt.want)
			}
		})
	}
}
