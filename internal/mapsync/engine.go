// engine.go: the shared integration chain behind `gnm push` and `gnm sync`
// (spec §7.2). Eleven steps, one lock, exactly one conflict point.
package mapsync

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
	"github.com/aweyonhub/git-notes-sync/internal/retry"
)

// newRunner builds a git runner honoring the configured per-command timeout.
func newRunner(dir string, cfg *config.Config) *git.Runner {
	r := git.NewRunner(dir)
	if cfg != nil && cfg.GitTimeoutSec > 0 {
		r.Timeout = time.Duration(cfg.GitTimeoutSec) * time.Second
	}
	return r
}

// Sync runs one automatic synchronization round (push/sync share the chain;
// sync mode auto-commits). Requires .syncable — without it the machine is
// MANUAL_REQUIRED and only guidance is printed (spec §7.4).
func Sync(env *Env) error {
	initd, ierr := IsInitialized(env)
	if ierr != nil {
		return fmt.Errorf("map: inspect worktree: %w", ierr)
	}
	if !initd {
		return errors.New("map: not initialized; run `gnm init` first")
	}
	if !HasSyncable(env) {
		env.logf("map %s: MANUAL_REQUIRED — automatic sync is gated off", env.MapRoot)
		out, err := Status(env)
		if err == nil {
			fmt.Println(out)
		}
		return errors.New("map: .syncable missing; resolve manually, then `gnm push` to re-arm")
	}
	return runChain(env, true)
}

// Push is the manual confirmation entry: it requires an already-committed
// worktree and creates .syncable after full success (spec §6.7).
func Push(env *Env) error {
	initd, ierr := IsInitialized(env)
	if ierr != nil {
		return fmt.Errorf("map: inspect worktree: %w", ierr)
	}
	if !initd {
		return errors.New("map: not initialized; run `gnm init` first")
	}
	return runChain(env, false)
}

