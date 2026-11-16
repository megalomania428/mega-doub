package config_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

// hygieneSecretKeys are the keys whose values are secrets; every value of the mapping
// under the key keys is a secret too.
var hygieneSecretKeys = []string{"password", "key", "key_passphrase", "token"}

// hygieneBucketSchemes are the URL schemes whose host is the name of a bucket.
var hygieneBucketSchemes = []string{"b2", "s3", "gcs", "azure"}

// hygieneReferences are the prefixes of the secret references that hold no value.
var hygieneReferences = []string{"env:", "file:", "vault:"}

// hygienePair matches a key and its value on a line: the value is quoted in single or
// double quotes or runs up to a space, a comma, } or #; a flow collection and the
// indicator of a block scalar are skipped.
var hygienePair = regexp.MustCompile(
	`('[^']*'|"[^"]*"|[\w-]+):[ \t]+('[^']*'|"[^"]*"|[^\s,}#{\[|>][^\s,}#]*)`)

// hygieneKeys matches the key keys, quoted or not, at the start of a line or inside a
// flow collection, with the spaces after it.
var hygieneKeys = regexp.MustCompile(`(?:^|[\s{,])(?:keys|'keys'|"keys"):[ \t]*`)

// fakeHost tells whether the host is under the reserved domain example.invalid.
func fakeHost(host string) bool {
	return strings.HasSuffix(host, ".example.invalid")
}

// valueViolations returns the violations of the value of the key: a host out of
// example.invalid, a URL with such a host unless its scheme names a bucket, and a
// plain secret or a secret without a prefix that does not start with fake-. An empty
// host or secret holds nothing and passes; env:, file: and vault: references pass.
func valueViolations(key, value string, secret bool) []string {
	var violations []string
	if key == "host" && value != "" && !fakeHost(value) {
		violations = append(violations, fmt.Sprintf("host %q is not under example.invalid",
			value))
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		switch {
		case err != nil:
			violations = append(violations, fmt.Sprintf("value of %s is not a valid URL", key))
		case !slices.Contains(hygieneBucketSchemes, u.Scheme) && !fakeHost(u.Hostname()):
			violations = append(violations, fmt.Sprintf(
				"URL host %q of %s is not under example.invalid", u.Hostname(), key))
		}
	}
	reference := slices.ContainsFunc(hygieneReferences, func(prefix string) bool {
		return strings.HasPrefix(value, prefix)
	})
	if secret && value != "" && !reference &&
		!strings.HasPrefix(strings.TrimPrefix(value, "plain:"), "fake-") {
		violations = append(violations, fmt.Sprintf("secret of %s does not start with fake-",
			key))
	}
	return violations
}

// yamlViolations returns the violations of the YAML documents in content, each with
// its line. The scalars come from the nodes, an alias is checked at its anchor.
func yamlViolations(content []byte) ([]string, error) {
	dec, err := yaml.NewLoader(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	var violations []string
	for {
		var doc yaml.Node
		switch err := dec.Load(&doc); {
		case errors.Is(err, io.EOF):
			return violations, nil
		case err != nil:
			return nil, err
		}
		violations = append(violations, nodeViolations(&doc, "", false, false)...)
	}
}

// nodeViolations returns the violations under the node n, the value of the key key. A
// scalar is a secret when secret is set, keys tells that n is the value of the key keys.
func nodeViolations(n *yaml.Node, key string, secret, keys bool) []string {
	var violations []string
	switch n.Kind {
	case yaml.ScalarNode:
		value := n.Value
		if n.ShortTag() == "!!null" {
			value = ""
		}
		for _, v := range valueViolations(key, value, secret) {
			violations = append(violations, fmt.Sprintf("%d: %s", n.Line, v))
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			violations = append(violations, nodeViolations(n.Content[i+1], k,
				keys || slices.Contains(hygieneSecretKeys, k), k == "keys")...)
		}
	case yaml.DocumentNode, yaml.SequenceNode:
		// An element of a list, such as key in auth, is not the value of a key.
		for _, c := range n.Content {
			violations = append(violations, nodeViolations(c, key, false, false)...)
		}
	}
	return violations
}

// keysRanges returns the ranges of line that hold a value of the key keys: a flow
// collection up to its closing bracket or the rest of the line.
func keysRanges(line string) [][2]int {
	var ranges [][2]int
	for _, m := range hygieneKeys.FindAllStringIndex(line, -1) {
		end := len(line)
		if strings.HasPrefix(line[m[1]:], "{") || strings.HasPrefix(line[m[1]:], "[") {
			end = flowEnd(line, m[1])
		}
		ranges = append(ranges, [2]int{m[1], end})
	}
	return ranges
}

// flowEnd returns the index after the flow collection that starts at start in line, or
// the end of line when the collection is not closed on it; quoted strings are skipped.
func flowEnd(line string, start int) int {
	depth := 0
	var quote byte
	for i := start; i < len(line); i++ {
		switch c := line[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return len(line)
}

// stripComment returns line without its trailing comment: the text from the first # that
// starts the line or follows a space or a tab outside quoted strings. A quote opens a
// quoted string only at the start of the line or after a space, a tab, ':', ',', '{' or
// '[', so an apostrophe inside a plain value does not hide the comment.
func stripComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			if i == 0 || strings.IndexByte(" \t:,{[", line[i-1]) >= 0 {
				quote = c
			}
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i]
		}
	}
	return line
}

