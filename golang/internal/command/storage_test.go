// cspell:ignore doub duplicacy sftp

package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/dup"
	"github.com/megalomania428/mega-doub/pkg/lock"
	"github.com/megalomania428/mega-doub/pkg/model"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// The values of the secrets of the tests of init; duplicacy requires a storage password
// of at least 8 bytes.
const (
	initPassword      = "fake-init-password"
	initOtherPassword = "fake-init-other-password"
	initSSHPassword   = "fake-init-ssh-password"
)

// initSetup is the setup of a test with the storage: the storage, the copy and the state
// directories are siblings in one temporary directory, the configuration is written next
// to them. url is storage.url, the storage directory by default; progress is the value
// of global.progress, [] by default; global holds the extra lines of global, keys the
// lines of storage.keys and prune the section prune, none when empty.
type initSetup struct {
	dir, storage, copyDir, stateDir string
	url, progress, global, keys     string
	prune                           string
}

// newInitSetup returns the setup of a test of init in a new temporary directory with the
// variables of the passwords set and the variables of global.retry unset.
func newInitSetup(t *testing.T) *initSetup {
	t.Helper()
	setRetryEnv(t, nil)
	t.Setenv("MEGA_DOUB_INIT_PASSWORD", initPassword)
	t.Setenv("MEGA_DOUB_INIT_SSH_PASSWORD", initSSHPassword)
	dir := t.TempDir()
	s := &initSetup{
		dir:      dir,
		storage:  filepath.Join(dir, "storage"),
		copyDir:  filepath.Join(dir, "copy"),
		stateDir: filepath.Join(dir, "state"),
	}
	s.url = s.storage
	s.progress = "[]"
	return s
}

// write writes the configuration of the setup and returns its path.
func (s *initSetup) write(t *testing.T) string {
	t.Helper()
	path := filepath.Join(s.dir, "init.yaml")
	content := "---\nglobal:\n  instance: init\n" +
		"  copy_dir: " + strconv.Quote(s.copyDir) + "\n" +
		"  state_dir: " + strconv.Quote(s.stateDir) + "\n" +
		"  progress: " + s.progress + "\n" + s.global +
		"storage:\n  url: " + strconv.Quote(s.url) + "\n  snapshot_id: init-main\n" +
		"  password: 'env:MEGA_DOUB_INIT_PASSWORD'\n" + s.keys + s.prune +
		"targets:\n  - name: local\n    transport: local\n" +
		"    sources: [{type: test, id: data, path: /srv/data}]\n"
	writeFiles(t, map[string]string{path: content})
	return path
}

// initialize runs init on the setup and fails the test unless it succeeds.
func (s *initSetup) initialize(t *testing.T) {
	t.Helper()
	if code, _, stderr := execute(t, Deps{}, "init", "-c", s.write(t)); code != 0 {
		t.Fatalf("init = %d, want 0: %s", code, stderr)
	}
}

// storageRecords returns the records of the text format written to s without their time
// field and without the records of the bridge of duplicacy, which tell the inner steps
// of duplicacy.
func storageRecords(s string) []string {
	var records []string
	for _, r := range textRecords(s) {
		if !strings.Contains(r, " msg=duplicacy ") {
			records = append(records, r)
		}
	}
	return records
}

// checkOutput checks that every line of stderr is a record of the logger and that the
// output holds no value of the secrets of the tests of init.
func checkOutput(t *testing.T, stdout, stderr string) {
	t.Helper()
	for line := range strings.Lines(stderr) {
		if !strings.HasPrefix(line, "time=") {
			t.Errorf("stderr line %q is not a record of the logger", line)
		}
	}
	for _, secret := range []string{initPassword, initOtherPassword, initSSHPassword} {
		if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
			t.Errorf("cli.Execute() output contains %q", secret)
		}
	}
}

// commandRecord returns the pattern of the record command of init with the configuration
// path and the flag --dry-run off.
func commandRecord(t *testing.T, path string) string {
	t.Helper()
	return `level=INFO msg=command command=init config=` + slogValue(t, path) +
		` dry_run=false log_level=info log_format=text no_progress=true`
}

