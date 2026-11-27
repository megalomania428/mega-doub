//go:build integration

package integration

// cspell:ignore clis doub duplicacy

import (
	"bufio"
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// pauseConfig is the configuration of pauseScenario as the first block shows it.
var pauseConfig = []string{
	"---",
	"global:",
	`  copy_dir: "/work/copy"`,
	`  state_dir: "/work/state"`,
	"  progress: []",
	"storage:",
	`  url: "/storage"`,
	`  snapshot_id: "it-local-1"`,
	`  password: "env:MEGA_DOUB_IT_PASSWORD"`,
	"prune:",
	"  keep: ['1:7']",
}

// pausePaths are the paths of pauseScenario in every block.
var pausePaths = []string{
	"work: /work",
	"copy_dir: /work/copy",
	"state_dir: /work/state",
	"config: /work/mega-doub.yaml",
	"storage.url: /storage",
}

// pauseCommand is the only command of pauseScenario since the last pause.
const pauseCommand = "cd /work && MEGA_DOUB_IT_PASSWORD=*** /bin/mega-doub backup"

// pauseScenario returns a scenario of a local storage with the data that the blocks of
// the pauses read, and one command since the last pause.
func pauseScenario(journal ...string) *scenario {
	return &scenario{
		cfg:    storageConfig{dir: "/work", url: "/storage", snapshotID: "it-local-1"},
		access: access{snapshotID: "it-local-1"},
		mega:   &megaDoub{bin: "/bin/mega-doub", config: "/work/mega-doub.yaml"},
		clis: []duplicacyCLI{{name: upstreamName, bin: "/bin/upstream"},
			{name: "patched-3.3.0-p003", bin: "/bin/patched"}},
		tree: &tree{root: "/work/copy", journal: journal},
		backups: map[int]backupCompleted{
			1: {Revision: 1, Files: 4321, Bytes: 77123456, NewChunks: 123, Duration: "3.2s"},
			3: {Revision: 3, Files: 4400, Bytes: 78000000, NewChunks: 9, Duration: "1.5s"},
		},
		commands: &commandLog{lines: []string{pauseCommand}},
	}
}

func Test_pauseRequested(t *testing.T) {
	type args struct {
		value string
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{name: "empty", args: args{value: ""}, want: false},
		{name: "one", args: args{value: "1"}, want: true},
		{name: "another number", args: args{value: "2"}, wantErr: true},
		{name: "word", args: args{value: "true"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pauseRequested(tt.args.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("pauseRequested() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				want := "MEGA_DOUB_IT_PAUSE=\"" + tt.args.value + "\", want 1 or an empty"
				if !strings.Contains(err.Error(), want) {
					t.Errorf("pauseRequested() error = %v, want it to contain %q", err, want)
				}
				return
			}
			if got != tt.want {
				t.Errorf("pauseRequested() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_pauseOff(t *testing.T) {
	type args struct {
		githubActions string
		ttyErr        error
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{name: "terminal opens", args: args{}, want: ""},
		{
			name: "GitHub Actions",
			args: args{githubActions: "true"},
			want: "GITHUB_ACTIONS=true",
		},
		{
			name: "no terminal",
			args: args{ttyErr: errors.New("open /dev/tty: no such device or address")},
			want: "open /dev/tty: no such device or address",
		},
		{
			name: "GitHub Actions and no terminal",
			args: args{githubActions: "true", ttyErr: errors.New("no terminal")},
			want: "GITHUB_ACTIONS=true",
		},
		{name: "another value of GITHUB_ACTIONS", args: args{githubActions: "false"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pauseOff(tt.args.githubActions, tt.args.ttyErr); got != tt.want {
				t.Errorf("pauseOff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_pauser_wait(t *testing.T) {
	const hint = "next and Enter – continue, q and Enter – stop\n"
	type fields struct {
		in string
	}
	tests := []struct {
		name    string
		fields  fields
		want    bool
		wantOut string
		wantErr bool
	}{
		{name: "next", fields: fields{in: "next\n"}, want: false, wantOut: hint},
		{name: "q", fields: fields{in: "q\n"}, want: true, wantOut: hint},
		{name: "q without a line end", fields: fields{in: "q"}, want: true, wantOut: hint},
		{
			name:    "empty lines and next",
			fields:  fields{in: "\n\n\nnext\n"},
			want:    false,
			wantOut: hint,
		},
		{
			name:    "empty lines and q",
			fields:  fields{in: "\n\n\nq\n"},
			want:    true,
			wantOut: hint,
		},
		{
			name:    "unknown word and next",
			fields:  fields{in: "x\nnext\n"},
			want:    false,
			wantOut: hint + hint,
		},
		{
			name:    "next with spaces",
			fields:  fields{in: " \t next \n"},
			want:    false,
			wantOut: hint,
		},
		{
			name:    "unknown keys and q",
			fields:  fields{in: "x\nQ\nq\n"},
			want:    true,
			wantOut: hint + hint + hint,
		},
		{name: "no input", fields: fields{in: ""}, wantOut: hint, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			p := &pauser{
				in:  bufio.NewReader(strings.NewReader(tt.fields.in)),
				out: &out,
			}
			got, err := p.wait()
			if (err != nil) != tt.wantErr {
				t.Fatalf("pauser.wait() error = %v, wantErr %v", err, tt.wantErr)
			}
			if out.String() != tt.wantOut {
				t.Errorf("pauser.wait() printed %q, want %q", out.String(), tt.wantOut)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("pauser.wait() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_commandLog_add(t *testing.T) {
	type fields struct {
		lines []string
	}
	type args struct {
		line string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []string
	}{
		{
			name: "empty log",
			args: args{line: "cd /a && /bin/x"},
			want: []string{"cd /a && /bin/x"},
		},
		{
			name:   "after another line",
			fields: fields{lines: []string{"cd /a && /bin/x"}},
			args:   args{line: "cd /b && /bin/y"},
			want:   []string{"cd /a && /bin/x", "cd /b && /bin/y"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &commandLog{
				lines: tt.fields.lines,
			}
			l.add(tt.args.line)
			if !reflect.DeepEqual(l.lines, tt.want) {
				t.Errorf("commandLog.add() lines = %v, want %v", l.lines, tt.want)
			}
		})
	}
}

func Test_commandLog_take(t *testing.T) {
	type fields struct {
		lines []string
	}
	tests := []struct {
		name   string
		fields fields
		want   []string
	}{
		{
			name:   "two lines",
			fields: fields{lines: []string{"cd /a && /bin/x", "cd /b && /bin/y"}},
			want:   []string{"cd /a && /bin/x", "cd /b && /bin/y"},
		},
		{name: "empty log", fields: fields{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &commandLog{
				lines: tt.fields.lines,
			}
			if got := l.take(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("commandLog.take() = %v, want %v", got, tt.want)
			}
			if l.lines != nil {
				t.Errorf("commandLog.take() left the lines %v", l.lines)
			}
		})
	}
}

func Test_shellLine_String(t *testing.T) {
	type fields struct {
		dir  string
		vars []string
		bin  string
		args []string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "values are masked, names stay",
			fields: fields{
				dir:  "/tmp/restore",
				vars: []string{"DUPLICACY_PASSWORD=fake-secret", "DUPLICACY_S3_ID=a=b", "EMPTY="},
				bin:  "/tools/duplicacy",
				args: []string{"init", "-e", "it-local-1", "/storage"},
			},
			want: "cd /tmp/restore && DUPLICACY_PASSWORD=*** DUPLICACY_S3_ID=*** EMPTY=*** " +
				"/tools/duplicacy init -e it-local-1 /storage",
		},
		{
			name: "no variables",
			fields: fields{
				dir:  "/work",
				bin:  "/bin/mega-doub",
				args: []string{"--config", "/work/mega-doub.yaml", "--log-format", "json", "init"},
			},
			want: "cd /work && /bin/mega-doub --config /work/mega-doub.yaml " +
				"--log-format json init",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := shellLine{
				dir:  tt.fields.dir,
				vars: tt.fields.vars,
				bin:  tt.fields.bin,
				args: tt.fields.args,
			}
			got := l.String()
			if got != tt.want {
				t.Errorf("shellLine.String() = %v, want %v", got, tt.want)
			}
			for _, variable := range tt.fields.vars {
				_, value, _ := strings.Cut(variable, "=")
				if value != "" && strings.Contains(got, value) {
					t.Errorf("shellLine.String() = %v shows the value %q", got, value)
				}
			}
		})
	}
}

func Test_pauseBlock_String(t *testing.T) {
	type fields struct {
		number   int
		done     []string
		paths    []string
		commands []string
	}
	rest := "Commands since the last pause:\n  cd /work && /bin/x\n" +
		"Paths:\n  work: /work\nDone:\n"
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "after init",
			fields: fields{number: 1, done: []string{"init: storage initialized"},
				paths: []string{"work: /work"}, commands: []string{"cd /work && /bin/x"}},
			want: "\n== 1/5 init done ==\n" + rest + "  init: storage initialized\n",
		},
		{
			name: "after the backup of r1",
			fields: fields{number: 2, done: []string{"backup: revision 1"},
				paths: []string{"work: /work"}, commands: []string{"cd /work && /bin/x"}},
			want: "\n== 2/5 r1 backup done ==\n" + rest + "  backup: revision 1\n",
		},
		{
			name: "after the backup of r3",
			fields: fields{number: 3, done: []string{"backup: revision 3"},
				paths: []string{"work: /work"}, commands: []string{"cd /work && /bin/x"}},
			want: "\n== 3/5 r3 backup done ==\n" + rest + "  backup: revision 3\n",
		},
		{
			name: "after the exclusive prune",
			fields: fields{number: 4, done: []string{"removed revisions: 2"},
				paths: []string{"work: /work"}, commands: []string{"cd /work && /bin/x"}},
			want: "\n== 4/5 exclusive prune done ==\n" + rest + "  removed revisions: 2\n",
		},
		{
			name: "after the restore of r4",
			fields: fields{number: 5, done: []string{"restored revision 4"},
				paths: []string{"work: /work"}, commands: []string{"cd /work && /bin/x"}},
			want: "\n== 5/5 r4 upstream restore done ==\n" + rest + "  restored revision 4\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := pauseBlock{
				number:   tt.fields.number,
				done:     tt.fields.done,
				paths:    tt.fields.paths,
				commands: tt.fields.commands,
			}
			if got := b.String(); got != tt.want {
				t.Errorf("pauseBlock.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_scenario_block(t *testing.T) {
	type args struct {
		number int
	}
	tests := []struct {
		name string
		s    *scenario
		args args
		want pauseBlock
	}{
		{
			name: "paths and commands",
			s:    pauseScenario(),
			args: args{number: pauseExclusive},
			want: pauseBlock{number: 4, paths: pausePaths, commands: []string{pauseCommand}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.block(tt.args.number); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scenario.block() = %v, want %v", got, tt.want)
			}
			if tt.s.commands.lines != nil {
				t.Errorf("scenario.block() left the commands %v", tt.s.commands.lines)
			}
		})
	}
}

func Test_scenario_initBlock(t *testing.T) {
	type args struct {
		recs records
	}
	done := []string{
		"snapshot_id: it-local-1",
		"mega-doub: /bin/mega-doub",
		"upstream-3.3.1: /bin/upstream",
		"patched-3.3.0-p003: /bin/patched",
		"init: storage initialized",
		"config:",
	}
	for _, line := range pauseConfig {
		done = append(done, "  "+line)
	}
	noRecords := append([]string{}, done...)
	noRecords[4] = "init: no log records"
	tests := []struct {
		name string
		s    *scenario
		args args
		want pauseBlock
	}{
		{
			name: "the last record is the result of init",
			s:    pauseScenario(),
			args: args{recs: records{{msg: "command"}, {msg: "storage initialized"}}},
			want: pauseBlock{number: 1, done: done, paths: pausePaths,
				commands: []string{pauseCommand}},
		},
		{
			name: "no records",
			s:    pauseScenario(),
			want: pauseBlock{number: 1, done: noRecords, paths: pausePaths,
				commands: []string{pauseCommand}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.initBlock(tt.args.recs); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scenario.initBlock() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_scenario_backupBlock(t *testing.T) {
	type args struct {
		n int
	}
	r1 := []string{"r1: git archive HEAD~10 (a1b2c3d)", "r1: synthetic/empty.txt: written"}
	r2r3 := []string{"r2: synthetic/added-r2.txt: added", "r3: synthetic/deep: removed"}
	tests := []struct {
		name string
		s    *scenario
		args args
		want pauseBlock
	}{
		{
			name: "r1 with its edits",
			s:    pauseScenario(r1...),
			args: args{n: 1},
			want: pauseBlock{
				number: 2,
				done: []string{
					"edits:",
					"  r1: git archive HEAD~10 (a1b2c3d)",
					"  r1: synthetic/empty.txt: written",
					"backup: revision 1, 4321 files, 77123456 bytes, 123 new chunks, 3.2s",
				},
				paths:    pausePaths,
				commands: []string{pauseCommand},
			},
		},
		{
			name: "r3 with the edits of r2 and r3",
			s:    pauseScenario(r2r3...),
			args: args{n: 3},
			want: pauseBlock{
				number: 3,
				done: []string{
					"edits:",
					"  r2: synthetic/added-r2.txt: added",
					"  r3: synthetic/deep: removed",
					"backup: revision 3, 4400 files, 78000000 bytes, 9 new chunks, 1.5s",
				},
				paths:    pausePaths,
				commands: []string{pauseCommand},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.backupBlock(tt.args.n); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scenario.backupBlock() = %v, want %v", got, tt.want)
			}
			if tt.s.tree.journal != nil {
				t.Errorf("scenario.backupBlock() left the journal %v", tt.s.tree.journal)
			}
		})
	}
}

func Test_scenario_pruneBlock(t *testing.T) {
	type args struct {
		before storageTotal
		after  storageTotal
	}
	tests := []struct {
		name string
		s    *scenario
		args args
		want pauseBlock
	}{
		{
			name: "revision 2 removed",
			s:    pauseScenario(),
			args: args{
				before: storageTotal{chunks: 17, bytes: 30547 << 10, revisions: []int{1, 2, 3}},
				after:  storageTotal{chunks: 14, bytes: 30546 << 10, revisions: []int{1, 3}},
			},
			want: pauseBlock{
				number: 4,
				done: []string{
					"removed revisions: 2",
					"storage before: 17 chunks, 31280128 bytes, revisions 1 2 3",
					"storage after: 14 chunks, 31279104 bytes, revisions 1 3",
					"difference: -3 chunks, -1024 bytes",
				},
				paths:    pausePaths,
				commands: []string{pauseCommand},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.s.pruneBlock(tt.args.before, tt.args.after)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scenario.pruneBlock() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_scenario_restoreBlock(t *testing.T) {
	type args struct {
		r restored
	}
	restoredPaths := append(append([]string{}, pausePaths...), "restore dir: /tmp/restore")
	tests := []struct {
		name string
		s    *scenario
		args args
		want pauseBlock
	}{
		{
			name: "tree equals the manifest",
			s:    pauseScenario(),
			args: args{r: restored{revision: 4, cli: upstreamName, dir: "/tmp/restore"}},
			want: pauseBlock{
				number: 5,
				done: []string{
					"restored revision 4 with upstream-3.3.1",
					"comparison: the tree equals the manifest",
				},
				paths:    restoredPaths,
				commands: []string{pauseCommand},
			},
		},
		{
			name: "differences",
			s:    pauseScenario(),
			args: args{r: restored{revision: 4, cli: upstreamName, dir: "/tmp/restore",
				differences: 3}},
			want: pauseBlock{
				number: 5,
				done: []string{
					"restored revision 4 with upstream-3.3.1",
					"comparison: 3 differences from the manifest",
				},
				paths:    restoredPaths,
				commands: []string{pauseCommand},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.restoreBlock(tt.args.r); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scenario.restoreBlock() = %v, want %v", got, tt.want)
			}
		})
	}
}
