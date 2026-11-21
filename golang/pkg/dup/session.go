// cspell:ignore duplicacy perr unescape unsetenv

// Package dup is documented in log.go.
package dup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	duplicacy "duplicacy.invalid/v3/src"
	ci "github.com/megalomania428/go-lib-ci"

	"github.com/megalomania428/mega-doub/pkg/retry"
)

// The defaults of the options of Open.
const (
	defaultStorageName = "default"
	defaultThreads     = 4
)

// The chunk sizes of the command init of the duplicacy CLI without flags; WithInit
// initializes the storage with them.
const (
	averageChunkSize = 4 << 20
	maximumChunkSize = 16 << 20
	minimumChunkSize = 1 << 20
)

// preferenceDir is the directory of the preferences of duplicacy in the state directory.
const preferenceDir = "duplicacy"

// envPrefix starts the names of the variables of the environment that duplicacy prefers
// to the keys of the storage; on Windows the names are compared without the case, as
// duplicacy finds them.
const envPrefix = "DUPLICACY_"

// keyPassword is the key of the storage password in the keys of the preference.
const keyPassword = "password"

// operationInit starts the name of the attempts of Open, which create the storage and
// read its config; the storage name completes it.
const operationInit = "storage init"

// The operations of the calls of duplicacy.
const (
	opCreateStorage   = "create storage"
	opDownloadConfig  = "download config"
	opConfigStorage   = "config storage"
	opCreateDirectory = "create directory"
)

// subdirectories are the subdirectories of the storage that ConfigStorage creates.
var subdirectories = []string{"chunks", "snapshots"}

// The msg of the records of a session.
const (
	msgRequestFailed = "storage request failed"
	msgEnvRemoved    = "duplicacy environment removed"
)

var (
	errSessionOpen   = errors.New("a session of duplicacy is already open")
	errUnencrypted   = errors.New("storage is not encrypted")
	errWrongPassword = errors.New("config of the storage cannot be decrypted: " +
		"wrong password or damaged config")
	errUnknownConfig  = errors.New("config of the storage has an unknown format")
	errNotInitialized = errors.New("storage is not initialized")
	errConfigMissing  = errors.New("config of the storage is not found after its " +
		"initialization")
)

// createStorage is duplicacy.CreateStorage; tests replace it.
var createStorage = duplicacy.CreateStorage

// envCaseless tells that the names of the variables of the environment are compared
// without the case, as on Windows; tests replace it.
var envCaseless = runtime.GOOS == "windows"

// openMu is held while a session is open: duplicacy keeps its state in globals, so a
// process has at most one session.
var openMu sync.Mutex

// SessionOption is an option of Open.
type SessionOption func(*sessionOptions)

// sessionOptions are the settings of Open.
type sessionOptions struct {
	copyDir     string
	stateDir    string
	storageName string
	url         string
	snapshotID  string
	password    string
	keys        map[string]string
	threads     int
	logger      *slog.Logger
	retry       retry.Options
	initStorage bool
	// progress is set by WithProgress, with its writer and the names of its elements.
	progress         bool
	progressWriter   io.Writer
	progressElements []string
}

// WithCopyDir sets the directory of the copy, the repository of duplicacy; it is
// required.
func WithCopyDir(dir string) SessionOption {
	return func(o *sessionOptions) { o.copyDir = dir }
}

// WithStateDir sets the state directory; the preferences of duplicacy are kept in its
// subdirectory duplicacy. It is required.
func WithStateDir(dir string) SessionOption {
	return func(o *sessionOptions) { o.stateDir = dir }
}

// WithStorageName sets the name of the storage; it is default without the option.
func WithStorageName(name string) SessionOption {
	return func(o *sessionOptions) { o.storageName = name }
}

// WithURL sets the URL of the storage; it is required.
func WithURL(storageURL string) SessionOption {
	return func(o *sessionOptions) { o.url = storageURL }
}

// WithSnapshotID sets the snapshot ID; it is required.
func WithSnapshotID(id string) SessionOption {
	return func(o *sessionOptions) { o.snapshotID = id }
}

// WithPassword sets the storage password, the resolved value of the secret; it is
// required.
func WithPassword(password string) SessionOption {
	return func(o *sessionOptions) { o.password = password }
}

