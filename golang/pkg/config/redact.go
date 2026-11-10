// Package config is documented in config.go.
package config

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/pkg/secret"
)

// masked replaces a hidden value in the output.
const masked = "***"

// Show returns the configuration in the safe form of Redact as one YAML document that
// starts with --- and indents the lists, as yamllint wants. The output must load
// again, so it is an error if it is larger than the limit of a configuration file.
func Show(c *Config) ([]byte, error) {
	out, err := yaml.Dump(Redact(c), yaml.WithExplicitStart(),
		yaml.WithCompactSeqIndent(false), yaml.WithIndent(2), yaml.WithLineWidth(-1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxFileSize {
		return nil, fmt.Errorf("output is larger than %d bytes", maxFileSize)
	}
	return out, nil
}

// Redact returns a deep copy of the configuration that is safe to print, c is not
// changed. The copy has no templates, a local target has none of the ssh fields that
// got their defaults on loading, a non-empty plain: reference and a value without a
// prefix become plain:***, and storage.url and vault.address pass SanitizeURL. The
// env:, file: and vault: references are kept: they are not values.
func Redact(c *Config) *Config {
	if c == nil {
		return nil
	}
	safe, _ := deepCopy(reflect.ValueOf(c)).Interface().(*Config)
	safe.Templates = yaml.Node{}
	for i := range safe.Targets {
		if t := &safe.Targets[i].TargetSettings; t.Transport == "local" {
			t.Host, t.User, t.KnownHosts = "", "", ""
			t.Port, t.Auth, t.ConnectTimeout = 0, nil, 0
			t.Key, t.KeyPassphrase, t.Password = "", "", ""
		}
	}
	WalkRefs(safe, func(_ string, ref *secret.Ref) {
		*ref = secret.Ref(ref.String())
	})
	safe.Storage.URL = SanitizeURL(safe.Storage.URL)
	safe.Vault.Address = SanitizeURL(safe.Vault.Address)
	return safe
}

// SanitizeURL returns raw with the password of its user information replaced by ***,
// an empty password included, and the rest kept as written: url.Parse only checks the
// string, since URL.String escapes *** and spaces. The authority starts only at a ://
// right after the scheme and ends before the first /, ? or #, the user information
// ends at its last @ and the password follows its first :. A string with an opaque
// part, such as scheme:user:pass@host, becomes the scheme as written with :***, and a
// string that url.Parse rejects becomes ***. The query and the fragment are not
// masked.
func SanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return masked
	}
	i := strings.Index(raw, ":")
	if u.Opaque != "" {
		return raw[:i+1] + masked
	}
	if i < 0 || !strings.HasPrefix(raw[i:], "://") {
		return raw
	}
	start := i + len("://")
	end := len(raw)
	if j := strings.IndexAny(raw[start:], "/?#"); j >= 0 {
		end = start + j
	}
	at := strings.LastIndex(raw[start:end], "@")
	if at < 0 {
		return raw
	}
	colon := strings.Index(raw[start:start+at], ":")
	if colon < 0 {
		return raw
	}
	return raw[:start+colon+1] + masked + raw[start+at:]
}

// deepCopy returns a copy of v that shares no pointer, slice or map with it. A value of
// an interface is copied through reflect.New of its concrete type, a yaml.Node is left
// zero: it may hold cycles. Unexported fields of a structure are copied shallowly.
func deepCopy(v reflect.Value) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			p := reflect.New(v.Type().Elem())
			p.Elem().Set(deepCopy(v.Elem()))
			out.Set(p)
		}
	case reflect.Interface:
		if !v.IsNil() {
			out.Set(deepCopy(v.Elem()))
		}
	case reflect.Struct:
		if v.Type() == nodeType {
			return out
		}
		out.Set(v)
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				out.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
	case reflect.Slice:
		if !v.IsNil() {
			out.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
			for i := range v.Len() {
				out.Index(i).Set(deepCopy(v.Index(i)))
			}
		}
	case reflect.Array:
		for i := range v.Len() {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
	case reflect.Map:
		if !v.IsNil() {
			out.Set(reflect.MakeMapWithSize(v.Type(), v.Len()))
			for it := v.MapRange(); it.Next(); {
				out.SetMapIndex(deepCopy(it.Key()), deepCopy(it.Value()))
			}
		}
	default:
		out.Set(v)
	}
	return out
}
