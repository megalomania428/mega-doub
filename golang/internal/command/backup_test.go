// cspell:ignore doub duplicacy

package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	duplicacy "duplicacy.invalid/v3/src"
	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/dup"
	"github.com/megalomania428/mega-doub/pkg/lock"
	"github.com/megalomania428/mega-doub/pkg/model"
)

// stopWait is the longest wait of a test for the request of the cancellation to reach
// duplicacy.
const stopWait = 5 * time.Second

// The sections prune of the tests with a policy that deletes nothing from fresh
// revisions, without and with the prune of backup.
const (
	prunePolicy  = "prune:\n  keep: ['1:7']\n"
	pruneEnabled = "prune:\n  enabled: true\n  keep: ['1:7']\n"
)

// fill writes a file to the copy directory of the setup, which it creates.
func (s *initSetup) fill(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(s.copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, map[string]string{filepath.Join(s.copyDir, "a.txt"): "fake-data\n"})
}

// succeed runs the command without flags on the setup and fails the test unless it
// succeeds.
func (s *initSetup) succeed(t *testing.T, command string) {
	t.Helper()
	if code, _, stderr := execute(t, Deps{}, command, "-c", s.write(t)); code != 0 {
		t.Fatalf("%s = %d, want 0: %s", command, code, stderr)
	}
}

// hold takes the run lock of the setup until the end of the test.
func (s *initSetup) hold(t *testing.T) {
	t.Helper()
	l, err := lock.Acquire(s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Release() })
}

// started returns the patterns of the records secrets resolved and command of the
// command name without --dry-run on the setup; prune adds the field enabled.
func (s *initSetup) started(t *testing.T, name string) []string {
	t.Helper()
	record := strings.Replace(commandRecord(t, filepath.Join(s.dir, "init.yaml")),
		"command=init", "command="+name, 1)
	if name == "prune" {
		record += " enabled=" + strconv.FormatBool(strings.Contains(s.prune, "enabled: true"))
	}
	return []string{resolvedOne, record}
}

// notInitializedRecord returns the pattern of the record storage is not initialized of
// the setup.
func (s *initSetup) notInitializedRecord(t *testing.T) string {
	t.Helper()
	return `level=ERROR msg="storage is not initialized" instance=init storage=` +
		slogValue(t, s.storage) + ` hint="run 'mega-doub init' first"`
}

// backupRecord returns the pattern of the record backup completed of the setup with the
// revision.
func (s *initSetup) backupRecord(t *testing.T, revision int) string {
	t.Helper()
	return `level=INFO msg="backup completed" instance=init storage=` +
		slogValue(t, s.storage) + ` snapshot_id=init-main revision=` +
		strconv.Itoa(revision) + ` files=\d+ bytes=\d+ new_chunks=\d+ new_bytes=\d+ ` +
		`uploaded_bytes=\d+ skipped=0 duration=\S+`
}

// pruneRecord returns the pattern of the record prune completed of the setup that
// deletes nothing.
func (s *initSetup) pruneRecord(t *testing.T) string {
	t.Helper()
	return `level=INFO msg="prune completed" instance=init storage=` +
		slogValue(t, s.storage) + ` snapshot_id=init-main nothing_to_delete=true ` +
		`deleted=0 fossilized=0 resurrected=0 failed=0 duration=\S+`
}

// busyRecord returns the pattern of the record another run is in progress of the setup
// with the lock held by this process.
func (s *initSetup) busyRecord(t *testing.T) string {
	t.Helper()
	return `level=ERROR msg="another run is in progress" lock=` +
		slogValue(t, filepath.Join(s.stateDir, "lock")) + ` pid=` + strconv.Itoa(os.Getpid())
}

// keepRecord returns the pattern of the record of prune on the setup without the section
// prune.
func (s *initSetup) keepRecord(t *testing.T) string {
	t.Helper()
	return `level=ERROR msg="invalid configuration" file=` +
		slogValue(t, filepath.Join(s.dir, "init.yaml")) +
		` line=1 column=1 path=prune\.keep error="required by the command prune"`
}

// dryRecords returns the patterns of all the records of the command name with --dry-run
// on the setup.
func (s *initSetup) dryRecords(t *testing.T, name string) []string {
	t.Helper()
	records := s.started(t, name)
	records[1] = strings.Replace(records[1], "dry_run=false", "dry_run=true", 1)
	return append(records, `level=INFO msg="dry run, nothing to do" command=`+name)
}

// exist fails the test when path is missing.
func exist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s: error = %v, want it present", path, err)
	}
}

