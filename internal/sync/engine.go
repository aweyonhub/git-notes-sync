// Package sync implements the core sync model from the spec:
//
//	optional commit → protect working tree → fetch → merge (no rebase) →
//	preserve text conflicts → merge commit → push
package sync

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/commit"
	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
	"github.com/aweyonhub/git-notes-sync/internal/retry"
)

// Report describes what a sync run did; Err is non-nil when the repo failed.
type Report struct {
	Err error
	out func(string, ...any) // optional live sink; nil-safe
}

// logf forwards a step to the live sink when one is set (nil is fine).
// Callers print live via the sink; there is no buffered step list.
func (r *Report) logf(format string, args ...any) {
	if r.out != nil {
		r.out(format, args...)
	}
}

// Sync runs the full sync flow for one repository.
func Sync(repo string, cfg *config.Config, logf func(string, ...any)) *Report {
	rep := &Report{}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	rep.out = logf

	g := newRunner(repo, cfg)
	if !g.IsRepo() {
		rep.Err = errors.New("not a git repository")
		return rep
	}
	if top, err := g.TopLevel(); err == nil {
		repo = top
	}
	rep.logf("repo: %s", repo)

	gd, err := g.GitDir()
	if err != nil {
		rep.Err = fmt.Errorf("git dir: %w", err)
		return rep
	}
	unlock, err := lock.Acquire(gd)
	if err != nil {
		rep.Err = err
		return rep
	}
	defer unlock()

	if inProg, err := g.MergeInProgress(); err == nil && inProg != "" {
		rep.Err = fmt.Errorf("git is in %s state; finish it manually first (or run `gns resolve`)", inProg)
		return rep
	}

	// 1. optional commit (debounce-aware, never overwrites user edits)
	if cfg.AutoCommit {
		cm := commit.New(repo, cfg, logf)
		if made, err := cm.CommitIfNeeded(true); err != nil {
			rep.Err = fmt.Errorf("commit: %w", err)
			return rep
		} else if made {
			rep.logf("committed local changes")
		}
	}

	// 2. upstream info
	remote, branch, ok := g.Upstream()
	if !ok {
		rep.logf("no upstream configured for branch %q; nothing to fetch/push", g.CurrentBranch())
		return rep
	}

	// 3. fetch (network retry; auth/permission errors fail fast)
	if err := retry.Do(cfg.RetryAttempts, func() error {
		return g.Fetch(remote)
	}, 2*time.Second, git.IsTransient); err != nil {
		rep.Err = fmt.Errorf("fetch %s: %w", remote, err)
		return rep
	}
	rep.logf("fetched %s", remote)

	// 4. merge remote changes
	if err := mergeUpstream(g, remote, branch, cfg, rep); err != nil {
		rep.Err = err
		return rep
	}

	// 5. push (with re-sync follow-up when remote moved)
	if err := pushWithFollowup(g, remote, branch, cfg, rep); err != nil {
		rep.Err = err
		return rep
	}
	return rep
}

// mergeUpstream merges the remote tracking branch when we are behind.
func mergeUpstream(g *git.Runner, remote, branch string, cfg *config.Config, rep *Report) error {
	behind, err := g.Count("HEAD", "refs/remotes/"+remote+"/"+branch)
	if err != nil {
		return fmt.Errorf("count behind: %w", err)
	}
	if behind == 0 {
		rep.logf("up to date with %s/%s", remote, branch)
		return nil
	}
	if err := g.Merge("refs/remotes/" + remote + "/" + branch); err == nil {
		rep.logf("merged %s/%s (%d commit(s))", remote, branch, behind)
		return nil
	} else {
		// merge failed: either conflicts or local changes in the way
		unmerged, uerr := g.Unmerged()
		if uerr == nil && len(unmerged) > 0 {
			return handleConflicts(g, unmerged, cfg, rep)
		}
		return fmt.Errorf("merge %s/%s failed: %w (uncommitted local changes in the way? wait for commit or use `gns commit`)", remote, branch, err)
	}
}

// pushWithFollowup pushes, and when the remote moved mid-sync, re-fetches and
// re-merges before retrying (bounded to avoid infinite loops).
func pushWithFollowup(g *git.Runner, remote, branch string, cfg *config.Config, rep *Report) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := retry.Do(cfg.RetryAttempts, func() error {
			return g.Push(remote, branch)
		}, 2*time.Second, git.IsTransient)
		if err == nil {
			rep.logf("pushed to %s/%s", remote, branch)
			return nil
		}
		if !pushRejected(err) {
			return fmt.Errorf("push: %w", err)
		}
		rep.logf("push rejected (remote moved); re-syncing")
		if err := retry.Do(cfg.RetryAttempts, func() error {
			return g.Fetch(remote)
		}, 2*time.Second, git.IsTransient); err != nil {
			return fmt.Errorf("re-fetch: %w", err)
		}
		if err := mergeUpstream(g, remote, branch, cfg, rep); err != nil {
			return err
		}
	}
	return errors.New("push rejected after 3 attempts (remote keeps moving)")
}

// pushRejected distinguishes "remote moved" from network / auth failures.
func pushRejected(err error) bool {
	if _, ok := err.(*git.CmdError); !ok {
		return false
	}
	s := strings.ToLower(err.Error()) // includes stderr
	return strings.Contains(s, "[rejected]") ||
		strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first") ||
		strings.Contains(s, "stale info")
}
