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

// StatusUninitialized renders the useful part of status before the map
// environment can be resolved. This keeps the first-run command actionable.
func StatusUninitialized(cfg *config.Config) string {
	mapRoot := cfg.Map.MapRoot
	if mapRoot == "" {
		mapRoot = "<not configured>"
	}
	gitRoot := cfg.Map.GitRoot
	if gitRoot == "" {
		gitRoot = "<not configured>"
	}
	mode := cfg.Map.Mode
	if mode == "" {
		mode = "auto"
	}
	var b strings.Builder
	fmt.Fprintln(&b, "state:      NOT_INITIALIZED")
	fmt.Fprintf(&b, "map-root:   %s\n", mapRoot)
	fmt.Fprintf(&b, "mode:       %s\n", mode)
	fmt.Fprintf(&b, "git-root:   %s\n", gitRoot)
	fmt.Fprintln(&b, "worktree:   (not created)")
	fmt.Fprintln(&b, ".syncable:  false")
	fmt.Fprintln(&b, "\nmappings:")
	if len(cfg.Map.Items) == 0 {
		fmt.Fprintln(&b, "  (none configured)")
	} else {
		fmt.Fprintf(&b, "  %d configured mapping(s) (run `gnm config validate`)\n", len(cfg.Map.Items))
	}
	fmt.Fprintln(&b, "\nNext:")
	if cfg.Map.GitRoot == "" {
		fmt.Fprintln(&b, "  gnm config git-root <path>")
	}
	if cfg.Map.MapRoot == "" {
		fmt.Fprintln(&b, "  gnm config map-root <name>")
	}
	if cfg.Map.GitRoot != "" && cfg.Map.MapRoot != "" {
		fmt.Fprintln(&b, "  gnm init")
	}
	return b.String()
}