func Test_handle_backup(t *testing.T) {
	type args struct {
		// command is backup or prune.
		command string
		// dryRun adds --dry-run.
		dryRun bool
	}
	tests := []struct {
		name string
		args args
		// prepare prepares the setup before the configuration is written; nil – nothing.
		prepare func(t *testing.T, s *initSetup)
		want    int
		// wantRecords returns the patterns of all the records, the records of the bridge of
		// duplicacy left out.
		wantRecords func(t *testing.T, s *initSetup) []string
		// wantConfig tells that the storage has its config after the run.
		wantConfig bool
		// check checks the files after the run; nil – nothing.
		check func(t *testing.T, s *initSetup)
	}{
		{
			name:    "backup before init",
			args:    args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) { s.fill(t) },
			want:    model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), s.notInitializedRecord(t))
			},
		},
		{
			name:    "prune before init",
			args:    args{command: "prune"},
			prepare: func(_ *testing.T, s *initSetup) { s.prune = prunePolicy },
			want:    model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "prune"), s.notInitializedRecord(t))
			},
		},
		{
			name: "first backup",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				s.fill(t)
				s.initialize(t)
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), s.backupRecord(t, 1))
			},
			wantConfig: true,
		},
		{
			name: "second backup with the prune",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				s.prune = pruneEnabled
				s.fill(t)
				s.initialize(t)
				s.succeed(t, "backup")
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), s.backupRecord(t, 2), s.pruneRecord(t))
			},
			wantConfig: true,
		},
		{
			name: "backup with more prune threads than storage threads",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				s.prune = pruneEnabled + "  threads: 8\n"
				s.fill(t)
				s.initialize(t)
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), s.backupRecord(t, 1), s.pruneRecord(t))
			},
			wantConfig: true,
		},
		{
			name: "prune",
			args: args{command: "prune"},
			prepare: func(t *testing.T, s *initSetup) {
				s.prune = prunePolicy
				s.fill(t)
				s.initialize(t)
				s.succeed(t, "backup")
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "prune"), s.pruneRecord(t))
			},
			wantConfig: true,
		},
		{
			name: "prune with more threads than storage threads",
			args: args{command: "prune"},
			prepare: func(t *testing.T, s *initSetup) {
				s.prune = prunePolicy + "  threads: 8\n"
				s.fill(t)
				s.initialize(t)
				s.succeed(t, "backup")
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "prune"), s.pruneRecord(t))
			},
			wantConfig: true,
		},
		{
			name: "prune without keep",
			args: args{command: "prune"},
			want: model.ExitConfig,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return []string{s.keepRecord(t)}
			},
			check: func(t *testing.T, s *initSetup) {
				notExist(t, s.storage)
				notExist(t, s.stateDir)
			},
		},
		{
			name: "prune dry run without keep",
			args: args{command: "prune", dryRun: true},
			want: model.ExitConfig,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return []string{s.keepRecord(t)}
			},
			check: func(t *testing.T, s *initSetup) {
				notExist(t, s.storage)
				notExist(t, s.stateDir)
			},
		},
		{
			name:    "empty copy directory",
			args:    args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) { s.initialize(t) },
			want:    model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), `level=ERROR `+
					`msg="storage operation failed" operation=backup id=SNAPSHOT_EMPTY `+
					`error=".*copy directory has no files"`)
			},
			wantConfig: true,
			check: func(t *testing.T, s *initSetup) {
				info, err := os.Lstat(s.copyDir)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
					t.Errorf("copy directory: info = %v, error = %v, want a directory 0700",
						info, err)
				}
			},
		},
		{
			name: "partial files",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				s.fill(t)
				s.initialize(t)
				outside := filepath.Join(s.dir, "outside")
				for _, dir := range []string{filepath.Join(s.copyDir, "sub", "c.part"),
					outside} {
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				writeFiles(t, map[string]string{
					filepath.Join(s.copyDir, "a.part"):                 "fake-partial\n",
					filepath.Join(s.copyDir, "F.PART"):                 "fake-data\n",
					filepath.Join(s.copyDir, "sub", "b.part"):          "fake-partial\n",
					filepath.Join(s.copyDir, "sub", "c.part", "d.txt"): "fake-data\n",
					filepath.Join(s.dir, "outside.part"):               "fake-target\n",
					filepath.Join(outside, "e.part"):                   "fake-target\n",
				})
				for link, target := range map[string]string{
					filepath.Join(s.copyDir, "link.part"):      filepath.Join(s.dir, "outside.part"),
					filepath.Join(s.copyDir, "sub", "outside"): outside,
				} {
					if err := os.Symlink(target, link); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"),
					`level=INFO msg="partial files removed" count=3`, s.backupRecord(t, 1))
			},
			wantConfig: true,
			check: func(t *testing.T, s *initSetup) {
				for _, name := range []string{"a.part", "link.part", "sub/b.part"} {
					notExist(t, filepath.Join(s.copyDir, name))
				}
				for _, name := range []string{"a.txt", "F.PART", "sub/c.part/d.txt",
					"sub/outside"} {
					exist(t, filepath.Join(s.copyDir, name))
				}
				exist(t, filepath.Join(s.dir, "outside.part"))
				exist(t, filepath.Join(s.dir, "outside", "e.part"))
			},
		},
		{
			name: "copy directory through a link",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				target := filepath.Join(s.dir, "target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				writeFiles(t, map[string]string{
					filepath.Join(target, "a.txt"):  "fake-data\n",
					filepath.Join(target, "x.part"): "fake-partial\n",
				})
				if err := os.Symlink(target, s.copyDir); err != nil {
					t.Fatal(err)
				}
				s.initialize(t)
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"),
					`level=INFO msg="partial files removed" count=1`, s.backupRecord(t, 1))
			},
			wantConfig: true,
			check: func(t *testing.T, s *initSetup) {
				notExist(t, filepath.Join(s.dir, "target", "x.part"))
				exist(t, filepath.Join(s.dir, "target", "a.txt"))
				if info, err := os.Lstat(s.copyDir); err != nil ||
					info.Mode()&os.ModeSymlink == 0 {
					t.Errorf("copy directory: info = %v, error = %v, want the link", info, err)
				}
			},
		},
		{
			name: "copy directory with .. after a link",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				deep := filepath.Join(s.dir, "other", "deep")
				if err := os.MkdirAll(deep, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(deep, filepath.Join(s.dir, "link")); err != nil {
					t.Fatal(err)
				}
				// The path stays as written: Join would clean the .. away.
				s.copyDir = filepath.Join(s.dir, "link") + "/../copy"
				for _, dir := range []string{filepath.Join(s.dir, "copy"),
					filepath.Join(s.dir, "other", "copy")} {
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				writeFiles(t, map[string]string{
					filepath.Join(s.dir, "copy", "a.txt"):           "fake-data\n",
					filepath.Join(s.dir, "copy", "x.part"):          "fake-partial\n",
					filepath.Join(s.dir, "other", "copy", "y.part"): "fake-target\n",
				})
				s.initialize(t)
			},
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"),
					`level=INFO msg="partial files removed" count=1`, s.backupRecord(t, 1))
			},
			wantConfig: true,
			check: func(t *testing.T, s *initSetup) {
				notExist(t, filepath.Join(s.dir, "copy", "x.part"))
				exist(t, filepath.Join(s.dir, "copy", "a.txt"))
				exist(t, filepath.Join(s.dir, "other", "copy", "y.part"))
			},
		},
		{
			name: "copy directory under a regular file",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				file := filepath.Join(s.dir, "file")
				writeFiles(t, map[string]string{file: "fake"})
				s.copyDir = filepath.Join(file, "copy")
				s.initialize(t)
			},
			want: model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), `level=ERROR `+
					`msg="storage operation failed" operation=backup error=`+
					slogValue(t, "prepare copy directory: mkdir "+
						filepath.Join(s.dir, "file")+": not a directory"))
			},
			wantConfig: true,
		},
		{
			name: "backup with the lock held",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				s.fill(t)
				s.initialize(t)
				s.hold(t)
			},
			want: model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), s.busyRecord(t))
			},
			wantConfig: true,
		},
		{
			name: "prune with the lock held",
			args: args{command: "prune"},
			prepare: func(t *testing.T, s *initSetup) {
				s.prune = prunePolicy
				s.initialize(t)
				s.hold(t)
			},
			want: model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "prune"), s.busyRecord(t))
			},
			wantConfig: true,
		},
		{
			name: "wrong password",
			args: args{command: "backup"},
			prepare: func(t *testing.T, s *initSetup) {
				s.fill(t)
				s.initialize(t)
				t.Setenv("MEGA_DOUB_INIT_PASSWORD", initOtherPassword)
			},
			want: model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), `level=ERROR `+
					`msg="storage operation failed" operation=open error="storage init `+
					`default: config of the storage cannot be decrypted: wrong password or `+
					`damaged config"`)
			},
			wantConfig: true,
		},
		{
			name:    "backup dry run",
			args:    args{command: "backup", dryRun: true},
			prepare: func(t *testing.T, s *initSetup) { s.fill(t) },
			want:    model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return s.dryRecords(t, "backup")
			},
			check: func(t *testing.T, s *initSetup) {
				notExist(t, s.storage)
				notExist(t, s.stateDir)
			},
		},
		{
			name:    "prune dry run",
			args:    args{command: "prune", dryRun: true},
			prepare: func(_ *testing.T, s *initSetup) { s.prune = prunePolicy },
			want:    model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return s.dryRecords(t, "prune")
			},
			check: func(t *testing.T, s *initSetup) {
				notExist(t, s.storage)
				notExist(t, s.stateDir)
				notExist(t, s.copyDir)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newInitSetup(t)
			if tt.prepare != nil {
				tt.prepare(t, s)
			}
			path := s.write(t)
			args := []string{tt.args.command, "-c", path}
			if tt.args.dryRun {
				args = append(args, "--dry-run")
			}
			got, stdout, stderr := execute(t, Deps{}, args...)
			if got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v: %s", got, tt.want, stderr)
			}
			// Only a dry run that succeeds prints the configuration.
			if show := tt.args.dryRun && tt.want == model.ExitOK; show !=
				strings.HasPrefix(stdout, "---\n") || !show && stdout != "" {
				t.Errorf("cli.Execute() stdout = %q, want the configuration: %t", stdout, show)
			}
			matchRecords(t, storageRecords(stderr), tt.wantRecords(t, s))
			checkOutput(t, stdout, stderr)
			if config := filepath.Join(s.storage, "config"); tt.wantConfig {
				exist(t, config)
			} else {
				notExist(t, config)
			}
			if tt.check != nil {
				tt.check(t, s)
			}
		})
	}
}