func runChain(env *Env, auto bool) error {
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

	// Re-check under the lock: a concurrent config change may have disarmed
	// the gate between the entry check and here (§3.2).
	if auto && !HasSyncable(env) {
		return errors.New("map: .syncable was removed while waiting for the lock; run `gnm status`")
	}

	// -- preconditions (§7.1)
	if !g.IsRepo() {
		return fmt.Errorf("map: git-root is not a repository: %s", env.GitRoot)
	}
	branch := g.CurrentBranch()
	if branch == "" {
		return errors.New("map: git-root is in detached HEAD; check out a branch first")
	}
	remote, rbranch, hasUp := g.Upstream()
	if !hasUp {
		return fmt.Errorf("map: git-root branch %q has no upstream; push it once manually", branch)
	}

	// An interrupted merge/rebase must never be concluded automatically:
	// add -A would stage conflict markers as if they were resolutions.
	if op, err := g.MergeInProgress(); err != nil {
		return fmt.Errorf("map: inspect git-root: %w", err)
	} else if op != "" {
		return fmt.Errorf("map: git-root has an in-progress %s; finish or abort it first (.syncable kept)", op)
	}
	if op, err := w.MergeInProgress(); err != nil {
		return fmt.Errorf("map: inspect worktree: %w", err)
	} else if op != "" {
		return fmt.Errorf("map: worktree has an in-progress %s; finish or abort it first (.syncable kept)", op)
	}
	if entries, err := g.Status(); err != nil {
		return fmt.Errorf("map: inspect git-root: %w", err)
	} else if len(entries) > 0 {
		return errors.New("map: git-root has uncommitted files; commit or clean them first (.syncable kept)")
	}
	wtBranch := BranchName(env.MapRoot)

	// step 2: git-root pull --ff-only. Divergence is confirmed history damage
	// → gate off; everything else is transient → keep the gate (§3.2).
	diverged, err := pullFFOnly(g, env.Cfg.RetryAttempts)
	if err != nil {
		if diverged {
			primary := fmt.Errorf("map: git-root diverged from upstream; run `gnm pull --force`, then add/get/commit/push")
			return blockAndStop(env, &BlockedState{
				Reason: "divergence",
				Detail: fmt.Sprintf("git-root branch %s diverged from its upstream", branch),
			}, primary)
		}
		return fmt.Errorf("map: git-root pull: %w (.syncable kept)", err)
	}

	// step 3: mapping-root presence/type pre-check — delete-safety, not a
	// Git conflict (§5.2).
	if viol := rootViolations(env); viol != "" {
		primary := fmt.Errorf("map: mapping root needs manual choice; .syncable removed\n  %s", viol)
		return blockAndStop(env, &BlockedState{Reason: "mapping-root", Detail: viol}, primary)
	}

	// step 4: one local→worktree pass. In copy mode it also collects the
	// metadata used to guard the later deploy, avoiding separate TOCTOU scans.
	// Push (non-auto) requires a clean worktree first: converging into an
	// already-dirty worktree leaves a half-converged state the user must
	// untangle (spec §7.2).
	if !auto {
		preDirty, derr := wtHasChanges(w)
		if derr != nil {
			return derr
		}
		if preDirty {
			return errors.New("map: worktree has uncommitted changes; run `gnm add -A`, `gnm commit`, then `gnm push`")
		}
	}
	baselines, err := convergeIntoWorktree(env, false)
	if err != nil {
		return classifySpecial(env, err)
	}
	dirty, err := wtHasChanges(w)
	if err != nil {
		return err
	}
	if dirty && !auto {
		return errors.New("map: local changes copied to worktree; run `gnm add -A`, `gnm commit`, then `gnm push`")
	}
	if dirty {
		if err := commitWorktree(env, ""); err != nil {
			return err
		}
	}

	// step 5: THE conflict point (§2.3)
	preMerge := w.Head()
	if err := w.Merge(branch); err != nil {
		unmerged, uerr := w.Unmerged()
		if uerr == nil && len(unmerged) > 0 {
			abortErr := w.MergeAbort()
			primary := fmt.Errorf("map: merge conflict in %d file(s); run `gnm pull`, choose with `gnm add|get`, commit, then push", len(unmerged))
			detail := "worktree merge git-root conflicted"
			if abortErr != nil {
				// Persist the abort failure so status can route the user to
				// `git merge --abort` instead of `gnm pull` (which refuses an
				// in-progress merge, causing a dead loop).
				detail = "worktree merge git-root conflicted; abort failed: " + abortErr.Error()
			}
			primary = blockAndStop(env, &BlockedState{
				Reason:    "merge-conflict",
				Detail:    detail,
				Conflicts: unmerged,
				GitHead:   g.Head(),
			}, primary)
			if abortErr != nil {
				return fmt.Errorf("%w; merge --abort also failed: %v", primary, abortErr)
			}
			return primary
		}
		return fmt.Errorf("map: worktree merge %s: %w", branch, err)
	}

	// step 6: post-merge root check. reset --merge restores the old commit
	// without using reset --hard on the machine worktree.
	if viol := rootViolations(env); viol != "" {
		if err := w.ResetMerge(preMerge); err != nil {
			viol += fmt.Sprintf("; restore merge failed: %v", err)
		}
		primary := fmt.Errorf("map: mapping root diverged after merge; reverted merge\n  %s", viol)
		return blockAndStop(env, &BlockedState{Reason: "mapping-root", Detail: viol}, primary)
	}
	if env.LinkMode() {
		if dirty, err := wtHasChanges(w); err != nil {
			return err
		} else if dirty {
			return blockAndStop(env, &BlockedState{Reason: "mapping-root", Detail: "mapped files changed during merge"}, errors.New("map: mapped files changed during merge; local content kept, review and push again"))
		}
	}

	// step 7: link cleanup is cheap. Copy mode first asks Git whether any
	// mapped path changed, avoiding a full deploy scan for unrelated commits.
	deploy := env.LinkMode()
	mergedHead := w.Head()
	if !deploy && preMerge != mergedHead {
		paths := make([]string, 0, len(env.Cfg.Map.Items))
		for _, item := range env.Cfg.Map.Items {
			path, err := RepoPathOf(item, env.MapRoot)
			if err != nil {
				return err
			}
			paths = append(paths, path)
		}
		deploy, err = w.PathsChanged(preMerge, mergedHead, paths...)
		if err != nil {
			return err
		}
	}
	if deploy {
		if err := deployFromWorktree(env, baselines); err != nil {
			return classifySpecial(env, err)
		}
	}

	// step 8: fast-forward git-root to the merged worktree HEAD
	if err := g.MergeFFOnly(wtBranch); err != nil {
		primary := fmt.Errorf("map: git-root cannot fast-forward to %s: %v; run `gnm status`", wtBranch, shortErr(err))
		return blockAndStop(env, &BlockedState{Reason: "fastforward-failed", Detail: err.Error()}, primary)
	}

	// step 9: every push failure keeps .syncable. The next round's
	// pull --ff-only and commit-graph check officially judge divergence
	// (§7.3), so push stderr wording does not affect state transitions.
	err = retry.Do(env.Cfg.RetryAttempts, func() error {
		return g.Push(remote, rbranch)
	}, 2*time.Second, git.IsTransient)
	if err != nil {
		return fmt.Errorf("map: git-root push failed; .syncable kept — retry or run `gnm status`: %w", err)
	}

	// step 10: success — arm/keep the gate and clear stale block info
	if err := CreateSyncable(env); err != nil {
		return fmt.Errorf("map: synchronized but could not create .syncable: %w", err)
	}
	if cerr := ClearBlocked(env); cerr != nil {
		env.logf("warn: clear blocked state: %v", cerr)
	}
	env.logf("map %s: synced → %s/%s", env.MapRoot, remote, rbranch)
	return nil
}

