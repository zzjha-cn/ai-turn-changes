// Package config holds the on-disk configuration for codex-rewind.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/zzjha-cn/codex-rewind/internal/fsutil"
)

const SchemaVersion = 1

type Retention struct {
	MaxRetainedTurns int `json:"maxRetainedTurns"`
	CleanAfterDays   int `json:"cleanAfterDays"`
}

type Limits struct {
	MaxPromptBytes       int `json:"maxPromptBytes"`
	MaxAssistantBytes    int `json:"maxAssistantBytes"`
	MaxEventBytesPerTurn int `json:"maxEventBytesPerTurn"`
	MaxEventsPerTurn     int `json:"maxEventsPerTurn"`
	MaxDisplayFiles      int `json:"maxDisplayFiles"`
	MaxDisplayTextChars  int `json:"maxDisplayTextChars"`
}

type Rewind struct {
	DefaultMode  string `json:"defaultMode"`
	AllowInPlace bool   `json:"allowInPlace"`
}

type Hooks struct {
	RecordPostToolUse bool `json:"recordPostToolUse"`
	RecordPreToolUse  bool `json:"recordPreToolUse,omitempty"`
}

type Config struct {
	SchemaVersion int       `json:"schemaVersion"`
	Retention     Retention `json:"retention"`
	Limits        Limits    `json:"limits"`
	Rewind        Rewind    `json:"rewind"`
	Hooks         Hooks     `json:"hooks"`
	StrictHook    bool      `json:"strictHook,omitempty"`
}

// Default returns the built-in configuration described by the design doc.
func Default() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Retention: Retention{
			MaxRetainedTurns: 10,
			CleanAfterDays:   30,
		},
		Limits: Limits{
			MaxPromptBytes:       65536,
			MaxAssistantBytes:    65536,
			MaxEventBytesPerTurn: 1048576,
			MaxEventsPerTurn:     200,
			MaxDisplayFiles:      10,
			MaxDisplayTextChars:  500,
		},
		Rewind: Rewind{
			DefaultMode:  "fork",
			AllowInPlace: false,
		},
		Hooks: Hooks{
			RecordPostToolUse: true,
		},
	}
}

// Load reads config from path. A missing file yields the built-in defaults.
// A malformed file also yields defaults together with the parse error so callers
// can surface it from `doctor` while continuing to work.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.normalize()
	if cfg.SchemaVersion != SchemaVersion {
		return cfg, fmt.Errorf("unsupported config schemaVersion %d (want %d)", cfg.SchemaVersion, SchemaVersion)
	}
	return cfg, nil
}

func (c *Config) normalize() {
	d := Default()
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if c.Retention.MaxRetainedTurns <= 0 {
		c.Retention.MaxRetainedTurns = d.Retention.MaxRetainedTurns
	}
	if c.Retention.CleanAfterDays <= 0 {
		c.Retention.CleanAfterDays = d.Retention.CleanAfterDays
	}
	if c.Limits.MaxPromptBytes <= 0 {
		c.Limits.MaxPromptBytes = d.Limits.MaxPromptBytes
	}
	if c.Limits.MaxAssistantBytes <= 0 {
		c.Limits.MaxAssistantBytes = d.Limits.MaxAssistantBytes
	}
	if c.Limits.MaxEventBytesPerTurn <= 0 {
		c.Limits.MaxEventBytesPerTurn = d.Limits.MaxEventBytesPerTurn
	}
	if c.Limits.MaxEventsPerTurn <= 0 {
		c.Limits.MaxEventsPerTurn = d.Limits.MaxEventsPerTurn
	}
	if c.Limits.MaxDisplayFiles <= 0 {
		c.Limits.MaxDisplayFiles = d.Limits.MaxDisplayFiles
	}
	if c.Limits.MaxDisplayTextChars <= 0 {
		c.Limits.MaxDisplayTextChars = d.Limits.MaxDisplayTextChars
	}
	if c.Rewind.DefaultMode == "" {
		c.Rewind.DefaultMode = d.Rewind.DefaultMode
	}
}

// Save writes cfg atomically, creating parent directories as needed.
func Save(path string, cfg Config) error {
	cfg.normalize()
	return fsutil.WriteJSON(path, cfg, 0600)
}

// Ensure creates a default config file when none exists.
func Ensure(path string) (Config, error) {
	if _, err := os.Stat(path); err == nil {
		return Load(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Default(), err
	}
	cfg := Default()
	if err := Save(path, cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// DisplayLimits converts the configured limits into the display package shape.
func (c Config) DisplayLimits() (maxPrompt, maxAssistant, maxFiles, maxText int) {
	return c.Limits.MaxPromptBytes, c.Limits.MaxAssistantBytes, c.Limits.MaxDisplayFiles, c.Limits.MaxDisplayTextChars
}