// stopWriter writes to buf. At the first write that holds text it calls stop, a signal or
// the expiry of global.timeout, and waits until the request of the cancellation reaches
// duplicacy, so that the next check of duplicacy stops the operation. The logger makes
// the writes one at a time.
type stopWriter struct {
	buf  bytes.Buffer
	text string
	stop func()
	// stopped is set at the write that holds text.
	stopped bool
}

func (w *stopWriter) Write(p []byte) (int, error) {
	if !w.stopped && bytes.Contains(p, []byte(w.text)) {
		w.stopped = true
		w.stop()
		deadline := time.Now().Add(stopWait)
		for !duplicacy.IsCancelRequested() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	return w.buf.Write(p)
}

func Test_handle_backupCancel(t *testing.T) {
	const (
		interrupted = `level=ERROR msg=interrupted error="context canceled"`
		timedOut    = `level=ERROR msg="timed out" timeout=1h0m0s`
	)
	type args struct {
		// command is backup or prune.
		command string
		// text is the text of the record of the bridge of duplicacy that stops the run.
		text string
		// signal cancels the run at text as a signal does; otherwise global.timeout of 1h
		// expires at text by the replaced withTimeoutCause.
		signal bool
	}
	tests := []struct {
		name string
		args args
		// prune is the section prune of the configuration.
		prune string
		// backups is the number of the backups before the run.
		backups int
		// wantRecords returns the patterns of all the records, the records of the bridge of
		// duplicacy left out.
		wantRecords func(t *testing.T, s *initSetup) []string
		// wantRevision tells that the run has created the revision after the backups.
		wantRevision bool
	}{
		{
			name: "signal during the backup",
			args: args{command: "backup", text: "id=PACK_END", signal: true},
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), interrupted)
			},
		},
		{
			name: "timeout during the backup",
			args: args{command: "backup", text: "id=PACK_END"},
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), timedOut)
			},
		},
		{
			name:    "signal during the prune",
			args:    args{command: "prune", text: "id=RETENTION_POLICY", signal: true},
			prune:   prunePolicy,
			backups: 1,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "prune"), interrupted)
			},
		},
		{
			name:    "timeout during the prune",
			args:    args{command: "prune", text: "id=RETENTION_POLICY"},
			prune:   prunePolicy,
			backups: 1,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "prune"), timedOut)
			},
		},
		{
			name:  "signal after the backup has written the revision",
			args:  args{command: "backup", text: "id=BACKUP_END", signal: true},
			prune: pruneEnabled,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), s.backupRecord(t, 1), interrupted)
			},
			wantRevision: true,
		},
		{
			name:    "signal at the end of the prune",
			args:    args{command: "prune", text: "id=SNAPSHOT_NONE", signal: true},
			prune:   prunePolicy,
			backups: 1,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "prune"), s.pruneRecord(t), interrupted)
			},
		},
		{
			name:  "signal at the end of the prune of the backup",
			args:  args{command: "backup", text: "id=SNAPSHOT_NONE", signal: true},
			prune: pruneEnabled,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return append(s.started(t, "backup"), s.backupRecord(t, 1), s.pruneRecord(t),
					interrupted)
			},
			wantRevision: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newInitSetup(t)
			s.prune = tt.prune
			s.fill(t)
			s.initialize(t)
			for range tt.backups {
				s.succeed(t, "backup")
			}
			if !tt.args.signal {
				s.global = "  timeout: 1h\n"
			}
			path := s.write(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stderr := &stopWriter{text: tt.args.text, stop: cancel}
			if !tt.args.signal {
				// The setup above has run with the real timer, the run expires at text.
				saved := withTimeoutCause
				t.Cleanup(func() { withTimeoutCause = saved })
				withTimeoutCause = func(parent context.Context, _ time.Duration,
					cause error) (context.Context, context.CancelFunc) {
					expired, expire := context.WithCancelCause(parent)
					stderr.stop = func() { expire(cause) }
					return expired, func() { expire(context.Canceled) }
				}
			}
			stdout := &bytes.Buffer{}
			got := cli.Execute(ctx, []string{tt.args.command, "-c", path}, stdout, stderr,
				cli.Build{}, New(Deps{}))
			if got != model.ExitInterrupted {
				t.Errorf("cli.Execute() = %v, want %v: %s", got, model.ExitInterrupted,
					&stderr.buf)
			}
			if !stderr.stopped {
				t.Errorf("cli.Execute() wrote no record with %q", tt.args.text)
			}
			if strings.Contains(stderr.buf.String(), "OPERATION_CANCELED") {
				t.Errorf("cli.Execute() wrote the record of the cancellation of duplicacy")
			}
			if stdout.Len() > 0 {
				t.Errorf("cli.Execute() stdout = %q, want none", stdout)
			}
			matchRecords(t, storageRecords(stderr.buf.String()), tt.wantRecords(t, s))
			checkOutput(t, stdout.String(), stderr.buf.String())
			revision := filepath.Join(s.storage, "snapshots", "init-main",
				strconv.Itoa(tt.backups+1))
			if tt.wantRevision {
				exist(t, revision)
			} else {
				notExist(t, revision)
			}
		})
	}
}

