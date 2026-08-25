// init.go: first-time worktree creation and one-shot mapping application
// (spec §6.3, §5). Later mapping changes go through config add/remove.
package mapsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

// Init creates the machine worktree from git-root HEAD and applies every
// configured mapping once, per the §4.5 table. It stays MANUAL_REQUIRED —
// no .syncable is created until the first manual `gnm push` succeeds.
func Init(env *Env) error {
	g := env.gitRunner()
	if !g.IsRepo() {
		return fmt.Errorf("map: git-root is not a repository: %s", env.GitRoot)
	}
	if g.Head() == "" {
		return errors.New("map: git-root has no commits; make an initial commit first")
	}
	if errs := ValidateItems(env.Cfg.Map.Items, env.MapRoot); len(errs) > 0 {
		return fmt.Errorf("map: invalid mappings (run `gnm config validate`): %v", errs[0])
	}

	wtBranch := BranchName(env.MapRoot)
	branchThere := g.BranchExists(wtBranch)
	wtGitPath := filepath.Join(env.Worktree, ".git")
	_, dirErr := os.Stat(wtGitPath)
	dirThere := dirErr == nil

	switch {
	case branchThere && dirThere:
		env.logf("map %s: already initialized (%s)", env.MapRoot, wtBranch)
		return nil
	case branchThere || dirThere:
		return fmt.Errorf("map: partial initialization remnants (branch exists=%v, worktree exists=%v at %s); resolve manually",
			branchThere, dirThere, env.Worktree)
	}

	// persist auto's resolution so it stays fixed afterwards (§4.4)
	if env.ConfigPath != "" && env.Cfg.Map.Mode != env.Mode {
		if err := config.SetKey(env.ConfigPath, "map", "mode", env.Mode); err != nil {
			env.logf("warn: persist resolved mode: %v", err)
		} else {
			env.Cfg.Map.Mode = env.Mode
		}
	}

	// base is whatever git-root currently has checked out — never forced to main
	if err := g.WorktreeAdd(wtBranch, env.Worktree, "HEAD"); err != nil {
		return fmt.Errorf("map: create worktree: %w", err)
	}
	env.logf("map %s: worktree created (branch %s, mode %s)", env.MapRoot, wtBranch, env.Mode)

	// materialize the per-machine config snapshot right away (§4.6)
	if err := SaveSnapshot(env); err != nil {
		env.logf("warn: write config snapshot: %v", err)
	}

	for _, it := range env.Cfg.Map.Items {
		if err := applyMappingItem(env, it); err != nil {
			return fmt.Errorf("map item %s: %w", it.LocalPath, err)
		}
	}
	env.logf("map %s: initialized — review with `gnm status`, choose via add/get, then `gnm commit` + `gnm push`", env.MapRoot)
	return nil
}

// applyMappingItem implements the §4.5 file table for a single mapping.
// Used by both Init and post-init `config add`.
func applyMappingItem(env *Env, it config.MapItem) error {
	localAbs := NormalizeLocal(it.LocalPath)
	wtPath, err := env.worktreePathOf(it)
	if err != nil {
		return err
	}
	lk, wk := kindOf(localAbs), kindOf(wtPath)

	switch {
	case lk == kOther:
		return &SpecialFileError{localAbs}
	case wk == kOther:
		return &SpecialFileError{wtPath}
	case lk == kMissing && wk == kMissing:
		return nil // unknown content on both sides — keep the definition only
	case wk != kMissing && lk == kMissing:
		// repo → local deployment
		if env.LinkMode() {
			return EnsureSymlink(localAbs, wtPath)
		}
		if err := ensureParent(localAbs); err != nil {
			return err
		}
		return SyncTree(wtPath, localAbs)
	case lk != kMissing && wk == kMissing, lk != kMissing && wk != kMissing:
		// local wins into the worktree; link mode then swaps the local path
		// for a symlink pointing at what it just published
		if env.LinkMode() {
			return ReplaceLocalWithLink(localAbs, wtPath, func() error {
				return SyncTree(localAbs, wtPath)
			})
		}
		return SyncTree(localAbs, wtPath)
	default: // both missing handled above; unreachable safety net
		return nil
	}
}

// removeMappingItem undoes one mapping so the local path keeps a real,
// directly usable file afterwards (spec §4.5 remove rules):
//
//   - link mode: drop the local symlink first;
//   - local missing but worktree has content → copy back before anything else;
//   - map-root scope deletes the machine-namespace content in the worktree;
//     git-root scope preserves shared content for other machines;
//   - resulting worktree deletions stay UNSTAGED normal Git changes.
func removeMappingItem(env *Env, it config.MapItem) error {
	localAbs := NormalizeLocal(it.LocalPath)
	wtPath, err := env.worktreePathOf(it)
	if err != nil {
		return err
	}
	lk, wk := kindOf(localAbs), kindOf(wtPath)

	if lk == kSymlink && env.LinkMode() {
		if err := os.Remove(localAbs); err != nil {
			return err
		}
		lk = kMissing
	}
	if lk == kMissing && wk != kMissing && wk != kOther {
		if err := ensureParent(localAbs); err != nil {
			return err
		}
		// copy-back serves both scopes: map-root scope deletes its copy next,
		// git-root scope must keep the shared original anyway
		if err := SyncTree(wtPath, localAbs); err != nil {
			return err
		}
	}
	if it.Scope == config.ScopeMapRoot && wk != kMissing {
		if err := os.RemoveAll(wtPath); err != nil {
			return err
		}
	}
	return nil
}
