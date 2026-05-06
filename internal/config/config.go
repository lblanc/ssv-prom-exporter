// Package config defines the YAML configuration schema for
// ssv-prom-exporter and a small loader.
//
// Config values are optional: any field left zero/nil falls through to
// the corresponding flag (and the flag's env-var default). Precedence
// at runtime is:
//
//   explicit command-line flag > matching env var > YAML config > built-in default
//
// The merge logic lives in cmd/ssv-prom-exporter; this package just
// reads and validates the file.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config mirrors the runtime knobs that can be persisted in a YAML file.
// Pointer types (*bool) preserve the "field not set" semantics needed
// for booleans whose default is true.
type Config struct {
	URL        string `yaml:"url"`
	User       string `yaml:"user"`
	Pass       string `yaml:"pass"`
	Host       string `yaml:"host"`
	Insecure   *bool  `yaml:"insecure"`
	Listen     string `yaml:"listen"`

	Bases       []string `yaml:"bases"`
	BackupCIDRs []string `yaml:"backup_cidrs"`

	PerfWorkers int `yaml:"perf_workers"`

	// Retries is the number of retries on transient failures after every
	// configured endpoint has been tried once. Use a *int so absent in
	// YAML stays distinct from explicit 0 ("never retry").
	Retries    *int          `yaml:"retries"`
	RetryDelay time.Duration `yaml:"retry_delay"`

	SvcName        string `yaml:"svc_name"`
	SvcDisplay     string `yaml:"svc_display"`
	SvcDescription string `yaml:"svc_description"`
}

// Load reads and parses a YAML config file. Unknown keys are rejected
// so a typo doesn't silently leave a setting at its default.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var c Config
	if err := dec.Decode(&c); err != nil {
		// An empty file is not an error — it just means "no overrides".
		if errors.Is(err, io.EOF) {
			return &c, nil
		}
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return &c, nil
}