func Test_handle_backupProgress(t *testing.T) {
	type args struct {
		// command is backup or prune.
		command string
		// noProgress adds --no-progress.
		noProgress bool
	}
	tests := []struct {
		name string
		args args
		// progress is the value of global.progress.
		progress string
		// want are the elements of the bar given to the session; nil – no bar.
		want []string
	}{
		{name: "backup with elements", args: args{command: "backup"},
			progress: "[percent, bar]", want: []string{"percent", "bar"}},
		{name: "backup with --no-progress", args: args{command: "backup", noProgress: true},
			progress: "[percent]"},
		{name: "backup without elements", args: args{command: "backup"}, progress: "[]"},
		{name: "prune", args: args{command: "prune"}, progress: "[percent]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newInitSetup(t)
			s.prune = prunePolicy
			s.fill(t)
			s.initialize(t)
			s.succeed(t, "backup")
			s.progress = tt.progress
			path := s.write(t)
			saved := withProgress
			t.Cleanup(func() { withProgress = saved })
			calls := 0
			var gotWriter io.Writer
			var got []string
			withProgress = func(w io.Writer, elements ...string) dup.SessionOption {
				calls++
				gotWriter, got = w, elements
				// No elements draw nothing, so the records stay the only output.
				return dup.WithProgress(w)
			}
			args := []string{tt.args.command, "-c", path}
			if tt.args.noProgress {
				args = append(args, "--no-progress")
			}
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			if code := cli.Execute(t.Context(), args, stdout, stderr, cli.Build{},
				New(Deps{})); code != model.ExitOK {
				t.Errorf("cli.Execute() = %v, want %v: %s", code, model.ExitOK, stderr)
			}
			checkOutput(t, stdout.String(), stderr.String())
			if tt.want == nil {
				if calls != 0 {
					t.Errorf("withProgress() called %d times, want none", calls)
				}
				return
			}
			if calls != 1 || gotWriter != stderr || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("withProgress() called %d times with %v and %q, want once with "+
					"the stderr of cli.Execute() and %q", calls, gotWriter, got, tt.want)
			}
		})
	}
}