// notExist fails the test when path exists.
func notExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: error = %v, want it missing", path, err)
	}
}

func Test_handle_init(t *testing.T) {
	type args struct {
		// dryRun adds --dry-run.
		dryRun bool
	}
	tests := []struct {
		name string
		args args
		// prepare prepares the setup before the configuration is written; nil – nothing.
		prepare func(t *testing.T, s *initSetup)
		want    int
		// wantRecords returns the patterns of the records after secrets resolved and
		// command, the records of the bridge of duplicacy left out.
		wantRecords func(t *testing.T, s *initSetup) []string
		// wantConfig tells that the storage has the config, chunks and snapshots after
		// the run; otherwise the storage directory is missing.
		wantConfig bool
	}{
		{
			name: "empty storage",
			want: model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return []string{`level=INFO msg="storage initialized" instance=init ` +
					`storage=` + slogValue(t, s.storage)}
			},
			wantConfig: true,
		},
		{
			name:    "initialized storage",
			prepare: func(t *testing.T, s *initSetup) { s.initialize(t) },
			want:    model.ExitOK,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return []string{`level=INFO msg="storage already initialized" ` +
					`instance=init storage=` + slogValue(t, s.storage)}
			},
			wantConfig: true,
		},
		{
			name: "wrong password",
			prepare: func(t *testing.T, s *initSetup) {
				s.initialize(t)
				t.Setenv("MEGA_DOUB_INIT_PASSWORD", initOtherPassword)
			},
			want: model.ExitStorage,
			wantRecords: func(*testing.T, *initSetup) []string {
				return []string{`level=ERROR msg="storage operation failed" ` +
					`operation=init error="storage init default: config of the storage ` +
					`cannot be decrypted: wrong password or damaged config"`}
			},
			wantConfig: true,
		},
		{
			name: "lock held by another run",
			prepare: func(t *testing.T, s *initSetup) {
				l, err := lock.Acquire(s.stateDir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = l.Release() })
			},
			want: model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return []string{`level=ERROR msg="another run is in progress" lock=` +
					slogValue(t, filepath.Join(s.stateDir, "lock")) + ` pid=` +
					strconv.Itoa(os.Getpid())}
			},
		},
		{
			name: "state directory under a regular file",
			prepare: func(t *testing.T, s *initSetup) {
				file := filepath.Join(s.dir, "file")
				writeFiles(t, map[string]string{file: "fake"})
				s.stateDir = filepath.Join(file, "state")
			},
			want: model.ExitStorage,
			wantRecords: func(t *testing.T, s *initSetup) []string {
				return []string{`level=ERROR msg="lock failed" lock=` +
					slogValue(t, filepath.Join(s.stateDir, "lock")) +
					` error="acquire lock .*: not a directory"`}
			},
		},
		{
			name: "password in the path of the storage",
			prepare: func(t *testing.T, s *initSetup) {
				file := filepath.Join(s.dir, "file")
				writeFiles(t, map[string]string{file: "fake"})
				s.url = filepath.Join(file, initPassword, "storage")
				s.global = "  retry:\n" + fastRetryYAML
			},
			want: model.ExitStorage,
			wantRecords: func(*testing.T, *initSetup) []string {
				return []string{`level=ERROR msg="storage operation failed" ` +
					`operation=init (id=\S+ )?error=".*\*\*\*.*"`}
			},
		},
		{
			name: "dry run",
			args: args{dryRun: true},
			want: model.ExitOK,
			wantRecords: func(*testing.T, *initSetup) []string {
				return []string{`level=INFO msg="dry run, nothing to do" command=init`}
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
			configPath := filepath.Join(s.storage, "config")
			// before is the config written by prepare, nil when there is none.
			before, _ := os.ReadFile(configPath)
			args := []string{"init", "-c", path}
			if tt.args.dryRun {
				args = append(args, "--dry-run")
			}
			got, stdout, stderr := execute(t, Deps{}, args...)
			if got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v: %s", got, tt.want, stderr)
			}
			command := commandRecord(t, path)
			if tt.args.dryRun {
				command = strings.Replace(command, "dry_run=false", "dry_run=true", 1)
				if !strings.HasPrefix(stdout, "---\n") {
					t.Errorf("cli.Execute() stdout = %q, want the configuration", stdout)
				}
				backup, backupOut, backupErr := execute(t, Deps{}, "backup", "-c", path,
					"--dry-run")
				if backup != model.ExitOK {
					t.Fatalf("backup --dry-run = %v, want %v: %s", backup, model.ExitOK,
						backupErr)
				}
				if stdout != backupOut {
					t.Errorf("init --dry-run stdout = %q, want that of backup --dry-run %q",
						stdout, backupOut)
				}
				notExist(t, s.stateDir)
			} else if stdout != "" {
				t.Errorf("cli.Execute() stdout = %q, want none", stdout)
			}
			want := append([]string{resolvedOne, command}, tt.wantRecords(t, s)...)
			matchRecords(t, storageRecords(stderr), want)
			checkOutput(t, stdout, stderr)
			notExist(t, s.copyDir)
			if !tt.wantConfig {
				notExist(t, s.storage)
				return
			}
			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if before != nil && !bytes.Equal(before, after) {
				t.Error("cli.Execute() changed the config")
			}
			for _, name := range []string{"chunks", "snapshots"} {
				info, err := os.Stat(filepath.Join(s.storage, name))
				if err != nil || !info.IsDir() {
					t.Errorf("storage %s: info = %v, error = %v", name, info, err)
				}
			}
		})
	}
}

