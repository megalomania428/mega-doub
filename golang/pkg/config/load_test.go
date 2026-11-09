// cspell:ignore testsource

package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/internal/testsource"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

func init() {
	testsource.Register()
}

func defaultTarget(name string) config.Target {
	return config.Target{
		TargetSettings: config.TargetSettings{
			Name:           name,
			Port:           22,
			Auth:           []string{"agent"},
			ConnectTimeout: config.Duration(10 * time.Second),
		},
		Sources: []config.SourceConfig{},
	}
}

// defaults returns the configuration of an empty mapping with the given targets.
func defaults(targets ...config.Target) *config.Config {
	return &config.Config{
		Global: config.Global{
			Instance:        "main",
			CopyDir:         "/var/lib/mega-doub/copy/main",
			StateDir:        "/var/lib/mega-doub/state/main",
			ParallelTargets: 4,
			ParallelSources: 2,
			LogLevel:        "info",
			LogFormat:       "text",
			Progress:        "auto",
			Timeout:         config.Duration(12 * time.Hour),
		},
		Vault:   config.Vault{Timeout: config.Duration(30 * time.Second)},
		Storage: config.Storage{Name: "default", Threads: 4, Tag: "mega-doub"},
		Prune:   config.Prune{Keep: []string{}, Threads: 4},
		Check: config.Check{Every: config.Duration(168 * time.Hour),
			TestRestoreBytes: 256 << 20},
		Targets: append([]config.Target{}, targets...),
	}
}

