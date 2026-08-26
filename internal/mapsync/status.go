// status.go: one screen answering "where am I and what do I run next"
// (spec §6.4). Only the three core states appear; everything else is detail.
package mapsync

import (
	"fmt"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
)

// Status renders state, facts and actionable next commands.
func Status(env *Env) (string, error) {
	var b strings.Builder
	g := env.gitRunner()
	w := env.wtRunner()
	init := IsInitialized(env)
	syncable := HasSyncable(env)
	blocked, _ := ReadBlocked(env)

	state := "NOT_INITIALIZED"
	switch {
	case !init:
	case syncable:
		state = "SYNCABLE"
	default:
		state = "MANUAL_REQUIRED"
	}

	fmt.Fprintf(&b, "state:      %s\n", state)
	fmt.Fprintf(&b, "map-root:   %s\n", env.MapRoot)
	fmt.Fprintf(&b, "mode:       %s", env.Cfg.Map.Mode)
	if env.Cfg.Map.Mode != env.Mode {
		fmt.Fprintf(&b, " → %s", env.Mode) // auto resolved at init time
	}
	b.WriteString("\n")

	if gr := repoLine(g); gr != "" {
		fmt.Fprintf(&b, "git-root:   %s\n", gr)
	}
	dirty := false
	if init {
		fmt.Fprintf(&b, "worktree:   %s (%s)\n", env.Worktree, BranchName(env.MapRoot))
		if entries, err := w.Status(); err == nil && len(entries) > 0 {
			dirty = true
			fmt.Fprintf(&b, "            dirty: %d change(s)\n", len(entries))
		}
	} else {
		fmt.Fprintf(&b, "worktree:   (not created)\n")
	}
	if blocked != nil {
		fmt.Fprintf(&b, "blocked:    %s — %s\n", blocked.Reason, blocked.Detail)
		for _, c := range blocked.Conflicts {
			fmt.Fprintf(&b, "  conflict: %s\n", c)
		}
	}
	fmt.Fprintf(&b, ".syncable:  %v\n", syncable)

	b.WriteString("\nmappings:\n")
	if len(env.Cfg.Map.Items) == 0 {
		b.WriteString("  (none configured; `gnm config add -a <repo-path> <local-path>`)\n")
	}
	needsChoice := false
	for _, it := range env.Cfg.Map.Items {
		local := NormalizeLocal(it.LocalPath)
		lk := kindOf(local)
		wk := kMissing
		wtPath := ""
		if p, err := env.worktreePathOf(it); err == nil {
			wtPath = p
			wk = kindOf(p)
		}
		note := ""
		wrongLink := false
		if env.LinkMode() {
			if linkPointsTo(local, wtPath) {
				lk = wk
			} else if lk == kSymlink {
				wrongLink = true
			}
		}
		switch {
		case lk == kMissing && wk == kMissing:
			note = "empty on both sides"
		case wrongLink || lk != wk:
			note = "NEEDS CHOICE"
			needsChoice = true
		}
		scope := it.Scope
		if scope == config.ScopeMapRoot {
			scope += fmt.Sprintf(" → %s/", env.MapRoot)
		}
		fmt.Fprintf(&b, "  %-28s [%s] local=%s repo=%s %s\n",
			it.LocalPath, scope, kindName(lk), kindName(wk), note)
	}

	b.WriteString("\n")
	b.WriteString(nextSteps(state, blocked, dirty, needsChoice))
	return b.String(), nil
}

func repoLine(g *git.Runner) string {
	if !g.IsRepo() {
		return ""
	}
	br := g.CurrentBranch()
	head := g.Head()
	disp := br
	if br == "" {
		disp = "(detached)"
	}
	if head != "" {
		disp += " @ " + short(head)
	}
	return disp
}

// nextSteps mirrors the suggestion table of spec §6.4.
func nextSteps(state string, blocked *BlockedState, dirty, needsChoice bool) string {
	var s []string
	switch {
	case state == "NOT_INITIALIZED":
		s = append(s, "Next: gnm init")
	case blocked != nil && blocked.Reason == "divergence":
		s = append(s,
			"Next: gnm pull --force",
			"      gnm status",
			"      gnm add <path> 或 gnm get <path>",
			"      gnm commit -m \"resolve map divergence\"",
			"      gnm push")
	case blocked != nil && blocked.Reason == "merge-conflict":
		s = append(s,
			"Next: gnm pull",
			"      gnm status",
			"      gnm add <path> 或 gnm get <path>",
			"      gnm commit -m \"resolve map conflict\"",
			"      gnm push")
	case state == "MANUAL_REQUIRED" && needsChoice:
		s = append(s,
			"Next: gnm add <path>    # keep the local version",
			"   or gnm get <path>    # adopt the HEAD version",
			"Then: gnm commit -m \"...\" ; gnm push")
	case state == "MANUAL_REQUIRED" && dirty:
		s = append(s,
			"Next: gnm add <path> 或 gnm get <path>   # choose content",
			"Then: gnm push                          # re-arm .syncable")
	case state == "MANUAL_REQUIRED":
		s = append(s, "Next: gnm push   # confirm state and arm .syncable")
	default: // SYNCABLE
		s = append(s, "Next: nothing required",
			"Optional: gnm sync   # sync immediately")
	}
	return strings.Join(s, "\n") + "\n"
}
