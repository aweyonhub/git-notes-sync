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
	AutoCommit      bool   `toml:"auto_commit"`
	CommitDebounce  int    `toml:"commit_debounce"`
	CommitMaxWait   int    `toml:"commit_max_wait"`
	CommitMessage   string `toml:"commit_message"` // timestamp | static | ai
	CommitStaticMsg string `toml:"commit_static_message"`
	AIFallback      string `toml:"ai_fallback"`     // timestamp | static
	BinaryStrategy  string `toml:"binary_strategy"` // ours | abort
	SyncInterval    int    `toml:"sync_interval"`   // daemon tick, seconds
	RetryAttempts   int    `toml:"retry_attempts"`
	GitTimeoutSec   int    `toml:"git_timeout"` // per git command deadline; 0 = no timeout
	Repos           Repos  `toml:"repos"`       // daemon/sync-all multi-repo list

	Conflict Conflict `toml:"conflict"`
	AI       AI       `toml:"ai"`
	Log      Log      `toml:"log"`
	Map      Map      `toml:"map"`
}

// Scope values for [[map.items]] entries (gns map feature).
const (
	ScopeMapRoot = "map-root" // path is prefixed with the machine's map_root
	ScopeGitRoot = "git-root" // path is shared across machines
)

// Map configures the `gns map` feature (doc/git-notes-sync_map.md): mapping
// local files into a dedicated git-root repository via a per-machine worktree.
type Map struct {
	GitRoot string    `toml:"git_root"` // integration repo path (user-managed)
	MapRoot string    `toml:"map_root"` // machine namespace inside the repo
	Mode    string    `toml:"mode"`     // auto | link | copy
	Sync    bool      `toml:"sync"`     // run `gns map sync` from the scheduler
	Items   []MapItem `toml:"items"`    // [[map.items]] mappings
}

// MapItem is one [[map.items]] entry: a repo-side path mapped to a local path.
type MapItem struct {
	Scope     string `toml:"scope"`
	Path      string `toml:"path"`       // repo-relative, normalized, no ".."
	LocalPath string `toml:"local_path"` // may contain ~/; unique identity
}

type Log struct {
	MaxSizeKB  int `toml:"max_size_kb"` // 日志文件最大大小（KB），超阈值轮转
	MaxBackups int `toml:"max_backups"` // 保留的历史日志副本数（.log.1, .log.2, ...）
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
	AgentFile    string `toml:"agent_file"` // repo-relative instructions file, sent with the diff
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
		SyncInterval:    600,
		RetryAttempts:   3,
		GitTimeoutSec:   120,
		Conflict: Conflict{
			Strategy: StrategyPreserve,
			TextExtensions: []string{
				".md", ".markdown", ".txt",
			},
		},
		AI: AI{
			APIKeyEnv:    "NOTES_AI_API_KEY",
			TimeoutSec:   60,
			MaxDiffBytes: 50 * 1024,
			AgentFile:    "AGENTS.md",
		},
		Log: Log{
			MaxSizeKB:  500,
			MaxBackups: 1,
		},
		Map: Map{
			Mode: "auto",
			Sync: false,
		},
	}
}

// Repo is one entry of the repos list.
type Repo struct {
	Name string `toml:"name"` // optional display name
	Path string `toml:"path"` // may contain ~/
}

// DisplayName returns the name used in logs/lists, falling back to path.
func (r Repo) DisplayName() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Path
}

// ExpandedPath resolves ~/ and relative paths for actual use.
func (r Repo) ExpandedPath() string { return expandPath(r.Path) }

// Repos accepts both TOML forms:
//
//	repos = ["~/notes", "~/wiki"]          (simple, name = path)
//
//	[[repos]]
//	name = "notes"
//	path = "~/notes"                       (named)
type Repos struct {
	list []Repo
}

func (r *Repos) UnmarshalTOML(data any) error {
	switch v := data.(type) {
	case []map[string]any:
		for _, m := range v {
			rep := Repo{}
			if s, ok := m["name"].(string); ok {
				rep.Name = s
			}
			if s, ok := m["path"].(string); ok {
				rep.Path = s
			}
			if rep.Path == "" {
				continue
			}
			r.list = append(r.list, rep)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				r.list = append(r.list, Repo{Path: s})
			}
		}
	}
	return nil
}

// All returns a copy of the repo list.
func (r *Repos) All() []Repo {
	out := make([]Repo, len(r.list))
	copy(out, r.list)
	return out
}

func (r *Repos) Len() int { return len(r.list) }

// Find matches a repo by name (exact), then by path (exact, then longest
// path prefix — so ~/notes2 doesn't match ~/notes).
func (r *Repos) Find(nameOrPath string) (Repo, bool) {
	for _, rep := range r.list {
		if rep.Name == nameOrPath || rep.Path == nameOrPath {
			return rep, true
		}
	}
	// Prefix match on path: a sub-directory of a configured repo should
	// still resolve to its owner. Return the longest matching prefix so
	// the most specific owner wins.
	var best Repo
	bestLen := -1
	for _, rep := range r.list {
		if rep.Path != "" && strings.HasPrefix(nameOrPath, rep.Path) {
			if len(rep.Path) > bestLen {
				best = rep
				bestLen = len(rep.Path)
			}
		}
	}
	if bestLen >= 0 {
		return best, true
	}
	return Repo{}, false
}

// expandPath resolves ~/ and relative paths to absolute paths.
func expandPath(p string) string {
	// Prefer $HOME over os.UserHomeDir(): consistent cross-platform behavior
	// (Windows uses USERPROFILE for UserHomeDir, but tools expect $HOME for ~).
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if p == "~" && home != "" {
		return home
	}
	if strings.HasPrefix(p, "~/") && home != "" {
		return filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) && !strings.HasPrefix(p, "/") {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return p
}

// GlobalPath returns the default global config file location.
// GNS_CONFIG overrides it (e.g. keep config in ~/.config for dotfiles
// management); otherwise os.UserConfigDir() is used — macOS:
// ~/Library/Application Support/git-notes-sync/config.toml (note: Darwin
// ignores XDG_CONFIG_HOME), Linux: ~/.config/git-notes-sync/config.toml,
// Windows: %AppData%\git-notes-sync\config.toml.
func GlobalPath() string {
	if p := os.Getenv("GNS_CONFIG"); p != "" {
		return expandPath(p)
	}
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
	// validate runs on every load (not just when a repo-level config exists),
	// so a typo like commit_message="timesamp" surfaces immediately instead
	// of silently falling through to a default branch.
	if err := cfg.validate(); err != nil {
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
	return nil
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
	// git_timeout: 0 = no timeout (documented escape hatch); 1-4 clamp to 5
	// so the timeout never fires mid-invocation on healthy setups.
	if c.GitTimeoutSec < 0 {
		c.GitTimeoutSec = 0
	}
	if c.GitTimeoutSec > 0 && c.GitTimeoutSec < 5 {
		c.GitTimeoutSec = 5
	}
	switch c.Map.Mode {
	case "", "auto", "link", "copy":
	default:
		return fmt.Errorf("map.mode must be one of auto|link|copy, got %q", c.Map.Mode)
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