// replaceWithLink wraps ctx so that the call of Err number at moves the directory dir to
// moved and puts a link to moved in its place, as a change of the tree during the walk.
type replaceWithLink struct {
	context.Context
	t          *testing.T
	dir, moved string
	at, calls  int
}

func (c *replaceWithLink) Err() error {
	c.calls++
	if c.calls == c.at {
		if err := os.Rename(c.dir, c.moved); err != nil {
			c.t.Fatal(err)
		}
		if err := os.Symlink(c.moved, c.dir); err != nil {
			c.t.Fatal(err)
		}
	}
	return c.Context.Err()
}

func Test_partialFiles_remove(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "copy")
	moved := filepath.Join(dir, "moved")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, map[string]string{
		filepath.Join(root, "sub", "x.part"): "fake-partial\n",
		filepath.Join(root, "sub", "y.txt"):  "fake-data\n",
	})
	// The calls of Err are the root, then sub, so the swap is before sub is read.
	ctx := &replaceWithLink{Context: t.Context(), t: t, dir: filepath.Join(root, "sub"),
		moved: moved, at: 2}
	p := partialFiles{dir: root}
	if err := p.remove(ctx); err != nil {
		t.Errorf("remove() error = %v, want nil", err)
	}
	if p.count != 0 {
		t.Errorf("remove() count = %d, want 0", p.count)
	}
	exist(t, filepath.Join(moved, "x.part"))
	exist(t, filepath.Join(moved, "y.txt"))
}

func Test_partialFiles_removeUnreadableSubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory without the read permission")
	}
	root := filepath.Join(t.TempDir(), "copy")
	sub := filepath.Join(root, "secret")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	type fields struct {
		dir   string
		count int
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name:    "unreadable subdirectory",
			fields:  fields{dir: root},
			args:    args{ctx: t.Context()},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &partialFiles{
				dir:   tt.fields.dir,
				count: tt.fields.count,
			}
			if err := p.remove(tt.args.ctx); (err != nil) != tt.wantErr {
				t.Errorf("partialFiles.remove() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// changeAtErr wraps ctx so that the call of Err number at runs change, as a change of the
// tree during the walk.
type changeAtErr struct {
	context.Context
	change    func()
	at, calls int
}

func (c *changeAtErr) Err() error {
	c.calls++
	if c.calls == c.at {
		c.change()
	}
	return c.Context.Err()
}

func Test_partialFiles_removeNotUTF8(t *testing.T) {
	root := filepath.Join(t.TempDir(), "copy")
	// The name is in Windows-1251: the byte 0xCF is not valid UTF-8.
	sub := filepath.Join(root, "dir-\xcf")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Skipf("the file system does not allow a name that is not UTF-8: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, map[string]string{
		filepath.Join(sub, "x.part"):           "fake-partial\n",
		filepath.Join(sub, "y.txt"):            "fake-data\n",
		filepath.Join(root, "other", "z.part"): "fake-partial\n",
		filepath.Join(root, "name-\xcf.part"):  "fake-partial\n",
		filepath.Join(root, "name-\xcf.keep"):  "fake-data\n",
	})
	p := partialFiles{dir: root}
	if err := p.remove(t.Context()); err != nil {
		t.Errorf("remove() error = %v, want nil", err)
	}
	if p.count != 3 {
		t.Errorf("remove() count = %d, want 3", p.count)
	}
	for _, gone := range []string{filepath.Join(sub, "x.part"),
		filepath.Join(root, "other", "z.part"), filepath.Join(root, "name-\xcf.part")} {
		notExist(t, gone)
	}
	exist(t, filepath.Join(sub, "y.txt"))
	exist(t, filepath.Join(root, "name-\xcf.keep"))
}

func Test_partialFiles_removeChanged(t *testing.T) {
	tests := []struct {
		name string
		// change changes the file batch.part before it is checked.
		change    func(t *testing.T, path string)
		wantDir   bool
		wantCount int
	}{
		{
			name: "replaced with a directory",
			change: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantDir: true,
		},
		{
			name: "removed",
			change: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "not changed",
			change:    func(*testing.T, string) {},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "copy")
			part := filepath.Join(root, "batch.part")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFiles(t, map[string]string{part: "fake-partial\n"})
			// The calls of Err are the root, then batch.part, which is read already.
			ctx := &changeAtErr{Context: t.Context(), at: 2,
				change: func() { tt.change(t, part) }}
			p := partialFiles{dir: root}
			if err := p.remove(ctx); err != nil {
				t.Errorf("remove() error = %v, want nil", err)
			}
			if p.count != tt.wantCount {
				t.Errorf("remove() count = %d, want %d", p.count, tt.wantCount)
			}
			info, err := os.Lstat(part)
			switch {
			case tt.wantDir && (err != nil || !info.IsDir()):
				t.Errorf("%s: Lstat() = %v, %v, want the directory", part, info, err)
			case !tt.wantDir && err == nil:
				t.Errorf("%s: still present, want it removed or gone", part)
			}
		})
	}
}

