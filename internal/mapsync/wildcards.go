// wildcards.go: `*` pattern expansion over mapped subtrees (spec §5.1/§6.3).
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

// globRegexp compiles a path pattern where * matches any name within a
// single level — including names starting with "." — and never crosses a
// separator (spec §5.1). Matching is case-insensitive on Windows.
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
		if seg == "*" {
			b.WriteString("[^/]+")
		} else if seg != "" || i == 0 {
			b.WriteString(regexp.QuoteMeta(seg))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// selectNodes resolves CLI args (paths, * patterns, or -A) to per-item node
// lists. Values are slash-relative paths under the mapping root; "" is the
// root itself. Candidates come from the union of the local filesystem and
// the requested git side; a pattern matching nothing is an error.
func selectNodes(env *Env, args []string, all bool, side gitSide) (map[int][]string, error) {
	out := map[int][]string{}
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
		idx, err := findOwningItem(env.Cfg.Map.Items, norm)
		if err != nil {
			return nil, fmt.Errorf("map add/get: %v", err)
		}
		item := env.Cfg.Map.Items[idx]
		rootAbs := NormalizeLocal(item.LocalPath)

		if !strings.Contains(filepath.Base(filepath.ToSlash(norm)), "*") {
			add(idx, relUnder(LocalKey(rootAbs), LocalKey(norm)))
			continue
		}

		cands, err := unionCandidates(env, item, side)
		if err != nil {
			return nil, err
		}
		re, err := globRegexp(norm)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %v", arg, err)
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
		if len(matched) == 0 {
			return nil, fmt.Errorf("pattern %q matched nothing in mapping %s", arg, item.LocalPath)
		}
		for _, rel := range collapseDescendants(matched) {
			add(idx, rel)
		}
	}
	return out, nil
}

// unionCandidates enumerates the candidate node set for one mapping:
// the local filesystem plus the requested git side (spec §6.3: add matches
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
// selected directory is processed once, recursively (spec §5.1).
func collapseDescendants(rels []string) []string {
	sorted := append([]string(nil), rels...)
	sort.Strings(sorted)
	var kept []string
	for _, r := range sorted {
		nested := false
		for _, k := range kept {
			if within(r, k) {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, r)
		}
	}
	return kept
}