// WithKeys adds the keys of the storage, the resolved values by the names of
// storage.keys without the prefix of the storage name. The option keeps its own copy
// of keys, and a repeated WithKeys adds its keys to the earlier ones, replacing the
// ones with the same name.
func WithKeys(keys map[string]string) SessionOption {
	keys = maps.Clone(keys)
	return func(o *sessionOptions) {
		if o.keys == nil {
			o.keys = make(map[string]string, len(keys))
		}
		maps.Copy(o.keys, keys)
	}
}

// WithThreads sets the number of threads of the storage; it is 4 without the option
// and must be at least 1.
func WithThreads(threads int) SessionOption {
	return func(o *sessionOptions) { o.threads = threads }
}

// WithLogger sets the logger of the records of the session and of duplicacy; nil, as
// well as no option, discards the records.
func WithLogger(logger *slog.Logger) SessionOption {
	return func(o *sessionOptions) { o.logger = logger }
}

// WithRetry sets the retries of the initialization of the storage; the zero value, as
// well as no option, means the defaults of go-lib-ci.
func WithRetry(opts retry.Options) SessionOption {
	return func(o *sessionOptions) { o.retry = opts }
}

// WithInit lets Open initialize the storage, as the command init of the duplicacy CLI
// does: a storage without a config gets an encrypted config with the defaults of the
// CLI and the subdirectories chunks and snapshots, and a storage with a config is
// opened as without the option. The limits of the CLI stay: the first initialization of
// a shared storage must be made once and from one host, and a backend that answers an
// error with a missing config gets its config overwritten, which makes the old content
// unreadable: S3 and S3C on the statuses 403 and 404, Azure on an error with 404 in its
// text, and Storj on any error.
func WithInit() SessionOption {
	return func(o *sessionOptions) { o.initStorage = true }
}

// WithProgress makes Backup draw a progress bar of go-lib-ci to w with the elements,
// the names of global.progress: bytes, elapsed, rate, bar, percent and eta. The bar
// counts the bytes of the file chunks against the size of the changed files and is
// finished before Backup returns; go-lib-ci draws it with mpb when w is a terminal and
// with lines of text otherwise. No elements, as well as no option, draw nothing, and
// Prune draws no bar. w is required with the option and an unknown element is an
// error of Open; the option keeps its own copy of elements. The bar writes to w from
// its own goroutine, so a w shared with the logger of the session or with other
// writers must be safe for concurrent writes, as os.Stderr is, and a buffer of a test
// must be synchronized.
func WithProgress(w io.Writer, elements ...string) SessionOption {
	elements = slices.Clone(elements)
	return func(o *sessionOptions) {
		o.progress = true
		o.progressWriter = w
		o.progressElements = elements
	}
}

// validate checks the required values, the number of threads and the elements of the
// progress bar.
func (o *sessionOptions) validate() error {
	switch {
	case o.copyDir == "":
		return errors.New("copy directory is required")
	case o.stateDir == "":
		return errors.New("state directory is required")
	case o.storageName == "":
		return errors.New("storage name is required")
	case o.url == "":
		return errors.New("storage URL is required")
	case o.snapshotID == "":
		return errors.New("snapshot ID is required")
	case o.password == "":
		return errors.New("storage password is required")
	case o.threads < 1:
		return errors.New("storage threads must be at least 1")
	case o.progress && o.progressWriter == nil:
		return errors.New("progress writer is required")
	}
	for _, name := range o.progressElements {
		if _, ok := progressElements[name]; !ok {
			return fmt.Errorf("unknown progress element %q", name)
		}
	}
	return nil
}

// Session is the open session of duplicacy of the process with its storage created
// and initialized.
type Session struct {
	copyDir    string
	stateDir   string
	operation  string
	preference duplicacy.Preference
	password   string
	threads    int
	retry      retry.Options
	logger     *slog.Logger
	masks      []string
	storage    duplicacy.Storage
	background bool
	// checkRedirect is the CheckRedirect of http.DefaultClient before Open, which the
	// WebDAV backend of duplicacy replaces.
	checkRedirect func(*http.Request, []*http.Request) error
	// initStorage is set by WithInit.
	initStorage bool
	// progressWriter and progress are the writer and the elements of the progress bar of
	// Backup; no elements draw no bar.
	progressWriter io.Writer
	progress       []ci.ProgressElement
	// subdirsPending is set before ConfigStorage starts the initialization and is
	// cleared when the subdirectories are confirmed: ConfigStorage has created them or
	// createSubdirectories has. A later attempt that reads the config creates them
	// while it is set, as the upload of the config may have succeeded without them.
	subdirsPending bool
	closeOnce      sync.Once
}