// lineViolations returns the violations of the lines of content, which may not parse as
// YAML, each with its line. A comment line is skipped and a comment after a value is
// ignored. The pairs inside a value of keys on its line are secrets; when keys has no
// value on its line, so are the pairs on the following lines with a deeper indent. The
// trailing \r of a line is cut, so CRLF content gives the violations of the LF one.
func lineViolations(content string) []string {
	var violations []string
	keysIndent := -1
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		line = stripComment(line)
		if keysIndent >= 0 && indent <= keysIndent {
			keysIndent = -1
		}
		under := keysIndent >= 0
		ranges := keysRanges(line)
		if len(ranges) > 0 {
			rest := strings.TrimSpace(line[ranges[len(ranges)-1][0]:])
			if rest == "" {
				keysIndent = indent
			}
		}
		for _, m := range hygienePair.FindAllStringSubmatchIndex(line, -1) {
			key, raw := unquote(line[m[2]:m[3]]), line[m[4]:m[5]]
			if key == "keys" {
				continue
			}
			value := unquote(raw)
			if raw == "null" || raw == "~" {
				value = ""
			}
			secret := under || slices.Contains(hygieneSecretKeys, key) ||
				slices.ContainsFunc(ranges, func(r [2]int) bool {
					return r[0] <= m[0] && m[0] < r[1]
				})
			for _, v := range valueViolations(key, value, secret) {
				violations = append(violations, fmt.Sprintf("%d: %s", i+1, v))
			}
		}
	}
	return violations
}

