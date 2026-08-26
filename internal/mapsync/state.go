// state.go: the resolved per-machine environment, the .syncable gate and
// the persisted blocked-state record (spec §3/§4.1).
package mapsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
)

// Env bundles everything one map-root operation needs at runtime.
type Env struct {
	Cfg        *config.Config
	ConfigPath string // user config path ("" = don't persist mode changes)
	MapRoot    string
	GitRoot    string // expanded absolute path
	Worktree   string
	State      string // <app>/map/<map-root> — lock, .syncable, blocked.json
	Mode       string // resolved actual mode: "link" | "copy"
	Logf       func(string, ...any)
}

// ResolveEnv validates the [map] config section and expands all paths.
func ResolveEnv(cfg *config.Config, cfgPath string, logf func(string, ...any)) (*Env, error) {
	mr := cfg.Map.MapRoot
	if err := ValidMapRoot(mr); err != nil {
		return nil, fmt.Errorf("map: %v (run `gnm config map-root <name>`)", err)
	}
	if cfg.Map.GitRoot == "" {
		return nil, errors.New("map: map.git_root is not configured (run `gnm config git-root <path>`)")
	}
	if cfg.Map.Mode != "" && cfg.Map.Mode != "auto" && cfg.Map.Mode != "link" && cfg.Map.Mode != "copy" {
		return nil, fmt.Errorf("map: invalid mode %q", cfg.Map.Mode)
	}
	gitRoot := NormalizeLocal(cfg.Map.GitRoot)
	if errs := ValidateItems(cfg.Map.Items, mr); len(errs) > 0 {
		return nil, fmt.Errorf("map: invalid mappings: %v", errs[0])
	}
	if errs := ValidatePlacement(cfg.Map.Items, gitRoot, mr); len(errs) > 0 {
		return nil, fmt.Errorf("map: invalid mapping placement: %v", errs[0])
	}
	mode := ResolveMode(cfg.Map.Mode)
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Env{
		Cfg:        cfg,
		ConfigPath: cfgPath,
		MapRoot:    mr,
		GitRoot:    gitRoot,
		Worktree:   WorktreeDir(mr),
		State:      StateDir(mr),
		Mode:       mode,
		Logf:       logf,
	}, nil
}

// ResolveMode turns the configured mode into the actual one. `auto` resolves
// to copy on Windows and link elsewhere; an explicit value is always honored
// (spec §4.4: link creation failure reports an error, no silent switch).
func ResolveMode(configured string) string {
	switch configured {
	case "copy":
		return "copy"
	case "link":
		return "link"
	default: // "" treated as auto
		if IsWindows() {
			return "copy"
		}
		return "link"
	}
}

func (e *Env) logf(format string, args ...any) { e.Logf(format, args...) }

// LinkMode reports whether mappings are materialized as symlinks.
func (e *Env) LinkMode() bool { return e.Mode == "link" }

func (e *Env) gitRunner() *git.Runner { return newRunner(e.GitRoot, e.Cfg) }
func (e *Env) wtRunner() *git.Runner  { return newRunner(e.Worktree, e.Cfg) }

// worktreePathOf maps an item to its absolute worktree-side root.
func (e *Env) worktreePathOf(item config.MapItem) (string, error) {
	rp, err := RepoPathOf(item, e.MapRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(e.Worktree, filepath.FromSlash(rp)), nil
}

// worktreeJoin returns the worktree-side absolute path of item+rel and the
// repo-relative slash path used in git commands.
func (e *Env) worktreeJoin(item config.MapItem, rel string) (abs string, repoRel string, err error) {
	rp, err := RepoPathOf(item, e.MapRoot)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		return filepath.Join(e.Worktree, filepath.FromSlash(rp)), rp, nil
	}
	return filepath.Join(e.Worktree, filepath.FromSlash(rp+"/"+rel)), rp + "/" + rel, nil
}

// localJoin returns the local-side absolute path of item+rel.
func (e *Env) localJoin(item config.MapItem, rel string) string {
	base := NormalizeLocal(item.LocalPath)
	if rel == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(rel))
}

// headRels lists HEAD-tracked node paths under one mapping's repo subtree,
// relative to that mapping root ("" excluded).
func (e *Env) headRels(item config.MapItem) ([]string, error) {
	rp, err := RepoPathOf(item, e.MapRoot)
	if err != nil {
		return nil, err
	}
	names, err := e.wtRunner().LsTreeHead(rp)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	prefix := rp + "/"
	set := make(map[string]bool, len(names))
	for _, n := range names {
		rel := strings.TrimPrefix(n, prefix)
		if rel == n {
			continue
		}
		set[rel] = true
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			set[parent] = true
		}
	}
	out := make([]string, 0, len(set))
	for rel := range set {
		out = append(out, rel)
	}
	return out, nil
}