func exampleConfig() *config.Config {
	cfg := defaults(
		config.Target{
			TargetSettings: config.TargetSettings{
				Name:           "db01",
				Transport:      "ssh",
				Host:           "db01.example.invalid",
				Port:           2222,
				User:           "root",
				Auth:           []string{"agent", "key"},
				Key:            "env:MEGA_DOUB_EXAMPLE_SSH_KEY",
				KnownHosts:     "/etc/ssh/ssh_known_hosts",
				ConnectTimeout: config.Duration(10 * time.Second),
			},
			Sources: []config.SourceConfig{},
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

func fullConfig() *config.Config {
	return &config.Config{
		Global: config.Global{
			Instance:        "full",
			CopyDir:         "/srv/mega-doub/copy",
			StateDir:        "/srv/mega-doub/state",
			ParallelTargets: 8,
			ParallelSources: 3,
			LogLevel:        "debug",
			LogFormat:       "json",
			Progress:        "never",
			Timeout:         config.Duration(6*time.Hour + 30*time.Minute),
			Retry: config.Retry{
				MaxRetriesTime:      config.Duration(2 * time.Minute),
				BackoffInitial:      config.Duration(2 * time.Second),
				BackoffStep:         config.Duration(3 * time.Second),
				BackoffStepAttempts: 5,
				BackoffCap:          config.Duration(30 * time.Second),
			},
		},
		Vault: config.Vault{
			Address: "https://vault.example.invalid:8200",
			Token:   "file:/run/secrets/fake-vault-token",
			Timeout: config.Duration(45 * time.Second),
			CAFile:  "/usr/local/share/ca-certificates/fake-vault.crt",
		},
		Storage: config.Storage{
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
		Prune: config.Prune{Enabled: true, Keep: []string{"0:365", "7:30", "1:7"}, Threads: 2,
			Exclusive: true},
		Check: config.Check{Enabled: true, Every: config.Duration(24 * time.Hour), Chunks: true,
			Files: true, TestRestore: true, TestRestoreBytes: 1536 << 10},
		Targets: []config.Target{{
			TargetSettings: config.TargetSettings{
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
				ConnectTimeout: config.Duration(15 * time.Second),
				Sudo:           true,
			},
			Sources: []config.SourceConfig{testsource.Source{
				SourceBase: config.SourceBase{Type: "test", ID: "test-main"},
				Path:       "/srv/data",
				Token:      "plain:fake-source-token",
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
	zeros := defaults(config.Target{
		TargetSettings: config.TargetSettings{Auth: []string{}},
		Sources:        []config.SourceConfig{},
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
	// The link leads to a directory with an invalid file, the path cleaned before it is
	// read names the valid one.
	linked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(linked, "real", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linked, "link")
	if err := os.Symlink(filepath.Join("real", "sub"), link); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"main.yaml": "---\n{}\n",
		filepath.Join("real", "main.yaml"): "---\nbogus: 1\n"} {
		err := os.WriteFile(filepath.Join(linked, name), []byte(content), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}
	type args struct {
		path string
	}
	tests := []struct {
		name string
		args args
		// content is written to a temporary file used instead of args.path.
		content       string
		want          *config.Config
		wantLevelSet  bool
		wantFormatSet bool
		// wantErr holds a fragment of each line of the error, {file} is the file path.
		wantErr []string
		// hidden must not appear in the error.
		hidden string
		quick  bool
		// noFile uses args.path as is, even when it is empty.
		noFile bool
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
				"unknown-key-root.yaml:4:1: bogus: field bogus not found in type config.configFile",
			},
		},
		{
			name: "bad duration",
			args: args{path: "../../testdata/invalid/bad-duration.yaml"},
			wantErr: []string{
				"bad-duration.yaml:3:12: global.timeout: invalid duration: want a number with a " +
					"unit, such as 30s",
			},
		},
		{
			name: "bad log level",
			args: args{path: "../../testdata/invalid/bad-log-level.yaml"},
			wantErr: []string{
				"bad-log-level.yaml:3:14: global.log_level: invalid log level: want one of debug, " +
					"info, warn, error",
			},
		},
		{
			name: "unknown nested key",
			args: args{path: "../../testdata/invalid/unknown-key-nested.yaml"},
			wantErr: []string{
				"unknown-key-nested.yaml:5:5: global.retry.bogus: field bogus not found",
			},
		},
		{
			name: "unknown target key",
			args: args{path: "../../testdata/invalid/unknown-key-target.yaml"},
			wantErr: []string{
				"unknown-key-target.yaml:5:5: targets[0].bogus: field bogus not found",
			},
		},
		{
			name: "unknown source key",
			args: args{path: "../../testdata/invalid/unknown-key-source.yaml"},
			wantErr: []string{
				"unknown-key-source.yaml:8:9: targets[0].sources[0].bogus: field bogus not found",
			},
		},
		{
			name: "unknown key from a template",
			args: args{path: "../../testdata/invalid/unknown-key-merge.yaml"},
			wantErr: []string{
				"unknown-key-merge.yaml:6:5: targets[0].bogus: field bogus not found",
				"unknown-key-merge.yaml:6:5: targets[1].bogus: field bogus not found",
			},
		},
		{
			name: "unknown keys on many levels",
			args: args{path: "../../testdata/invalid/unknown-keys-many.yaml"},
			wantErr: []string{
				"unknown-keys-many.yaml:2:1: bogus: field bogus not found in type config.configFile",
				"unknown-keys-many.yaml:4:3: storage.extra: field extra not found",
				"unknown-keys-many.yaml:8:5: targets[0].surplus: field surplus not found",
			},
		},
		{
			name: "duplicate key",
			args: args{path: "../../testdata/invalid/duplicate-key.yaml"},
			wantErr: []string{
				`duplicate-key.yaml:4:3: vault.timeout: mapping key "timeout" already defined`,
			},
		},
		{
			name: "duplicate merge key",
			args: args{path: "../../testdata/invalid/duplicate-merge.yaml"},
			wantErr: []string{
				`duplicate-merge.yaml:8:5: targets[0]: mapping key "<<" already defined at line 7`,
			},
		},
		{
			name: "alias key and explicit key",
			args: args{path: "../../testdata/invalid/already-set.yaml"},
			wantErr: []string{
				"already-set.yaml:6:3: storage.name: field name already set in type config.Storage",
			},
		},
		{
			name: "unknown source type",
			args: args{path: "../../testdata/invalid/unknown-source-type.yaml"},
			wantErr: []string{
				"unknown-source-type.yaml:6:15: targets[0].sources[0].type: unknown source type",
			},
		},
		{
			name: "missing source type",
			args: args{path: "../../testdata/invalid/missing-source-type.yaml"},
			wantErr: []string{
				"missing-source-type.yaml:6:9: targets[0].sources[0]: source type is required",
			},
		},
		{
			name: "bad size",
			args: args{path: "../../testdata/invalid/bad-size.yaml"},
			wantErr: []string{
				"bad-size.yaml:3:23: check.test_restore_bytes: invalid size: want digits",
			},
		},
		{
			name: "wrong type",
			args: args{path: "../../testdata/invalid/wrong-type.yaml"},
			wantErr: []string{
				"wrong-type.yaml:3:12: storage.threads: cannot construct !!str into int",
			},
		},
		{
			name:    "alias in two sections",
			content: "---\nglobal:\n  timeout: &t 30\nvault:\n  timeout: *t\n",
			wantErr: []string{
				"{file}:3:12: global.timeout: invalid duration",
				"{file}:3:12: vault.timeout: invalid duration",
			},
		},
		{
			// Only one of the two values the alias gives fails, so the path is unknown.
			name:    "alias in fields of two types",
			content: "---\ntargets:\n  - name: &t abc\n    port: *t\n",
			wantErr: []string{"{file}:3:11: cannot construct !!str into int"},
		},
		{
			// The library skips the value of an unknown key, it is no candidate.
			name:    "alias under an unknown key",
			content: "---\nglobal:\n  timeout: &t 30\n  bogus: *t\n",
			wantErr: []string{
				"{file}:3:12: global.timeout: invalid duration",
				"{file}:4:3: global.bogus: field bogus not found",
			},
		},
		{
			// The library drops a null key silently.
			name:    "null keys",
			content: "---\nglobal:\n  null: x\nstorage:\n  keys:\n    ~: 'plain:fake'\n",
			wantErr: []string{
				"{file}:3:3: global: null key is not allowed",
				"{file}:6:5: storage.keys: null key is not allowed",
			},
		},
		{
			// The library does not see a null key in a value it skips, in a mapping with
			// a repeated key or in a source that no decoder decodes.
			name: "null keys the library does not construct",
			content: "---\ntargets:\n  - name: a\n    bogus:\n      null: 1\n    sources:\n" +
				"      - type: bogus\n        ~: 2\n      - type: test\n        id: x\n" +
				"        id: y\n        null: 3\n",
			wantErr: []string{
				"{file}:4:5: targets[0].bogus: field bogus not found in type config.targetItem",
				"{file}:7:15: targets[0].sources[0].type: unknown source type",
				`{file}:11:9: targets[0].sources[1].id: mapping key "id" already defined`,
			},
		},
		{
			// The root is decoded before the target that stops decoding.
			name: "null keys after a fatal error",
			content: "---\nglobal:\n  null: x\ntargets:\n  - name: a\n    <<: 5\n" +
				"    ~: y\n  - name: b\n    null: z\n",
			wantErr: []string{
				"{file}:3:3: global: null key is not allowed",
				"{file}:6:9: map merge requires map or sequence of maps as the value",
			},
		},
		{
			// The repeated key of a template merged twice has no certain path, so no
			// mapping of the template is constructed.
			name: "null keys in a template with a repeated key",
			content: "---\ntemplates:\n  t: &t\n    name: a\n    name: b\n    null: 1\n" +
				"targets:\n  - <<: [*t, *t]\n",
			wantErr: []string{
				`{file}:5:5: targets[0].name: mapping key "name" already defined at line 4`,
			},
		},
		{
			// The library stops in global, storage is decoded before it and prune is not.
			name: "null keys around a fatal error of the root",
			content: "---\nstorage:\n  threads: 2\n  null: x\nglobal:\n  ~: y\n  <<: 5\n" +
				"prune:\n  null: z\n",
			wantErr: []string{
				"{file}:4:3: storage: null key is not allowed",
				"{file}:7:7: map merge requires map or sequence of maps as the value",
			},
		},
		{
			// The fatal errors stop the library before it reads the targets or the sources.
			name:    "null keys in the targets after a fatal error of the root",
			content: "---\nstorage:\n  <<: 5\ntargets:\n  - name: a\n    null: x\n",
			wantErr: []string{
				"{file}:3:7: map merge requires map or sequence of maps as the value",
			},
		},
		{
			name: "null keys in the sources after a fatal error of the target",
			content: "---\ntargets:\n  - name: a\n    <<: 5\n    sources:\n" +
				"      - type: x\n        null: x\n",
			wantErr: []string{
				"{file}:4:9: map merge requires map or sequence of maps as the value",
			},
		},
		{
			// A merging mapping is flattened before its values, so its null keys are seen
			// before a fatal error in a value.
			name: "null keys of a merging mapping before a fatal error",
			content: "---\ntemplates:\n  base: &base\n    name: a\n    null: y\n" +
				"targets:\n  - <<: *base\n    port: !!binary \"!!!\"\n    null: x\n",
			wantErr: []string{
				"{file}:5:5: targets[0]: null key is not allowed",
				"{file}:8:11: !!binary value contains invalid base64 data",
				"{file}:9:5: targets[0]: null key is not allowed",
			},
		},
		{
			// No node is at a fatal error in a key, the keys before it are seen.
			name: "null keys before a fatal error without a node",
			content: "---\nstorage:\n  threads: 2\n  null: x\n? !!binary \"!!!\"\n: 1\n" +
				"prune:\n  null: y\n",
			wantErr: []string{
				"{file}:4:3: storage: null key is not allowed",
				"{file}:5:3: !!binary value contains invalid base64 data",
			},
		},
		{
			// The library drops the template with a repeated key and its values, not the
			// target.
			name: "null keys of a target that merges a template with a repeated key",
			content: "---\ntemplates:\n  t: &t\n    name: a\n    name: b\n    null: y\n" +
				"    host:\n      null: z\ntargets:\n  - <<: *t\n    port: abc\n    null: x\n",
			wantErr: []string{
				`{file}:5:5: targets[0].name: mapping key "name" already defined at line 4`,
				"{file}:11:11: targets[0].port: cannot construct !!str into int",
				"{file}:12:5: targets[0]: null key is not allowed",
			},
		},
		{
			// A key without a name has no path.
			name:    "a key of a wrong type",
			content: "---\ntargets:\n  - name: a\n    ? [x]\n    : 1\n",
			wantErr: []string{"{file}:4:7: cannot construct !!seq into string"},
		},
		{
			// The library stops at a fatal key without a name, the keys after it are not seen.
			name: "null keys after a fatal key without a name",
			content: "---\nstorage:\n  null: w\n  ? !!binary \"!!!\"\n  : 1\n  ~: x\n" +
				"prune:\n  null: y\n",
			wantErr: []string{
				"{file}:3:3: storage: null key is not allowed",
				"{file}:4:5: !!binary value contains invalid base64 data",
			},
		},
		{
			// The library flattens a merging mapping key by key and stops at a fatal key.
			name: "null keys after a fatal key of a merging mapping",
			content: "---\ntemplates:\n  t: &t\n    name: a\n    null: z\ntargets:\n" +
				"  - <<: *t\n    ? !!binary \"!!!\"\n    : x\n    null: y\n  - name: b\n",
			wantErr: []string{"{file}:8:7: !!binary value contains invalid base64 data"},
		},
		{
			// The library drops a merged mapping with a repeated key, the next one sets the
			// key instead.
			name: "a merged mapping with a repeated key",
			content: "---\ntemplates:\n  bad: &bad\n    name: a\n    name: b\n    port: 1\n" +
				"    host:\n      null: z\n  good: &good\n    port: abc\n" +
				"targets:\n  - <<: [*bad, *good]\n    null: x\n",
			wantErr: []string{
				`{file}:5:5: targets[0].name: mapping key "name" already defined at line 4`,
				"{file}:10:11: targets[0].port: cannot construct !!str into int",
				"{file}:13:5: targets[0]: null key is not allowed",
			},
		},
		{
			// The dropped mapping hides no value of the next one on the same path.
			name: "a merged mapping with a repeated key and a null key in the next one",
			content: "---\ntemplates:\n  a: &a\n    name: x\n    name: y\n    keys:\n" +
				"      b2_id: 'plain:x'\n  b: &b\n    keys:\n      null: z\nstorage:\n" +
				"  <<: [*a, *b]\n  threads: 2\n",
			wantErr: []string{
				`{file}:5:5: storage.name: mapping key "name" already defined at line 4`,
				"{file}:10:7: storage.keys: null key is not allowed",
			},
		},
		{
			// The keys of the dropped mapping are marked even when a mapping before it sets
			// them.
			name: "a merged mapping with a repeated key after another",
			content: "---\ntemplates:\n  a: &a\n    name: x\n    name: y\n    host:\n" +
				"      null: z\n  b: &b\n    name: w\ntargets:\n  - <<: [*b, *a]\n",
			wantErr: []string{
				`{file}:5:5: targets[0].name: mapping key "name" already defined at line 4`,
			},
		},
		{
			// A repeated merge key of the dropped mapping belongs to the mapping it is merged
			// into.
			name: "a merged mapping with a repeated merge key",
			content: "---\ntemplates:\n  a: &a {name: a}\n  b: &b {host: b}\n  bad: &bad\n" +
				"    <<: *a\n    <<: *b\ntargets:\n  - <<: *bad\n",
			wantErr: []string{
				`{file}:7:5: targets[0]: mapping key "<<" already defined at line 6`,
			},
		},
		{
			// A repeated key without a name has no path, the library still drops the
			// mapping.
			name: "null keys in a mapping with a repeated null key",
			content: "---\ntargets:\n  - name: a\n    null: 1\n    null: 2\n    host:\n" +
				"      ~: 3\n",
			wantErr: []string{`{file}:5:5: mapping key "null" already defined at line 4`},
		},
		{
			// An alias in a mapping the library does not construct is not a candidate.
			name: "an alias in a mapping with a repeated null key",
			content: "---\nglobal:\n  timeout: &t 30\nstorage:\n  keys:\n    b2_id: *t\n" +
				"    null: x\n    null: y\n",
			wantErr: []string{
				"{file}:3:12: global.timeout: invalid duration: want a number with a unit, " +
					"such as 30s",
				`{file}:8:5: mapping key "null" already defined at line 7`,
			},
		},
		{
			// The keys of a mapping with a repeated key in another such mapping are not
			// candidates either.
			name: "a mapping with a repeated key in another one",
			content: "---\ntemplates:\n  d: &d\n    name: a\n    name: b\nglobal:\n" +
				"  retry:\n    bad: *d\n    null: x\n    null: y\nstorage: *d\n",
			wantErr: []string{
				`{file}:5:5: storage.name: mapping key "name" already defined at line 4`,
				`{file}:10:5: mapping key "null" already defined at line 9`,
			},
		},
		{
			// The key that holds a mapping with a repeated key is in a mapping the library
			// constructs.
			name: "an unknown key with a repeated key in its value",
			content: "---\ntemplates:\n  t: &t\n    port: 1\ntargets:\n  - <<: *t\n" +
				"    keys:\n      null: 1\n      null: 2\n      surplus: 3\n",
			wantErr: []string{
				"{file}:7:5: targets[0].keys: field keys not found in type config.targetItem",
			},
		},
		{
			// A key of a dropped mapping keeps its path when the local key of the same name
			// holds a mapping with a repeated key.
			name: "a dropped mapping and a local mapping with a repeated key",
			content: "---\ntemplates:\n  r: &r\n    keys: a\n    keys: b\nstorage:\n" +
				"  <<: *r\n  keys:\n    b2_id: 'plain:x'\n    b2_id: 'plain:y'\n",
			wantErr: []string{
				`{file}:5:5: storage.keys: mapping key "keys" already defined at line 4`,
				`{file}:10:5: storage.keys.b2_id: mapping key "b2_id" already defined at line 9`,
			},
		},
		{
			// The library reports a value of an alias to a mapping on the way at its anchor,
			// where the mapping is too.
			name: "an alias to a mapping on the way",
			content: "---\nglobal: &g {timeout: *g}\nprune:\n  keep: &k [*k]\nstorage:\n" +
				"  keys: &s {x: *s}\n",
			wantErr: []string{
				"{file}:2:9: cannot construct !!map into string",
				"{file}:4:9: cannot construct !!seq into string",
				"{file}:6:9: cannot construct !!map into secret.Ref",
			},
		},
		{
			// The library does not construct the value of an unknown key, so the alias in it
			// is not a candidate, whichever of them comes first.
			name: "an alias under an unknown key",
			content: "---\ntemplates:\n  t: &t {bogus: 1}\nglobal:\n  extra: *t\n" +
				"vault: *t\n",
			wantErr: []string{
				"{file}:3:10: vault.bogus: field bogus not found in type config.Vault",
				"{file}:5:3: global.extra: field extra not found in type config.Global",
			},
		},
		{
			name: "an alias under a later unknown key",
			content: "---\ntemplates:\n  t: &t {bogus: 1}\nvault: *t\nglobal:\n" +
				"  extra: *t\n",
			wantErr: []string{
				"{file}:3:10: vault.bogus: field bogus not found in type config.Vault",
				"{file}:6:3: global.extra: field extra not found in type config.Global",
			},
		},
		{
			// An unknown key given again is not under the first one.
			name: "an unknown key given again",
			content: "---\ntemplates:\n  k: &k bogus\n  t: &t {bogus: 1}\nglobal:\n" +
				"  bogus: *t\n  *k : *t\nvault: *t\n",
			wantErr: []string{
				"{file}:4:10: vault.bogus: field bogus not found in type config.Vault",
				"{file}:6:3: global.bogus: field bogus not found in type config.Global",
				"{file}:7:3: global.bogus: field bogus not found in type config.Global",
			},
		},
		{
			name:    "an unknown key given again after an alias",
			content: "---\ntemplates:\n  b: &b bogus\nstorage:\n  *b : 1\n  bogus: 2\n",
			wantErr: []string{
				"{file}:5:3: storage.bogus: field bogus not found in type config.Storage",
				"{file}:6:3: storage.bogus: field bogus not found in type config.Storage",
			},
		},
		{
			// The library does not construct the value of a field set again, so an alias in
			// it is not a candidate, whatever the first value holds.
			name: "an alias in a field set again",
			content: "---\ntemplates:\n  g: &g global\n  t: &t {bogus: 1}\n" +
				"global: {bogus: 2}\n*g : *t\nvault: *t\n",
			wantErr: []string{
				"{file}:4:10: vault.bogus: field bogus not found in type config.Vault",
				"{file}:5:10: global.bogus: field bogus not found in type config.Global",
				"{file}:6:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "an alias in a later field set again",
			content: "---\ntemplates:\n  g: &g global\n  t: &t {bogus: 1}\nvault: *t\n" +
				"global: {instance: a}\n*g : *t\n",
			wantErr: []string{
				"{file}:4:10: vault.bogus: field bogus not found in type config.Vault",
				"{file}:7:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "a repeated key in a field set again",
			content: "---\ntemplates:\n  g: &g global\n  t: &t {bogus: 1, bogus: 2}\n" +
				"global: {bogus: 3}\n*g : *t\nvault: *t\n",
			wantErr: []string{
				"{file}:4:20: vault.bogus: mapping key \"bogus\" already defined at line 4",
				"{file}:5:10: global.bogus: field bogus not found in type config.Global",
				"{file}:6:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "a merged repeated key in a field set again",
			content: "---\ntemplates:\n  g: &g global\n  r: &r {bogus: 2, bogus: 3}\n" +
				"global: {bogus: 4}\n*g : {<<: *r}\nvault: *r\n",
			wantErr: []string{
				"{file}:4:20: vault.bogus: mapping key \"bogus\" already defined at line 4",
				"{file}:5:10: global.bogus: field bogus not found in type config.Global",
				"{file}:6:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "a wrong value in a field set again",
			content: "---\ntemplates:\n  g: &g global\n  t: &t {timeout: abc}\n" +
				"global: {instance: a}\n*g : *t\nvault: *t\n",
			wantErr: []string{
				"{file}:4:19: vault.timeout: invalid duration: want a number with a unit, " +
					"such as 30s",
				"{file}:6:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "the same alias in a field set again",
			content: "---\ntemplates:\n  g: &g global\n  t: &t {timeout: abc}\n" +
				"global: *t\n*g : *t\n",
			wantErr: []string{
				"{file}:4:19: global.timeout: invalid duration: want a number with a unit, " +
					"such as 30s",
				"{file}:6:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "a list in a field set again",
			content: "---\ntemplates:\n  e: &e {bogus: 1}\n  k: &k targets\n" +
				"targets: [*e]\n*k : [*e]\n",
			wantErr: []string{
				"{file}:3:10: targets[0].bogus: field bogus not found in type config.targetItem",
				"{file}:6:1: targets: field targets already set in type config.configFile",
			},
		},
		{
			// A repeated key in a value the library does not construct hides nothing.
			name: "a repeated key only in a field set again",
			content: "---\ntemplates:\n  g: &g global\n  t: &t {bogus: 1, bogus: 2}\n" +
				"global: {timeout: abc}\n*g : *t\n",
			wantErr: []string{
				"{file}:5:19: global.timeout: invalid duration: want a number with a unit, " +
					"such as 30s",
				"{file}:6:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "a null key before a repeated key in a field set again",
			content: "---\ntemplates:\n  g: &g global\n  t: &t {bogus: 1, bogus: 2}\n" +
				"global: {null: 1}\n*g : *t\n",
			wantErr: []string{
				"{file}:5:10: global: null key is not allowed",
				"{file}:6:1: global: field global already set in type config.configFile",
			},
		},
		{
			name: "a repeated key in a value of a field set again",
			content: "---\ntemplates:\n  g: &g global\n" +
				"  t: &t {retry: {x: 1}, bogus: 1, bogus: 2}\nglobal:\n" +
				"  retry: {bogus: 1, bogus: 2}\n*g : *t\n",
			wantErr: []string{
				"{file}:6:21: global.retry.bogus: mapping key \"bogus\" already defined at " +
					"line 6",
				"{file}:7:1: global: field global already set in type config.configFile",
			},
		},
		{
			// Whether a mapping hides its keys by a repeated key is known only after the
			// fields set again are found, so no candidate is taken before.
			name: "fields set again with repeated keys",
			content: "---\ntemplates:\n  g: &g global\n  s: &s storage\n" +
				"  k: &k max_retries_time\n  M: &M {max_retries_time: 2s, *k: 1}\n" +
				"storage:\n  name: *M\n*s : {bogus: 1, bogus: 2}\nglobal:\n  retry: *M\n" +
				"*g : {bogus: 1, bogus: 2}\n",
			wantErr: []string{
				"{file}:6:6: cannot construct !!map into string",
				"{file}:6:32: field max_retries_time already set in type config.Retry",
				"{file}:9:1: storage: field storage already set in type config.configFile",
				"{file}:12:1: global: field global already set in type config.configFile",
			},
		},
		{
			// The library constructs a key that is not a string and reports a repeated key
			// in it, an unknown key found later hides the only other candidate.
			name: "a repeated key in a key that is not a string",
			content: "---\ntemplates:\n  a1: &a1 {k1: 1, k1: 2}\nglobal:\n  *a1 : 1\n" +
				"  name: {k2: *a1}\n",
			wantErr: []string{
				"{file}:3:19: mapping key \"k1\" already defined at line 3",
				"{file}:6:3: global.name: field name not found in type config.Global",
			},
		},
		{
			name: "a repeated key in a key that is not a string and a field set again",
			content: "---\ntemplates:\n  a1: &a1 {k1: 1, k1: 2}\n  s: &s storage\n" +
				"global:\n  *a1 : 1\n  name: {k2: *a1}\nstorage: {}\n*s : {name: *a1}\n",
			wantErr: []string{
				"{file}:3:19: mapping key \"k1\" already defined at line 3",
				"{file}:7:3: global.name: field name not found in type config.Global",
				"{file}:9:1: storage: field storage already set in type config.configFile",
			},
		},
		{
			// The value of the field set again is not constructed, so the key that is not a
			// string is the only other source of the repeated key.
			name: "a repeated key in a key that is not a string after a field set again",
			content: "---\ntemplates:\n" +
				"  a0: &a0 {max_retries_time: 2s, max_retries_time: abc}\n  g: &g global\n" +
				"storage: *a0\n*g : {}\nglobal: *a0\n*a0 : *g\n",
			wantErr: []string{
				"{file}:3:34: mapping key \"max_retries_time\" already defined at line 3",
				"{file}:7:1: global: field global already set in type config.configFile",
			},
		},
		{
			// The library constructs the first value of a field set again, not the second.
			name: "a null key in a field set again",
			content: "---\ntemplates:\n  k: &k keys\nstorage:\n  keys: {null: x}\n" +
				"  *k : {}\n",
			wantErr: []string{
				"{file}:5:10: storage.keys: null key is not allowed",
				"{file}:6:3: storage.keys: field keys already set in type config.Storage",
			},
		},
		{
			name: "a null key merged into the first value of a field set again",
			content: "---\ntemplates:\n  k: &k keys\n  t: &t {null: y}\nstorage:\n" +
				"  keys: {<<: *t}\n  *k : {a: b}\n",
			wantErr: []string{
				"{file}:4:10: storage.keys: null key is not allowed",
				"{file}:7:3: storage.keys: field keys already set in type config.Storage",
			},
		},
		{
			name: "a null key merged into a field set again",
			content: "---\ntemplates:\n  k: &k keys\n  t: &t {null: y}\nstorage:\n" +
				"  keys: {a: b}\n  *k : {<<: *t}\n",
			wantErr: []string{
				"{file}:7:3: storage.keys: field keys already set in type config.Storage",
			},
		},
		{
			name:    "an empty unknown key",
			content: "---\nstorage:\n  '': 1\ntargets:\n  - name: a\n    '': 2\n",
			wantErr: []string{
				"{file}:3:3: storage[\"\"]: field  not found in type config.Storage",
				"{file}:6:5: targets[0][\"\"]: field  not found in type config.targetItem",
			},
		},
		{
			// A problem at a key that is not a string has no path, the mapping that starts at
			// the key is not constructed either.
			name: "keys that are not strings",
			content: "---\nstorage:\n  [a]: x\n  {a: b}: y\nglobal:\n  instance:\n" +
				"    [a]: x\n    null: y\n",
			wantErr: []string{
				"{file}:3:3: cannot construct !!seq into string",
				"{file}:4:3: cannot construct !!map into string",
				"{file}:7:5: cannot construct !!map into string",
			},
		},
		{
			name: "null keys in a value of a wrong type",
			content: "---\nglobal:\n  instance:\n    null: x\nprune:\n  keep:\n" +
				"    - null: y\n",
			wantErr: []string{
				"{file}:4:5: global.instance: cannot construct !!map into string",
				"{file}:7:7: prune.keep[0]: cannot construct",
			},
		},
		{
			// The same position goes in the order of assignment: the root, the targets,
			// then the sources, not in the order of the file.
			name: "template in every scope",
			content: "---\ntemplates:\n  t: &t\n    bogus: 1\ntargets:\n  - <<: *t\n" +
				"    name: a\n    sources:\n      - <<: *t\n        type: test\n" +
				"  - <<: *t\n    name: b\nglobal:\n  <<: *t\n",
			wantErr: []string{
				"{file}:4:5: global.bogus: field bogus not found in type config.Global",
				"{file}:4:5: targets[0].bogus: field bogus not found in type config.targetItem",
				"{file}:4:5: targets[1].bogus: field bogus not found in type config.targetItem",
				"{file}:4:5: targets[0].sources[0].bogus: field bogus not found in type",
			},
		},
		{
			// The local key overrides the template, the library skips the template value.
			name: "alias of an overridden template value",
			content: "---\ntemplates:\n  t: &t {timeout: &x 30}\nglobal:\n  <<: *t\n" +
				"  timeout: 1h\nvault:\n  timeout: *x\n",
			wantErr: []string{"{file}:3:19: vault.timeout: invalid duration"},
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
				"{file}:3:14: global.log_level: invalid log level: want one of debug, info, warn, " +
					"error",
				"{file}:4:15: global.log_format: invalid log format: want one of text, json",
				"{file}:5:12: global.timeout: invalid duration: want a number with a unit, " +
					"such as 30s",
				"{file}:7:12: storage.threads: cannot construct !!str into int",
				"{file}:8:9: storage.keys: cannot construct !!str into map[string]secret.Ref",
				"{file}:10:23: check.test_restore_bytes: invalid size: want digits with an " +
					"optional K, M, G or T suffix",
				"{file}:14:15: targets[0].sources[0].type: unknown source type",
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
				"{file}:3:13: storage.password: cannot construct tag:example.invalid,2026:secret ref",
			},
			hidden: "fake-pw",
		},
		{
			name: "value after a tag with a backtick",
			content: "---\nstorage:\n  password: !<tag:example.invalid,2026:a%20%60b> " +
				"'fake-pw'\n",
			wantErr: []string{
				"{file}:3:13: storage.password: cannot construct tag:example.invalid,2026:a into",
			},
			hidden: "fake-pw",
		},
		{
			name: "template key in two targets",
			content: "---\ntemplates:\n  tpl: &tpl\n    transport: ssh\n    bogus: 1\n" +
				"targets:\n  - <<: *tpl\n    name: a\n  - <<: *tpl\n    name: b\n",
			wantErr: []string{
				"{file}:5:5: targets[0].bogus: field bogus not found in type config.targetItem",
				"{file}:5:5: targets[1].bogus: field bogus not found in type config.targetItem",
			},
		},
		{
			name: "two merge keys",
			content: "---\ntemplates:\n  a: &a\n    port: 1\n  b: &b\n    user: u\n" +
				"targets:\n  - name: x\n    <<: *a\n    <<: *b\n",
			wantErr: []string{
				`{file}:10:5: targets[0]: mapping key "<<" already defined at line 9`,
			},
		},
		{
			name: "fatal error stops decoding",
			content: "---\ntargets:\n  - name: a\n    extra: 1\n  - name: b\n    <<: 1\n" +
				"  - name: c\n    extra: 2\n",
			wantErr: []string{
				"{file}:4:5: targets[0].extra: field extra not found in type config.targetItem",
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
				"{file}:4:5: targets[0].extra: field extra not found",
				"{file}:6:12: global.timeout: invalid duration",
				"{file}:7:3: global.bogus: field bogus not found in type config.Global",
			},
		},
		{
			name: "source problems",
			content: "---\ntargets:\n  - name: a\n    sources:\n      - type: bogus\n" +
				"      - id: no-type\n      - type: test\n        extra: 1\n" +
				"      - type: [test]\n      - type: null\n      - type: test\n" +
				"        id: x\n        id: y\n      - type: ''\n      - [type]\n",
			wantErr: []string{
				"{file}:5:15: targets[0].sources[0].type: unknown source type",
				"{file}:6:9: targets[0].sources[1]: source type is required",
				"{file}:8:9: targets[0].sources[2].extra: field extra not found in type",
				"{file}:9:15: targets[0].sources[3].type: cannot construct !!seq into string",
				"{file}:10:9: targets[0].sources[4]: source type is required",
				`{file}:13:9: targets[0].sources[5].id: mapping key "id" already defined`,
				"{file}:14:15: targets[0].sources[6].type: unknown source type",
				"{file}:15:9: targets[0].sources[7]: cannot construct !!seq into config.sourceHead",
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
			name:    "empty path",
			noFile:  true,
			wantErr: []string{"configuration path is empty"},
		},
		{
			name: "link and parent",
			// filepath.Join would clean the path before Load.
			args: args{path: link + string(filepath.Separator) + ".." +
				string(filepath.Separator) + "main.yaml"},
			want: defaults(),
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
			if (tt.content != "" || path == "") && !tt.noFile {
				path = filepath.Join(t.TempDir(), "main.yaml")
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			got, err := config.Load(path)
			if tt.quick && time.Since(start) > time.Second {
				t.Errorf("Load() took %v, want less than a second", time.Since(start))
			}
			if (err != nil) != (tt.wantErr != nil) {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				var cfgErr *config.Error
				if !errors.As(err, &cfgErr) {
					t.Fatalf("Load() error type = %T, want *Error", err)
				}
				file := ""
				if path != "" {
					file, _ = filepath.Abs(path)
				}
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
			if got.HasValue("global.log_level") != tt.wantLevelSet ||
				got.HasValue("global.log_format") != tt.wantFormatSet {
				t.Errorf("Load() HasValue of log_level, log_format = %v, %v, want %v, %v",
					got.HasValue("global.log_level"), got.HasValue("global.log_format"),
					tt.wantLevelSet, tt.wantFormatSet)
			}
			got.Config.Templates = yaml.Node{}
			if !reflect.DeepEqual(got.Config, tt.want) {
				t.Errorf("Load() Config = %+v, want %+v", got.Config, tt.want)
			}
		})
	}
}
