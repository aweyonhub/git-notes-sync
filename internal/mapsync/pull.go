// pull.go: the manual recovery entry after a block (spec §8.1). It moves the
// machine onto a new git-root baseline with reset --mixed — worktree files
// and local real files are never touched.
package mapsync

import (
	"errors"
	"fmt"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/git"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
	"github.com/aweyonhub/git-notes-sync/internal/retry"
)

// Pull re-bases the worktree onto git-root's (possibly force-updated) HEAD.
//
//	gnm pull        git-root must fast-forward to its upstream;
//	gnm pull --force git-root is reset --hard to the upstream, discarding its
//	                 own commits (their content survives in the worktree and
//	                 in the real files as unstaged differences).
func Pull(env *Env, force bool) error {
	if !IsInitialized(env) {
		return errors.New("map: not initialized; run `gnm init` first")
	}
	g := env.gitRunner()
	w := env.wtRunner()

	if err := ensureStateDir(env); err != nil {
		return err
	}
	unlock, err := lock.Acquire(env.State)
	if err != nil {
		return fmt.Errorf("map: %w", err)
	}
	defer unlock()

	if !g.IsRepo() {
		return fmt.Errorf("map: git-root is not a repository: %s", env.GitRoot)
	}
	branch := g.CurrentBranch()
	if branch == "" {
		return errors.New("map: git-root is in detached HEAD; check out a branch first")
	}

	oldWt := w.Head()
	if oldWt != "" {
		// keep the pre-reset machine branch recoverable (spec §9.5)
		g.UpdateRefBestEffort(BackupRef(env.MapRoot), oldWt)
	}

	if force {
		remote, _, ok := g.Upstream()
		if !ok {
			return fmt.Errorf("map: git-root branch %q has no upstream to force-align with", branch)
		}
		if err := retry.Do(env.Cfg.RetryAttempts, func() error {
			return g.Fetch(remote)
		}, 2*time.Second, git.IsTransient); err != nil {
			return fmt.Errorf("map: fetch %s: %w", remote, err)
		}
		upHash, uerr := g.Out("rev-parse", "@{u}")
		if uerr != nil {
			return fmt.Errorf("map: resolve upstream: %w", uerr)
		}
		// --force resets ONLY the dedicated git-root; never the worktree (§9.4)
		if err := g.ResetHard(upHash); err != nil {
			return fmt.Errorf("map: git-root reset --hard: %w", err)
		}
		env.logf("map %s: git-root force-aligned to upstream %s/%s", env.MapRoot, remote, branch)
	} else {
		diverged, err := pullFFOnly(g, env.Cfg.RetryAttempts)
		if err != nil {
			if diverged {
				return errors.New("map: git-root diverged from upstream; use `gnm pull --force`")
			}
			return fmt.Errorf("map: git-root pull: %w", err)
		}
	}

	base := g.Head()
	if base == "" {
		return errors.New("map: git-root has no commits")
	}
	// mixed reset moves HEAD+index only; working files and real files stay
	if err := w.ResetMixed(base); err != nil {
		return fmt.Errorf("map: worktree reset --mixed to %s: %w", short(base), err)
	}

	entries, serr := w.Status()
	if serr == nil {
		env.logf("map %s: rebased onto %s — %d pending difference(s) to choose", env.MapRoot, short(base), len(entries))
		for i, e := range entries {
			if i == 10 {
				env.logf("  ... and %d more (see `gnm status`)", len(entries)-i)
				break
			}
			env.logf("  %s %s", e.Status, e.Path)
		}
	}
	env.logf("next: `gnm status` → `gnm add|get <path>` → `gnm commit -m \"resolve map conflict\"` → `gnm push`")
	return nil
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}
