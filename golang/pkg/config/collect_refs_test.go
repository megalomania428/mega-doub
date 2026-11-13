// cspell:ignore doub

package config

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/megalomania428/mega-doub/pkg/secret"
)

func TestRefs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "refs.yaml")
	data := "---\nvault:\n  address: 'https://vault.example.invalid'\n" +
		"  token: 'env:MEGA_DOUB_REFS_TOKEN'\nstorage:\n  url: 'b2://refs-bucket/prefix'\n" +
		"  snapshot_id: refs-main\n  password: 'env:MEGA_DOUB_REFS_A'\n  keys:\n" +
		"    b2_key: 'file:/run/secrets/b2-key'\n    b2_id: 'plain:fake-refs-id'\n"
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	type args struct {
		c *Config
	}
	tests := []struct {
		name string
		args args
		want []RefPath
	}{
		{name: "nil configuration", args: args{c: nil}},
		{
			name: "without vault.token in the order of the walk",
			args: args{c: loaded.Config},
			want: []RefPath{
				{Path: "storage.password", Ref: "env:MEGA_DOUB_REFS_A"},
				{Path: "storage.keys.b2_id", Ref: "plain:fake-refs-id"},
				{Path: "storage.keys.b2_key", Ref: "file:/run/secrets/b2-key"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The walks only read the configuration, so they run at the same time: the race
			// detector reports a write to the mapping of storage.keys.
			got := make([][]RefPath, 8)
			var wg sync.WaitGroup
			for i := range got {
				wg.Go(func() { got[i] = Refs(tt.args.c) })
			}
			wg.Wait()
			for _, refs := range got {
				if !reflect.DeepEqual(refs, tt.want) {
					t.Errorf("Refs() = %v, want %v", refs, tt.want)
				}
			}
		})
	}
}

func Test_walkRefs_unchanged(t *testing.T) {
	// source is a value in an interface with fields that are not equal to themselves.
	type source struct {
		Ratio float64
		Hook  func()
		Token secret.Ref
	}
	type tree struct {
		Sources []any
		Keys    map[string]secret.Ref
	}
	type args struct {
		v reflect.Value
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "interface and mapping values",
			args: args{v: reflect.ValueOf(&tree{
				Sources: []any{source{Ratio: math.NaN(), Hook: func() {},
					Token: "env:MEGA_DOUB_WALK_A"}},
				Keys: map[string]secret.Ref{"b2_id": "plain:fake-walk-id"},
			}).Elem()},
			want: []string{"sources[0].token", "keys.b2_id"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A walk that changes nothing writes nothing, so the walks run at the same time:
			// the race detector reports a write to the slice or the mapping.
			got := make([][]string, 8)
			changed := make([]bool, len(got))
			var wg sync.WaitGroup
			for i := range got {
				wg.Go(func() {
					changed[i] = walkRefs(tt.args.v, "", func(path string, _ *secret.Ref) {
						got[i] = append(got[i], path)
					})
				})
			}
			wg.Wait()
			for i := range got {
				if changed[i] || !reflect.DeepEqual(got[i], tt.want) {
					t.Errorf("walkRefs() = %v with visits %v, want false with %v", changed[i],
						got[i], tt.want)
				}
			}
		})
	}
}