// Error is an Exception of duplicacy that a call of the operation Op panicked with.
type Error struct {
	Op      string // operation, such as create storage
	LogID   string // log ID of the Exception
	Message string // message of the Exception with the secrets replaced with ***
	Err     error  // error of the Exception, nil when it has none
}

// Error returns the operation and the log ID only: the message may hold the storage
// URL, and the cause is left to Unwrap.
func (e *Error) Error() string {
	return e.Op + ": duplicacy " + e.LogID
}

// Unwrap returns the error of the Exception.
func (e *Error) Unwrap() error {
	return e.Err
}

// maskedError replaces the secrets of the session in the text of err with *** and
// keeps err for errors.Is and errors.As.
type maskedError struct {
	err   error
	masks []string
}

func (e *maskedError) Error() string {
	return mask(e.err.Error(), e.masks)
}

func (e *maskedError) Unwrap() error {
	return e.err
}

// readError marks an error of reading the config file, so that it is told apart from
// the errors of decrypting and parsing the file.
type readError struct {
	Err error
}

func (e *readError) Error() string {
	return e.Err.Error()
}

func (e *readError) Unwrap() error {
	return e.Err
}

// configReader passes the calls to Storage and marks the errors of the two methods
// that DownloadConfig reads the config file with; it is used only for DownloadConfig.
type configReader struct {
	duplicacy.Storage
}

func (r *configReader) GetFileInfo(
	threadIndex int, filePath string,
) (exist, isDir bool, size int64, err error) {
	exist, isDir, size, err = r.Storage.GetFileInfo(threadIndex, filePath)
	if err != nil {
		err = &readError{Err: err}
	}
	return exist, isDir, size, err
}

func (r *configReader) DownloadFile(
	threadIndex int, filePath string, chunk *duplicacy.Chunk,
) error {
	if err := r.Storage.DownloadFile(threadIndex, filePath, chunk); err != nil {
		return &readError{Err: err}
	}
	return nil
}

// Open opens the session of duplicacy of the process: it sets the global state of
// duplicacy, removes the variables DUPLICACY_* from the environment, so that they do
// not override the keys, creates the storage and reads its config. A storage without a
// config is an error at once without retries, and the session itself writes nothing to
// the storage, unless WithInit lets it initialize the storage; creating the storage may
// still create its directory or its subdirectories, as some backends do, such as the
// local one, which creates a missing directory, the same as the commands of the CLI.
// The attempts that fail with a transient error of the storage are retried within the
// budget of WithRetry, and each such failure is a warning record storage request
// failed; canceling ctx stops the wait between the attempts. Opening a second session
// before Close is an error, and so is a missing required option, which is checked
// without any access to the storage. During the session the changes of the backends in
// net/http stay, such as the redirect policy of http.DefaultClient set by WebDAV, and
// Close gives them back. On an error or a panic the global state is given back, and the
// text of the error holds no secrets; the errors it wraps keep their own texts.
func Open(ctx context.Context, opts ...SessionOption) (*Session, error) {
	o := sessionOptions{storageName: defaultStorageName, threads: defaultThreads}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	if !openMu.TryLock() {
		return nil, errSessionOpen
	}
	s := newSession(&o)
	opened := false
	defer func() {
		if !opened {
			s.Close()
		}
	}()
	s.start()
	err := s.prepare()
	if err == nil {
		err = s.initialize(ctx)
	}
	if err != nil {
		err = fmt.Errorf("%s: %w", s.operation, err)
		return nil, &maskedError{err: err, masks: s.masks}
	}
	opened = true
	return s, nil
}

// newSession returns a session with the settings o.
func newSession(o *sessionOptions) *Session {
	keys := maps.Clone(o.keys)
	if keys == nil {
		keys = make(map[string]string, 1)
	}
	keys[keyPassword] = o.password
	masks := append([]string{o.password}, slices.Collect(maps.Values(o.keys))...)
	masks = append(masks, urlPasswords(o.url)...)
	logger := o.logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	preference := duplicacy.Preference{
		Name:       o.storageName,
		SnapshotID: o.snapshotID,
		StorageURL: o.url,
		Keys:       keys,
	}
	progress := make([]ci.ProgressElement, 0, len(o.progressElements))
	for _, name := range o.progressElements {
		progress = append(progress, progressElements[name])
	}
	return &Session{
		copyDir:        o.copyDir,
		stateDir:       o.stateDir,
		operation:      operationInit + " " + o.storageName,
		preference:     preference,
		password:       o.password,
		threads:        o.threads,
		retry:          o.retry,
		logger:         logger,
		masks:          masks,
		initStorage:    o.initStorage,
		progressWriter: o.progressWriter,
		progress:       progress,
	}
}