func Test_partialFiles_removeChangedSubdir(t *testing.T) {
	tests := []struct {
		name string
		// sub is the name of the subdirectory, change changes it before it is read.
		sub       string
		change    func(t *testing.T, path string)
		wantCount int
		// wantSub is whether sub is present after the walk, wantInner is whether
		// sub/x.part is gone with the subdirectory still there.
		wantSub, wantInner bool
	}{
		{
			name: "removed",
			sub:  "sub",
			change: func(t *testing.T, path string) {
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
			},
			wantCount: 2,
		},
		{
			name: "removed with the suffix",
			sub:  "sub.part",
			change: func(t *testing.T, path string) {
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
			},
			wantCount: 2,
		},
		{
			name: "replaced with a file with the suffix",
			sub:  "sub.part",
			change: func(t *testing.T, path string) {
				replaceWithFile(t, path)
			},
			wantCount: 3,
		},
		{
			name: "replaced with a file with another name",
			sub:  "sub",
			change: func(t *testing.T, path string) {
				replaceWithFile(t, path)
			},
			wantCount: 2,
			wantSub:   true,
		},
		{
			name:      "not changed",
			sub:       "sub",
			change:    func(*testing.T, string) {},
			wantCount: 3,
			wantSub:   true,
			wantInner: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "copy")
			sub := filepath.Join(root, tt.sub)
			if err := os.MkdirAll(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFiles(t, map[string]string{
				filepath.Join(root, "a.part"): "fake-partial\n",
				filepath.Join(sub, "x.part"):  "fake-partial\n",
				filepath.Join(root, "z.part"): "fake-partial\n",
			})
			// The entries are walked in the lexical order a.part, sub, z.part, so the
			// calls of Err are the root, a.part, then sub, which is read already.
			ctx := &changeAtErr{Context: t.Context(), at: 3,
				change: func() { tt.change(t, sub) }}
			p := partialFiles{dir: root}
			if err := p.remove(ctx); err != nil {
				t.Errorf("remove() error = %v, want nil", err)
			}
			if p.count != tt.wantCount {
				t.Errorf("remove() count = %d, want %d", p.count, tt.wantCount)
			}
			notExist(t, filepath.Join(root, "a.part"))
			notExist(t, filepath.Join(root, "z.part"))
			if tt.wantSub {
				exist(t, sub)
			} else {
				notExist(t, sub)
			}
			if tt.wantInner {
				notExist(t, filepath.Join(sub, "x.part"))
			}
		})
	}
}

func Test_partialFiles_removeLinkedSubdir(t *testing.T) {
	tests := []struct {
		name string
		// sub is the name of the subdirectory that is replaced with a link, inside
		// tells whether the link leads to a directory inside the root, by a relative path
		// that os.Root accepts.
		sub       string
		inside    bool
		wantCount int
		// wantSub is whether the link sub stays.
		wantSub bool
		// wantCalls is the number of the checks of ctx: a walk into the link adds more.
		wantCalls int
	}{
		{name: "outside with the suffix", sub: "sub.part", wantCount: 3},
		{name: "outside", sub: "sub", wantCount: 2, wantSub: true},
		{name: "inside", sub: "sub", inside: true, wantCount: 3, wantSub: true,
			wantCalls: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "copy")
			sub := filepath.Join(root, tt.sub)
			target := filepath.Join(dir, "outside")
			if tt.inside {
				target = filepath.Join(root, "zeta")
			}
			for _, d := range []string{sub, target} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeFiles(t, map[string]string{
				filepath.Join(root, "a.part"):   "fake-partial\n",
				filepath.Join(sub, "x.part"):    "fake-partial\n",
				filepath.Join(root, "z.part"):   "fake-partial\n",
				filepath.Join(target, "w.part"): "fake-partial\n",
				filepath.Join(target, "v.txt"):  "fake-data\n",
			})
			// The calls of Err are the root, a.part, then sub, which is read already.
			ctx := &changeAtErr{Context: t.Context(), at: 3, change: func() {
				if err := os.RemoveAll(sub); err != nil {
					t.Fatal(err)
				}
				linkTo := target
				if tt.inside {
					linkTo = filepath.Base(target)
				}
				if err := os.Symlink(linkTo, sub); err != nil {
					t.Fatal(err)
				}
			}}
			p := partialFiles{dir: root}
			if err := p.remove(ctx); err != nil {
				t.Errorf("remove() error = %v, want nil", err)
			}
			if p.count != tt.wantCount {
				t.Errorf("remove() count = %d, want %d", p.count, tt.wantCount)
			}
			notExist(t, filepath.Join(root, "a.part"))
			notExist(t, filepath.Join(root, "z.part"))
			exist(t, filepath.Join(target, "v.txt"))
			if tt.inside {
				notExist(t, filepath.Join(target, "w.part"))
				if ctx.calls != tt.wantCalls {
					t.Errorf("remove() checked ctx %d times, want %d: the link is walked",
						ctx.calls, tt.wantCalls)
				}
			} else {
				exist(t, filepath.Join(target, "w.part"))
			}
			info, err := os.Lstat(sub)
			switch {
			case tt.wantSub && (err != nil || info.Mode()&fs.ModeSymlink == 0):
				t.Errorf("%s: Lstat() = %v, %v, want the link", sub, info, err)
			case !tt.wantSub && err == nil:
				t.Errorf("%s: still present, want the link removed", sub)
			}
		})
	}
}

func Test_partialFiles_removeChangedAncestor(t *testing.T) {
	tests := []struct {
		name string
		// file is the entry of the directory a that is visited after a is replaced with
		// a file.
		file string
	}{
		{name: "subdirectory", file: filepath.Join("sub", "x.part")},
		{name: "file", file: "y.part"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "copy")
			a := filepath.Join(root, "a")
			if err := os.MkdirAll(filepath.Dir(filepath.Join(a, tt.file)), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFiles(t, map[string]string{
				filepath.Join(root, "0.part"): "fake-partial\n",
				filepath.Join(a, tt.file):     "fake-partial\n",
				filepath.Join(root, "z.part"): "fake-partial\n",
				filepath.Join(root, "z.keep"): "fake-data\n",
			})
			// The calls of Err are the root, 0.part, a, then the entry of a, whose parent
			// is read already.
			ctx := &changeAtErr{Context: t.Context(), at: 4,
				change: func() { replaceWithFile(t, a) }}
			p := partialFiles{dir: root}
			if err := p.remove(ctx); err != nil {
				t.Errorf("remove() error = %v, want nil", err)
			}
			if p.count != 2 {
				t.Errorf("remove() count = %d, want 2", p.count)
			}
			notExist(t, filepath.Join(root, "0.part"))
			notExist(t, filepath.Join(root, "z.part"))
			exist(t, filepath.Join(root, "z.keep"))
			if info, err := os.Lstat(a); err != nil || !info.Mode().IsRegular() {
				t.Errorf("%s: Lstat() = %v, %v, want the file", a, info, err)
			}
		})
	}
}

