// Package config loads and merges git-notes-sync configuration.
//
// Precedence (later wins):
//
//	defaults ← global config (~/.config/git-notes-sync/config.toml) ←
//	explicit -c file ← repo config (./.notes-sync.toml)
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	MessageTimestamp = "timestamp"
	MessageStatic    = "static"
	MessageAI        = "ai"

	StrategyPreserve = "preserve"
	StrategyAbort    = "abort"

	BinaryOurs  = "ours"
	BinaryAbort = "abort"
)

// Config is the merged tool configuration.
type Config struct {
	AutoCommit      bool     `toml:"auto_commit"`
	CommitDebounce  int      `toml:"commit_debounce"`
	CommitMaxWait   int      `toml:"commit_max_wait"`
	CommitMessage   string   `toml:"commit_message"` // timestamp | static | ai
	CommitStaticMsg string   `toml:"commit_static_message"`
	AIFallback      string   `toml:"ai_fallback"`     // timestamp | static
	BinaryStrategy  string   `toml:"binary_strategy"` // ours | abort
	SyncInterval    int      `toml:"sync_interval"`   // daemon tick, seconds
	RetryAttempts   int      `toml:"retry_attempts"`
	Repos           []string `toml:"repos"` // daemon multi-repo list

	Conflict Conflict `toml:"conflict"`
	AI       AI       `toml:"ai"`
}

type Conflict struct {
	Strategy       string   `toml:"strategy"` // preserve | abort
	TextExtensions []string `toml:"text_extensions"`
}

type AI struct {
	Type         string `toml:"type"` // api | command
	BaseURL      string `toml:"base_url"`
	Model        string `toml:"model"`
	APIKeyEnv    string `toml:"api_key_env"`
	Command      string `toml:"command"`
	TimeoutSec   int    `toml:"timeout"`
	MaxDiffBytes int    `toml:"max_diff_bytes"`
}

// Defaults returns the built-in defaults.
func Defaults() *Config {
	return &Config{
		AutoCommit:      true,
		CommitDebounce:  60,
		CommitMaxWait:   300,
		CommitMessage:   MessageTimestamp,
		CommitStaticMsg: "notes: auto sync",
		AIFallback:      MessageTimestamp,
		BinaryStrategy:  BinaryOurs,
		SyncInterval:    60,
		RetryAttempts:   3,
		Conflict: Conflict{
			Strategy: StrategyPreserve,
			TextExtensions: []string{
				".md", ".markdown", ".txt", ".yaml", ".yml", ".toml",
				".json", ".org", ".rst", ".adoc", ".csv",
			},
		},
		AI: AI{
			APIKeyEnv:    "NOTES_AI_API_KEY",
			TimeoutSec:   60,
			MaxDiffBytes: 50 * 1024,
		},
	}
}

// GlobalPath returns the default global config file location.
func GlobalPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "git-notes-sync", "config.toml")
}

// Load merges configuration for a repository.
func Load(explicit string, repoDir string) (*Config, error) {
	cfg := Defaults()
	paths := []string{}
	if explicit != "" {
		paths = append(paths, explicit)
	} else {
		if p := GlobalPath(); fileExists(p) {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		if _, err := toml.DecodeFile(p, cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", p, err)
		}
	}
	if err := MergeRepo(cfg, repoDir); err != nil {
		return nil, err
	}
	return cfg, nil
}

// MergeRepo applies a repository-level .notes-sync.toml on top of cfg.
func MergeRepo(cfg *Config, repoDir string) error {
	if repoDir == "" {
		return nil
	}
	p := filepath.Join(repoDir, ".notes-sync.toml")
	if !fileExists(p) {
		return nil
	}
	if _, err := toml.DecodeFile(p, cfg); err != nil {
		return fmt.Errorf("config %s: %w", p, err)
	}
	return cfg.validate()
}

func (c *Config) validate() error {
	if c.CommitDebounce < 0 {
		return fmt.Errorf("commit_debounce must be >= 0")
	}
	if c.CommitMaxWait < c.CommitDebounce {
		c.CommitMaxWait = c.CommitDebounce
	}
	switch c.CommitMessage {
	case MessageTimestamp, MessageStatic, MessageAI:
	default:
		return fmt.Errorf("commit_message must be one of timestamp|static|ai, got %q", c.CommitMessage)
	}
	switch c.AIFallback {
	case MessageTimestamp, MessageStatic:
	default:
		return fmt.Errorf("ai_fallback must be one of timestamp|static, got %q", c.AIFallback)
	}
	switch c.BinaryStrategy {
	case BinaryOurs, BinaryAbort:
	default:
		return fmt.Errorf("binary_strategy must be one of ours|abort, got %q", c.BinaryStrategy)
	}
	switch c.Conflict.Strategy {
	case StrategyPreserve, StrategyAbort:
	default:
		return fmt.Errorf("conflict.strategy must be one of preserve|abort, got %q", c.Conflict.Strategy)
	}
	if c.SyncInterval < 5 {
		c.SyncInterval = 5
	}
	if c.RetryAttempts < 1 {
		c.RetryAttempts = 1
	}
	return nil
}

// IsTextExt reports whether a path is treated as text by the conflict preset.
func (c *Config) IsTextExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range c.Conflict.TextExtensions {
		if strings.ToLower(e) == ext {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
