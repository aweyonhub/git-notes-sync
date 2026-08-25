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
	if !IsInitialized(env) {
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
	if !IsInitialized(env) {
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
	wtBranch := BranchName(env.MapRoot)

	// step 2: git-root pull --ff-only. Divergence is confirmed history damage
	// → gate off; everything else is transient → keep the gate (§3.2).
	diverged, err := pullFFOnly(g, env.Cfg.RetryAttempts)
	if err != nil {
		if diverged {
			RemoveSyncable(env)
			WriteBlocked(env, &BlockedState{
				Reason: "divergence",
				Detail: fmt.Sprintf("git-root branch %s diverged from its upstream", branch),
			})
			return fmt.Errorf("map: git-root diverged from upstream; run `gnm pull --force`, then add/get/commit/push")
		}
		return fmt.Errorf("map: git-root pull: %w (.syncable kept)", err)
	}

	// step 3: mapping-root presence/type pre-check — delete-safety, not a
	// Git conflict (§5.2).
	if viol := rootViolations(env); viol != "" {
		RemoveSyncable(env)
		WriteBlocked(env, &BlockedState{Reason: "mapping-root", Detail: viol})
		return fmt.Errorf("map: mapping root needs manual choice; .syncable removed\n  %s", viol)
	}

	// step 4: converge local→worktree (sync mode) / require clean (push mode)
	if auto {
		if err := convergeIntoWorktree(env); err != nil {
			return classifySpecial(env, err)
		}
		dirty, derr := wtHasChanges(w)
		if derr != nil {
			return derr
		}
		if dirty {
			if err := commitWorktree(env, ""); err != nil {
				return err
			}
		}
	} else {
		dirty, derr := wtHasChanges(w)
		if derr != nil {
			return derr
		}
		if dirty {
			return errors.New("map: worktree has uncommitted changes; finish `gnm add/get` + `gnm commit` first (see `gnm status`)")
		}
	}

	// step 5: THE conflict point (§2.3)
	preMerge := w.Head()
	if err := w.Merge(branch); err != nil {
		unmerged, uerr := w.Unmerged()
		if uerr == nil && len(unmerged) > 0 {
			_ = w.MergeAbort() // restore pre-merge committed content
			RemoveSyncable(env)
			WriteBlocked(env, &BlockedState{
				Reason:    "merge-conflict",
				Detail:    "worktree merge git-root conflicted",
				Conflicts: unmerged,
				GitHead:   g.Head(),
			})
			return fmt.Errorf("map: merge conflict in %d file(s); run `gnm pull`, choose with `gnm add|get`, commit, then push", len(unmerged))
		}
		return fmt.Errorf("map: worktree merge %s: %w", branch, err)
	}

	// step 7: post-merge re-check. On violation undo ONLY the merge commit
	// just created — its tree equals the protected pre-merge state, so this
	// hard reset never touches user content (§9 rule 4 targets base switches).
	if viol := rootViolations(env); viol != "" {
		_ = w.ResetHard(preMerge)
		RemoveSyncable(env)
		WriteBlocked(env, &BlockedState{Reason: "mapping-root", Detail: viol})
		return fmt.Errorf("map: mapping root diverged after merge; reverted merge\n  %s", viol)
	}

	// step 8: deploy merged worktree content down to the local files
	if err := deployFromWorktree(env); err != nil {
		return classifySpecial(env, err)
	}

	// step 9: fast-forward git-root to the merged worktree HEAD
	if err := g.MergeFFOnly(wtBranch); err != nil {
		RemoveSyncable(env)
		WriteBlocked(env, &BlockedState{Reason: "fastforward-failed", Detail: err.Error()})
		return fmt.Errorf("map: git-root cannot fast-forward to %s: %v; run `gnm status`", wtBranch, shortErr(err))
	}

	// step 10: push. A rejection keeps .syncable on purpose — the next
	// round's pull --ff-only is what officially judges divergence (§7.3).
	err = retry.Do(env.Cfg.RetryAttempts, func() error {
		return g.Push(remote, rbranch)
	}, 2*time.Second, git.IsTransient)
	if err != nil {
		if isPushRejected(err) {
			return fmt.Errorf("map: git-root push rejected (remote moved); .syncable kept — next sync re-judges: %w", err)
		}
		return fmt.Errorf("map: git-root push: %w", err)
	}

	// step 11: success — arm/keep the gate and clear stale block info
	CreateSyncable(env)
	ClearBlocked(env)
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
		// In link mode a mapped symlink IS the repo-side node: comparing its
		// kind against the worktree's would flag the normal steady state.
		if env.LinkMode() && lk == kSymlink {
			lk = wk
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
func convergeIntoWorktree(env *Env) error {
	if env.LinkMode() {
		return nil
	}
	for _, it := range env.Cfg.Map.Items {
		localAbs := NormalizeLocal(it.LocalPath)
		if kindOf(localAbs) == kMissing {
			continue // root violation would have caught real divergence
		}
		wtPath, err := env.worktreePathOf(it)
		if err != nil {
			continue
		}
		if err := SyncTree(localAbs, wtPath); err != nil {
			return err
		}
	}
	return nil
}

// deployFromWorktree pushes merged worktree content down to local files
// (chain step 8): copy mode re-converges each subtree; link mode only makes
// sure the root symlink exists for live mappings.
func deployFromWorktree(env *Env) error {
	for _, it := range env.Cfg.Map.Items {
		localAbs := NormalizeLocal(it.LocalPath)
		wtPath, err := env.worktreePathOf(it)
		if err != nil {
			continue
		}
		wk := kindOf(wtPath)
		if env.LinkMode() {
			if wk == kMissing {
				_ = os.RemoveAll(localAbs) // link with no target: remove the dead link
				continue
			}
			if err := EnsureSymlink(localAbs, wtPath); err != nil {
				return err
			}
			continue
		}
		if wk == kMissing {
			if err := os.RemoveAll(localAbs); err != nil {
				return err
			}
			continue
		}
		if err := ensureParent(localAbs); err != nil {
			return err
		}
		if err := SyncTree(wtPath, localAbs); err != nil {
			return err
		}
	}
	return nil
}

// classifySpecial converts a SpecialFileError met during automatic sync into
// the gated-off state (spec §6.4: such errors remove .syncable).
func classifySpecial(env *Env, err error) error {
	var sfe *SpecialFileError
	if errors.As(err, &sfe) {
		RemoveSyncable(env)
		WriteBlocked(env, &BlockedState{Reason: "special-file", Detail: sfe.Error()})
		return fmt.Errorf("%w; .syncable removed", err)
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

// pullFFOnly wraps pull --ff-only and classifies a failure as history
// divergence (non-fast-forward family) versus transient trouble.
func pullFFOnly(g *git.Runner, attempts int) (diverged bool, err error) {
	e := retry.Do(attempts, func() error { return g.PullFFOnly() }, 2*time.Second, git.IsTransient)
	if e == nil {
		return false, nil
	}
	s := strings.ToLower(e.Error())
	for _, p := range []string{"fast-forward", "fetch first", "[rejected]", "non-fast-forward"} {
		if strings.Contains(s, p) {
			return true, e
		}
	}
	return false, e
}

// isPushRejected mirrors the sync engine's rejection detection: remote moved
// vs network/auth trouble.
func isPushRejected(err error) bool {
	s := strings.ToLower(err.Error())
	for _, p := range []string{"[rejected]", "non-fast-forward", "fetch first", "stale info"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
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
	env, err := ResolveEnv(cfg, "", logf)
	if err != nil {
		return err
	}
	if !IsInitialized(env) || !cfg.Map.Sync {
		return nil
	}
	return Sync(env)
}