// unquote removes the single or double quotes around s.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// TestTestdataHygiene checks that the files of testdata hold only hosts under
// example.invalid and fake secrets. The files of invalid may not parse and are checked
// by lines, golden holds the masked output and is skipped.
func TestTestdataHygiene(t *testing.T) {
	const root = "../../testdata"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == "golden" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".yaml" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var violations []string
		if filepath.Dir(rel) == "invalid" {
			violations = lineViolations(string(content))
		} else if violations, err = yamlViolations(content); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		for _, v := range violations {
			t.Errorf("%s:%s", rel, v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func Test_yamlViolations(t *testing.T) {
	type args struct {
		content string
	}
	tests := []struct {
		name string
		args args
		// want are parts of the violations in their order.
		want []string
	}{
		{
			name: "quotes",
			args: args{"---\nhost: 'db.example.invalid'\nkey: \"plain:fake-x\"\n"},
		},
		{
			name: "quotes with violations",
			args: args{"---\nhost: \"db.example.com\"\npassword: 'plain:real'\n"},
			want: []string{`2: host "db.example.com"`, "3: secret of password"},
		},
		{
			name: "flow style",
			args: args{"---\nt: {host: db.example.invalid, key: 'plain:fake-x'}\n"},
		},
		{
			name: "flow style with violations",
			args: args{"---\nt: {host: db.example.com, key: 'plain:real'}\n"},
			want: []string{`2: host "db.example.com"`, "2: secret of key"},
		},
		{
			name: "block scalar",
			args: args{"---\nkey: |-\n  plain:fake-x\n"},
		},
		{
			name: "block scalar with a violation",
			args: args{"---\nkey: |-\n  plain:real\n"},
			want: []string{"2: secret of key"},
		},
		{
			name: "IPv6 host with a port",
			args: args{"---\nurl: 'sftp://u@[::1]:22/x'\n"},
			want: []string{`2: URL host "::1" of url`},
		},
		{
			name: "user information with @",
			args: args{"---\nurl: 'sftp://u:fa@ke@db.example.invalid/x'\n"},
		},
		{
			name: "user information with @ and a violation",
			args: args{"---\nurl: 'sftp://u:p@ss@evil.example.com/x'\n"},
			want: []string{`2: URL host "evil.example.com" of url`},
		},
		{
			name: "buckets",
			args: args{"---\nurl: 'b2://bucket/x'\nother: 'S3://bucket/x'\n"},
		},
		{
			name: "references",
			args: args{"---\npassword: 'env:REAL'\ntoken: 'file:/run/real'\n" +
				"key: 'vault:a/b/real'\n"},
		},
		{
			name: "key in auth",
			args: args{"---\nauth: ['key', 'password']\nlist:\n  - key\n"},
		},
		{
			name: "keys",
			args: args{"---\nkeys:\n  b2_id: fake-id\n  b2_key: 'plain:real'\n"},
			want: []string{"4: secret of b2_key"},
		},
		{
			name: "empty secrets",
			args: args{"---\npassword: ''\nkey: null\ntoken:\n"},
		},
		{
			name: "alias",
			args: args{"---\na: &a {host: db.example.com}\nb: *a\n"},
			want: []string{`2: host "db.example.com"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := yamlViolations([]byte(tt.args.content))
			if err != nil {
				t.Fatalf("yamlViolations() error = %v", err)
			}
			checkViolations(t, "yamlViolations()", got, tt.want)
		})
	}
}

func Test_lineViolations(t *testing.T) {
	type args struct {
		content string
	}
	tests := []struct {
		name string
		args args
		// want are parts of the violations in their order.
		want []string
	}{
		{
			name: "three forms of the value",
			args: args{"password: 'plain:fake-a'\ntoken: \"fake-b\"\nkey: fake-c # comment\n"},
		},
		{
			name: "three forms of the value with violations",
			args: args{"password: 'plain:real'\ntoken: \"real\"\nkey: real # comment\n"},
			want: []string{"1: secret of password", "2: secret of token", "3: secret of key"},
		},
		{
			name: "comment",
			args: args{"  # password: real\n"},
		},
		{
			name: "values under keys",
			args: args{"keys:\n  b2_id: real\n  '': 'env:X'\nother: real\n"},
			want: []string{"2: secret of b2_id"},
		},
		{
			name: "values after keys on its line",
			args: args{"keys: {a: real, b: 'fake-b'}\n"},
			want: []string{"1: secret of a"},
		},
		{
			name: "keys inside a flow collection",
			args: args{"storage: {url: 'b2://x/y', keys: {b2_key: real}}\nother: real\n"},
			want: []string{"1: secret of b2_key"},
		},
		{
			name: "quoted keys",
			args: args{"storage: {\"keys\": {b2_key: real}}\nkeys2: {'keys': {a: real}}\n"},
			want: []string{"1: secret of b2_key", "2: secret of a"},
		},
		{
			name: "pairs after the value of keys",
			args: args{"storage: {keys: {b2_key: 'env:X', b: 'fake-}'}, name: backup}\n"},
		},
		{
			name: "block scalar",
			args: args{"key: |-\n  plain:fake-x\npassword: >\n  fake-y\n"},
		},
		{
			name: "host and URL",
			args: args{"  - {name: x, host: db.example.com}\nurl: sftp://u@db.example.com/x\n"},
			want: []string{`1: host "db.example.com"`, `2: URL host "db.example.com" of url`},
		},
		{
			name: "key in auth",
			args: args{"auth: ['key']\nauth:\n  - key\n"},
		},
		{
			name: "empty secrets",
			args: args{"password: ''\nkey: null\ntoken: ~\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkViolations(t, "lineViolations()", lineViolations(tt.args.content), tt.want)
		})
	}
}

// checkViolations checks that every violation contains its part of want.
func checkViolations(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %q, want %d violations", name, got, len(want))
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("%s violation %d = %q, want …%s…", name, i, got[i], w)
		}
	}
}
