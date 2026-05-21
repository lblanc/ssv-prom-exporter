// Package promclip implements prom-clip: a small web tool that exports a
// time-window of Prometheus data to OpenMetrics (.txt.gz) and replays it
// into a target Prometheus instance via the remote-write protocol.
//
// The UI is a tiny html/template-based site (Connection / Export /
// Import / Status). State is persisted as JSON under an OS-native state
// directory (see DefaultStateDir): %LOCALAPPDATA%\prom-clip on Windows,
// ~/.local/state/prom-clip on Linux/macOS, both with chmod 600 on the
// state.json file.
package promclip

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Connection holds the coordinates of a single Prometheus endpoint.
// The same connection acts as the source in Export mode and as the
// target (remote-write receiver) in Import mode.
type Connection struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// S3Target is the user-configured S3-compatible destination for the
// optional "push to S3" step at the end of an export. Empty Bucket
// means S3 is not configured.
type S3Target struct {
	Endpoint      string `json:"endpoint"`         // host or host:port, no scheme
	Region        string `json:"region,omitempty"` // defaults to "us-east-1"
	Bucket        string `json:"bucket"`
	Prefix        string `json:"prefix,omitempty"` // optional key prefix
	AccessKey     string `json:"access_key,omitempty"`
	SecretKey     string `json:"secret_key,omitempty"`
	UseSSL        bool   `json:"use_ssl"`         // toggle https://
	PathStyle     bool   `json:"path_style"`      // path-style addressing (true for most non-AWS S3)
	Public        bool   `json:"public"`          // return a direct URL instead of a presigned one
	PublicBaseURL string `json:"public_base_url,omitempty"`
}

// Kind enumerates the two run types.
const (
	KindExport = "export"
	KindImport = "import"
)

// Status enumerates run lifecycle states.
const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// Run is the persisted record of one export or import.
type Run struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Export-specific.
	SourceURL   string    `json:"source_url,omitempty"`
	From        time.Time `json:"from,omitempty"`
	To          time.Time `json:"to,omitempty"`
	Step        string    `json:"step,omitempty"`
	MetricRegex string    `json:"metric_regex,omitempty"`
	OutputPath  string    `json:"output_path,omitempty"`
	ShareURL    string    `json:"share_url,omitempty"`
	Series      int       `json:"series,omitempty"`
	Samples     int64     `json:"samples,omitempty"`
	Bytes       int64     `json:"bytes,omitempty"`

	// Import-specific.
	InputPath string `json:"input_path,omitempty"`
	TargetURL string `json:"target_url,omitempty"`
}

// State is the persisted store: the single Prometheus connection, the
// optional S3 target, and the run history.
type State struct {
	LastConnection Connection `json:"last_connection"`
	S3             S3Target   `json:"s3,omitempty"`
	Runs           []Run      `json:"runs"`

	mu   sync.Mutex
	path string
}

// stateOnDisk mirrors State but also carries the legacy split fields
// (last_source / last_target). It is used only at load time so older
// state.json files migrate transparently into LastConnection.
type stateOnDisk struct {
	LastConnection Connection `json:"last_connection"`
	S3             S3Target   `json:"s3,omitempty"`
	LegacySource   Connection `json:"last_source,omitempty"`
	LegacyTarget   Connection `json:"last_target,omitempty"`
	Runs           []Run      `json:"runs"`
}

// LoadState reads (or creates) the state file at path. Missing file is
// not an error; callers get an empty State whose Save() writes to path.
// The file is created/maintained with 0600 perms since it may carry
// Prometheus basic-auth credentials. State written by an older version
// (with separate last_source / last_target fields) is migrated in place
// on the first save: the first non-empty of the two becomes the unified
// last_connection.
func LoadState(path string) (*State, error) {
	s := &State{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var d stateOnDisk
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	s.LastConnection = d.LastConnection
	s.S3 = d.S3
	s.Runs = d.Runs
	migrated := false
	if s.LastConnection.URL == "" {
		switch {
		case d.LegacySource.URL != "":
			s.LastConnection = d.LegacySource
			migrated = true
		case d.LegacyTarget.URL != "":
			s.LastConnection = d.LegacyTarget
			migrated = true
		}
	} else if d.LegacySource.URL != "" || d.LegacyTarget.URL != "" {
		// Legacy fields present alongside the unified one: drop them.
		migrated = true
	}
	if migrated {
		s.mu.Lock()
		_ = s.save()
		s.mu.Unlock()
	}
	return s, nil
}

// save writes the state file atomically with 0600 perms. Caller holds mu.
func (s *State) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}

// SetLastConnection records the last Prometheus the user configured.
// The same value is reused as export source and import target.
func (s *State) SetLastConnection(c Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastConnection = c
	return s.save()
}

// Snapshot returns a copy of the current LastConnection.
func (s *State) Snapshot() Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastConnection
}

// SetS3 records the S3 target configuration.
func (s *State) SetS3(t S3Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.S3 = t
	return s.save()
}

// S3Snapshot returns a copy of the current S3 target.
func (s *State) S3Snapshot() S3Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.S3
}

// AddRun inserts r at the head of the run history (newest first) and
// trims to the last 50 runs.
func (s *State) AddRun(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Runs = append([]Run{r}, s.Runs...)
	const maxRuns = 50
	if len(s.Runs) > maxRuns {
		s.Runs = s.Runs[:maxRuns]
	}
	return s.save()
}

// UpdateRun applies mutate to the run with id; persists on success.
// Returns os.ErrNotExist if no such run.
func (s *State) UpdateRun(id string, mutate func(*Run)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Runs {
		if s.Runs[i].ID == id {
			mutate(&s.Runs[i])
			return s.save()
		}
	}
	return os.ErrNotExist
}

// ListRuns returns a copy of the run history.
func (s *State) ListRuns() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Run, len(s.Runs))
	copy(out, s.Runs)
	return out
}

// GetRun returns the run with id, or nil if not found.
func (s *State) GetRun(id string) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Runs {
		if s.Runs[i].ID == id {
			cp := s.Runs[i]
			return &cp
		}
	}
	return nil
}

// DefaultStateDir resolves the OS-native location for state.json and
// the exports directory. On Windows it follows the Windows convention
// (%LOCALAPPDATA%\prom-clip via os.UserCacheDir); elsewhere it follows
// XDG (~/.local/state/prom-clip, honoring $XDG_STATE_HOME).
func DefaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "prom-clip")
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserCacheDir(); err == nil {
			return filepath.Join(d, "prom-clip")
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "prom-clip")
	}
	return filepath.Join(os.TempDir(), "prom-clip")
}