// ErrWorktreeBroken marks a worktree directory that exists but cannot be
// used: it belongs to another repository or sits on the wrong branch.
// Callers must surface it instead of degrading to "not initialized" —
// silently treating debris as uninitialized hides a stale machine state.
var ErrWorktreeBroken = errors.New("worktree is bound to another repository or on an unexpected branch")

// IsInitialized reports whether the machine worktree exists with its branch
// (both-or-neither; a lone side is partial-init debris reported by Init).
// gitErr != nil means inspection itself failed; brokenErr != nil means the
// directory exists but is NOT a usable worktree for this map-root — both
// must be surfaced, never folded into plain "not initialized".
func IsInitialized(env *Env) (initd bool, brokenErr error) {
	if _, err := os.Stat(filepath.Join(env.Worktree, ".git")); err != nil {
		return false, nil // nothing there yet — genuinely uninitialized
	}
	w := env.wtRunner()
	if w.CurrentBranch() != BranchName(env.MapRoot) {
		return false, fmt.Errorf("%w: %s", ErrWorktreeBroken, env.Worktree)
	}
	// Bind the worktree to THIS git-root: after a git-root switch a stale
	// directory would otherwise pass the branch check and operate on the
	// wrong repository (common dir lives under <git-root>/.git).
	common, err := w.Out("rev-parse", "--git-common-dir")
	if err != nil {
		return false, fmt.Errorf("inspect worktree %s: %w", env.Worktree, err)
	}
	commonAbs := NormalizeLocal(common)
	if !strings.HasPrefix(LocalKey(commonAbs), LocalKey(env.GitRoot)+"/") &&
		LocalKey(commonAbs) != LocalKey(env.GitRoot) {
		return false, fmt.Errorf("%w: %s belongs to %s", ErrWorktreeBroken, env.Worktree, commonAbs)
	}
	return true, nil
}

func ensureStateDir(env *Env) error { return os.MkdirAll(env.State, 0o755) }

// ---------- .syncable gate ----------

func SyncablePath(env *Env) string { return filepath.Join(env.State, ".syncable") }

// HasSyncable reports whether automatic sync is currently trusted.
func HasSyncable(env *Env) bool {
	_, err := os.Stat(SyncablePath(env))
	return err == nil
}

// CreateSyncable arms the gate after a fully successful manual push.
func CreateSyncable(env *Env) error {
	if err := ensureStateDir(env); err != nil {
		return err
	}
	line := fmt.Sprintf("syncable since %s\n", time.Now().Format(time.RFC3339))
	return os.WriteFile(SyncablePath(env), []byte(line), 0o644)
}

// RemoveSyncable disarms the gate; failure is returned because silently
// leaving the marker armed would let the scheduler retry unsafe content.
func RemoveSyncable(env *Env) error {
	stateInfo, err := os.Stat(env.State)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !stateInfo.IsDir() {
		return fmt.Errorf("map state path is not a directory: %s", env.State)
	}
	err = os.Remove(SyncablePath(env))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ---------- blocked state (why MANUAL_REQUIRED) ----------

// BlockedState records why automatic sync stopped, consumed by `gnm status`.
type BlockedState struct {
	Reason    string   `json:"reason"`           // divergence | merge-conflict | fastforward-failed | mapping-root | special-file
	Detail    string   `json:"detail,omitempty"` //
	Conflicts []string `json:"conflicts,omitempty"`
	GitHead   string   `json:"git_head,omitempty"`
}

func BlockedPath(env *Env) string { return filepath.Join(env.State, "blocked.json") }

func WriteBlocked(env *Env, b *BlockedState) error {
	if err := ensureStateDir(env); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(BlockedPath(env), append(data, '\n'), 0o644)
}

// blockAndStop records a manual boundary without hiding a failure to disarm
// or persist the reason. The primary error remains visible to the caller.
func blockAndStop(env *Env, state *BlockedState, primary error) error {
	if err := RemoveSyncable(env); err != nil {
		return fmt.Errorf("%w; remove .syncable: %v", primary, err)
	}
	if err := WriteBlocked(env, state); err != nil {
		return fmt.Errorf("%w; write blocked state: %v", primary, err)
	}
	return primary
}

// ReadBlocked loads the blocked record; (nil, nil) when not blocked.
func ReadBlocked(env *Env) (*BlockedState, error) {
	data, err := os.ReadFile(BlockedPath(env))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var b BlockedState
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// ClearBlocked drops the record once a manual push succeeded.
// ClearBlocked drops the record once recovery succeeded. A missing record
// is a successful no-op; other failures surface to the caller.
func ClearBlocked(env *Env) error {
	if err := os.Remove(BlockedPath(env)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
