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

// safeWorktreePath returns a worktree-side path after verifying that no
// intermediate ancestor is an unmanaged symlink that would let RemoveAll /
// SyncTree escape the worktree. The mapping root itself (the first segment
// after Worktree) is allowed to be a directory or, in link mode, a managed
// symlink — those are the expected entry points.
func (e *Env) safeWorktreePath(item config.MapItem, rel string) (string, error) {
	abs, _, err := e.worktreeJoin(item, rel)
	if err != nil {
		return "", err
	}
	// Verify the resolved physical path stays inside the worktree. This
	// catches symlinks at ANY level (including the final segment) by
	// resolving all symlinks and comparing prefixes.
	physical, perr := filepath.EvalSymlinks(abs)
	if perr != nil {
		// EvalSymlinks failed (including NotExist when a symlink points
		// at a missing target). Always fall back to checkAncestors so a
		// dangling symlink can't bypass validation.
		if err := checkAncestors(abs, e.Worktree, false); err != nil {
			return "", err
		}
	}
	if perr == nil {
		wtPhysical, _ := filepath.EvalSymlinks(e.Worktree)
		if wtPhysical == "" {
			wtPhysical = e.Worktree
		}
		if !within(LocalKey(physical), LocalKey(wtPhysical)) {
			return "", fmt.Errorf("path escapes worktree via symlink: %s -> %s", abs, physical)
		}
	}
	return abs, nil
}

// safeLocalPath returns a local-side path after verifying that no intermediate
// ancestor (between the mapping root and the final path) is an unmanaged
// symlink. allowRootLink=true (link mode) permits the mapping root itself to
// be a managed symlink pointing at the worktree.
func (e *Env) safeLocalPath(item config.MapItem, rel string, allowRootLink bool) (string, error) {
	abs := e.localJoin(item, rel)
	if err := checkAncestors(abs, NormalizeLocal(item.LocalPath), allowRootLink); err != nil {
		return "", err
	}
	return abs, nil
}

// checkAncestors verifies that every directory between root and path (exclusive
// of root, inclusive of path) is not a symlink, unless allowRootLink is true and
// the symlink IS the root (the managed link-mode entry point).
func checkAncestors(path, root string, allowRootLink bool) error {
	rootKey := LocalKey(filepath.Clean(root))
	// Walk from root downward, lstat each intermediate segment.
	cur := filepath.Clean(root)
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("path relation: %w", err)
	}
	if rel == "." {
		return nil // path == root
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		isLast := i == len(parts)-1
		li, lerr := os.Lstat(cur)
		if lerr != nil {
			if os.IsNotExist(lerr) {
				return nil // missing path has no symlink to traverse
			}
			return fmt.Errorf("lstat %s: %w", cur, lerr)
		}
		if li.Mode()&os.ModeSymlink != 0 {
			// In link mode the mapping root itself is a managed symlink —
			// allow it as the sole exception.
			if allowRootLink && LocalKey(cur) == rootKey {
				continue
			}
			// A symlink at the final segment is fine for read operations
			// (linkPointsTo), but dangerous for RemoveAll.
			if isLast {
				continue // final segment handled by caller (kindOf/linkPointsTo)
			}
			return fmt.Errorf("refusing to traverse unmanaged symlink: %s", cur)
		}
	}
	return nil
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
	// Ownership must be compared on physical paths: git resolves symlinks
	// when reporting the common dir, so a symlinked TMPDIR on macOS
	// (/var → /private/var) would otherwise false-negative here. See
	// canonicalDir (ownership) vs NormalizeLocal (link identity).
	rr := canonicalDir(env.GitRoot)
	if !strings.HasPrefix(LocalKey(commonAbs), LocalKey(rr)+"/") &&
		LocalKey(commonAbs) != LocalKey(rr) {
		return false, fmt.Errorf("%w: %s belongs to %s", ErrWorktreeBroken, env.Worktree, commonAbs)
	}
	return true, nil
}

// canonicalDir resolves symlinks for path-ownership comparisons. Unlike
// NormalizeLocal — which deliberately keeps the link identity of mapped
// files — ownership checks must compare physical paths, or macOS /var →
// /private/var aliasing (and similar) would false-negative.
func canonicalDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

func ensureStateDir(env *Env) error { return os.MkdirAll(env.State, 0o755) }

// WorktreeOwnedBy checks whether a worktree exists for the given map-root
// and belongs to the given git-root — without requiring valid mapping items.
// Used by `config load` to prevent overwriting an initialized machine even
// when ResolveEnv fails on invalid items (spec §4.6, plan B).
func WorktreeOwnedBy(mapRoot, gitRoot string, cfg *config.Config) (owned bool, err error) {
	if err := ValidMapRoot(mapRoot); err != nil {
		return false, nil
	}
	if gitRoot == "" {
		return false, nil
	}
	wtDir := WorktreeDir(mapRoot)
	if _, err := os.Stat(filepath.Join(wtDir, ".git")); err != nil {
		return false, nil // no worktree
	}
	w := newRunner(wtDir, cfg)
	common, err := w.Out("rev-parse", "--git-common-dir")
	if err != nil {
		return false, fmt.Errorf("inspect worktree %s: %w", wtDir, err)
	}
	commonAbs := NormalizeLocal(common)
	rr := canonicalDir(gitRoot)
	if !strings.HasPrefix(LocalKey(commonAbs), LocalKey(rr)+"/") &&
		LocalKey(commonAbs) != LocalKey(rr) {
		return false, fmt.Errorf("%w: %s belongs to %s", ErrWorktreeBroken, wtDir, commonAbs)
	}
	return true, nil
}

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

// ClearBlocked drops the record once a manual push succeeded. A missing record
// is a successful no-op; other failures surface to the caller.
func ClearBlocked(env *Env) error {
	if err := os.Remove(BlockedPath(env)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
