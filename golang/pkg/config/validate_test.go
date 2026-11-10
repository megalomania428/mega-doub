// cspell:ignore doub duplicacy

package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/pkg/config"
)

// ownPath tells whether the path belongs to the rules of global and global.retry, the
// problems of the other sections are not compared here.
func ownPath(path string) bool {
	return path == "global" || strings.HasPrefix(path, "global.")
}

// ownSections are the sections of the rules of vault, storage, prune and check.
var ownSections = []string{"vault", "storage", "prune", "check"}

// sectionWarnings are the warnings of the rules of the sections; the other warnings on
// their paths, such as the ones of the secret references, are not compared.
var sectionWarnings = []string{"vault token is sent in clear text",
	"url contains a password"}

// owned tells whether the path belongs to one of the sections, or to global and
// global.retry without sections.
func owned(path string, sections []string) bool {
	if len(sections) == 0 {
		return ownPath(path)
	}
	for _, s := range sections {
		if path == s || strings.HasPrefix(path, s+".") {
			return true
		}
	}
	return false
}

func TestValidate(t *testing.T) {
	// want is one expected problem: the path, the position and a part of the message.
	type want struct {
		path         string
		line, column int
		msg          string
	}
	const invalid = "../../testdata/invalid/"
	// base is a valid storage section of five lines before the section of a test.
	const base = "---\nstorage:\n  url: b2://bucket/prefix\n  snapshot_id: main\n" +
		"  password: env:PASSWORD\n"
	// address returns a document with the vault address at 7:12.
	address := func(s string) string { return base + "vault:\n  address: '" + s + "'\n" }
	// storage returns a document with the storage URL at 3:8.
	storage := func(s string) string {
		return "---\nstorage:\n  url: '" + s + "'\n  snapshot_id: main\n" +
			"  password: env:PASSWORD\n"
	}
	// vaultRequired are the problems of a vault: reference without the vault section.
	vaultRequired := []want{{"vault.address", 1, 1, "required"},
		{"vault.token", 1, 1, "required"}}
	tests := []struct {
		name string
		// path is a file of testdata, content is written to a temporary file instead.
		path    string
		content string
		// sections select the compared paths, global and global.retry when empty.
		sections []string
		want     []want
		warnings []want
		// hidden is a value that no compared message contains.
		hidden string
		// all compares every problem and every warning, sections are left unused.
		all bool
	}{
		{name: "example", path: "../../testdata/example.yaml"},
		{name: "full", path: "../../testdata/full.yaml"},
		{
			name: "global values",
			path: invalid + "global-values.yaml",
			want: []want{
				{"global.instance", 8, 13, `"a/b" must match ^[A-Za-z0-9_-]+$`},
				{"global.parallel_targets", 9, 21, "must be at least 1"},
				{"global.parallel_sources", 10, 21, "must be at least 1"},
				{"global.progress", 11, 13, `"foo" must be one of auto, always, never`},
				{"global.timeout", 12, 12, "must be greater than zero"},
			},
		},
		{
			name: "global directories",
			path: invalid + "global-dirs.yaml",
			want: []want{{"global.copy_dir", 9, 13, "must not be inside global.state_dir"}},
		},
		{
			name: "retry values",
			path: invalid + "retry-values.yaml",
			want: []want{{"global.retry.backoff_cap", 9, 3, "less than backoff_initial"}},
		},
		{
			// An empty value is left out of the message.
			name: "empty strings",
			content: "---\nglobal:\n  instance: ''\n  copy_dir: ''\n  state_dir: ''\n" +
				"  progress: ''\n",
			want: []want{
				{"global.instance", 3, 13, "must match"},
				{"global.copy_dir", 4, 13, "must be an absolute path"},
				{"global.state_dir", 5, 14, "must be an absolute path"},
				{"global.progress", 6, 13, "must be one of"},
			},
		},
		{
			name: "explicit null keeps the defaults",
			content: "---\nglobal:\n  instance: null\n  copy_dir: null\n" +
				"  parallel_targets: null\n  progress: null\n  timeout: null\n" +
				"  retry:\n    backoff_cap: null\n",
		},
		{
			// The computed directories of the instance are equal after cleaning; the
			// missing state_dir is reported at the key global and goes first.
			name:    "invalid instance with computed directories",
			content: "---\nglobal:\n  instance: ..\n",
			want: []want{
				{"global.state_dir", 2, 1, "must differ from global.copy_dir"},
				{"global.instance", 3, 13, `".." must match`},
			},
		},
		{
			// Both values are the anchor, the same position keeps the order of the fields.
			name:    "same position",
			content: "---\nglobal:\n  parallel_targets: &zero 0\n  parallel_sources: *zero\n",
			want: []want{
				{"global.parallel_targets", 3, 21, "must be at least 1"},
				{"global.parallel_sources", 3, 21, "must be at least 1"},
			},
		},
		{
			name: "value from a template",
			content: "---\ntemplates:\n  base: &base\n    parallel_sources: 0\nglobal:\n" +
				"  <<: *base\n",
			want: []want{{"global.parallel_sources", 4, 23, "must be at least 1"}},
		},
		{
			name: "cap below initial",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 5s\n" +
				"    backoff_cap: 2s\n",
			want: []want{{"global.retry.backoff_cap", 5, 18, `effective "2s" < "5s"`}},
		},
		{
			name:    "initial above the default cap",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 10s\n",
			want:    []want{{"global.retry.backoff_cap", 3, 3, `effective "7s" < "10s"`}},
		},
		{
			name: "zero cap selects the default",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 10s\n" +
				"    backoff_cap: 0s\n",
			want: []want{{"global.retry.backoff_cap", 5, 18, `effective "7s" < "10s"`}},
		},
		{
			name: "null cap selects the default",
			content: "---\nglobal:\n  retry:\n    backoff_initial: 10s\n" +
				"    backoff_cap: null\n",
			want: []want{{"global.retry.backoff_cap", 5, 18, `effective "7s" < "10s"`}},
		},
		{
			name: "negative values select the defaults",
			content: "---\nglobal:\n  retry:\n    max_retries_time: -1s\n" +
				"    backoff_initial: -1s\n    backoff_step: 0s\n" +
				"    backoff_step_attempts: -1\n    backoff_cap: 0s\n",
		},
		{
			name: "zero values select the defaults",
			content: "---\nglobal:\n  retry:\n    max_retries_time: 0\n" +
				"    backoff_initial: 0s\n    backoff_step: -2s\n" +
				"    backoff_step_attempts: 0\n    backoff_cap: -1s\n",
		},
		{
			// 8 * 2^61 overflows uint64.
			name: "cycle overflow with the defaults",
			content: "---\nglobal:\n  retry:\n" +
				"    backoff_step_attempts: 2305843009213693952\n",
			want: []want{{"global.retry.backoff_step_attempts", 4, 28, "overflows int"}},
		},
		{
			// 2 * (2^62 - 1) is 2^63 - 2.
			name: "largest cycle",
			content: "---\nglobal:\n  retry:\n    backoff_step: 4s\n" +
				"    backoff_step_attempts: 4611686018427387903\n",
		},
		{
			// 2 * 2^62 is 2^63.
			name: "smallest overflowing cycle",
			content: "---\nglobal:\n  retry:\n    backoff_step: 4s\n" +
				"    backoff_step_attempts: 4611686018427387904\n",
			want: []want{{"global.retry.backoff_step_attempts", 5, 28, "overflows int"}},
		},
		{
			// The step above the cap leaves one block per cycle.
			name: "one block per cycle",
			content: "---\nglobal:\n  retry:\n    backoff_step: 10s\n" +
				"    backoff_step_attempts: 9223372036854775807\n",
		},
		{
			name:    "equal directories",
			content: "---\nglobal:\n  copy_dir: /srv/same\n  state_dir: /srv/same/\n",
			want:    []want{{"global.state_dir", 4, 14, "must differ from global.copy_dir"}},
		},
		{
			name: "state directory inside the copy directory after cleaning",
			content: "---\nglobal:\n  copy_dir: /srv/copy\n" +
				"  state_dir: /srv/other/../copy/state\n",
			want: []want{{"global.state_dir", 4, 14, "must not be inside global.copy_dir"}},
		},
		{
			name:    "common prefix",
			content: "---\nglobal:\n  copy_dir: /a/copy\n  state_dir: /a/copy2\n",
		},
		{
			name:    "letter case",
			content: "---\nglobal:\n  copy_dir: /A\n  state_dir: /a\n",
		},
		{
			name:    "root copy directory",
			content: "---\nglobal:\n  copy_dir: /\n  state_dir: /srv/state\n",
			want:    []want{{"global.state_dir", 4, 14, "must not be inside global.copy_dir"}},
		},
		{
			name:    "relative directory",
			content: "---\nglobal:\n  copy_dir: srv/copy\n",
			want:    []want{{"global.copy_dir", 3, 13, `"srv/copy" must be an absolute path`}},
		},
		{
			name:    "null copy directory inside the state directory",
			content: "---\nglobal:\n  copy_dir: null\n  state_dir: /var/lib/mega-doub\n",
			want:    []want{{"global.copy_dir", 3, 13, "must not be inside global.state_dir"}},
		},
		{
			name:    "missing copy directory inside the state directory",
			content: "---\nglobal:\n  state_dir: /var/lib/mega-doub\n",
			want:    []want{{"global.copy_dir", 2, 1, "must not be inside global.state_dir"}},
		},
		{name: "example sections", path: "../../testdata/example.yaml", sections: ownSections},
		{
			name: "full sections", path: "../../testdata/full.yaml", sections: ownSections,
			warnings: []want{{"storage.url", 29, 8, "url contains a password"}},
			hidden:   "fake-url-password",
		},
		{
			name: "vault missing", path: invalid + "vault-missing.yaml", sections: ownSections,
			want: []want{{"vault.address", 7, 1, "required"}, {"vault.token", 7, 1, "required"}},
		},
		{
			name: "vault token vault", path: invalid + "vault-token-vault.yaml",
			sections: ownSections,
			want:     []want{{"vault.token", 9, 10, "must not be a vault: reference"}},
		},
		{
			name: "vault values", path: invalid + "vault-values.yaml", sections: ownSections,
			want: []want{
				{"vault.address", 8, 12, "must not contain a path"},
				{"vault.timeout", 9, 12, "must be greater than zero"},
				{"vault.ca_file", 10, 12, `"certs/vault.crt" must be an absolute path`},
			},
			hidden: "vault.example.invalid",
		},
		{
			name: "storage values", path: invalid + "storage-values.yaml", sections: ownSections,
			want: []want{
				{"storage.password", 4, 1, "required"},
				{"storage.name", 5, 9, `"SSH" is reserved by duplicacy`},
				{"storage.url", 6, 8, "must be a URL with a scheme and a host"},
				{"storage.snapshot_id", 7, 16, `"storage main" must match ^[A-Za-z0-9_-]+$`},
				{"storage.threads", 8, 12, "must be at least 1"},
				{`storage.keys[""]`, 10, 9, "required"},
			},
			hidden: "b2:bucket",
		},
		{
			name: "prune keep", path: invalid + "prune-keep.yaml", sections: ownSections,
			want: []want{
				{"prune.threads", 9, 12, "must be at least 1"},
				{"prune.keep[0]", 11, 7, `"0:0" must have an age m of at least 1`},
				{"prune.keep[1]", 12, 7, `"1:0" must have an age m of at least 1`},
				{"prune.keep[2]", 13, 7, `" 1:7" must have the form n:m`},
				{"prune.keep[3]", 14, 7, `"99999999999999999999:5" is out of range`},
				{"prune.keep[4]", 15, 7, `"7" must have the form n:m`},
			},
		},
		{
			name: "prune order", path: invalid + "prune-order.yaml", sections: ownSections,
			want: []want{
				{"prune.keep[1]", 9, 18, `"3:44" must have an age m below the previous one`},
				{"prune.keep[2]", 9, 26, `"1:44" must have an age m below the previous one`},
				{"prune.keep[4]", 9, 42, `"0:60" must have an age m below the previous one`},
			},
		},
		{
			name: "check values", path: invalid + "check-values.yaml", sections: ownSections,
			want: []want{{"check.every", 8, 10, "must not be negative"}},
		},
		{
			name: "vault reference in storage.password",
			content: "---\nstorage:\n  url: b2://bucket/prefix\n  snapshot_id: main\n" +
				"  password: vault:a/b/c\n",
			sections: ownSections,
			want:     vaultRequired,
		},
		{
			// The prefix counts as written, the syntax of the reference is not checked.
			name:     "malformed vault reference",
			content:  base + "targets:\n  - name: here\n    key_passphrase: vault:x\n",
			sections: ownSections,
			want:     vaultRequired,
		},
		{
			name:     "vault reference in vault.token is not counted",
			content:  base + "vault:\n  token: vault:a/b/c\n",
			sections: ownSections,
			want:     []want{{"vault.token", 7, 10, "must not be a vault: reference"}},
		},
		{
			name: "vault reference in a forbidden field of a local target is not counted",
			content: base + "targets:\n  - name: here\n    transport: local\n" +
				"    key_passphrase: vault:a/b/c\n",
			sections: ownSections,
		},
		{
			name:     "vault reference in a target without transport",
			content:  base + "targets:\n  - name: here\n    key_passphrase: vault:a/b/c\n",
			sections: ownSections,
			want:     vaultRequired,
		},
		{
			name: "vault reference in an ftp target",
			content: base + "targets:\n  - name: here\n    transport: ftp\n" +
				"    key_passphrase: vault:a/b/c\n",
			sections: ownSections,
			want:     vaultRequired,
		},
		{
			name: "http address", content: address("http://vault.example.invalid"),
			sections: ownSections,
			warnings: []want{{"vault.address", 7, 12, "vault token is sent in clear text"}},
			hidden:   "vault.example.invalid",
		},
		{
			name: "upper case http address", content: address("HTTP://vault.example.invalid"),
			sections: ownSections,
			warnings: []want{{"vault.address", 7, 12, "vault token is sent in clear text"}},
			hidden:   "vault.example.invalid",
		},
		{name: "https address", content: address("https://h"), sections: ownSections},
		{name: "address with a slash", content: address("https://h/"), sections: ownSections},
		{name: "upper case https address", content: address("HTTPS://h"),
			sections: ownSections},
		{name: "address with a port", content: address("https://[::1]:8200"),
			sections: ownSections},
		{name: "address with an empty port", content: address("https://h:"),
			sections: ownSections},
		{
			name: "opaque address", content: address("https:h"), sections: ownSections,
			want:   []want{{"vault.address", 7, 12, "must have // before the host"}},
			hidden: "https:h",
		},
		{
			name: "address without a host", content: address("https:///x"),
			sections: ownSections,
			want:     []want{{"vault.address", 7, 12, "must have a host"}},
			hidden:   "https:///x",
		},
		{
			name: "address with a user", content: address("https://u@h"),
			sections: ownSections,
			want:     []want{{"vault.address", 7, 12, "must not contain user information"}},
			hidden:   "https://u@h",
		},
		{
			name: "address with a path", content: address("https://h/x"), sections: ownSections,
			want:   []want{{"vault.address", 7, 12, "must not contain a path"}},
			hidden: "https://h/x",
		},
		{
			name: "address with an empty query", content: address("https://h?"),
			sections: ownSections,
			want:     []want{{"vault.address", 7, 12, "must not contain a query or a fragment"}},
			hidden:   "https://h?",
		},
		{
			name: "address with an empty fragment", content: address("https://h#"),
			sections: ownSections,
			want:     []want{{"vault.address", 7, 12, "must not contain a query or a fragment"}},
			hidden:   "https://h#",
		},
		{
			name: "address with a query", content: address("https://h?a=1"),
			sections: ownSections,
			want:     []want{{"vault.address", 7, 12, "must not contain a query or a fragment"}},
			hidden:   "https://h?a=1",
		},
		{
			name: "address with port 0", content: address("https://h:0"), sections: ownSections,
			want:   []want{{"vault.address", 7, 12, "must have a port from 1 to 65535"}},
			hidden: "https://h:0",
		},
		{
			name: "address with port 65536", content: address("https://h:65536"),
			sections: ownSections,
			want:     []want{{"vault.address", 7, 12, "must have a port from 1 to 65535"}},
			hidden:   "https://h:65536",
		},
		{
			name: "ftp address", content: address("ftp://h"), sections: ownSections,
			want:   []want{{"vault.address", 7, 12, "must use http or https"}},
			hidden: "ftp://h",
		},
		{
			name: "address without a scheme", content: address("h"), sections: ownSections,
			want: []want{{"vault.address", 7, 12, "must use http or https"}},
		},
		{
			// An empty string in an optional field means not set.
			name:     "empty vault values",
			content:  base + "vault:\n  address: ''\n  ca_file: ''\n",
			sections: ownSections,
		},
		{name: "bucket url", content: storage("b2://bucket/prefix"), sections: ownSections},
		{name: "sftp url with a user", content: storage("sftp://u@h.example.invalid/x"),
			sections: ownSections},
		{name: "url with a user without a password", content: storage("sftp://u@h/x"),
			sections: ownSections},
		{
			name: "windows path url", content: storage(`C:\backup`), sections: ownSections,
			want:   []want{{"storage.url", 3, 8, "must be a URL with a scheme and a host"}},
			hidden: `C:\backup`,
		},
		{
			name: "host and port url", content: storage("localhost:8080/x"),
			sections: ownSections,
			want:     []want{{"storage.url", 3, 8, "must be a URL with a scheme and a host"}},
			hidden:   "localhost:8080/x",
		},
		{
			name: "absolute path url", content: storage("/abs/path"), sections: ownSections,
			want:   []want{{"storage.url", 3, 8, "must be a URL with a scheme and a host"}},
			hidden: "/abs/path",
		},
		{
			name: "opaque url", content: storage("file:opaque"), sections: ownSections,
			want:   []want{{"storage.url", 3, 8, "must be a URL with a scheme and a host"}},
			hidden: "file:opaque",
		},
		{
			name: "url without a host", content: storage("sftp:///x"), sections: ownSections,
			want:   []want{{"storage.url", 3, 8, "must be a URL with a scheme and a host"}},
			hidden: "sftp:///x",
		},
		{
			name: "url without a scheme", content: storage("://x"), sections: ownSections,
			want:   []want{{"storage.url", 3, 8, "must be a URL with a scheme and a host"}},
			hidden: "://x",
		},
		{
			name: "url with a password", content: storage("sftp://u:p@h/x"),
			sections: ownSections,
			warnings: []want{{"storage.url", 3, 8, "url contains a password"}},
			hidden:   "u:p@h",
		},
		{
			name: "url with a password without a host", content: storage("sftp://u:p@/x"),
			sections: ownSections,
			want:     []want{{"storage.url", 3, 8, "must be a URL with a scheme and a host"}},
			warnings: []want{{"storage.url", 3, 8, "url contains a password"}},
			hidden:   "u:p@",
		},
		{
			name: "url with an empty password", content: storage("sftp://u:@h/x"),
			sections: ownSections,
			warnings: []want{{"storage.url", 3, 8, "url contains a password"}},
			hidden:   "u:@h",
		},
		{
			// An empty required value gives required only, without the checks of its format.
			name: "empty url", content: storage(""), sections: ownSections,
			want: []want{{"storage.url", 3, 8, "required"}},
		},
		{
			name: "empty snapshot id",
			content: "---\nstorage:\n  url: b2://bucket/prefix\n  snapshot_id: ''\n" +
				"  password: env:PASSWORD\n",
			sections: ownSections,
			want:     []want{{"storage.snapshot_id", 4, 16, "required"}},
		},
		{
			name:     "missing snapshot id",
			content:  "---\nstorage:\n  url: b2://bucket/prefix\n  password: env:PASSWORD\n",
			sections: ownSections,
			want:     []want{{"storage.snapshot_id", 2, 1, "required"}},
		},
		{
			name: "missing storage", content: "---\nglobal: {}\n", sections: ownSections,
			want: []want{
				{"storage.url", 1, 1, "required"},
				{"storage.snapshot_id", 1, 1, "required"},
				{"storage.password", 1, 1, "required"},
			},
		},
		{
			name: "empty storage name",
			content: "---\nstorage:\n  name: ''\n  url: b2://bucket/prefix\n" +
				"  snapshot_id: main\n  password: env:PASSWORD\n",
			sections: ownSections,
			want:     []want{{"storage.name", 3, 9, "must not be empty"}},
		},
		{
			name: "lower case ssh storage name",
			content: "---\nstorage:\n  name: ssh\n  url: b2://bucket/prefix\n" +
				"  snapshot_id: main\n  password: env:PASSWORD\n",
			sections: ownSections,
			want:     []want{{"storage.name", 3, 9, `"ssh" is reserved by duplicacy`}},
		},
		{
			name:     "empty key values",
			content:  base + "  keys:\n    b2_id: ''\n    b2_key: null\n    '': env:KEY\n",
			sections: ownSections,
			want: []want{
				{"storage.keys.b2_id", 7, 12, "required"},
				{"storage.keys.b2_key", 8, 13, "required"},
				{`storage.keys[""]`, 9, 9, "required"},
			},
		},
		{
			name:     "empty key name and value",
			content:  base + "  keys:\n    '': ''\n",
			sections: ownSections,
			want:     []want{{`storage.keys[""]`, 7, 9, "required"}},
		},
		{
			name: "keep of the general document",
			content: base + "prune:\n  enabled: true\n" +
				"  keep: ['0:755', '33:386', '22:144', '9:44', '3:11', '1:1']\n",
			sections: ownSections,
		},
		{
			name:     "enabled prune without keep",
			content:  base + "prune:\n  enabled: true\n",
			sections: ownSections,
			want:     []want{{"prune.keep", 6, 1, "must not be empty when prune is enabled"}},
		},
		{
			name:     "enabled prune with an empty keep",
			content:  base + "prune:\n  enabled: true\n  keep: []\n",
			sections: ownSections,
			want:     []want{{"prune.keep", 8, 9, "must not be empty when prune is enabled"}},
		},
		{
			name:     "keep format of a disabled prune",
			content:  base + "prune:\n  keep: ['x']\n",
			sections: ownSections,
			want:     []want{{"prune.keep[0]", 7, 10, `"x" must have the form n:m`}},
		},
		{
			name:     "increasing ages",
			content:  base + "prune:\n  keep: ['1:7', '1:30']\n",
			sections: ownSections,
			want: []want{
				{"prune.keep[1]", 7, 17, `"1:30" must have an age m below the previous one`},
			},
		},
		{
			// An element with an invalid age remains the previous parsed element.
			name:     "invalid age remains previous before a valid age",
			content:  base + "prune:\n  keep: ['1:0', '0:5']\n",
			sections: ownSections,
			want: []want{
				{"prune.keep[0]", 7, 10, `"1:0" must have an age m of at least 1`},
				{"prune.keep[1]", 7, 17, `"0:5" must have an age m below the previous one`},
			},
		},
		{
			name:     "invalid ages take priority over order",
			content:  base + "prune:\n  keep: ['0:0', '1:0']\n",
			sections: ownSections,
			want: []want{
				{"prune.keep[0]", 7, 10, `"0:0" must have an age m of at least 1`},
				{"prune.keep[1]", 7, 17, `"1:0" must have an age m of at least 1`},
			},
		},
		{
			name:     "enabled check with zero every",
			content:  base + "check:\n  enabled: true\n  every: 0\n",
			sections: ownSections,
			want: []want{
				{"check.every", 8, 10, "must be greater than zero when check is enabled"},
			},
		},
		{
			name:     "disabled check with zero every",
			content:  base + "check:\n  every: 0\n",
			sections: ownSections,
		},
		{
			name: "example targets", path: "../../testdata/example.yaml", all: true,
			warnings: []want{
				{"targets[0]", 26, 5, `target "db01" has no sources`},
				{"targets[1]", 32, 5, `target "local" has no sources`},
			},
		},
		{
			name: "full targets", path: "../../testdata/full.yaml", all: true,
			warnings: []want{
				{"storage.url", 29, 8, "url contains a password"},
				{"targets[0].password", 60, 15, "value without prefix is treated as plain"},
			},
			hidden: "fake-",
		},
		{
			name: "target duplicate", path: invalid + "target-duplicate.yaml", all: true,
			want: []want{
				{"targets[0].name", 8, 12, `"." must match ^[A-Za-z0-9_][A-Za-z0-9_.-]*$`},
				{"targets[1].name", 9, 12, `".." must match`},
				{"targets[2].name", 10, 12, `".hidden" must match`},
				{"targets[3].name", 11, 12, `"-x" must match`},
				{"targets[7].name", 15, 6, `"db" is not unique`},
				{"targets[8].name", 16, 6, `"db" is not unique`},
				{"targets[9].name", 17, 12, "required"},
				{"targets[10].name", 18, 12, "required"},
				{"targets[11].name", 19, 12, "required"},
			},
		},
		{
			name: "target ssh", path: invalid + "target-ssh.yaml", all: true,
			want: []want{
				{"targets[1].port", 6, 11, "must be from 1 to 65535"},
				{"targets[0].host", 12, 5, "required"},
				{"targets[0].user", 12, 5, "required"},
				{"targets[0].auth", 14, 11, "must not be empty"},
				{"targets[0].known_hosts", 16, 18,
					`"etc/ssh/known_hosts" must be an absolute path`},
				{"targets[0].connect_timeout", 17, 22, "must be greater than zero"},
				{"targets[1].key", 18, 5, "required"},
				{"targets[1].auth[2]", 23, 31, `"otp" must be one of agent, key, password`},
				{"targets[1].auth[3]", 23, 38, `"key" is not unique`},
				{"targets[1].auth[4]", 23, 45, `"otp" must be one of agent, key, password`},
				{"targets[1].auth[4]", 23, 45, `"otp" is not unique`},
				{"targets[1].password", 24, 15, "required"},
				{"targets[2].known_hosts", 26, 5, "required"},
				{"targets[2].key_passphrase", 30, 5, "must not be set without key in auth"},
				{"targets[3].transport", 31, 28, `"ftp" must be one of local, ssh`},
				{"targets[4].transport", 32, 5, "required"},
			},
			warnings: []want{
				{"targets[0]", 12, 5, `target "bare" has no sources`},
				{"targets[1]", 18, 5, `target "methods" has no sources`},
				{"targets[2]", 26, 5, `target "passphrase" has no sources`},
				{"targets[3]", 31, 5, `target "ftp" has no sources`},
				{"targets[4]", 32, 5, `target "none" has no sources`},
			},
		},
		{
			// The secret syntax of the forbidden fields is not checked.
			name: "target local", path: invalid + "target-local.yaml", all: true,
			want: []want{
				{"targets[0].host", 5, 5, "must not be set for a local target"},
				{"targets[0].port", 6, 5, "must not be set for a local target"},
				{"targets[0].auth", 7, 5, "must not be set for a local target"},
				{"targets[0].key", 16, 5, "must not be set for a local target"},
				{"targets[0].key_passphrase", 17, 5, "must not be set for a local target"},
				{"targets[2].user", 25, 5, "must not be set for a local target"},
				{"targets[2].password", 26, 5, "must not be set for a local target"},
				{"targets[2].known_hosts", 27, 5, "must not be set for a local target"},
				{"targets[2].connect_timeout", 28, 5, "must not be set for a local target"},
			},
		},
		{
			// The problems of the source type join the result, an empty id gives one.
			name: "source duplicate", path: invalid + "source-duplicate.yaml", all: true,
			want: []want{
				{"targets[0].sources[0].id", 11, 26, `".." must match`},
				{"targets[0].sources[1].id", 12, 26, `"-x" must match`},
				{"targets[0].sources[3].id", 14, 22, `"app" is not unique`},
				{"targets[0].sources[4].id", 15, 22, `"app" is not unique`},
				{"targets[0].sources[5]", 16, 9, "id is required"},
				{"targets[0].sources[5].id", 16, 26, "required"},
				{"targets[0].sources[6].path", 17, 37, "path must be absolute"},
			},
		},
		{
			name: "secret syntax", path: invalid + "secret-syntax.yaml", all: true,
			want: []want{
				{"vault.token", 4, 10, "must not be a vault: reference"},
				{"storage.keys.b2_id", 10, 12, "file reference must be a clean absolute path"},
				{"storage.keys.b2_key", 11, 13, "env reference must name a variable"},
				{"targets[0].key", 18, 10, "env reference must name a variable"},
				{"targets[0].key_passphrase", 19, 21, "env reference must name a variable"},
				{"targets[0].sources[0].token", 21, 66,
					"file reference must be a clean absolute path"},
			},
			hidden: "1KEY",
		},
		{
			name: "no targets", content: base, all: true,
			warnings: []want{{"targets", 1, 1, "no targets configured"}},
		},
		{
			name: "empty targets", content: base + "targets: []\n", all: true,
			warnings: []want{{"targets", 6, 10, "no targets configured"}},
		},
		{
			name: "empty names without sources",
			content: base + "targets:\n  - name: ''\n    transport: local\n  - name: null\n" +
				"    transport: local\n",
			all: true,
			want: []want{
				{"targets[0].name", 7, 11, "required"},
				{"targets[1].name", 9, 11, "required"},
			},
			warnings: []want{
				{"targets[0]", 7, 5, "target has no sources"},
				{"targets[1]", 9, 5, "target has no sources"},
			},
		},
		{
			name: "value without prefix",
			content: "---\nstorage:\n  url: b2://bucket/prefix\n  snapshot_id: main\n" +
				"  password: fake-bare-password\n",
			all: true,
			warnings: []want{
				{"targets", 1, 1, "no targets configured"},
				{"storage.password", 5, 13, "value without prefix is treated as plain"},
			},
			hidden: "fake-bare-password",
		},
		{
			name: "empty ssh values",
			content: base + "targets:\n  - name: empty\n    transport: ssh\n    host: ''\n" +
				"    user: null\n    auth: ['key', 'password']\n    key: ''\n" +
				"    password: ''\n    known_hosts: ''\n" +
				"    sources: [{type: test, id: empty, path: /srv/empty}]\n",
			all: true,
			want: []want{
				{"targets[0].host", 9, 11, "required"},
				{"targets[0].user", 10, 11, "required"},
				{"targets[0].key", 12, 10, "required"},
				{"targets[0].password", 13, 15, "required"},
				{"targets[0].known_hosts", 14, 18, "required"},
			},
		},
		{
			name: "secrets of targets with an empty or an unknown transport",
			content: base + "targets:\n  - name: wrong\n    transport: ftp\n" +
				"    key_passphrase: 'env:1X'\n" +
				"    sources: [{type: test, id: w, path: /srv/w}]\n" +
				"  - name: empty\n    key_passphrase: 'env:2X'\n" +
				"    sources: [{type: test, id: e, path: /srv/e}]\n",
			all: true,
			want: []want{
				{"targets[0].transport", 8, 16, `"ftp" must be one of local, ssh`},
				{"targets[0].key_passphrase", 9, 21, "env reference must name a variable"},
				{"targets[1].transport", 11, 5, "required"},
				{"targets[1].key_passphrase", 12, 21, "env reference must name a variable"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if tt.content != "" {
				path = filepath.Join(t.TempDir(), "main.yaml")
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			file, err := filepath.Abs(path)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			result := config.Validate(loaded)
			checkErr(t, result)
			var problems, warnings []config.Problem
			for _, p := range result.Problems {
				if tt.all || owned(p.Path, tt.sections) {
					problems = append(problems, p)
				}
			}
			for _, w := range result.Warnings {
				if tt.all || (owned(w.Path, tt.sections) &&
					(len(tt.sections) == 0 || slices.Contains(sectionWarnings, w.Message))) {
					warnings = append(warnings, w)
				}
			}
			compare := func(kind string, got []config.Problem, wants []want) {
				t.Helper()
				if len(got) != len(wants) {
					t.Fatalf("Validate() %s = %v, want %d", kind, got, len(wants))
				}
				for i, w := range wants {
					p := got[i]
					if p.File != file || p.Path != w.path || p.Line != w.line ||
						p.Column != w.column || !strings.Contains(p.Message, w.msg) ||
						strings.Contains(p.Message, `""`) ||
						strings.Contains(p.Message, "://") ||
						tt.hidden != "" && strings.Contains(p.Message, tt.hidden) {
						t.Errorf("Validate() %s %d = %v, want %s:%d:%d: %s: …%s…", kind, i, p,
							file, w.line, w.column, w.path, w.msg)
					}
				}
			}
			compare("problem", problems, tt.want)
			compare("warning", warnings, tt.warnings)
		})
	}
}

// checkErr checks that Err returns a true nil without problems and *Error with the
// same problems otherwise.
func checkErr(t *testing.T, result config.Result) {
	t.Helper()
	err := result.Err()
	if len(result.Problems) == 0 {
		if err != nil {
			t.Errorf("Result.Err() = %v, want nil", err)
		}
		return
	}
	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) || len(cfgErr.Problems) != len(result.Problems) {
		t.Errorf("Result.Err() = %v, want *config.Error with %d problems", err,
			len(result.Problems))
	}
}
