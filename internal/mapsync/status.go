// status.go: one screen answering "where am I and what do I run next"
// (spec §6.4). Only the three core states appear; everything else is detail.
package mapsync

import (
	"fmt"
	"path/filepath"
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
	blocked, err := ReadBlocked(env)
	if err != nil {
		return "", fmt.Errorf("read map blocked state: %w", err)
	}
	mappingProblem := ""
	operation := ""
	if init {
		mappingProblem = rootViolations(env)
		if op, err := g.MergeInProgress(); err == nil && op != "" {
			operation = fmt.Sprintf("git-root has %s", op)
		}
		if op, err := w.MergeInProgress(); err == nil && op != "" {
			operation = fmt.Sprintf("worktree has %s", op)
		}
	}

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
		fmt.Fprintf(&b, "git-root:   %s\n", env.GitRoot)
		fmt.Fprintf(&b, "            %s\n", gr)
	}
	dirty := false
	if init {
		fmt.Fprintf(&b, "worktree:   %s\n", env.Worktree)
		fmt.Fprintf(&b, "            %s\n", repoLine(w))
		if entries, err := w.Status(); err == nil {
			dirty = len(entries) > 0
			staged, unstaged, untracked := statusCounts(entries)
			fmt.Fprintf(&b, "            dirty=%v staged=%d unstaged=%d untracked=%d\n",
				dirty, staged, unstaged, untracked)
		}
		if !env.LinkMode() {
			localDirty := 0
			for _, it := range env.Cfg.Map.Items {
				wtPath, _ := env.worktreePathOf(it)
				if same, err := treesSameMetadata(NormalizeLocal(it.LocalPath), wtPath); err == nil && !same {
					localDirty++
				}
			}
			if localDirty > 0 {
				dirty = true
				fmt.Fprintf(&b, "            local-dirty=%d mapping(s)\n", localDirty)
			}
		}
	} else {
		fmt.Fprintf(&b, "worktree:   (not created)\n")
	}
	if blocked != nil {
		fmt.Fprintf(&b, "blocked:    %s — %s\n", blocked.Reason, blocked.Detail)
		for _, c := range blocked.Conflicts {
			fmt.Fprintf(&b, "  conflict: %s\n", localPathForRepo(env, c))
		}
	}
	if mappingProblem != "" {
		fmt.Fprintf(&b, "mapping:    %s\n", mappingProblem)
	}
	if operation != "" {
		fmt.Fprintf(&b, "operation:  %s\n", operation)
	}
	fmt.Fprintf(&b, ".syncable:  %v\n", syncable)

	b.WriteString("\nmappings:\n")
	if len(env.Cfg.Map.Items) == 0 {
		b.WriteString("  (none configured; `gnm config add -a <repo-path> <local-path>`)\n")
	}
	for _, it := range env.Cfg.Map.Items {
		lk := kindOf(NormalizeLocal(it.LocalPath))
		hk := kMissing
		if _, err := env.worktreePathOf(it); err == nil {
			hk = headKind(w, it, env.MapRoot)
		}
		note := ""
		match := mappingKindsMatch(env, it, lk, hk)
		switch {
		case lk == kMissing && hk == kMissing:
			note = "empty on both sides"
		case !match:
			note = "NEEDS CHOICE"
		}
		scope := it.Scope
		if scope == config.ScopeMapRoot {
			scope += fmt.Sprintf(" → %s/", env.MapRoot)
		}
		fmt.Fprintf(&b, "  %-28s [%s] local=%s HEAD=%s %s\n",
			it.LocalPath, scope, kindName(lk), kindName(hk), note)
	}

	b.WriteString("\n")
	b.WriteString(nextSteps(env, state, blocked, dirty, mappingProblem, operation))
	return b.String(), nil
}

func statusCounts(entries []git.Entry) (staged, unstaged, untracked int) {
	for _, e := range entries {
		if e.Status == "??" {
			untracked++
			continue
		}
		if len(e.Status) > 0 && e.Status[0] != ' ' {
			staged++
		}
		if len(e.Status) > 1 && e.Status[1] != ' ' {
			unstaged++
		}
	}
	return
}

