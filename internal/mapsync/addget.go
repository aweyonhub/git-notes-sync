// addget.go: content selection (add/get) and the bare commit command
// (spec §6.5, §6.6). These are the only commands that decide which version
// of a path wins; everything else converges in one direction.
package mapsync

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/aweyonhub/git-notes-sync/internal/lock"
)

// Add selects the LOCAL version for each argument (path or * pattern) or for
// every mapping with -A, and stages it in the worktree (spec §6.5). -A ends
// with a whole-worktree `git add -A`, so tool-written files like the .gns
// config snapshot get staged alongside the mapped content.
func Add(env *Env, args []string, all bool) error {
	if all {
		initd, ierr := IsInitialized(env)
		if ierr != nil {
			return fmt.Errorf("map: inspect worktree: %w", ierr)
		}
		if !initd {
			return errors.New("map: not initialized; run `gnm init` first")
		}
		if err := ensureStateDir(env); err != nil {
			return err
		}
		unlock, err := lock.Acquire(env.State)
		if err != nil {
			return fmt.Errorf("map: %w", err)
		}
		defer unlock()
		if _, err := convergeIntoWorktree(env, true); err != nil {
			return classifySpecial(env, err)
		}
		if err := env.wtRunner().AddAll(); err != nil {
			return err
		}
		env.logf("map %s: staged entire worktree", env.MapRoot)
		return nil
	}
	return mutateSelected(env, args, false, sideWorktree, addNode)
}

// Get selects the HEAD version and deploys it to index + worktree + local
// (spec §6.5). Paths absent from HEAD confirm a deletion on both sides.
func Get(env *Env, args []string, all bool) error {
	return mutateSelected(env, args, all, sideHEAD, getNode)
}

// mutateSelected shares the plumbing of Add/Get: resolve arguments to nodes,
// collapse nested selections, take the lock, then run op per node.
func mutateSelected(env *Env, args []string, all bool, side gitSide, op func(env *Env, itemIdx int, rel string) error) error {
	initd, ierr := IsInitialized(env)
	if ierr != nil {
		return fmt.Errorf("map: inspect worktree: %w", ierr)
	}
	if !initd {
		return errors.New("map: not initialized; run `gnm init` first")
	}
	if len(args) == 0 && !all {
		return errors.New("map: nothing selected (pass paths/patterns or -A)")
	}
	nodes, err := selectNodes(env, args, all, side)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return errors.New("map: nothing matched")
	}
	if err := ensureStateDir(env); err != nil {
		return err
	}
	unlock, err := lock.Acquire(env.State)
	if err != nil {
		return fmt.Errorf("map: %w", err)
	}
	defer unlock()

	idxs := make([]int, 0, len(nodes))
	for idx := range nodes {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	count := 0
	for _, idx := range idxs {
		rels := nodes[idx]
		sort.Strings(rels)
		for _, rel := range rels { // roots sort before their children; children were collapsed already
			if err := op(env, idx, rel); err != nil {
				return err
			}
			count++
		}
	}
	env.logf("map %s: %d node(s) processed", env.MapRoot, count)
	return nil
}

// addNode stages the local state of one node into the worktree.
func addNode(env *Env, idx int, rel string) error {
	it := env.Cfg.Map.Items[idx]
	localAbs := NormalizeLocal(it.LocalPath)
	if rel != "" {
		var err error
		localAbs, err = env.safeLocalPath(it, rel, env.LinkMode())
		if err != nil {
			return err
		}
	}
	wtAbs, repoRel, err := env.worktreeJoin(it, rel)
	if err != nil {
		return err
	}
	// Validate the worktree path doesn't escape via symlink before any delete.
	if wtSafe, werr := env.safeWorktreePath(it, rel); werr != nil {
		return werr
	} else {
		wtAbs = wtSafe
	}
	switch kindOf(localAbs) {
	case kOther:
		return &SpecialFileError{localAbs}
	case kMissing:
		// explicit delete-confirm: remove worktree side, stage the deletion
		if k := kindOf(wtAbs); k != kMissing {
			if k == kOther {
				return &SpecialFileError{wtAbs}
			}
			if err := os.RemoveAll(wtAbs); err != nil {
				return err
			}
		}
	default:
		if env.LinkMode() && rel == "" {
			// Link mode: the local root should be a managed symlink to the
			// worktree. If it's been replaced by a real file/dir (recovery
			// scenario), adopt the local content into the worktree so `gnm
			// add` actually publishes the user's version, then the caller
			// (mutateSelected/ReplaceLocalWithLink) rebuilds the symlink.
			wtRoot, rerr := env.worktreePathOf(it)
			if rerr != nil {
				return rerr
			}
			if !linkPointsTo(localAbs, wtRoot) {
				if err := SyncTree(localAbs, wtAbs); err != nil {
					return err
				}
			}
		} else if !env.LinkMode() {
			if err := SyncTree(localAbs, wtAbs); err != nil {
				return err
			}
		}
	}
	// Runner.Add already inserts the "--" separator
	return env.wtRunner().Add(repoRel)
}