func Test_partialFiles_removeLinkedAncestor(t *testing.T) {
	tests := []struct {
		name string
		// file is the entry under the directory a that is visited after a is replaced
		// with a link, at is the number of the call of Err that makes the replacement.
		file string
		at   int
		// inside tells whether the link leads to a directory inside the root, by a
		// relative path that os.Root accepts, wantCalls is then the number of the checks
		// of ctx: a removal through the link leaves fewer entries of the target to walk.
		inside    bool
		wantCalls int
	}{
		{name: "file outside", file: "y.part", at: 4},
		{name: "subdirectory outside", file: filepath.Join("sub", "x.part"), at: 4},
		{name: "two levels outside", file: filepath.Join("b", "y.part"), at: 5},
		{name: "file inside", file: "y.part", at: 4, inside: true, wantCalls: 9},
		{name: "subdirectory inside", file: filepath.Join("sub", "x.part"), at: 4,
			inside: true, wantCalls: 10},
		{name: "two levels inside", file: filepath.Join("b", "y.part"), at: 5,
			inside: true, wantCalls: 11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "copy")
			a := filepath.Join(root, "a")
			target := filepath.Join(dir, "outside")
			if tt.inside {
				target = filepath.Join(root, "zeta")
			}
			for _, d := range []string{a, target} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(d, tt.file)), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeFiles(t, map[string]string{
				filepath.Join(root, "0.part"):  "fake-partial\n",
				filepath.Join(a, tt.file):      "fake-partial\n",
				filepath.Join(root, "z.part"):  "fake-partial\n",
				filepath.Join(root, "z.keep"):  "fake-data\n",
				filepath.Join(target, tt.file): "fake-partial\n",
				filepath.Join(target, "v.txt"): "fake-data\n",
			})
			ctx := &changeAtErr{Context: t.Context(), at: tt.at, change: func() {
				if err := os.RemoveAll(a); err != nil {
					t.Fatal(err)
				}
				linkTo := target
				if tt.inside {
					linkTo = filepath.Base(target)
				}
				if err := os.Symlink(linkTo, a); err != nil {
					t.Fatal(err)
				}
			}}
			p := partialFiles{dir: root}
			if err := p.remove(ctx); err != nil {
				t.Errorf("remove() error = %v, want nil", err)
			}
			wantCount := 2
			if tt.inside {
				wantCount = 3
			}
			if p.count != wantCount {
				t.Errorf("remove() count = %d, want %d", p.count, wantCount)
			}
			notExist(t, filepath.Join(root, "0.part"))
			notExist(t, filepath.Join(root, "z.part"))
			exist(t, filepath.Join(root, "z.keep"))
			exist(t, filepath.Join(target, "v.txt"))
			if tt.inside {
				notExist(t, filepath.Join(target, tt.file))
				if ctx.calls != tt.wantCalls {
					t.Errorf("remove() checked ctx %d times, want %d: the link is followed",
						ctx.calls, tt.wantCalls)
				}
			} else {
				exist(t, filepath.Join(target, tt.file))
			}
			if info, err := os.Lstat(a); err != nil || info.Mode()&fs.ModeSymlink == 0 {
				t.Errorf("%s: Lstat() = %v, %v, want the link", a, info, err)
			}
		})
	}
}

func Test_intact_settle(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "d", "e"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "outside"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, map[string]string{
		filepath.Join(dir, "f"):                "fake-data\n",
		filepath.Join(dir, "d", "e", "x.part"): "fake-partial\n",
		filepath.Join(dir, "d", "g"):           "fake-data\n",
	})
	for link, to := range map[string]string{"out": "../outside", "in": "d"} {
		if err := os.Symlink(to, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	tests := []struct {
		name string
		rel  string
		// wantIntact is the result of intact, wantGone is that settle says that the
		// entry is gone, wantInfo that it exists.
		wantIntact, wantGone, wantInfo bool
	}{
		{name: "in the root", rel: "f", wantIntact: true, wantInfo: true},
		{name: "missing in the root", rel: "missing", wantIntact: true, wantGone: true},
		{name: "nested", rel: filepath.Join("d", "e", "x.part"), wantIntact: true,
			wantInfo: true},
		{name: "missing nested", rel: filepath.Join("d", "e", "y.part"), wantIntact: true,
			wantGone: true},
		{name: "ancestor is a file", rel: filepath.Join("f", "x.part"), wantGone: true},
		{name: "ancestor is missing", rel: filepath.Join("m", "x.part"), wantGone: true},
		{name: "ancestor is a link outside", rel: filepath.Join("out", "x.part"),
			wantGone: true},
		{name: "ancestor is a link inside", rel: filepath.Join("in", "e", "x.part"),
			wantGone: true},
		{name: "second ancestor is a file", rel: filepath.Join("d", "g", "x.part"),
			wantGone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := intact(root, tt.rel)
			if err != nil || got != tt.wantIntact {
				t.Errorf("intact() = %v, %v, want %v, nil", got, err, tt.wantIntact)
			}
			opErr := syscall.ENOTDIR
			info, err := settle(root, tt.rel, opErr)
			switch {
			case tt.wantGone && (info != nil || err != nil):
				t.Errorf("settle() = %v, %v, want nil, nil", info, err)
			case tt.wantInfo && (info == nil || err != nil):
				t.Errorf("settle() = %v, %v, want the info, nil", info, err)
			}
		})
	}
}