func headKind(w *git.Runner, item config.MapItem, mapRoot string) entryKind {
	rp, err := RepoPathOf(item, mapRoot)
	if err != nil {
		return kMissing
	}
	out, err := w.Out("ls-tree", "HEAD", "--", rp)
	if err != nil || out == "" {
		return kMissing
	}
	fields := strings.Fields(strings.SplitN(out, "\n", 2)[0])
	if len(fields) < 2 {
		return kMissing
	}
	switch {
	case fields[0] == "120000":
		return kSymlink
	case fields[1] == "tree":
		return kDir
	default:
		return kFile
	}
}

func mappingKindsMatch(env *Env, item config.MapItem, localKind, headKind entryKind) bool {
	if !env.LinkMode() {
		return localKind == headKind
	}
	wtPath, err := env.worktreePathOf(item)
	if err != nil {
		return false
	}
	if localKind == kMissing || headKind == kMissing {
		return localKind == headKind
	}
	if !linkPointsTo(NormalizeLocal(item.LocalPath), wtPath) {
		return false
	}
	return kindOf(wtPath) == headKind
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
func nextSteps(env *Env, state string, blocked *BlockedState, dirty bool, mappingProblem, operation string) string {
	var s []string
	switch {
	case state == "NOT_INITIALIZED":
		s = append(s, "Next: gnm init")
	case operation != "":
		s = append(s,
			"Next: finish or abort the in-progress Git operation shown above",
			"Then: gnm status")
	case blocked != nil && blocked.Reason == "divergence":
		s = append(s,
			"Next: gnm pull --force",
			"      gnm status",
			"      gnm add <path> 或 gnm get <path>",
			"      gnm commit -m \"resolve map divergence\"",
			"      gnm push")
	case blocked != nil && blocked.Reason == "merge-conflict":
		if strings.Contains(blocked.Detail, "abort failed") {
			s = append(s, fmt.Sprintf("Next: git -C %s merge --abort", quoteCLI(env.Worktree)))
		} else {
			s = append(s, "Next: gnm pull")
		}
		s = append(s, "      gnm status")
		for _, p := range blocked.Conflicts {
			local := localPathForRepo(env, p)
			s = append(s, fmt.Sprintf("      gnm add %s 或 gnm get %s", quoteCLI(local), quoteCLI(local)))
		}
		s = append(s, "      gnm commit -m \"resolve map conflict\"", "      gnm push")
	case state == "MANUAL_REQUIRED" && mappingProblem != "":
		s = append(s,
			"Next: gnm add <path>    # keep the local version",
			"   or gnm get <path>    # adopt the HEAD version",
			"Then: gnm commit -m \"...\" ; gnm push")
	case state == "MANUAL_REQUIRED" && !dirty:
		s = append(s, "Next: gnm push   # confirm the clean initial/recovery state")
	case state == "MANUAL_REQUIRED":
		s = append(s,
			"Next: gnm add <path> 或 gnm get <path>   # choose content",
			"Then: gnm push                          # re-arm .syncable")
	case state == "SYNCABLE" && dirty:
		s = append(s,
			"Next: gnm sync",
			"   or gnm add <path...> ; gnm commit -m \"...\" ; gnm push")
	default: // SYNCABLE + clean
		s = append(s, "Next: nothing required",
			"Optional: gnm sync   # sync immediately")
	}
	return strings.Join(s, "\n") + "\n"
}

func quoteCLI(path string) string {
	if IsWindows() {
		return "'" + strings.ReplaceAll(path, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(path, "'", `'"'"'`) + "'"
}

func localPathForRepo(env *Env, repoPath string) string {
	repoPath = filepath.ToSlash(repoPath)
	for _, it := range env.Cfg.Map.Items {
		rp, err := RepoPathOf(it, env.MapRoot)
		if err != nil || !within(repoPath, rp) {
			continue
		}
		rel := strings.TrimPrefix(repoPath, rp)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return NormalizeLocal(it.LocalPath)
		}
		return filepath.Join(NormalizeLocal(it.LocalPath), filepath.FromSlash(rel))
	}
	return filepath.Join(env.Worktree, filepath.FromSlash(repoPath))
}
