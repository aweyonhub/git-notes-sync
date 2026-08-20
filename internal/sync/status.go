package sync

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

// Status renders a human-readable report for `gns status`.
func Status(repo string, cfg *config.Config) (string, error) {
	g := newRunner(repo, cfg)
	if !g.IsRepo() {
		return "", errors.New("not a git repository")
	}
	var b strings.Builder

	top, _ := g.TopLevel()
	fmt.Fprintf(&b, "repo: %s\n", top)
	fmt.Fprintf(&b, "branch: %s", g.CurrentBranch())
	hasUpstream := false
	if remote, branch, ok := g.Upstream(); ok {
		hasUpstream = true
		fmt.Fprintf(&b, " (tracking %s/%s)", remote, branch)
	}
	fmt.Fprintln(&b)

	if hasUpstream {
		remote, branch, _ := g.Upstream()
		ahead, e1 := g.Count("refs/remotes/"+remote+"/"+branch, "HEAD")
		behind, e2 := g.Count("HEAD", "refs/remotes/"+remote+"/"+branch)
		if e1 == nil && e2 == nil {
			fmt.Fprintf(&b, "remote: ahead %d | behind %d (vs %s/%s)\n", ahead, behind, remote, branch)
		}
	} else {
		fmt.Fprintln(&b, "remote: no upstream configured")
	}

	entries, err := g.Status()
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		fmt.Fprintln(&b, "worktree: clean")
	} else {
		fmt.Fprintf(&b, "worktree: %d change(s)\n", len(entries))
		for _, e := range entries {
			fmt.Fprintf(&b, "  %s %s\n", e.Status, e.Path)
		}
	}

	conflicts, err := FindConflicts(repo, cfg)
	if err != nil {
		return "", err
	}
	if len(conflicts) == 0 {
		fmt.Fprintln(&b, "conflicts: none")
	} else {
		fmt.Fprintf(&b, "conflicts: %d file(s)\n", len(conflicts))
		for _, c := range conflicts {
			fmt.Fprintf(&b, "  %s (%d block(s))\n", c.Path, c.Blocks)
		}
	}
	return b.String(), nil
}
