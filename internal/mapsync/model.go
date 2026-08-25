// model.go: mapping items, path normalization and validation (spec §4.3).
package mapsync

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

// IsWindows reports whether the tool runs on Windows (path case folding and
// the auto→copy mode resolution depend on it).
func IsWindows() bool { return runtime.GOOS == "windows" }

// NormalizeLocal expands ~, resolves relative paths against cwd and cleans
// the result. It deliberately does NOT resolve symlinks: the link node
// itself is the mapped identity (spec §3.3).
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
// on Windows, case-folded (spec §3.3: comparisons ignore case there).
func LocalKey(p string) string {
	s := filepath.ToSlash(filepath.Clean(p))
	if IsWindows() {
		return strings.ToLower(s)
	}
	return s
}

// RepoPathOf resolves an item's repo-relative path for a map root:
// map-root scope prefixes the namespace; git-root scope uses the raw path.
func RepoPathOf(item config.MapItem, mapRoot string) (string, error) {
	if item.Scope != config.ScopeMapRoot && item.Scope != config.ScopeGitRoot {
		return "", fmt.Errorf("invalid scope %q (want %q or %q)",
			item.Scope, config.ScopeMapRoot, config.ScopeGitRoot)
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(item.Path)))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("empty repo path")
	}
	if filepath.IsAbs(item.Path) || strings.HasPrefix(clean, "/") ||
		clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("repo path must be relative without %q: %q", "..", item.Path)
	}
	if item.Scope == config.ScopeMapRoot {
		clean = mapRoot + "/" + clean
	}
	return clean, nil
}

// ValidateItems checks both sides of the mapping list for duplicates and
// mutual containment (spec §3.3). Returns all problems found.
func ValidateItems(items []config.MapItem, mapRoot string) []error {
	var errs []error
	var localKeys, repoKeys []string
	repoOwner := map[string]string{} // repoKey -> localKey that claimed it

	for i, it := range items {
		rp, err := RepoPathOf(it, mapRoot)
		if err != nil {
			errs = append(errs, fmt.Errorf("item #%d (%s): %v", i+1, it.LocalPath, err))
			continue
		}
		lk := LocalKey(NormalizeLocal(it.LocalPath))
		localKeys = append(localKeys, lk)
		repoKeys = append(repoKeys, rp)
		if owner, ok := repoOwner[rp]; ok && owner == lk {
			errs = append(errs, fmt.Errorf("duplicate mapping (same local and repo path): %s ↔ %s", it.LocalPath, rp))
		} else if owner != "" {
			errs = append(errs, fmt.Errorf("duplicate repo path %s (also mapped from %s)", rp, owner))
		} else {
			repoOwner[rp] = lk
		}
	}
	errs = append(errs, containmentErrors(localKeys, "local")...)
	errs = append(errs, containmentErrors(repoKeys, "repo")...)
	return errs
}

// containmentErrors reports pairs where one path contains another — nested
// mappings would make copy direction ambiguous (spec §3.3).
func containmentErrors(keys []string, side string) []error {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	var errs []error
	for i := 1; i < len(sorted); i++ {
		if within(sorted[i], sorted[i-1]) {
			errs = append(errs, fmt.Errorf("%s paths must not nest: %s contains %s", side, sorted[i-1], sorted[i]))
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
