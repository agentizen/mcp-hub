package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level dispatcher configuration.
type Config struct {
	Subprocesses []SubprocessConfig      `yaml:"subprocesses"`
	Remotes      []RemoteConfig          `yaml:"remotes"`
	Handles      map[string]HandleConfig `yaml:"handles"`
}

// MaxTimeoutSeconds is the largest allowed explicit timeout_seconds value
// (5 minutes — matches RequestForwardTimeout, the global fallback).
const MaxTimeoutSeconds = 300

// validateTimeoutSeconds enforces the 0 < t <= 5min rule. A zero value
// means "unset" (fall back) and is always valid; negatives and values
// beyond the global forward bound are rejected.
func validateTimeoutSeconds(where string, t int) error {
	if t < 0 {
		return fmt.Errorf("config: %s: timeout_seconds must be >= 0, got %d", where, t)
	}
	if t > MaxTimeoutSeconds {
		return fmt.Errorf("config: %s: timeout_seconds %d exceeds %d (5 min), use 0 for the global default", where, t, MaxTimeoutSeconds)
	}
	return nil
}

// validateMaxResponseBytes rejects negative explicit caps; zero means
// "unset" and is valid.
func validateMaxResponseBytes(where string, b int) error {
	if b < 0 {
		return fmt.Errorf("config: %s: max_response_bytes must be >= 0, got %d", where, b)
	}
	return nil
}

// SubprocessConfig describes a local subprocess backend.
type SubprocessConfig struct {
	Name             string            `yaml:"name"`
	Type             string            `yaml:"type"` // "node" | "python" — informational only
	Port             int               `yaml:"port"`
	Path             string            `yaml:"path,omitempty"` // upstream URL path; defaults to /mcp
	Cwd              string            `yaml:"cwd"`
	Command          []string          `yaml:"command"`
	Env              map[string]string `yaml:"env,omitempty"`
	TimeoutSeconds   int               `yaml:"timeout_seconds,omitempty"`    // 0 = use global default
	MaxResponseBytes int               `yaml:"max_response_bytes,omitempty"` // 0 = use global default
}

// RemoteConfig describes a vendor-hosted MCP backend reachable over HTTPS.
type RemoteConfig struct {
	Name             string `yaml:"name"`
	URL              string `yaml:"url"`
	TimeoutSeconds   int    `yaml:"timeout_seconds,omitempty"`    // 0 = use global default
	MaxResponseBytes int    `yaml:"max_response_bytes,omitempty"` // 0 = use global default
}

// HandleConfig is the public routing entry for a single handle.
//
// Exactly one of Subprocess or Remote must be set. Tools, when non-empty,
// is the allow-list that filters `tools/list` responses and gates
// `tools/call` requests.
type HandleConfig struct {
	Subprocess       string   `yaml:"subprocess,omitempty"`
	Remote           string   `yaml:"remote,omitempty"`
	Tools            []string `yaml:"tools,omitempty"`
	TimeoutSeconds   int      `yaml:"timeout_seconds,omitempty"`    // 0 = fall back to backend / global default
	MaxResponseBytes int      `yaml:"max_response_bytes,omitempty"` // 0 = fall back to backend / global default

	// ToolSet is derived from Tools at load time. Empty means pass-through.
	ToolSet map[string]bool `yaml:"-"`

	// ResolvedTimeout is the effective per-handle forward timeout derived
	// at load time (handle → backend → RequestForwardTimeout).
	ResolvedTimeout time.Duration `yaml:"-"`

	// ResolvedMaxBytes is the effective response-size cap derived at load
	// time (handle → backend → MaxResponseBodyBytes).
	ResolvedMaxBytes int `yaml:"-"`
}