func replaceWithFile(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fake-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Test_handle_backupProgressApplied checks that the session of backup applies the option
// that withProgress returns: an option with an unknown element fails the opening of the
// session, which a lost option would not.
func Test_handle_backupProgressApplied(t *testing.T) {
	s := newInitSetup(t)
	s.fill(t)
	s.initialize(t)
	s.progress = "[percent]"
	path := s.write(t)
	saved := withProgress
	t.Cleanup(func() { withProgress = saved })
	withProgress = func(w io.Writer, _ ...string) dup.SessionOption {
		return dup.WithProgress(w, "no-such-element")
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := cli.Execute(t.Context(), []string{"backup", "-c", path}, stdout, stderr,
		cli.Build{}, New(Deps{}))
	if code != model.ExitStorage || !strings.Contains(stderr.String(),
		"unknown progress element") {
		t.Errorf("cli.Execute() = %v, want %v with the unknown progress element: %s", code,
			model.ExitStorage, stderr)
	}
}

func Test_handle_backupDuration(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			s := newInitSetup(t)
			s.prune = pruneEnabled
			s.fill(t)
			s.initialize(t)
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			args := []string{"--log-format", format, "backup", "-c", s.write(t)}
			if code := cli.Execute(t.Context(), args, stdout, stderr, cli.Build{},
				New(Deps{})); code != model.ExitOK {
				t.Fatalf("cli.Execute() = %v, want %v: %s", code, model.ExitOK, stderr)
			}
			if format == "text" {
				for _, msg := range []string{msgBackupCompleted, msgPruneCompleted} {
					m := regexp.MustCompile(`msg="` + msg + `".* duration=(\S+)\n`).
						FindStringSubmatch(stderr.String())
					if m == nil {
						t.Errorf("stderr has no duration in the record %q: %s", msg, stderr)
						continue
					}
					if d, err := time.ParseDuration(m[1]); err != nil || d.String() != m[1] {
						t.Errorf("record %q: duration = %q, want a Duration.String(): %v",
							msg, m[1], err)
					}
				}
				return
			}
			found := map[string]bool{}
			for _, r := range decodeRecords(t, stderr) {
				msg, _ := r["msg"].(string)
				if msg != msgBackupCompleted && msg != msgPruneCompleted {
					continue
				}
				found[msg] = true
				text, ok := r["duration"].(string)
				if !ok {
					t.Errorf("record %q: duration = %#v, want a string", msg, r["duration"])
					continue
				}
				if d, err := time.ParseDuration(text); err != nil || d.String() != text {
					t.Errorf("record %q: duration = %q, want a Duration.String(): %v", msg,
						text, err)
				}
			}
			if len(found) != 2 {
				t.Errorf("records with a duration = %v, want backup and prune completed", found)
			}
		})
	}
}

func Test_partialFiles_removeLstatFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the missing search permission")
	}
	tests := []struct {
		name string
		rel  string
		kept string
	}{
		{name: "file", rel: filepath.Join("sub", "x.part"),
			kept: filepath.Join("sub", "x.part")},
		{name: "subdirectory", rel: filepath.Join("sub", "d"),
			kept: filepath.Join("sub", "d", "y.part")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "copy")
			sub := filepath.Join(root, "sub")
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, tt.kept)),
				0o700); err != nil {
				t.Fatal(err)
			}
			writeFiles(t, map[string]string{
				filepath.Join(root, tt.kept): "fake-partial\n",
			})
			t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
			modes := map[string]os.FileMode{opLstat: 0o600, opRecheck: 0o700}
			p := partialFiles{dir: root, afterCheck: func(op, rel string) {
				if mode, ok := modes[op]; ok && rel == tt.rel {
					if err := os.Chmod(sub, mode); err != nil {
						t.Error(err)
					}
				}
			}}
			err := p.remove(t.Context())
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("remove() error = %v, want a permission error", err)
			}
			if p.count != 0 {
				t.Errorf("remove() count = %d, want 0", p.count)
			}
			exist(t, filepath.Join(root, tt.kept))
		})
	}
}

func Test_partialFiles_removeSubdirBecameFile(t *testing.T) {
	tests := []struct {
		name      string
		sub       string
		wantCount int
		wantFile  bool
	}{
		{name: "with the suffix", sub: "sub.part", wantCount: 1},
		{name: "without the suffix", sub: "sub", wantFile: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "copy")
			path := filepath.Join(root, tt.sub)
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFiles(t, map[string]string{filepath.Join(path, "in.txt"): "fake\n"})
			p := partialFiles{dir: root, afterCheck: func(op, rel string) {
				if op != opRead || rel != tt.sub {
					return
				}
				if err := os.RemoveAll(path); err != nil {
					t.Error(err)
				}
				writeFiles(t, map[string]string{path: "fake-became-file\n"})
			}}
			if err := p.remove(t.Context()); err != nil {
				t.Errorf("remove() error = %v, want nil", err)
			}
			if p.count != tt.wantCount {
				t.Errorf("remove() count = %d, want %d", p.count, tt.wantCount)
			}
			if tt.wantFile {
				exist(t, path)
			} else {
				notExist(t, path)
			}
		})
	}
}
