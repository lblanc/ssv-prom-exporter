// Package promclip implements prom-clip: a small web tool that exports a
// time-window of Prometheus data to OpenMetrics (.txt.gz) and replays it
// into a target Prometheus instance via the remote-write protocol.
//
// The UI is a tiny html/template-based site (Connection / Export /
// Import / Status). State is persisted as JSON under a state directory
// (default ~/.local/state/prom-clip/state.json, chmod 600).
package promclip

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Connection holds the coordinates of a Prometheus endpoint used either
// as the source of an export or the target of an import.
type Connection struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
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

// State is the persisted store: last connections and run history.
type State struct {
	LastSource Connection `json:"last_source"`
	LastTarget Connection `json:"last_target"`
	Runs       []Run      `json:"runs"`

	mu   sync.Mutex
	path string
}

// LoadState reads (or creates) the state file at path. Missing file is
// not an error; callers get an empty State whose Save() writes to path.
// The file is created/maintained with 0600 perms since it may carry
// Prometheus basic-auth credentials.
func LoadState(path string) (*State, error) {
	s := &State{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
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

// SetLastSource records the last Prometheus used as an export source.
func (s *State) SetLastSource(c Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSource = c
	return s.save()
}

// SetLastTarget records the last Prometheus used as an import target.
func (s *State) SetLastTarget(c Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastTarget = c
	return s.save()
}

// Snapshot returns a copy of the current LastSource / LastTarget.
func (s *State) Snapshot() (src, tgt Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastSource, s.LastTarget
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

// DefaultStateDir is ~/.local/state/prom-clip (XDG-ish), with $HOME
// resolved via os.UserHomeDir().
func DefaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "prom-clip")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "prom-clip")
	}
	return filepath.Join(os.TempDir(), "prom-clip")
}