// rootViolations lists delete-safety problems across all mappings (§5.2):
// a root existing on only one side, or both sides with different types.
// The worktree side is judged by working-tree presence: after a crash that
// leaves a deletion unstaged this errs toward blocking, which is safe.
func rootViolations(env *Env) string {
	wt := env.wtRunner()
	var probs []string
	for _, it := range env.Cfg.Map.Items {
		localAbs := NormalizeLocal(it.LocalPath)
		lk := kindOf(localAbs)
		wtPath, err := env.worktreePathOf(it)
		if err != nil {
			continue
		}
		wk := kindOf(wtPath)
		if env.LinkMode() {
			switch {
			case lk == kMissing && wk == kMissing:
				continue
			case lk != kSymlink:
				probs = append(probs, fmt.Sprintf("%s: managed link is missing or replaced; choose with gnm add/get", it.LocalPath))
				continue
			case !linkPointsTo(localAbs, wtPath):
				probs = append(probs, fmt.Sprintf("%s: link points outside the managed worktree", it.LocalPath))
				continue
			}
			// 规范派(§5.2): treat the managed link as the repo side only
			// while the worktree node still exists. When the remote deleted
			// the whole root, the broken link must surface as a one-side
			// violation for manual choice — never silently disappear.
			if wk != kMissing {
				lk = wk
			}
		}
		switch {
		case lk == kMissing && wk == kMissing:
			// consistent-empty — nothing to choose
		case lk == kMissing:
			probs = append(probs, fmt.Sprintf("%s: missing locally, present in repo (`gnm get %s` restores, `gnm add %s` confirms deletion)",
				it.LocalPath, it.LocalPath, it.LocalPath))
		case wk == kMissing && !wtTracks(env, wt, it):
			probs = append(probs, fmt.Sprintf("%s: only on this machine (`gnm add %s` publishes, `gnm get %s` discards)",
				it.LocalPath, it.LocalPath, it.LocalPath))
		case wk == kMissing:
			probs = append(probs, fmt.Sprintf("%s: deleted locally but still tracked (`gnm add %s` confirms deletion, `gnm get %s` restores)",
				it.LocalPath, it.LocalPath, it.LocalPath))
		case lk != wk:
			probs = append(probs, fmt.Sprintf("%s: type differs (local=%s, repo=%s); choose with `gnm add|get %s`",
				it.LocalPath, kindName(lk), kindName(wk), it.LocalPath))
		}
	}
	return strings.Join(probs, "\n  ")
}

// wtTracks reports whether HEAD still tracks anything under the item root,
// distinguishing "deleted locally, deletion already committed" from "deleted
// locally, deletion pending".
func wtTracks(env *Env, wt *git.Runner, item config.MapItem) bool {
	rp, err := RepoPathOf(item, env.MapRoot)
	if err != nil {
		return false
	}
	names, err := wt.LsTreeHead(rp)
	return err == nil && len(names) > 0
}

// convergeIntoWorktree copies local content up in copy mode (link mode needs
// nothing: the worktree file IS the local file). Callers run the root checks
// first, so every mapping here is consistent or consistently empty.
func convergeIntoWorktree(env *Env, confirmMissing bool) (map[int]copyBaseline, error) {
	if env.LinkMode() {
		return nil, nil
	}
	baselines := make(map[int]copyBaseline, len(env.Cfg.Map.Items))
	for i, it := range env.Cfg.Map.Items {
		localAbs := NormalizeLocal(it.LocalPath)
		wtPath, err := env.worktreePathOf(it)
		if err != nil {
			// config-invalid mapping: deploy must not touch it unguarded
			baselines[i] = copyBaseline{}
			continue
		}
		if kindOf(localAbs) == kMissing {
			if confirmMissing && kindOf(wtPath) != kMissing {
				if err := os.RemoveAll(wtPath); err != nil {
					return nil, err
				}
			}
			// Record the absence so the deploy pass refuses to overwrite a
			// file created here while Git was integrating (§9.1).
			baselines[i] = copyBaseline{"": {Kind: kMissing}}
			continue
		}
		baseline, err := syncTreeTracked(localAbs, wtPath)
		if err != nil {
			return nil, err
		}
		baselines[i] = baseline
	}
	return baselines, nil
}

