// model.go: mapping items, path normalization and validation (spec §4.3).
package mapsync

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

// IsWindows reports whether the tool runs on Windows (path case folding and
// the auto→copy mode resolution depend on it).
func IsWindows() bool { return runtime.GOOS == "windows" }

// NormalizeLocal expands ~, resolves relative paths against cwd and cleans
// the result. It deliberately does NOT resolve symlinks: the link node
// itself is the mapped identity (spec §4.3).
func NormalizeLocal(p string) string {
	p = expandHome(strings.TrimSpace(p))
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	return filepath.Clean(p)
}

// LocalKey is the comparison form of a local path: slash separators and,
// on Windows, case-folded (spec §4.3: comparisons ignore case there).
func LocalKey(p string) string {
	s := filepath.ToSlash(filepath.Clean(p))
	if IsWindows() {
		return strings.ToLower(s)
	}
	return s
}

func repoKey(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if IsWindows() {
		return strings.ToLower(p)
	}
	return p
}

// RepoPathOf resolves an item's repo-relative path for a map root:
// map-root scope prefixes the namespace; git-root scope uses the raw path.
func RepoPathOf(item config.MapItem, mapRoot string) (string, error) {
	clean, err := validateRepoRel(item)
	if err != nil {
		return "", err
	}
	if item.Scope == config.ScopeMapRoot {
		clean = mapRoot + "/" + clean
	}
	if clean == ".gns/map" || strings.HasPrefix(clean, ".gns/map/") {
		return "", fmt.Errorf("repo path overlaps reserved .gns/map: %q", item.Path)
	}
	return clean, nil
}

// validateRepoRel checks the item scope and its unprefixed repository path.
// It deliberately does not require mapRoot, so pre-init config validation
// does not need a fake namespace.
func validateRepoRel(item config.MapItem) (string, error) {
	if item.Scope != config.ScopeMapRoot && item.Scope != config.ScopeGitRoot {
		return "", fmt.Errorf("invalid scope %q (want %q or %q)",
			item.Scope, config.ScopeMapRoot, config.ScopeGitRoot)
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(item.Path)))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("empty repo path")
	}
	// Reject backslashes: on POSIX they are ordinary filename characters,
	// but on Windows they act as path separators — the same repo path would
	// resolve to different directory trees on different machines, breaking
	// snapshot portability and cross-machine mapping integrity.
	if strings.Contains(item.Path, `\`) {
		return "", fmt.Errorf("repo path must not contain backslash (use / on all platforms): %q", item.Path)
	}
	if filepath.IsAbs(item.Path) || filepath.VolumeName(item.Path) != "" || strings.HasPrefix(clean, "/") ||
		clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("repo path must be relative without %q: %q", "..", item.Path)
	}
	for _, part := range strings.Split(clean, "/") {
		if strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("repo path must not contain .git: %q", item.Path)
		}
		if strings.ContainsAny(part, `<>:"|?*`) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || windowsReservedName(part) {
			return "", fmt.Errorf("repo path is not portable to Windows: %q", part)
		}
	}
	if item.Scope == config.ScopeGitRoot && (clean == ".gns/map" || strings.HasPrefix(clean, ".gns/map/")) {
		return "", fmt.Errorf("repo path overlaps reserved .gns/map: %q", item.Path)
	}
	return clean, nil
}

