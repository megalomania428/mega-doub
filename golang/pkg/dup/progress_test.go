// cspell:ignore duplicacy
package dup

import (
	"bytes"
	"context"
	"crypto/rand"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	duplicacy "duplicacy.invalid/v3/src"
	ci "github.com/megalomania428/go-lib-ci"
)

// slowUpload is the time of every upload of slowStorage.
const slowUpload = 150 * time.Millisecond

// syncBuffer is a buffer that go-lib-ci writes from its own goroutine and the test
// reads from another one.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// slowStorage is a storage that uploads slowly, so a backup lasts several ticks of the
// progress bar; chunks are not uploaded before ready tells that the bar has drawn its
// elements.
type slowStorage struct {
	duplicacy.Storage
	ready  func() bool
	until  time.Time
	chunks atomic.Int64
}

func (s *slowStorage) UploadFile(threadIndex int, filePath string, content []byte) error {
	isChunk := strings.HasPrefix(filePath, "chunks/")
	if isChunk && s.ready != nil && s.chunks.Load() > 0 {
		for !s.ready() {
			if !s.until.IsZero() && time.Now().After(s.until) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	time.Sleep(slowUpload)
	err := s.Storage.UploadFile(threadIndex, filePath, content)
	if err == nil && isChunk {
		s.chunks.Add(1)
	}
	return err
}

// slowDown replaces createStorage for the test with the one that gives a slowStorage
// over the storage of duplicacy.
func slowDown(t *testing.T, ready func() bool) {
	t.Helper()
	var until time.Time
	if deadline, ok := t.Deadline(); ok {
		until = deadline.Add(-5 * time.Second)
	}
	replaceCreateStorage(t, func(
		p duplicacy.Preference, readOnly bool, threads int,
	) duplicacy.Storage {
		return &slowStorage{
			Storage: duplicacy.CreateStorage(p, readOnly, threads),
			ready:   ready,
			until:   until,
		}
	})
}

// replaceProgressOptions replaces progressOptions for the test.
func replaceProgressOptions(t *testing.T, opts ...ci.ProgressOption) {
	t.Helper()
	saved := progressOptions
	progressOptions = opts
	t.Cleanup(func() { progressOptions = saved })
}

// isWave tells whether line is the wave that go-lib-ci draws before the first bytes:
// renderWaveLine(ciBarWidth, true) gives 79 columns of spaces with one whole marker
// "-=O=-" at the position 0..73 and one to four "#" in the columns 1..78 that may
// overwrite the marker (go-lib-ci v1.0.6, progress.go: ciBarWidth at line 22, frame()
// at line 232 and renderWaveLine() at lines 456-495).
func isWave(line string) bool {
	const width, marker, lastPos = 79, "-=O=-", 73
	if len(line) != width || strings.Count(line, "#") < 1 ||
		strings.Count(line, "#") > 4 {
		return false
	}
	for pos := 0; pos <= lastPos; pos++ {
		matched := true
		for i := 0; i < width && matched; i++ {
			switch {
			case line[i] == '#' && i >= 1 && i <= width-2:
			case i >= pos && i < pos+len(marker):
				matched = line[i] == marker[i-pos]
			default:
				matched = line[i] == ' '
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// progressLines splits text into the lines without the carriage returns.
func progressLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, "\r")
	}
	return lines
}

// elementLine tells whether text has a line that matches pattern, a line of the
// elements rather than of the wave.
func elementLine(text string, pattern *regexp.Regexp) bool {
	for _, line := range progressLines(text) {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

func TestSession_Backup_progress(t *testing.T) {
	type args struct {
		ctx  context.Context
		opts []BackupOption
	}
	tests := []struct {
		name     string
		args     args
		elements []string
		pattern  *regexp.Regexp
	}{
		{
			name:     "percent",
			args:     args{ctx: t.Context()},
			elements: []string{"percent"},
			pattern:  regexp.MustCompile(`^ *\d{1,3}%$`),
		},
		{
			name:     "elapsed and percent in any order",
			args:     args{ctx: t.Context()},
			elements: []string{"percent", "elapsed"},
			pattern:  regexp.MustCompile(`^\d+:\d\d:\d\d +\d{1,3}%$`),
		},
		{
			name:     "no elements",
			args:     args{ctx: t.Context()},
			elements: nil,
			pattern:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &syncBuffer{}
			if tt.pattern != nil {
				slowDown(t, func() bool { return elementLine(buf.String(), tt.pattern) })
			} else {
				slowDown(t, nil)
			}
			replaceProgressOptions(t, ci.WithProgressCIMode(true))
			s := openBackupTest(t, WithProgress(buf, tt.elements...))
			data := make([]byte, 17<<20)
			if _, err := rand.Read(data); err != nil {
				t.Fatal(err)
			}
			writeCopy(t, s.copyDir, map[string]string{"big.bin": string(data)})
			got, err := s.Backup(tt.args.ctx, tt.args.opts...)
			if err != nil {
				t.Fatalf("Session.Backup() error = %v", err)
			}
			if got == nil {
				t.Fatal("Session.Backup() = nil")
			}
			out := buf.String()
			if tt.pattern == nil {
				if out != "" {
					t.Errorf("progress output = %q, want empty", out)
				}
				return
			}
			lines := 0
			for _, line := range progressLines(out) {
				if line == "" || isWave(line) {
					continue
				}
				if !tt.pattern.MatchString(line) {
					t.Errorf("line %q is neither a wave nor matches %v; output = %q",
						line, tt.pattern, out)
					continue
				}
				lines++
			}
			if lines == 0 {
				t.Errorf("no line of the elements %v; output = %q", tt.elements, out)
			}
		})
	}
}

func TestBackupProgress_event_panic(t *testing.T) {
	type test struct {
		name string
		// events are sent after the start that panics.
		events []duplicacy.ProgressEvent
	}
	tests := []test{
		{name: "finish after the panic of the start"},
		{
			name: "step and end after the panic of the start",
			events: []duplicacy.ProgressEvent{
				{Kind: duplicacy.ProgressKindStep, Bytes: 1},
				{Kind: duplicacy.ProgressKindEnd},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &backupProgress{start: func(int64) ci.ProgressBar {
				panic("bar is not created")
			}}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("backupProgress.event() did not panic on the start")
					}
				}()
				p.event(duplicacy.ProgressEvent{
					Kind: duplicacy.ProgressKindStart, TotalBytes: 10,
				})
			}()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for _, event := range tt.events {
					p.event(event)
				}
				p.finish(errBackupInterrupted)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("backupProgress.finish() hangs after the panic of the start")
			}
		})
	}
}
