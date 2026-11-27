//go:build integration

package integration

// cspell:ignore doub rustfs

import (
	"encoding/json"
	"reflect"
	"testing"
)

func Test_parseRecords(t *testing.T) {
	command := `{"time":"t","level":"INFO","msg":"command","command":"backup"}`
	completed := `{"time":"t","level":"INFO","msg":"backup completed","revision":2}`
	output := command + "\n" +
		"[=====>      ] 42% 12 MiB 1s\n" +
		"  " + completed + "  \n" +
		"{not json\n" +
		"\n" +
		"text after the records"
	type args struct {
		out []byte
	}
	tests := []struct {
		name string
		args args
		want records
	}{
		{
			name: "records among the progress lines",
			args: args{out: []byte(output)},
			want: records{
				{msg: "command", raw: json.RawMessage(command)},
				{msg: "backup completed", raw: json.RawMessage(completed)},
			},
		},
		{
			name: "no records",
			args: args{out: []byte("[=====>      ] 42% 12 MiB 1s\n")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRecords(tt.args.out); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseRecords() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_records_decode(t *testing.T) {
	recs := records{
		{msg: "backup completed", raw: json.RawMessage(`{"revision":1,"new_chunks":5}`)},
		{msg: "command", raw: json.RawMessage(`{"command":"backup"}`)},
		{msg: "backup completed", raw: json.RawMessage(`{"revision":2,"new_chunks":3}`)},
	}
	type args struct {
		msg string
		v   any
	}
	tests := []struct {
		name    string
		r       records
		args    args
		want    any
		wantErr bool
	}{
		{
			name: "last record of the message",
			r:    recs,
			args: args{msg: "backup completed", v: &backupCompleted{}},
			want: &backupCompleted{Revision: 2, NewChunks: 3},
		},
		{
			name:    "no record of the message",
			r:       recs,
			args:    args{msg: "prune completed", v: &pruneCompleted{}},
			want:    &pruneCompleted{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.r.decode(tt.args.msg, tt.args.v); (err != nil) != tt.wantErr {
				t.Errorf("records.decode() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(tt.args.v, tt.want) {
				t.Errorf("records.decode() v = %v, want %v", tt.args.v, tt.want)
			}
		})
	}
}

func Test_storageConfig_yaml(t *testing.T) {
	plain := "---\n" +
		"global:\n" +
		"  copy_dir: \"/work/copy\"\n" +
		"  state_dir: \"/work/state\"\n" +
		"  progress: []\n" +
		"storage:\n" +
		"  url: \"/storage\"\n" +
		"  snapshot_id: \"it-local-1\"\n" +
		"  password: \"env:MEGA_DOUB_IT_PASSWORD\"\n" +
		"prune:\n" +
		"  keep: ['1:7']\n"
	withKeys := "---\n" +
		"global:\n" +
		"  copy_dir: \"/work/copy\"\n" +
		"  state_dir: \"/work/state\"\n" +
		"storage:\n" +
		"  url: \"minio://region@127.0.0.1:9000/bucket/path\"\n" +
		"  snapshot_id: \"it-rustfs-1\"\n" +
		"  password: \"env:MEGA_DOUB_IT_PASSWORD\"\n" +
		"  keys:\n" +
		"    s3_id: \"env:MEGA_DOUB_IT_KEY_S3_ID\"\n" +
		"    s3_secret: \"env:MEGA_DOUB_IT_KEY_S3_SECRET\"\n" +
		"prune:\n" +
		"  keep: ['1:7']\n"
	type fields struct {
		dir        string
		url        string
		snapshotID string
		keys       []string
		demo       bool
	}
	rustfs := fields{dir: "/work", url: "minio://region@127.0.0.1:9000/bucket/path",
		snapshotID: "it-rustfs-1", keys: []string{"s3_id", "s3_secret"}, demo: true}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name:   "local storage without progress",
			fields: fields{dir: "/work", url: "/storage", snapshotID: "it-local-1"},
			want:   plain,
		},
		{
			name:   "keys and the progress bars of the demo mode",
			fields: rustfs,
			want:   withKeys,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := storageConfig{
				dir:        tt.fields.dir,
				url:        tt.fields.url,
				snapshotID: tt.fields.snapshotID,
				keys:       tt.fields.keys,
				demo:       tt.fields.demo,
			}
			if got := c.yaml(); got != tt.want {
				t.Errorf("storageConfig.yaml() = %v, want %v", got, tt.want)
			}
		})
	}
}