// ValidateItems checks both sides of the mapping list for duplicates and
// mutual containment (spec §4.3). Returns all problems found.
func ValidateItems(items []config.MapItem, mapRoot string) []error {
	var errs []error
	var localKeys, repoKeys []string
	repoOwner := map[string]string{} // repoKey -> localKey that claimed it

	for i, it := range items {
		if strings.TrimSpace(it.LocalPath) == "" {
			errs = append(errs, fmt.Errorf("item #%d: empty local path", i+1))
			continue
		}
		rp, err := RepoPathOf(it, mapRoot)
		if err != nil {
			errs = append(errs, fmt.Errorf("item #%d (%s): %v", i+1, it.LocalPath, err))
			continue
		}
		lk := LocalKey(NormalizeLocal(it.LocalPath))
		localKeys = append(localKeys, lk)
		rk := repoKey(rp)
		repoKeys = append(repoKeys, rk)
		if owner, ok := repoOwner[rk]; ok && owner == lk {
			errs = append(errs, fmt.Errorf("duplicate mapping (same local and repo path): %s ↔ %s", it.LocalPath, rp))
		} else if owner != "" {
			errs = append(errs, fmt.Errorf("duplicate repo path %s (also mapped from %s)", rp, owner))
		} else {
			repoOwner[rk] = lk
		}
	}
	errs = append(errs, containmentErrors(localKeys, "local")...)
	errs = append(errs, containmentErrors(repoKeys, "repo")...)
	return errs
}

// ValidatePlacement prevents a mapping from copying the repository or map
// state into itself.
func ValidatePlacement(items []config.MapItem, gitRoot, mapRoot string) []error {
	var errs []error
	for i, it := range items {
		local := LocalKey(NormalizeLocal(it.LocalPath))
		for _, target := range []struct{ name, path string }{
			{"git-root", gitRoot},
			{"map state", StateDir(mapRoot)},
			{"worktree", WorktreeDir(mapRoot)},
		} {
			if strings.TrimSpace(target.path) == "" {
				continue
			}
			other := LocalKey(NormalizeLocal(target.path))
			if within(local, other) || within(other, local) {
				errs = append(errs, fmt.Errorf("item #%d overlaps %s: %s", i+1, target.name, it.LocalPath))
			}
		}
	}
	return errs
}

// containmentErrors reports pairs where one path contains another — nested
// mappings would make copy direction ambiguous (spec §4.3).
//
// Detection enumerates each key's "/"-prefix ancestors and probes the set.
// Sorted-adjacency comparison is NOT sufficient: a sibling like `parent.txt`
// sorts between `parent` and `parent/sub` ('.' < '/'), hiding the pair
// (verified: that exact input passed the old implementation).
func containmentErrors(keys []string, side string) []error {
	counts := make(map[string]int, len(keys))
	inSet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		counts[k]++
		inSet[k] = struct{}{}
	}
	var errs []error
	reported := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if _, dup := reported[k]; dup {
			continue
		}
		if counts[k] > 1 {
			// exact repeats: the old sorted-adjacency form caught these via
			// within(x, x); keep them errors now that detection changed
			errs = append(errs, fmt.Errorf("%s paths must not repeat: %s", side, k))
			reported[k] = struct{}{}
			continue
		}
		for p := k; ; {
			i := strings.LastIndexByte(p, '/')
			if i < 0 {
				break
			}
			p = p[:i]
			if _, ok := inSet[p]; !ok {
				continue
			}
			errs = append(errs, fmt.Errorf("%s paths must not nest: %s contains %s", side, p, k))
			reported[k] = struct{}{}
			break
		}
	}
	return errs
}

// within reports whether child equals root or lives under it (slash form).
func within(child, root string) bool {
	return child == root || strings.HasPrefix(child, root+"/")
}

// resolveSelection maps CLI path arguments onto owning items. Only paths at
// or under configured mapping roots are accepted (spec §5.1).
func findOwningItem(items []config.MapItem, abs string) (idx int, err error) {
	key := LocalKey(abs)
	best := -1
	bestLen := -1
	for i, it := range items {
		root := LocalKey(NormalizeLocal(it.LocalPath))
		if !within(key, root) {
			continue
		}
		if len(root) > bestLen { // deepest root wins for nested roots
			best, bestLen = i, len(root)
		}
	}
	if best < 0 {
		return -1, fmt.Errorf("%s is not within any configured mapping", abs)
	}
	return best, nil
}

// relUnder returns the slash-separated relative path of abs under root
// ("" when equal). Both must be in comparison form already.
func relUnder(root, abs string) string {
	if abs == root {
		return ""
	}
	return strings.TrimPrefix(abs, root+"/")
}