// deployFromWorktree pushes merged worktree content down to local files
// (chain step 8): copy mode re-converges each subtree; link mode only makes
// sure the root symlink exists for live mappings.
func deployFromWorktree(env *Env, baselines map[int]copyBaseline) error {
	for i, it := range env.Cfg.Map.Items {
		localAbs := NormalizeLocal(it.LocalPath)
		wtPath, err := env.worktreePathOf(it)
		if err != nil {
			continue
		}
		wk := kindOf(wtPath)
		if env.LinkMode() {
			if wk == kMissing {
				lk := kindOf(localAbs)
				if lk == kMissing {
					continue
				}
				if lk != kSymlink || !linkPointsTo(localAbs, wtPath) {
					return fmt.Errorf("refusing to remove unmanaged local path %s", localAbs)
				}
				if err := os.Remove(localAbs); err != nil {
					return err
				}
				continue
			}
			if err := EnsureSymlink(localAbs, wtPath); err != nil {
				return err
			}
			continue
		}
		if wk == kMissing {
			// Remote deleted the whole root: guard against clobbering a file
			// the user created or edited while Git was integrating (§9.1).
			if err := syncTreeGuarded(wtPath, localAbs, baselines[i]); err != nil {
				return err
			}
			continue
		}
		if err := ensureParent(localAbs); err != nil {
			return err
		}
		if err := syncTreeGuarded(wtPath, localAbs, baselines[i]); err != nil {
			return err
		}
	}
	return nil
}

// classifySpecial converts a SpecialFileError met during automatic sync into
// the gated-off state (spec §6.4: such errors remove .syncable).
func classifySpecial(env *Env, err error) error {
	var sfe *SpecialFileError
	var cce *ConcurrentChangeError
	if errors.As(err, &sfe) || errors.As(err, &cce) {
		return blockAndStop(env, &BlockedState{Reason: "special-file", Detail: err.Error()}, fmt.Errorf("%w; .syncable removed", err))
	}
	return err
}

func wtHasChanges(w *git.Runner) (bool, error) {
	entries, err := w.Status()
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

// commitWorktree stages everything and commits with the default message
// (format itself is a pending decision, spec §十).
func commitWorktree(env *Env, msg string) error {
	w := env.wtRunner()
	if msg == "" {
		msg = fmt.Sprintf("map(%s): update mapped content", env.MapRoot)
	}
	if err := w.AddAll(); err != nil {
		return fmt.Errorf("map: stage: %w", err)
	}
	if err := w.Commit(msg); err != nil {
		return fmt.Errorf("map: commit: %w", err)
	}
	env.logf("map %s: committed worktree changes", env.MapRoot)
	return nil
}

// pullFFOnly wraps pull --ff-only. On failure it classifies history from the
// commit graph instead of parsing localized/version-dependent Git stderr.
// An unavailable/stale upstream check is treated as an ordinary retryable
// failure; only a confirmed two-sided graph split blocks automatic sync.
func pullFFOnly(g *git.Runner, attempts int) (diverged bool, err error) {
	e := retry.Do(attempts, func() error { return g.PullFFOnly() }, 2*time.Second, git.IsTransient)
	if e == nil {
		return false, nil
	}
	diverged, inspectErr := upstreamDiverged(g)
	if inspectErr != nil {
		return false, e
	}
	return diverged, e
}

func upstreamDiverged(g *git.Runner) (bool, error) {
	out, err := g.Out("rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err != nil {
		return false, err
	}
	var ahead, behind int
	if n, err := fmt.Sscanf(out, "%d %d", &ahead, &behind); err != nil || n != 2 {
		return false, fmt.Errorf("parse ahead/behind %q", out)
	}
	return ahead > 0 && behind > 0, nil
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, ':'); i > 0 && len(s) > i+1 {
		s = strings.TrimSpace(s[i+1:])
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

// RunSchedulerTick performs one scheduler-driven map sync round — the
// `gns map sync` that existing cron/systemd/launchd/daemon entries run
// when map.sync=true (spec §7.4). Not-initialized machines skip silently.
func RunSchedulerTick(cfg *config.Config, logf func(string, ...any)) error {
	if !cfg.Map.Sync {
		return nil
	}
	env, err := ResolveEnv(cfg, "", logf)
	if err != nil {
		return err
	}
	initd, ierr := IsInitialized(env)
	if ierr != nil {
		// surface inspection failures instead of silently skipping —
		// a broken worktree must be visible in scheduler logs
		logf("map %s: inspect: %v", env.MapRoot, ierr)
		return nil
	}
	if !initd {
		return nil
	}
	if !HasSyncable(env) {
		env.logf("map %s: MANUAL_REQUIRED — skipped automatic sync; run `gnm status`", env.MapRoot)
		return nil
	}
	return Sync(env)
}