// urlPasswords returns the candidates for the password of the userinfo of storageURL,
// as they are written and, when it differs, decoded. The authority, which duplicacy
// ends at the first slash, may hold several @ in a password or in a query, so the text
// between the first colon and each @ is a candidate.
func urlPasswords(storageURL string) []string {
	_, rest, ok := strings.Cut(storageURL, "://")
	if !ok {
		return nil
	}
	authority, _, _ := strings.Cut(rest, "/")
	var passwords []string
	for at := range len(authority) {
		if authority[at] != '@' {
			continue
		}
		_, written, ok := strings.Cut(authority[:at], ":")
		if !ok || written == "" {
			continue
		}
		passwords = append(passwords, written)
		if decoded, err := url.PathUnescape(written); err == nil && decoded != written {
			passwords = append(passwords, decoded)
		}
	}
	return passwords
}

// start sets the global state of duplicacy for the session.
func (s *Session) start() {
	s.background = duplicacy.RunInBackground
	s.checkRedirect = http.DefaultClient.CheckRedirect
	duplicacy.RunInBackground = true
	duplicacy.LogFunction = LogFunction(s.logger, WithMasks(s.masks...))
	duplicacy.ProgressFunction = nil
	duplicacy.ResetCancel()
}

// prepare removes the variables DUPLICACY_* from the environment and creates the
// directory of the preferences of duplicacy. On Windows the names are compared without
// the case, as duplicacy finds them.
func (s *Session) prepare() error {
	var removed []string
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if !hasEnvPrefix(name) {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("unset %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	if len(removed) > 0 {
		s.logger.Debug(msgEnvRemoved, "variables", removed)
	}
	dir := filepath.Join(s.stateDir, preferenceDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the directory of the preferences: %w", err)
	}
	duplicacy.SetDuplicacyPreferencePath(dir)
	return nil
}

// hasEnvPrefix tells whether duplicacy finds the variable with the name as DUPLICACY_*.
func hasEnvPrefix(name string) bool {
	if envCaseless {
		return len(name) >= len(envPrefix) &&
			strings.EqualFold(name[:len(envPrefix)], envPrefix)
	}
	return strings.HasPrefix(name, envPrefix)
}

// initialize runs the attempts to open the storage with the retries of the session.
func (s *Session) initialize(ctx context.Context) error {
	return s.retryAttempts(ctx, attemptRun{operation: s.operation, run: s.attempt})
}

// attemptRun is the attempt run of an operation: the name of the operation of the records
// of its failed attempts and the function that makes an attempt.
type attemptRun struct {
	operation string
	run       func() error
}

// retryAttempts runs the attempts of a with the retries of the session. Every failed
// attempt with a transient error is a warning record, the last one included; a permanent
// error is returned at once, and a canceled ctx wins over the error of the attempts, but
// not over a successful one.
func (s *Session) retryAttempts(ctx context.Context, a attemptRun) error {
	opts := s.retry.CI(a.operation)
	opts.Stderr = io.Discard
	var attempt int
	var final error
	err := ci.Retry(ctx, opts, func(ctx context.Context) error {
		attempt++
		err := a.run()
		if err == nil || ctx.Err() != nil || !transient(err) {
			final = err
			return nil
		}
		s.logger.Warn(msgRequestFailed, "operation", a.operation, "attempt", attempt,
			"error", s.errorText(err))
		return err
	})
	if err == nil && final == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return err
	}
	return final
}