// Status renders state, facts and actionable next commands.
func Status(env *Env) (string, error) {
	var b strings.Builder
	g := env.gitRunner()
	w := env.wtRunner()
	init, initErr := IsInitialized(env)
	syncable := HasSyncable(env)
	blocked, err := ReadBlocked(env)
	if err != nil {
		return "", fmt.Errorf("read map blocked state: %w", err)
	}
	if initErr != nil && blocked == nil {
		// a broken worktree is exactly what status must diagnose — surface
		// the reason instead of failing the whole command
		blocked = &BlockedState{Reason: "worktree-broken", Detail: initErr.Error()}
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
		if initErr != nil {
			state = "MANUAL_REQUIRED" // broken debris needs a human decision
		}
	case syncable:
		state = "SYNCABLE"
	default:
		state = "MANUAL_REQUIRED"
	}

	fmt.Fprintf(&b, "state:      %s\n", state)
	fmt.Fprintf(&b, "map-root:   %s\n", env.MapRoot)
	modeCfg := env.Cfg.Map.Mode
	if modeCfg == "" {
		modeCfg = "auto" // same fallback StatusUninitialized applies
	}
	fmt.Fprintf(&b, "mode:       %s", modeCfg)
	if modeCfg != env.Mode {
		fmt.Fprintf(&b, " → %s", env.Mode) // auto resolved at init time
	}
	b.WriteString("\n")

	if gr := repoLine(g); gr != "" {
		fmt.Fprintf(&b, "git-root:   %s\n", env.GitRoot)
		fmt.Fprintf(&b, "            %s\n", gr)
	} else {
		fmt.Fprintf(&b, "git-root:   %s\n            NOT_A_GIT_REPOSITORY\n            Next: clone or create a Git repository there, or fix map.git_root\n", env.GitRoot)
	}
	dirty := false
	var wtEntries []git.Entry
	if init {
		fmt.Fprintf(&b, "worktree:   %s\n", env.Worktree)
		fmt.Fprintf(&b, "            %s\n", repoLine(w))
		if entries, err := w.Status(); err == nil {
			wtEntries = entries
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
	pendingChoices := []string{}
	hasStaged := false
	rows := make([]mapRow, 0, len(env.Cfg.Map.Items))
	for _, it := range env.Cfg.Map.Items {
		lk := kindOf(NormalizeLocal(it.LocalPath))
		hk := kMissing
		if _, err := env.worktreePathOf(it); err == nil {
			hk = headKind(w, it, env.MapRoot)
		}
		match := mappingKindsMatch(env, it, lk, hk)
		progress := mappingProgressFor(wtEntries, it, env.MapRoot)
		commitReady := progress.staged && !progress.remaining
		if !commitReady && ((!match && !(lk == kMissing && hk == kMissing)) ||
			(progress.staged && progress.remaining)) {
			pendingChoices = append(pendingChoices, it.LocalPath)
		}
		rows = append(rows, buildMapRow(it, lk, hk, match, progress))
	}
	b.WriteString(renderMapRows(rows))
	for _, e := range wtEntries {
		if e.Status != "??" && len(e.Status) > 0 && e.Status[0] != ' ' {
			hasStaged = true
			break
		}
	}

	if init && len(wtEntries) > 0 {
		b.WriteString(renderChanges(env, wtEntries))
	}

	b.WriteString("\n")
	b.WriteString(nextSteps(env, state, blocked, dirty, mappingProblem, operation, pendingChoices, hasStaged))
	return b.String(), nil
}

// renderChanges lists the worktree's dirty files with their owning mapping
// (or "other" for non-mapped files like .gitignore and the .gns snapshot) and
// a recommended action, so the user knows exactly which path to act on.
func renderChanges(env *Env, entries []git.Entry) string {
	var b strings.Builder
	b.WriteString("\nchanges:\n")
	for _, e := range entries {
		if local, ok := mappedLocalPathForRepo(env, e.Path); ok {
			b.WriteString("  " + local + "  [repo: " + e.Path + "]")
		} else {
			b.WriteString("  " + e.Path + "  [other]")
		}
		if action := changeAction(e.Status); action != "" {
			b.WriteString("  [" + action + "]")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// mappedLocalPathForRepo converts a mapped worktree-relative path into the
// exact local path accepted by gnm add/get. Non-mapped paths return false.
func mappedLocalPathForRepo(env *Env, repoPath string) (string, bool) {
	for _, it := range env.Cfg.Map.Items {
		rp, err := RepoPathOf(it, env.MapRoot)
		if err != nil {
			continue
		}
		rel, ok := repoRelUnder(repoPath, rp)
		if !ok {
			continue
		}
		local := NormalizeLocal(it.LocalPath)
		if rel != "" {
			local = filepath.Join(local, filepath.FromSlash(rel))
		}
		return local, true
	}
	return "", false
}

// changeAction maps a porcelain XY status to a recommended [TO …] marker:
// untracked → add, staged → commit, unstaged → add OR get.
func changeAction(status string) string {
	if status == "??" {
		return "TO add"
	}
	staged := len(status) > 0 && status[0] != ' '
	remaining := len(status) > 1 && status[1] != ' '
	if staged && remaining {
		return "TO add OR get"
	}
	if staged {
		return "TO commit"
	}
	return "TO add OR get"
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

// mapRow is one mapping row split into columns for aligned rendering.
type mapRow struct {
	local string // local path
	lk    string // local kind, only when sides differ
	scope string // (map-root) | (git-root)
	repo  string // repo path
	hk    string // HEAD kind, only when sides differ
	op    string // [TO …] recommendation (or [empty])
}

// mapProgress is the index/worktree progress under one mapping root. staged
// means the user has selected at least one change; remaining means there are
// also unstaged or untracked changes that still need a choice.
type mapProgress struct {
	staged    bool
	remaining bool
}

func mappingProgressFor(entries []git.Entry, item config.MapItem, mapRoot string) mapProgress {
	rp, err := RepoPathOf(item, mapRoot)
	if err != nil {
		return mapProgress{}
	}
	var p mapProgress
	for _, e := range entries {
		if _, ok := repoRelUnder(e.Path, rp); !ok {
			continue
		}
		if e.Status == "??" {
			p.remaining = true
			continue
		}
		if len(e.Status) > 0 && e.Status[0] != ' ' {
			p.staged = true
		}
		if len(e.Status) > 1 && e.Status[1] != ' ' {
			p.remaining = true
		}
	}
	return p
}

// repoRelUnder returns path relative to root when path is root itself or a
// descendant. Git status may append '/' to an untracked directory, so both
// values are normalized to slash form without a trailing separator first.
func repoRelUnder(path, root string) (string, bool) {
	path = strings.TrimSuffix(filepath.ToSlash(path), "/")
	root = strings.TrimSuffix(filepath.ToSlash(root), "/")
	if path == root {
		return "", true
	}
	if strings.HasPrefix(path, root+"/") {
		return strings.TrimPrefix(path, root+"/"), true
	}
	return "", false
}

// buildMapRow computes the columns of one mapping row.
func buildMapRow(item config.MapItem, lk, hk entryKind, match bool, progress mapProgress) mapRow {
	scope := "(git-root)"
	if item.Scope == config.ScopeMapRoot {
		scope = "(map-root)"
	}
	r := mapRow{local: item.LocalPath, scope: scope, repo: item.Path}
	commitReady := progress.staged && !progress.remaining
	if !match && !commitReady {
		r.lk = "[" + kindName(lk) + "]"
		r.hk = "[" + kindName(hk) + "]"
	}
	switch {
	case commitReady:
		r.op = "[TO commit]"
	case progress.staged && progress.remaining:
		r.op = "[TO add OR get]"
	case lk == kMissing && hk == kMissing:
		r.op = "[empty]"
	case !match && lk == kMissing:
		r.op = "[TO get]"
	case !match && hk == kMissing:
		r.op = "[TO add]"
	case !match:
		r.op = "[TO add OR get]"
	}
	return r
}

// renderMapRows aligns every mapping row column-wise (spec §6.4 redesign).
func renderMapRows(rows []mapRow) string {
	var wLocal, wLk, wScope, wRepo, wHk int
	for _, r := range rows {
		if n := len(r.local); n > wLocal {
			wLocal = n
		}
		if n := len(r.lk); n > wLk {
			wLk = n
		}
		if n := len(r.scope); n > wScope {
			wScope = n
		}
		if n := len(r.repo); n > wRepo {
			wRepo = n
		}
		if n := len(r.hk); n > wHk {
			wHk = n
		}
	}
	var b strings.Builder
	for _, r := range rows {
		line := fmt.Sprintf("  %-*s  %-*s  %-*s %-*s  %-*s  %s",
			wLocal, r.local, wLk, r.lk, wScope, r.scope, wRepo, r.repo, wHk, r.hk, r.op)
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return b.String()
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
func nextSteps(env *Env, state string, blocked *BlockedState, dirty bool, mappingProblem, operation string, pendingChoices []string, hasStaged bool) string {
	var s []string
	switch {
	case state == "NOT_INITIALIZED":
		s = append(s, "Next: gnm init")
	case blocked != nil && blocked.Reason == "worktree-broken":
		s = append(s,
			"Next: fix or remove the worktree directory shown above",
			"Then: gnm init   # re-creates it and reapplies every mapping")
	case operation != "":
		s = append(s,
			"Next: finish or abort the in-progress Git operation shown above",
			"Then: gnm status")
	case blocked != nil && blocked.Reason == "divergence":
		s = append(s,
			"Next: gnm pull --force",
			"      gnm status",
			"      gnm add <path> or gnm get <path>",
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
			s = append(s, fmt.Sprintf("      gnm add %s or gnm get %s", quoteCLI(local), quoteCLI(local)))
		}
		s = append(s, "      gnm commit -m \"resolve map conflict\"", "      gnm push")
	case state == "MANUAL_REQUIRED" && len(pendingChoices) > 0:
		if len(pendingChoices) == 1 {
			s = append(s,
				fmt.Sprintf("Next: gnm add %s   # keep local", quoteCLI(pendingChoices[0])),
				fmt.Sprintf("   or gnm get %s   # adopt HEAD", quoteCLI(pendingChoices[0])))
		} else {
			s = append(s,
				"Next: gnm add -A   # keep all local",
				"   or gnm add <path...>   # pick individually")
		}
		s = append(s, "Then: gnm commit -m \"...\" ; gnm push")
	case state == "MANUAL_REQUIRED" && hasStaged:
		s = append(s, "Next: gnm commit -m \"...\"", "Then: gnm push")
	case state == "MANUAL_REQUIRED" && !dirty:
		s = append(s, "Next: gnm push   # confirm the clean initial/recovery state")
	case state == "MANUAL_REQUIRED":
		s = append(s,
			"Next: gnm add <path> or gnm get <path>   # choose content",
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
		// Double quotes are accepted by both cmd.exe and PowerShell. Windows
		// paths cannot contain a literal double quote, so no extra escaping is
		// needed here.
		return `"` + path + `"`
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
