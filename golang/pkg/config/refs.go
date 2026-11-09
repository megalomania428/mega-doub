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
// value, which fn gets as a pointer to a copy stored back after the call.
func WalkRefs(c *Config, fn func(path string, ref *secret.Ref)) {
	if c == nil {
		return
	}
	walkRefs(reflect.ValueOf(c).Elem(), "", fn)
}

// walkRefs walks the addressable value v at path. The templates are not walked: a YAML
// node has cyclic pointers.
func walkRefs(v reflect.Value, path string, fn func(path string, ref *secret.Ref)) {
	switch v.Type() {
	case refType:
		if ref, _ := v.Addr().Interface().(*secret.Ref); *ref != "" {
			fn(path, ref)
		}
		return
	case nodeType:
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			walkRefs(v.Elem(), path, fn)
		}
	case reflect.Interface:
		walkInterface(v, path, fn)
	case reflect.Struct:
		walkFields(v, path, fn)
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkRefs(v.Index(i), path+"["+strconv.Itoa(i)+"]", fn)
		}
	case reflect.Map:
		walkMap(v, path, fn)
	}
}

// walkInterface walks the content of the interface v. A pointer is walked in place, any
// other value is not addressable and is walked as a copy stored back.
func walkInterface(v reflect.Value, path string, fn func(path string, ref *secret.Ref)) {
	if v.IsNil() {
		return
	}
	elem := v.Elem()
	if elem.Kind() == reflect.Pointer {
		walkRefs(elem, path, fn)
		return
	}
	cp := reflect.New(elem.Type()).Elem()
	cp.Set(elem)
	walkRefs(cp, path, fn)
	v.Set(cp)
}

// walkFields walks the exported fields of the structure v with the keys yaml/v4 gives
// them: the name of the yaml tag, the lowercased field name without one, the path of
// the structure for an inline field. A field tagged "-" is skipped.
func walkFields(v reflect.Value, path string, fn func(path string, ref *secret.Ref)) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if f.PkgPath != "" || tag == "-" {
			continue
		}
		name, flags, _ := strings.Cut(tag, ",")
		if slices.Contains(strings.Split(flags, ","), "inline") {
			walkRefs(v.Field(i), path, fn)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		walkRefs(v.Field(i), joinKey(path, name), fn)
	}
}

// walkMap walks the values of the mapping v in the byte order of the keys. A value
// is not addressable, so it is walked as a copy stored back.
func walkMap(v reflect.Value, path string, fn func(path string, ref *secret.Ref)) {
	keys := v.MapKeys()
	slices.SortFunc(keys, func(a, b reflect.Value) int {
		return strings.Compare(mapKeyName(a), mapKeyName(b))
	})
	for _, k := range keys {
		cp := reflect.New(v.Type().Elem()).Elem()
		cp.Set(v.MapIndex(k))
		walkRefs(cp, joinKey(path, mapKeyName(k)), fn)
		v.SetMapIndex(k, cp)
	}
}

// mapKeyName returns the key of a mapping as a string.
func mapKeyName(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	return fmt.Sprint(k.Interface())
}
