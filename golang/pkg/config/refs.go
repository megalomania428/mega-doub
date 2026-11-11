// Package config is documented in config.go.
package config

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/pkg/secret"
)

var (
	refType  = reflect.TypeFor[secret.Ref]()
	nodeType = reflect.TypeFor[yaml.Node]()
)

// WalkRefs calls fn for every non-empty secret reference of the configuration with
// its path: the fields in the order of the structures, the keys of the mappings sorted
// by their bytes. A change through the pointer is kept, also for a mapping or interface
// value, which fn gets as a pointer to a copy stored back after the call when fn changed
// a reference in it. A walk that changes no reference does not write the configuration,
// so such walks may run at the same time.
func WalkRefs(c *Config, fn func(path string, ref *secret.Ref)) {
	if c == nil {
		return
	}
	walkRefs(reflect.ValueOf(c).Elem(), "", fn)
}

// RefPath is a non-empty secret reference with its path in the configuration.
type RefPath struct {
	Path string
	Ref  secret.Ref
}

// Refs returns the non-empty secret references of the configuration with their paths in
// the order of WalkRefs without writing the configuration. vault.token is left out: only
// the Vault client needs it.
func Refs(c *Config) []RefPath {
	var refs []RefPath
	WalkRefs(c, func(path string, ref *secret.Ref) {
		if path != "vault.token" {
			refs = append(refs, RefPath{Path: path, Ref: *ref})
		}
	})
	return refs
}

// walkRefs walks the addressable value v at path and reports whether fn changed a
// reference in it. The templates are not walked: a YAML node has cyclic pointers.
func walkRefs(v reflect.Value, path string, fn func(path string, ref *secret.Ref)) bool {
	switch v.Type() {
	case refType:
		ref, _ := v.Addr().Interface().(*secret.Ref)
		if *ref == "" {
			return false
		}
		old := *ref
		fn(path, ref)
		return *ref != old
	case nodeType:
		return false
	}
	switch v.Kind() {
	case reflect.Pointer:
		return !v.IsNil() && walkRefs(v.Elem(), path, fn)
	case reflect.Interface:
		return walkInterface(v, path, fn)
	case reflect.Struct:
		return walkFields(v, path, fn)
	case reflect.Slice, reflect.Array:
		changed := false
		for i := range v.Len() {
			changed = walkRefs(v.Index(i), path+"["+strconv.Itoa(i)+"]", fn) || changed
		}
		return changed
	case reflect.Map:
		return walkMap(v, path, fn)
	}
	return false
}

// walkInterface walks the content of the interface v and reports whether fn changed a
// reference in it. A pointer is walked in place, any other value is not addressable and
// is walked as a copy stored back only when fn changed a reference in it.
func walkInterface(v reflect.Value, path string,
	fn func(path string, ref *secret.Ref)) bool {
	if v.IsNil() {
		return false
	}
	elem := v.Elem()
	if elem.Kind() == reflect.Pointer {
		return walkRefs(elem, path, fn)
	}
	cp := reflect.New(elem.Type()).Elem()
	cp.Set(elem)
	if !walkRefs(cp, path, fn) {
		return false
	}
	v.Set(cp)
	return true
}

// walkFields walks the exported fields of the structure v with the keys yaml/v4 gives
// them: the name of the yaml tag, the lowercased field name without one, the path of
// the structure for an inline field. A field tagged "-" is skipped. It reports whether
// fn changed a reference in them.
func walkFields(v reflect.Value, path string,
	fn func(path string, ref *secret.Ref)) bool {
	t := v.Type()
	changed := false
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if f.PkgPath != "" || tag == "-" {
			continue
		}
		name, flags, _ := strings.Cut(tag, ",")
		if slices.Contains(strings.Split(flags, ","), "inline") {
			changed = walkRefs(v.Field(i), path, fn) || changed
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		changed = walkRefs(v.Field(i), joinKey(path, name), fn) || changed
	}
	return changed
}

// walkMap walks the values of the mapping v in the byte order of the keys and reports
// whether fn changed a reference in them. A value is not addressable, so it is walked
// as a copy stored back only when fn changed a reference in it.
func walkMap(v reflect.Value, path string, fn func(path string, ref *secret.Ref)) bool {
	keys := v.MapKeys()
	slices.SortFunc(keys, func(a, b reflect.Value) int {
		return strings.Compare(mapKeyName(a), mapKeyName(b))
	})
	changed := false
	for _, k := range keys {
		cp := reflect.New(v.Type().Elem()).Elem()
		cp.Set(v.MapIndex(k))
		if walkRefs(cp, joinKey(path, mapKeyName(k)), fn) {
			v.SetMapIndex(k, cp)
			changed = true
		}
	}
	return changed
}

// mapKeyName returns the key of a mapping as a string.
func mapKeyName(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	return fmt.Sprint(k.Interface())
}