// attempt creates the storage and reads its config. A storage without a config is
// errNotInitialized without WithInit and is initialized with it, and its config is
// read again. While the subdirectories of an initialization that this Open has begun
// are not confirmed, they are created once the config is read, whichever attempt
// finds it.
func (s *Session) attempt() error {
	var storage duplicacy.Storage
	if err := s.call(opCreateStorage, func() {
		storage = createStorage(s.preference, false, s.threads)
	}); err != nil {
		return err
	}
	config, err := s.readConfig(storage)
	if err != nil {
		return err
	}
	if config == nil {
		if !s.initStorage {
			return errNotInitialized
		}
		// The result false means that another client has created the config
		// meanwhile; the config is read again either way.
		s.subdirsPending = true
		var created bool
		if err := s.call(opConfigStorage, func() {
			created = duplicacy.ConfigStorage(storage,
				duplicacy.CONFIG_DEFAULT_ITERATIONS, duplicacy.DEFAULT_COMPRESSION_LEVEL,
				averageChunkSize, maximumChunkSize, minimumChunkSize, s.password, nil,
				false, "", 0, 0)
		}); err != nil {
			return err
		}
		if created {
			s.subdirsPending = false
		}
		if config, err = s.readConfig(storage); err != nil {
			return err
		}
		if config == nil {
			return errConfigMissing
		}
	}
	if s.subdirsPending {
		if err := s.createSubdirectories(storage); err != nil {
			return err
		}
		s.subdirsPending = false
	}
	s.storage = storage
	return nil
}

// createSubdirectories creates the subdirectories of storage; an existing one is not an
// error.
func (s *Session) createSubdirectories(storage duplicacy.Storage) error {
	for _, dir := range subdirectories {
		var err error
		if perr := s.call(opCreateDirectory, func() {
			err = storage.CreateDirectory(0, dir)
		}); perr != nil {
			return perr
		}
		if err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// readConfig reads the config of storage with the password of the session; a missing
// config is nil without an error. An error of reading the file keeps its cause, and a
// config that is not decrypted is told apart by the probe without a password: an
// unencrypted storage, a wrong password or a damaged config, or an unknown format.
func (s *Session) readConfig(storage duplicacy.Storage) (*duplicacy.Config, error) {
	config, encrypted, err := s.downloadConfig(storage, s.password)
	if err == nil {
		return config, nil
	}
	if cause := accessError(err); cause != nil {
		return nil, cause
	}
	if !encrypted {
		return nil, errWrongPassword
	}
	plain, _, err := s.downloadConfig(storage, "")
	if err != nil {
		if cause := accessError(err); cause != nil {
			return nil, cause
		}
		return nil, errUnknownConfig
	}
	if plain != nil {
		return nil, errUnencrypted
	}
	return nil, errUnknownConfig
}

// downloadConfig calls DownloadConfig with the reads of storage marked; an Exception
// is returned as the error.
func (s *Session) downloadConfig(
	storage duplicacy.Storage, password string,
) (config *duplicacy.Config, encrypted bool, err error) {
	if perr := s.call(opDownloadConfig, func() {
		config, encrypted, err = duplicacy.DownloadConfig(
			&configReader{Storage: storage}, password)
	}); perr != nil {
		return nil, false, perr
	}
	return config, encrypted, err
}

// accessError returns the error of err that is not about the content of the config:
// a failed read of the file, with the mark removed, or an Exception of duplicacy. It
// returns nil for the other errors.
func accessError(err error) error {
	if r, ok := errors.AsType[*readError](err); ok {
		return fmt.Errorf("read config: %w", r.Err)
	}
	if _, ok := errors.AsType[*Error](err); ok {
		return err
	}
	return nil
}

// call runs f, a call of duplicacy, and turns the Exception that f panics with into an
// *Error of the operation op; any other panic is a defect and goes on.
func (s *Session) call(op string, f func()) (err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		e, ok := r.(duplicacy.Exception)
		if !ok {
			panic(r)
		}
		message := mask(e.Message, s.masks)
		err = &Error{Op: op, LogID: e.LogID, Message: message, Err: e.Err}
	}()
	f()
	return nil
}

// errorText is the text of err for the field error of a record: the short text of an
// *Error is followed by the text of its cause, and the secrets are replaced with ***.
func (s *Session) errorText(err error) string {
	text := err.Error()
	if e, ok := errors.AsType[*Error](err); ok && e.Err != nil {
		text += ": " + e.Err.Error()
	}
	return mask(text, s.masks)
}

// Close closes the session: it gives the global state of duplicacy back and lets
// another session open, with the changes of the backends in net/http given back. The
// variables DUPLICACY_* removed by Open stay removed. A repeated Close does nothing.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		duplicacy.LogFunction = nil
		duplicacy.ProgressFunction = nil
		duplicacy.ResetCancel()
		duplicacy.RunInBackground = s.background
		http.DefaultClient.CheckRedirect = s.checkRedirect
		s.storage = nil
		openMu.Unlock()
	})
}