// cancelWriter writes to buf and calls cancel at every write that holds text.
type cancelWriter struct {
	buf    bytes.Buffer
	text   string
	cancel context.CancelFunc
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(w.text)) {
		w.cancel()
	}
	return w.buf.Write(p)
}

// closedAddr returns an address of 127.0.0.1 with a port that nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func Test_handle_initRetry(t *testing.T) {
	type args struct {
		// global holds the extra lines of global.
		global string
		// cancel cancels the run at the first record storage request failed, as a signal
		// does.
		cancel bool
	}
	tests := []struct {
		name string
		args args
		want int
		// minAttempts and maxAttempts bound the number of the records storage request
		// failed; maxAttempts 0 – no upper bound.
		minAttempts, maxAttempts int
		// wantLast is the pattern of the last record.
		wantLast string
		// shortForm allows the output of the only record wantLast, which a timeout
		// that expires before the secrets are resolved leaves.
		shortForm bool
	}{
		{
			name:        "retries run out",
			args:        args{global: "  retry:\n" + fastRetryYAML},
			want:        model.ExitStorage,
			minAttempts: 1,
			wantLast: `level=ERROR msg="storage operation failed" operation=init ` +
				`(id=\S+ )?error=".*connection refused"`,
		},
		{
			// The timeout may expire before the first attempt on a slow host, the first
			// sleep of 2 s leaves no room for a second one. It may expire even before
			// the secrets are resolved, then the only record is timed out.
			name:        "timeout during the retries",
			args:        args{global: "  timeout: 500ms\n  retry:\n" + slowRetryYAML},
			want:        model.ExitInterrupted,
			maxAttempts: 1,
			wantLast:    `level=ERROR msg="timed out" timeout=500ms`,
			shortForm:   true,
		},
		{
			name:        "signal during the retries",
			args:        args{global: "  retry:\n" + slowRetryYAML, cancel: true},
			want:        model.ExitInterrupted,
			minAttempts: 1,
			maxAttempts: 1,
			wantLast:    `level=ERROR msg=interrupted error="context canceled"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newInitSetup(t)
			s.url = "sftp://" + closedAddr(t) + "/x"
			s.global = tt.args.global
			s.keys = "  keys:\n    ssh_password: 'env:MEGA_DOUB_INIT_SSH_PASSWORD'\n"
			path := s.write(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stderr := &cancelWriter{text: "storage request failed", cancel: func() {}}
			if tt.args.cancel {
				stderr.cancel = cancel
			}
			stdout := &bytes.Buffer{}
			got := cli.Execute(ctx, []string{"init", "-c", path}, stdout, stderr,
				cli.Build{}, New(Deps{}))
			if got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v: %s", got, tt.want, &stderr.buf)
			}
			if stdout.Len() > 0 {
				t.Errorf("cli.Execute() stdout = %q, want none", stdout)
			}
			checkOutput(t, stdout.String(), stderr.buf.String())
			records := storageRecords(stderr.buf.String())
			if all := textRecords(stderr.buf.String()); tt.shortForm && len(all) == 1 {
				matchRecords(t, all, []string{tt.wantLast})
				return
			}
			if len(records) < 3 {
				t.Fatalf("cli.Execute() wrote %q, want at least 3 records", records)
			}
			matchRecords(t, records[:2], []string{`level=INFO msg="secrets resolved" count=2`,
				commandRecord(t, path)})
			attempts := records[2 : len(records)-1]
			if len(attempts) < tt.minAttempts ||
				tt.maxAttempts > 0 && len(attempts) > tt.maxAttempts {
				t.Errorf("cli.Execute() wrote %d records storage request failed, want %d to %d: %q",
					len(attempts), tt.minAttempts, tt.maxAttempts, attempts)
			}
			for i, r := range attempts {
				pattern := fmt.Sprintf(`^level=WARN msg="storage request failed" `+
					`operation="storage init default" attempt=%d error=.+$`, i+1)
				if !regexp.MustCompile(pattern).MatchString(r) {
					t.Errorf("cli.Execute() record %q, want a match for %q", r, pattern)
				}
			}
			matchRecords(t, records[len(records)-1:], []string{tt.wantLast})
		})
	}
}

func Test_storageRun_failed(t *testing.T) {
	cause := &dup.Error{Op: "create storage", LogID: "STORAGE_CREATE",
		Err: errors.New("fake cause")}
	type args struct {
		// done cancels ctx before the call.
		done bool
		f    stepFailure
	}
	tests := []struct {
		name string
		args args
		want int
		// wantRecords are the patterns of the records of failed.
		wantRecords []string
	}{
		{
			name: "deadline of one attempt with ctx alive",
			args: args{f: stepFailure{operation: operationInit,
				err: fmt.Errorf("dial: %w", context.DeadlineExceeded)}},
			want: model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage operation failed" ` +
				`operation=init error="dial: context deadline exceeded"`},
		},
		{
			name: "cancellation with ctx alive",
			args: args{f: stepFailure{operation: "backup",
				err: fmt.Errorf("upload: %w", context.Canceled)}},
			want: model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage operation failed" ` +
				`operation=backup error="upload: context canceled"`},
		},
		{
			name: "cancellation with ctx done",
			args: args{done: true, f: stepFailure{operation: operationInit,
				err: fmt.Errorf("open: %w", context.Canceled)}},
			want: model.ExitInterrupted,
		},
		{
			name: "deadline with ctx done",
			args: args{done: true, f: stepFailure{operation: operationInit,
				err: fmt.Errorf("open: %w", context.DeadlineExceeded)}},
			want: model.ExitInterrupted,
		},
		{
			name: "other error with ctx done",
			args: args{done: true, f: stepFailure{operation: operationInit,
				err: errors.New("fake failure")}},
			want: model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage operation failed" ` +
				`operation=init error="fake failure"`},
		},
		{
			name: "error of duplicacy",
			args: args{f: stepFailure{operation: operationInit,
				err: fmt.Errorf("storage init default: %w", cause)}},
			want: model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage operation failed" ` +
				`operation=init id=STORAGE_CREATE error="storage init default: ` +
				`create storage: duplicacy STORAGE_CREATE: fake cause"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.args.done {
				cancel()
			}
			var buf bytes.Buffer
			sr := storageRun{logger: slog.New(slog.NewTextHandler(&buf, nil))}
			if got := sr.failed(ctx, tt.args.f); got != tt.want {
				t.Errorf("storageRun.failed() = %v, want %v", got, tt.want)
			}
			matchRecords(t, textRecords(buf.String()), tt.wantRecords)
		})
	}
}

// fakeResolver is a secret.Resolver that fails every reference with err or, when err is
// nil, resolves it to a fake value.
type fakeResolver struct {
	err error
}

func (r fakeResolver) Resolve(context.Context, secret.Ref) (secret.Value, error) {
	if r.err != nil {
		return secret.Value{}, r.err
	}
	return secret.NewValue("fake-value"), nil
}

func Test_storageRun_run(t *testing.T) {
	type fields struct {
		// resolveErr is the error of the resolver.
		resolveErr error
		// openErr is the error of the opening of the session; nil – the session must not
		// be opened.
		openErr error
	}
	type args struct {
		// done cancels ctx before the call.
		done bool
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
		// wantRecords are the patterns of the records of run.
		wantRecords []string
		// wantLock tells that the lock has been taken and released; otherwise the state
		// directory is missing.
		wantLock bool
	}{
		{
			name:   "deadline of the secrets with ctx alive",
			fields: fields{resolveErr: fmt.Errorf("vault: %w", context.DeadlineExceeded)},
			want:   model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage operation failed" ` +
				`operation=init error="resolve storage.password: vault: context deadline ` +
				`exceeded"`},
		},
		{
			name:   "cancellation of the secrets with ctx done",
			fields: fields{resolveErr: fmt.Errorf("vault: %w", context.Canceled)},
			args:   args{done: true},
			want:   model.ExitInterrupted,
		},
		{
			name: "deadline of the opening with ctx alive",
			fields: fields{
				openErr: fmt.Errorf("storage init default: %w", context.DeadlineExceeded),
			},
			want: model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage operation failed" ` +
				`operation=init error="storage init default: context deadline exceeded"`},
			wantLock: true,
		},
		{
			name: "cancellation of the opening with ctx alive",
			fields: fields{
				openErr: fmt.Errorf("storage init default: %w", context.Canceled),
			},
			want: model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage operation failed" ` +
				`operation=init error="storage init default: context canceled"`},
			wantLock: true,
		},
		{
			name: "cancellation of the opening with ctx done",
			fields: fields{
				openErr: fmt.Errorf("storage init default: %w", context.Canceled),
			},
			args:     args{done: true},
			want:     model.ExitInterrupted,
			wantLock: true,
		},
		{
			name: "storage without a config",
			fields: fields{
				openErr: fmt.Errorf("storage init default: %w", dup.ErrNotInitialized),
			},
			want: model.ExitStorage,
			wantRecords: []string{`level=ERROR msg="storage is not initialized" ` +
				`instance="" storage=\S* hint="run 'mega-doub init' first"`},
			wantLock: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saved := openSession
			t.Cleanup(func() { openSession = saved })
			openSession = func(context.Context, ...dup.SessionOption) (*dup.Session, error) {
				if tt.fields.openErr == nil {
					t.Error("storageRun.run() opened the session")
					return nil, errors.New("fake opening")
				}
				return nil, tt.fields.openErr
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.args.done {
				cancel()
			}
			stateDir := filepath.Join(t.TempDir(), "state")
			var buf bytes.Buffer
			sr := storageRun{
				logger: slog.New(slog.NewTextHandler(&buf, nil)),
				loaded: &config.Loaded{Config: &config.Config{
					Global: config.Global{StateDir: stateDir},
				}},
				resolver:  fakeResolver{err: tt.fields.resolveErr},
				operation: operationInit,
			}
			work := func(context.Context, *dup.Session) int {
				t.Error("storageRun.run() called the work")
				return model.ExitOK
			}
			if got := sr.run(ctx, work); got != tt.want {
				t.Errorf("storageRun.run() = %v, want %v", got, tt.want)
			}
			matchRecords(t, textRecords(buf.String()), tt.wantRecords)
			if !tt.wantLock {
				notExist(t, stateDir)
				return
			}
			l, err := lock.Acquire(stateDir)
			if err != nil {
				t.Fatalf("lock.Acquire() after storageRun.run() error = %v", err)
			}
			if err := l.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
