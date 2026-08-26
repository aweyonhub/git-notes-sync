// wildcards.go: `*` pattern expansion over mapped subtrees (spec §5.1/§6.5).
package mapsync

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

type gitSide int

const (
	sideWorktree gitSide = iota // add: union(local fs, worktree fs)
	sideHEAD                    // get: union(local fs, HEAD tree)
)

// globRegexp compiles a path pattern where * matches any characters within
// a single level — including names starting with "." — and never crosses a
// separator (spec §5.1). Both full-segment stars (`/a/*`) and embedded ones
// (`*.md`, `foo*`) work. Matching is case-insensitive on Windows.
func globRegexp(pattern string) (*regexp.Regexp, error) {
	norm := filepath.ToSlash(filepath.Clean(pattern))
	var b strings.Builder
	if IsWindows() {
		b.WriteString("(?i)")
	}
	b.WriteString("^")
	for i, seg := range strings.Split(norm, "/") {
		if i > 0 {
			b.WriteString("/")
		}
		switch {
		case seg == "*":
			// full-segment star matches at least one name character
			b.WriteString("[^/]+")
		case seg != "" || i == 0:
			b.WriteString(segmentPattern(seg))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// segmentPattern converts one path segment into regex: embedded stars match
// any (possibly empty) run of non-separator characters; everything else is
// literal.
func segmentPattern(seg string) string {
	parts := strings.Split(seg, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return strings.Join(parts, "[^/]*")
}

// selectNodes resolves CLI args (paths, * patterns, or -A) to per-item node
// lists. Values are slash-relative paths under the mapping root; "" is the
// root itself. Candidates come from the union of the local filesystem and
// the requested git side; a pattern matching nothing is an error.
func selectNodes(env *Env, args []string, all bool, side gitSide) (map[int][]string, error) {
	out := map[int][]string{}
	cached := map[int][]string{}
	add := func(idx int, rel string) {
		for _, r := range out[idx] {
			if r == rel {
				return
			}
		}
		out[idx] = append(out[idx], rel)
	}

	if all {
		for idx := range env.Cfg.Map.Items {
			add(idx, "")
		}
	}

	for _, arg := range args {
		norm := NormalizeLocal(arg)
		if !strings.Contains(filepath.ToSlash(norm), "*") {
			idx, err := findOwningItem(env.Cfg.Map.Items, norm)
			if err != nil {
				return nil, fmt.Errorf("map add/get: %v", err)
			}
			rootAbs := NormalizeLocal(env.Cfg.Map.Items[idx].LocalPath)
			rel := relUnder(LocalKey(rootAbs), LocalKey(norm))
			if rel != "" {
				exists, err := exactSelectionExists(env, env.Cfg.Map.Items[idx], rel, side)
				if err != nil {
					return nil, err
				}
				if !exists {
					return nil, fmt.Errorf("map add/get: path does not exist on either selected side: %s", arg)
				}
			}
			add(idx, rel)
			continue
		}

		re, err := globRegexp(norm)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %v", arg, err)
		}
		matchedAny := false
		for idx, item := range env.Cfg.Map.Items {
			rootAbs := NormalizeLocal(item.LocalPath)
			cands, ok := cached[idx]
			if !ok {
				cands, err = unionCandidates(env, item, side)
				if err != nil {
					return nil, err
				}
				cands = append(cands, "") // allow a pattern to select a mapping root
				cached[idx] = cands
			}
			var matched []string
			for _, candRel := range cands {
				full := filepath.ToSlash(rootAbs)
				if candRel != "" {
					full += "/" + candRel
				}
				if re.MatchString(full) {
					matched = append(matched, candRel)
				}
			}
			for _, rel := range collapseDescendants(matched) {
				add(idx, rel)
				matchedAny = true
			}
		}
		if !matchedAny {
			return nil, fmt.Errorf("pattern %q matched nothing", arg)
		}
	}
	return out, nil
}

func exactSelectionExists(env *Env, item config.MapItem, rel string, side gitSide) (bool, error) {
	if kindOf(env.localJoin(item, rel)) != kMissing {
		return true, nil
	}
	wtPath, repoRel, err := env.worktreeJoin(item, rel)
	if err != nil {
		return false, err
	}
	if side == sideWorktree {
		return kindOf(wtPath) != kMissing, nil
	}
	return env.headContains(repoRel)
}

// unionCandidates enumerates the candidate node set for one mapping:
// the local filesystem plus the requested git side (spec §6.5: add matches
// the local∪worktree union so deletions of locally-removed files stay
// selectable; get matches local∪HEAD).
func unionCandidates(env *Env, item config.MapItem, side gitSide) ([]string, error) {
	set := map[string]bool{}

	localRels, err := walkNodes(NormalizeLocal(item.LocalPath))
	if err != nil {
		return nil, err
	}
	for _, r := range localRels {
		set[r] = true
	}

	wtPath, err := env.worktreePathOf(item)
	if err != nil {
		return nil, err
	}
	switch side {
	case sideWorktree:
		wtRels, err := walkNodes(wtPath)
		if err != nil {
			return nil, err
		}
		for _, r := range wtRels {
			set[r] = true
		}
	case sideHEAD:
		headRels, err := env.headRels(item)
		if err != nil {
			return nil, err
		}
		for _, r := range headRels {
			set[r] = true
		}
	}

	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

// collapseDescendants drops nodes whose ancestor is also selected, so a
// selected directory is processed once, recursively (spec §5.1). Sorted
// input makes a single pass sufficient: every descendant of a kept node is
// adjacent-after it in sort order.
func collapseDescendants(rels []string) []string {
	sorted := append([]string(nil), rels...)
	sort.Strings(sorted)
	var kept []string
	last := ""
	for _, r := range sorted {
		if last != "" && within(r, last) {
			continue // descendant of the nearest kept ancestor
		}
		kept = append(kept, r)
		last = r
	}
	return kept
}
