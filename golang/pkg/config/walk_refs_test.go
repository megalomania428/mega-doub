// cspell:ignore testsource

package config_test

import (
	"reflect"
	"testing"

	"github.com/megalomania428/mega-doub/internal/testsource"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// refVisit is a reference the walk passes with its path.
type refVisit struct {
	path string
	ref  secret.Ref
}

func TestWalkRefs(t *testing.T) {
	loaded, err := config.Load("../../testdata/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	full := loaded.Config
	// The walk would not end in the cycle of the templates.
	full.Templates.Alias = &full.Templates
	type args struct {
		c *config.Config
	}
	tests := []struct {
		name string
		args args
		want []refVisit
	}{
		{
			name: "every secret field of full.yaml",
			args: args{c: full},
			want: []refVisit{
				{"vault.token", "file:/run/secrets/fake-vault-token"},
				{"storage.password", "vault:depot/mega-doub/full/storage-password"},
				{"storage.keys.b2_id", "vault:depot/mega-doub/full/b2-id"},
				{"storage.keys.b2_key", "plain:fake-b2-key"},
				{"targets[0].key", "file:/root/.ssh/fake_ed25519"},
				{"targets[0].key_passphrase", "env:MEGA_DOUB_FULL_PASSPHRASE"},
				{"targets[0].password", "fake-password"},
				{"targets[0].sources[0].token", "plain:fake-source-token"},
			},
		},
		{
			name: "no configuration",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []refVisit
			config.WalkRefs(tt.args.c, func(path string, ref *secret.Ref) {
				got = append(got, refVisit{path, *ref})
				*ref = secret.Ref("plain:changed-" + path)
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("WalkRefs() visits = %v, want %v", got, tt.want)
			}
		})
	}
	// The changes are stored in the configuration, also in a mapping and an interface.
	source, _ := full.Targets[0].Sources[0].(testsource.Source)
	stored := map[string]secret.Ref{
		"vault.token":                 full.Vault.Token,
		"storage.keys.b2_key":         full.Storage.Keys["b2_key"],
		"targets[0].sources[0].token": source.Token,
	}
	for path, got := range stored {
		if want := secret.Ref("plain:changed-" + path); got != want {
			t.Errorf("WalkRefs() stored %q at %s, want %q", string(got), path, string(want))
		}
	}
}
