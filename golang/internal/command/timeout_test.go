// cspell:ignore doub

package command

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megalomania428/mega-doub/internal/cli"
	"github.com/megalomania428/mega-doub/pkg/model"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

func Test_timeLimit_run(t *testing.T) {
	type fields struct {
		timeout time.Duration
		// parentTimeout is the deadline of the parent context; zero – none.
		parentTimeout time.Duration
	}
	type args struct {
		// work is the work of the run with its context and stop, the cancellation of the
		// parent context, as a signal does.
		work func(ctx context.Context, stop context.CancelFunc) int
	}
	tests := []struct {
		name        string
		fields      fields
		args        args
		want        int
		wantRecords []string
	}{
		{
			name:   "work within the timeout",
			fields: fields{timeout: time.Hour},
			args: args{work: func(context.Context, context.CancelFunc) int {
				return model.ExitStorage
			}},
			want: model.ExitStorage,
		},
		{
			name:   "timeout runs out",
			fields: fields{timeout: 20 * time.Millisecond},
			args: args{work: func(ctx context.Context, _ context.CancelFunc) int {
				<-ctx.Done()
				return model.ExitConfig
			}},
			want:        model.ExitInterrupted,
			wantRecords: []string{`level=ERROR msg="timed out" timeout=20ms`},
		},
		{
			name:   "parent canceled",
			fields: fields{timeout: time.Hour},
			args: args{work: func(ctx context.Context, stop context.CancelFunc) int {
				stop()
				<-ctx.Done()
				return model.ExitStorage
			}},
			want: model.ExitStorage,
		},
		{
			name:   "parent canceled after the timeout",
			fields: fields{timeout: 20 * time.Millisecond},
			args: args{work: func(ctx context.Context, stop context.CancelFunc) int {
				<-ctx.Done()
				stop()
				return model.ExitConfig
			}},
			want:        model.ExitInterrupted,
			wantRecords: []string{`level=ERROR msg="timed out" timeout=20ms`},
		},
		{
			name:   "parent deadline before the timeout",
			fields: fields{timeout: time.Hour, parentTimeout: 20 * time.Millisecond},
			args: args{work: func(ctx context.Context, _ context.CancelFunc) int {
				<-ctx.Done()
				return model.ExitStorage
			}},
			want: model.ExitStorage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			if tt.fields.parentTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.fields.parentTimeout)
				defer cancel()
			}
			var buf bytes.Buffer
			l := timeLimit{
				timeout: tt.fields.timeout,
				logger:  slog.New(slog.NewTextHandler(&buf, nil)),
			}
			got := l.run(ctx, func(workCtx context.Context) int {
				return tt.args.work(workCtx, stop)
			})
			if got != tt.want {
				t.Errorf("timeLimit.run() = %v, want %v", got, tt.want)
			}
			matchRecords(t, textRecords(buf.String()), tt.wantRecords)
		})
	}
}

func Test_handle_timeout(t *testing.T) {
	setRetryEnv(t, nil)
	file := filepath.Join(t.TempDir(), "timeout.yaml")
	content := strings.Replace(resolveConfig, "global: {progress: []}",
		"global: {progress: [], timeout: 50ms}", 1)
	if content == resolveConfig {
		t.Fatal("resolveConfig has no line global: {progress: []}")
	}
	// long is the configuration with a timeout that a run of the test does not reach.
	long := filepath.Join(t.TempDir(), "long.yaml")
	writeFiles(t, map[string]string{file: content, long: strings.Replace(content,
		"timeout: 50ms", "timeout: 1h", 1)})
	const (
		timedOut    = `level=ERROR msg="timed out" timeout=50ms`
		interrupted = `level=ERROR msg=interrupted error="context canceled"`
	)
	// waitDone is the resolver that waits for the end of the context of its run.
	waitDone := func(ctx context.Context, _ context.CancelFunc) (secret.Value, error) {
		<-ctx.Done()
		return secret.Value{}, ctx.Err()
	}
	// resolveCall is the resolver of a call with stop, the cancellation of the run as a
	// signal does.
	type resolveCall func(ctx context.Context, stop context.CancelFunc) (secret.Value,
		error)
	tests := []struct {
		name    string
		args    []string
		resolve resolveCall
		want    int
		// wantRecords are the patterns of all the records; nil – not checked.
		wantRecords []string
		// wantDeadline tells that the context of the resolver has a deadline.
		wantDeadline bool
		// expires tells that the timeout runs out, so that on a slow host it may run out
		// before the resolver is called.
		expires bool
	}{
		{
			name:         "backup times out during the secrets",
			args:         []string{"backup", "--dry-run", "-c", file},
			resolve:      waitDone,
			want:         model.ExitInterrupted,
			wantRecords:  []string{timedOut},
			wantDeadline: true,
			expires:      true,
		},
		{
			name:         "prune times out during the secrets",
			args:         []string{"prune", "-c", file},
			resolve:      waitDone,
			want:         model.ExitInterrupted,
			wantRecords:  []string{timedOut},
			wantDeadline: true,
			expires:      true,
		},
		{
			name: "signal during the secrets of backup",
			args: []string{"backup", "--dry-run", "-c", long},
			resolve: func(ctx context.Context, stop context.CancelFunc) (secret.Value,
				error) {
				stop()
				return waitDone(ctx, stop)
			},
			want:         model.ExitInterrupted,
			wantRecords:  []string{interrupted},
			wantDeadline: true,
		},
		{
			name: "backup within the timeout",
			args: []string{"backup", "--dry-run", "-c", long},
			resolve: func(context.Context, context.CancelFunc) (secret.Value, error) {
				return secret.NewValue("fake-stub-value"), nil
			},
			want:         model.ExitOK,
			wantDeadline: true,
		},
		{
			name: "config validate is not limited",
			args: []string{"config", "validate", "--resolve", "-c", long},
			resolve: func(context.Context, context.CancelFunc) (secret.Value, error) {
				return secret.NewValue("fake-stub-value"), nil
			},
			want: model.ExitOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			deadlines := 0
			calls := 0
			deps := depsOf(func(runCtx context.Context, _ secret.Ref) (secret.Value, error) {
				calls++
				if _, ok := runCtx.Deadline(); ok {
					deadlines++
				}
				return tt.resolve(runCtx, stop)
			})
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			if got := cli.Execute(ctx, tt.args, stdout, stderr, cli.Build{},
				New(deps)); got != tt.want {
				t.Errorf("cli.Execute() = %v, want %v; stderr = %q", got, tt.want, stderr)
			}
			if calls == 0 && !tt.expires {
				t.Fatal("the resolver is not called")
			}
			wantDeadlines := 0
			if tt.wantDeadline {
				wantDeadlines = calls
			}
			if deadlines != wantDeadlines {
				t.Errorf("resolver calls with a deadline = %d of %d, want %d", deadlines,
					calls, wantDeadlines)
			}
			if tt.wantRecords == nil {
				return
			}
			if stdout.Len() > 0 {
				t.Errorf("cli.Execute() stdout = %q, want none", stdout)
			}
			matchRecords(t, textRecords(stderr.String()), tt.wantRecords)
		})
	}
}
