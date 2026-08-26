// pull.go: the manual recovery entry after a block (spec §8.1). It moves the
// machine onto a new git-root baseline with reset --mixed — worktree files
// and local real files are never touched. The gate is disarmed before any
// history movement; a later `gnm push` re-arms after resolution (§8.2).
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
	initd, ierr := IsInitialized(env)
	if ierr != nil {
		return fmt.Errorf("map: inspect worktree: %w", ierr)
	}
	if !initd {
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

	// An interrupted merge/rebase must be resolved by hand first: moving the
	// baseline underneath it would silently discard the user's state.
	if op, err := g.MergeInProgress(); err != nil {
		return fmt.Errorf("map: inspect git-root: %w", err)
	} else if op != "" {
		return fmt.Errorf("map: git-root has an in-progress %s; finish or abort it first", op)
	}
	if op, err := w.MergeInProgress(); err != nil {
		return fmt.Errorf("map: inspect worktree: %w", err)
	} else if op != "" {
		return fmt.Errorf("map: worktree has an in-progress %s; finish or abort it first", op)
	}

	oldWt := w.Head()
	if oldWt != "" {
		// keep the pre-reset machine branch recoverable (spec §9.5)
		g.UpdateRefBestEffort(BackupRef(env.MapRoot), oldWt)
	}
	oldRoot := g.Head()

	// Disarm the gate BEFORE any history movement: this command enters the
	// recovery flow, and a later `gnm push` re-arms after resolution (§8.2).
	if err := RemoveSyncable(env); err != nil {
		return fmt.Errorf("map: disarm .syncable: %w", err)
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
		if oldRoot != "" {
			// Destroying git-root history demands a guaranteed escape hatch,
			// not a best-effort one (spec §9.5).
			if _, err := g.Out("update-ref", GitRootBackupRef(env.MapRoot), oldRoot); err != nil {
				return fmt.Errorf("map: write git-root backup ref: %w", err)
			}
		}
		// --force resets ONLY the dedicated git-root; never the worktree (§9.4)
		if err := g.ResetHard(upHash); err != nil {
			return fmt.Errorf("map: git-root reset --hard: %w", err)
		}
		env.logf("map %s: git-root force-aligned to upstream %s/%s", env.MapRoot, remote, branch)
	} else {
		entries, serr := g.Status()
		if serr != nil {
			return fmt.Errorf("map: inspect git-root: %w", serr)
		}
		if len(entries) > 0 {
			return errors.New("map: git-root has uncommitted files; commit or clean it before pull")
		}
		diverged, err := pullFFOnly(g, env.Cfg.RetryAttempts)
		if err != nil {
			if diverged {
				primary := errors.New("map: git-root diverged from upstream; use `gnm pull --force`")
				return blockAndStop(env, &BlockedState{Reason: "divergence"}, primary)
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
	// The machine is on the new baseline: stale block reasons (divergence,
	// old conflict list) no longer describe reality — clear them and let
	// status derive guidance from live state.
	if err := ClearBlocked(env); err != nil {
		env.logf("warn: clear blocked state: %v", err)
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
