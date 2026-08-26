// init.go: first-time worktree creation and one-shot mapping application
// (spec §6.3, §5). Later mapping changes go through config add/remove.
package mapsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
)

// Init creates the machine worktree from git-root HEAD and applies every
// configured mapping once, per the §4.5 table. It stays MANUAL_REQUIRED —
// no .syncable is created until the first manual `gnm push` succeeds.
func Init(env *Env) error {
	if err := ensureStateDir(env); err != nil {
		return err
	}
	unlock, err := lock.Acquire(env.State)
	if err != nil {
		return fmt.Errorf("map: %w", err)
	}
	defer unlock()

	g := env.gitRunner()
	if !g.IsRepo() {
		return fmt.Errorf("map: git-root is not a repository: %s", env.GitRoot)
	}
	if g.Head() == "" {
		return errors.New("map: git-root has no commits; make an initial commit first")
	}
	if g.CurrentBranch() == "" {
		return errors.New("map: git-root is in detached HEAD; check out a branch first")
	}
	if entries, err := g.Status(); err != nil {
		return fmt.Errorf("map: inspect git-root: %w", err)
	} else if len(entries) != 0 {
		return errors.New("map: git-root has uncommitted files; commit or remove them before init")
	}
	if errs := ValidateItems(env.Cfg.Map.Items, env.MapRoot); len(errs) > 0 {
		return fmt.Errorf("map: invalid mappings (run `gnm config validate`): %v", errs[0])
	}
	if errs := ValidatePlacement(env.Cfg.Map.Items, env.GitRoot, env.MapRoot); len(errs) > 0 {
		return fmt.Errorf("map: invalid mapping placement: %v", errs[0])
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

	// base is whatever git-root currently has checked out — never forced to main
	if err := g.WorktreeAdd(wtBranch, env.Worktree, "HEAD"); err != nil {
		_ = g.WorktreeRemove(env.Worktree)
		_ = g.DeleteBranch(wtBranch)
		return fmt.Errorf("map: create worktree: %w", err)
	}
	env.logf("map %s: worktree created (branch %s, mode %s)", env.MapRoot, wtBranch, env.Mode)

	// materialize the per-machine config snapshot right away (§4.6)
	if err := SaveSnapshot(env); err != nil {
		_ = g.WorktreeRemove(env.Worktree)
		_ = g.DeleteBranch(wtBranch)
		return fmt.Errorf("map: write config snapshot: %w", err)
	}

	var applied []initItem
	for _, it := range env.Cfg.Map.Items {
		entry := initItem{item: it, localExisted: kindOf(NormalizeLocal(it.LocalPath)) != kMissing}
		if err := applyMappingItem(env, it); err != nil {
			cause := fmt.Errorf("map item %s: %w", it.LocalPath, err)
			if cleanupErr := rollbackInit(env, g, wtBranch, applied); cleanupErr != nil {
				return fmt.Errorf("%w; cleanup failed: %v", cause, cleanupErr)
			}
			return cause
		}
		applied = append(applied, entry)
	}
	env.logf("map %s: initialized — review with `gnm status`, choose via add/get, then `gnm commit` + `gnm push`", env.MapRoot)
	return nil
}

type initItem struct {
	item         config.MapItem
	localExisted bool
}

func rollbackInit(env *Env, g *git.Runner, branch string, applied []initItem) error {
	var first error
	for i := len(applied) - 1; i >= 0; i-- {
		entry := applied[i]
		local := NormalizeLocal(entry.item.LocalPath)
		wt, err := env.worktreePathOf(entry.item)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if env.LinkMode() && linkPointsTo(local, wt) {
			_ = os.Remove(local)
			if entry.localExisted {
				err = SyncTree(wt, local)
			}
		} else if !entry.localExisted {
			err = os.RemoveAll(local)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	if err := g.WorktreeRemove(env.Worktree); err != nil && first == nil {
		first = err
	}
	if err := g.DeleteBranch(branch); err != nil && first == nil {
		first = err
	}
	return first
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
		if err := SyncTree(wtPath, localAbs); err != nil {
			_ = os.RemoveAll(localAbs)
			return err
		}
		return nil
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

	// Link mode stashes the managed link instead of deleting it: if the
	// copy-back below fails mid-way, the original link is restored and no
	// half-materialized state is left behind (spec §4.5 safety).
	stashed := ""
	restore := func() {
		if stashed != "" {
			_ = os.RemoveAll(localAbs) // drop partial copy remnants first
			_ = os.Rename(stashed, localAbs)
			stashed = ""
		}
	}

	if lk == kSymlink && env.LinkMode() {
		if !linkPointsTo(localAbs, wtPath) {
			return fmt.Errorf("refusing to remove unmanaged symlink: %s", localAbs)
		}
		aside := localAbs + ".gns-unmap-bak"
		_ = os.Remove(aside)
		if err := os.Rename(localAbs, aside); err != nil {
			return fmt.Errorf("stash managed link: %w", err)
		}
		stashed = aside
		lk = kMissing
	}
	if lk == kMissing && wk != kMissing && wk != kOther {
		if err := ensureParent(localAbs); err != nil {
			restore()
			return err
		}
		// copy-back serves both scopes: map-root scope deletes its copy next,
		// git-root scope must keep the shared original anyway
		if err := SyncTree(wtPath, localAbs); err != nil {
			restore()
			return err
		}
	}
	if stashed != "" {
		if err := os.Remove(stashed); err != nil {
			// content is already materialized at localAbs — only clutter left
			return fmt.Errorf("mapping applied; remove stashed link %s manually: %w", stashed, err)
		}
	}
	if it.Scope == config.ScopeMapRoot && wk != kMissing {
		if err := os.RemoveAll(wtPath); err != nil {
			return err
		}
	}
	return nil
}