// LoadConfig reads the given path, strictly decodes YAML, and runs all
// validation rules. The returned Config is safe to use as an immutable
// snapshot for the dispatcher's lifetime.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 — operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: decode %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	subByName := make(map[string]SubprocessConfig, len(cfg.Subprocesses))
	for _, sp := range cfg.Subprocesses {
		subByName[sp.Name] = sp
	}
	remoteByName := make(map[string]RemoteConfig, len(cfg.Remotes))
	for _, r := range cfg.Remotes {
		remoteByName[r.Name] = r
	}

	for name, h := range cfg.Handles {
		if len(h.Tools) > 0 {
			set := make(map[string]bool, len(h.Tools))
			for _, t := range h.Tools {
				set[t] = true
			}
			h.ToolSet = set
		}

		// Resolve per-handle timeout with nearest-global semantics:
		// handle → backend → RequestForwardTimeout.
		timeoutSeconds := h.TimeoutSeconds
		if timeoutSeconds <= 0 {
			if h.Subprocess != "" {
				if sp, ok := subByName[h.Subprocess]; ok {
					timeoutSeconds = sp.TimeoutSeconds
				}
			} else if h.Remote != "" {
				if r, ok := remoteByName[h.Remote]; ok {
					timeoutSeconds = r.TimeoutSeconds
				}
			}
		}
		if timeoutSeconds > 0 {
			h.ResolvedTimeout = time.Duration(timeoutSeconds) * time.Second
		} else {
			h.ResolvedTimeout = RequestForwardTimeout
		}

		// Resolve per-handle response cap: handle → backend →
		// MaxResponseBodyBytes.
		maxBytes := h.MaxResponseBytes
		if maxBytes <= 0 {
			if h.Subprocess != "" {
				if sp, ok := subByName[h.Subprocess]; ok {
					maxBytes = sp.MaxResponseBytes
				}
			} else if h.Remote != "" {
				if r, ok := remoteByName[h.Remote]; ok {
					maxBytes = r.MaxResponseBytes
				}
			}
		}
		h.ResolvedMaxBytes = maxBytes
		if h.ResolvedMaxBytes <= 0 {
			h.ResolvedMaxBytes = MaxResponseBodyBytes
		}

		cfg.Handles[name] = h
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	subByName := make(map[string]struct{}, len(c.Subprocesses))
	seenPorts := make(map[int]string, len(c.Subprocesses))
	for i, sp := range c.Subprocesses {
		if sp.Name == "" {
			return fmt.Errorf("config: subprocesses[%d]: missing name", i)
		}
		if _, dup := subByName[sp.Name]; dup {
			return fmt.Errorf("config: subprocesses: duplicate name %q", sp.Name)
		}
		subByName[sp.Name] = struct{}{}

		if len(sp.Command) == 0 {
			return fmt.Errorf("config: subprocesses[%q]: command must be non-empty", sp.Name)
		}
		if sp.Port <= 0 || sp.Port > 65535 {
			return fmt.Errorf("config: subprocesses[%q]: invalid port %d", sp.Name, sp.Port)
		}
		if sp.Port == DefaultHTTPPort {
			return fmt.Errorf("config: subprocesses[%q]: port %d collides with dispatcher", sp.Name, sp.Port)
		}
		if owner, dup := seenPorts[sp.Port]; dup {
			return fmt.Errorf("config: subprocesses[%q]: port %d already used by %q", sp.Name, sp.Port, owner)
		}
		seenPorts[sp.Port] = sp.Name
		if err := validateTimeoutSeconds(fmt.Sprintf("subprocesses[%q]", sp.Name), sp.TimeoutSeconds); err != nil {
			return err
		}
		if err := validateMaxResponseBytes(fmt.Sprintf("subprocesses[%q]", sp.Name), sp.MaxResponseBytes); err != nil {
			return err
		}
	}

	remoteByName := make(map[string]struct{}, len(c.Remotes))
	for i, r := range c.Remotes {
		if r.Name == "" {
			return fmt.Errorf("config: remotes[%d]: missing name", i)
		}
		if _, dup := remoteByName[r.Name]; dup {
			return fmt.Errorf("config: remotes: duplicate name %q", r.Name)
		}
		if r.URL == "" {
			return fmt.Errorf("config: remotes[%q]: missing url", r.Name)
		}
		remoteByName[r.Name] = struct{}{}
		if err := validateTimeoutSeconds(fmt.Sprintf("remotes[%q]", r.Name), r.TimeoutSeconds); err != nil {
			return err
		}
		if err := validateMaxResponseBytes(fmt.Sprintf("remotes[%q]", r.Name), r.MaxResponseBytes); err != nil {
			return err
		}
	}

	for name, h := range c.Handles {
		if name == "" {
			return fmt.Errorf("config: handles: empty handle name")
		}
		hasSub := h.Subprocess != ""
		hasRemote := h.Remote != ""
		if hasSub == hasRemote {
			return fmt.Errorf("config: handles[%q]: exactly one of subprocess/remote must be set", name)
		}
		if hasSub {
			if _, ok := subByName[h.Subprocess]; !ok {
				return fmt.Errorf("config: handles[%q]: unknown subprocess %q", name, h.Subprocess)
			}
		}
		if hasRemote {
			if _, ok := remoteByName[h.Remote]; !ok {
				return fmt.Errorf("config: handles[%q]: unknown remote %q", name, h.Remote)
			}
		}
		if err := validateTimeoutSeconds(fmt.Sprintf("handles[%q]", name), h.TimeoutSeconds); err != nil {
			return err
		}
		if err := validateMaxResponseBytes(fmt.Sprintf("handles[%q]", name), h.MaxResponseBytes); err != nil {
			return err
		}
	}

	return nil
}

// HandleNames returns a sorted snapshot of the configured handles.
func (c *Config) HandleNames() []string {
	names := make([]string, 0, len(c.Handles))
	for name := range c.Handles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
