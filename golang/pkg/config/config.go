// Package config loads the configuration file.
package config

import (
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/megalomania428/mega-doub/pkg/secret"
)

// Default directories, the instance name is appended to them.
const (
	defaultCopyDir  = "/var/lib/mega-doub/copy/"
	defaultStateDir = "/var/lib/mega-doub/state/"
)

// Defaults of global.retry, equal to those of go-lib-ci.
const (
	defaultMaxRetriesTime      = 111 * time.Second
	defaultBackoffInitial      = time.Second
	defaultBackoffStep         = time.Second
	defaultBackoffStepAttempts = 3
	defaultBackoffCap          = 7 * time.Second
)

// Config is the whole configuration file.
type Config struct {
	// Templates holds anchors for reuse, its content is not checked by itself.
	Templates yaml.Node `yaml:"templates,omitempty"`
	Global    Global    `yaml:"global"`
	Vault     Vault     `yaml:"vault"`
	Storage   Storage   `yaml:"storage"`
	Prune     Prune     `yaml:"prune"`
	Check     Check     `yaml:"check"`
	Targets   []Target  `yaml:"targets"`
}

// Global holds the settings shared by all targets.
type Global struct {
	Instance        string    `yaml:"instance"`
	CopyDir         string    `yaml:"copy_dir"`
	StateDir        string    `yaml:"state_dir"`
	ParallelTargets int       `yaml:"parallel_targets"`
	ParallelSources int       `yaml:"parallel_sources"`
	LogLevel        LogLevel  `yaml:"log_level"`
	LogFormat       LogFormat `yaml:"log_format"`
	Progress        string    `yaml:"progress"`
	Timeout         Duration  `yaml:"timeout"`
	Retry           Retry     `yaml:"retry"`
}

// Retry holds the retry settings with the defaults of go-lib-ci. A zero
// max_retries_time selects the default and a negative one disables the retries, a zero
// or negative backoff value selects its default; explicit values are kept as written.
type Retry struct {
	MaxRetriesTime      Duration `yaml:"max_retries_time"`
	BackoffInitial      Duration `yaml:"backoff_initial"`
	BackoffStep         Duration `yaml:"backoff_step"`
	BackoffStepAttempts int      `yaml:"backoff_step_attempts"`
	BackoffCap          Duration `yaml:"backoff_cap"`
}

// Vault holds the connection to the secret store.
type Vault struct {
	Address string     `yaml:"address,omitempty"`
	Token   secret.Ref `yaml:"token,omitempty"`
	Timeout Duration   `yaml:"timeout"`
	CAFile  string     `yaml:"ca_file,omitempty"`
}

// Storage holds the backup storage settings.
type Storage struct {
	Name       string                `yaml:"name"`
	URL        string                `yaml:"url,omitempty"`
	SnapshotID string                `yaml:"snapshot_id"`
	Password   secret.Ref            `yaml:"password,omitempty"`
	Threads    int                   `yaml:"threads"`
	Tag        string                `yaml:"tag"`
	Keys       map[string]secret.Ref `yaml:"keys,omitempty"`
}

// Prune holds the retention settings.
type Prune struct {
	Enabled   bool     `yaml:"enabled"`
	Keep      []string `yaml:"keep"`
	Threads   int      `yaml:"threads"`
	Exclusive bool     `yaml:"exclusive"`
}

// Check holds the storage integrity check settings.
type Check struct {
	Enabled          bool     `yaml:"enabled"`
	Every            Duration `yaml:"every"`
	Chunks           bool     `yaml:"chunks"`
	Files            bool     `yaml:"files"`
	TestRestore      bool     `yaml:"test_restore"`
	TestRestoreBytes Size     `yaml:"test_restore_bytes"`
}

// Target is one backed up host with its sources.
type Target struct {
	TargetSettings `yaml:",inline"`
	Sources        []SourceConfig `yaml:"sources"`
}

// TargetSettings holds the target fields except its sources.
type TargetSettings struct {
	Name           string     `yaml:"name"`
	Transport      string     `yaml:"transport"`
	Host           string     `yaml:"host,omitempty"`
	Port           int        `yaml:"port,omitempty"`
	User           string     `yaml:"user,omitempty"`
	Auth           []string   `yaml:"auth,omitempty"`
	Key            secret.Ref `yaml:"key,omitempty"`
	KeyPassphrase  secret.Ref `yaml:"key_passphrase,omitempty"`
	Password       secret.Ref `yaml:"password,omitempty"`
	KnownHosts     string     `yaml:"known_hosts,omitempty"`
	ConnectTimeout Duration   `yaml:"connect_timeout,omitempty"`
	Sudo           bool       `yaml:"sudo"`
}

// defaultConfig returns the configuration with the defaults of all sections.
// The directories depend on the instance and are set after loading.
func defaultConfig() *Config {
	return &Config{
		Global: Global{
			Instance:        "main",
			ParallelTargets: 4,
			ParallelSources: 2,
			LogLevel:        "info",
			LogFormat:       "text",
			Progress:        "auto",
			Timeout:         Duration(12 * time.Hour),
			Retry: Retry{
				MaxRetriesTime:      Duration(defaultMaxRetriesTime),
				BackoffInitial:      Duration(defaultBackoffInitial),
				BackoffStep:         Duration(defaultBackoffStep),
				BackoffStepAttempts: defaultBackoffStepAttempts,
				BackoffCap:          Duration(defaultBackoffCap),
			},
		},
		Vault:   Vault{Timeout: Duration(30 * time.Second)},
		Storage: Storage{Name: "default", Threads: 4, Tag: "mega-doub"},
		Prune:   Prune{Keep: []string{}, Threads: 4},
		Check:   Check{Every: Duration(168 * time.Hour), TestRestoreBytes: 256 << 20},
	}
}

// defaultTargetSettings returns the defaults of one target.
func defaultTargetSettings() TargetSettings {
	return TargetSettings{
		Port:           22,
		Auth:           defaultAuth(),
		ConnectTimeout: Duration(10 * time.Second),
	}
}

func defaultAuth() []string {
	return []string{"agent"}
}