// getNode stages the HEAD state of one node and converges it down to local.
func getNode(env *Env, idx int, rel string) error {
	it := env.Cfg.Map.Items[idx]
	wtAbs, repoRel, err := env.worktreeJoin(it, rel)
	if err != nil {
		return err
	}
	// Validate worktree path doesn't escape via symlink before any delete.
	wtSafe, werr := env.safeWorktreePath(it, rel)
	if werr != nil {
		return werr
	}
	wtAbs = wtSafe

	inHead, err := env.headContains(repoRel)
	if err != nil {
		return err
	}
	if !inHead {
		// Reset the index first. get adopts HEAD and must not stage a deletion.
		if err := env.wtRunner().ResetPaths("HEAD", repoRel); err != nil {
			return err
		}
		for _, p := range []string{wtAbs, env.localJoin(it, rel)} {
			if k := kindOf(p); k == kOther {
				return &SpecialFileError{p}
			}
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
		return nil
	}

	// Remove the selected worktree node first so checkout also drops untracked
	// children that are absent from HEAD.
	if kindOf(wtAbs) == kOther {
		return &SpecialFileError{wtAbs}
	}
	if err := os.RemoveAll(wtAbs); err != nil {
		return err
	}
	if out, cerr := env.wtRunner().Out("checkout", "HEAD", "--", repoRel); cerr != nil {
		_ = out
		return fmt.Errorf("checkout %s: %w", repoRel, cerr)
	}

	// deploy to local: link mode is covered by the root symlink, except when
	// the root itself was restored from absence. If the root link is missing
	// (e.g. after pull --mixed), EnsureSymlink it before the early return so
	// the mapping is actually deployed to local (spec §5.1 get contract).
	if env.LinkMode() && rel != "" {
		localRoot := env.localJoin(it, "")
		wtRoot, rerr := env.worktreePathOf(it)
		if rerr != nil {
			return rerr
		}
		if !linkPointsTo(localRoot, wtRoot) && kindOf(localRoot) != kOther {
			if err := os.RemoveAll(localRoot); err != nil {
				return err
			}
			if err := EnsureSymlink(localRoot, wtRoot); err != nil {
				return err
			}
		}
		return nil
	}
	localAbs := env.localJoin(it, rel)
	switch kindOf(wtAbs) {
	case kMissing:
		return os.RemoveAll(localAbs)
	case kOther:
		return &SpecialFileError{wtAbs}
	}
	if env.LinkMode() {
		if kindOf(localAbs) == kOther {
			return &SpecialFileError{localAbs}
		}
		if !linkPointsTo(localAbs, wtAbs) {
			if err := os.RemoveAll(localAbs); err != nil {
				return err
			}
		}
		return EnsureSymlink(localAbs, wtAbs)
	}
	if err := ensureParent(localAbs); err != nil {
		return err
	}
	return SyncTree(wtAbs, localAbs)
}

// headContains reports whether HEAD tracks repoRel itself or anything under
// it (file node or directory subtree).
func (e *Env) headContains(repoRel string) (bool, error) {
	names, err := e.wtRunner().LsTreeHead(repoRel)
	if err != nil {
		return false, err
	}
	return len(names) > 0, nil
}

// Commit commits whatever is staged — never staging anything itself, never
// touching remotes or .syncable (spec §6.6). Takes the map lock so a manual
// commit cannot interleave with a running chain on the same worktree.
func Commit(env *Env, msg string) error {
	initd, ierr := IsInitialized(env)
	if ierr != nil {
		return fmt.Errorf("map: inspect worktree: %w", ierr)
	}
	if !initd {
		return errors.New("map: not initialized; run `gnm init` first")
	}
	if err := ensureStateDir(env); err != nil {
		return err
	}
	unlock, err := lock.Acquire(env.State)
	if err != nil {
		return fmt.Errorf("map: %w", err)
	}
	defer unlock()

	w := env.wtRunner()
	staged, err := w.HasStaged()
	if err != nil {
		return err
	}
	if !staged {
		env.logf("map %s: nothing staged", env.MapRoot)
		return nil
	}
	if msg == "" {
		msg = defaultCommitMessage(env.MapRoot)
	}
	if err := w.Commit(msg); err != nil {
		return fmt.Errorf("map: commit: %w", err)
	}
	env.logf("map %s: committed", env.MapRoot)
	return nil
}

// defaultCommitMessage is the automatic message format (exact wording is an
// open item, spec §十).
func defaultCommitMessage(mapRoot string) string {
	return "map(" + mapRoot + "): update mapped content"
}
