// configops.go: the `gnm config` operations — item block editing at line
// level (comments and unrelated keys survive, same approach as the repos
// package), validation, and the per-machine config snapshot (spec §3/§4).
package mapsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
)

// ---------- list / validate ----------

// ListItems renders the effective mapping table.
func ListItems(cfg *config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "git-root: %s\n", cfg.Map.GitRoot)
	fmt.Fprintf(&b, "map-root: %s\n", cfg.Map.MapRoot)
	fmt.Fprintf(&b, "mode:     %s\n", cfg.Map.Mode)
	fmt.Fprintf(&b, "sync:     %v\n", cfg.Map.Sync)
	if len(cfg.Map.Items) == 0 {
		b.WriteString("items:    (none)\n")
		return b.String()
	}
	b.WriteString("items:\n")
	for _, it := range cfg.Map.Items {
		rp, err := RepoPathOf(it, cfg.Map.MapRoot)
		if err != nil {
			rp = "<invalid>"
		}
		fmt.Fprintf(&b, "  %-24s → %s  (%s ← %s)\n", rp, it.LocalPath, it.Scope, it.Path)
	}
	return b.String()
}

// ValidateReport returns every problem with the current [map] section.
func ValidateReport(cfg *config.Config) []error {
	var errs []error
	if cfg.Map.GitRoot == "" {
		errs = append(errs, errors.New("map.git_root is not set"))
	} else {
		gr := NormalizeLocal(cfg.Map.GitRoot)
		st, err := os.Stat(gr)
		if err != nil || !st.IsDir() {
			errs = append(errs, fmt.Errorf("map.git_root does not exist or is not a directory: %s", cfg.Map.GitRoot))
		} else if !newRunner(gr, cfg).IsRepo() {
			errs = append(errs, fmt.Errorf("map.git_root is not a Git repository: %s", cfg.Map.GitRoot))
		}
	}
	if err := ValidMapRoot(cfg.Map.MapRoot); err != nil {
		errs = append(errs, fmt.Errorf("map.map_root: %v", err))
	}
	switch cfg.Map.Mode {
	case "auto", "link", "copy", "":
	default:
		errs = append(errs, fmt.Errorf("map.mode must be auto|link|copy, got %q", cfg.Map.Mode))
	}
	for i, it := range cfg.Map.Items {
		if _, err := validateRepoRel(it); err != nil {
			errs = append(errs, fmt.Errorf("item #%d (%s): %v", i+1, it.LocalPath, err))
			continue
		}
		raw := strings.TrimSpace(it.LocalPath)
		if raw != "" && !strings.HasPrefix(raw, "~") && !filepath.IsAbs(raw) {
			errs = append(errs, fmt.Errorf("item #%d: local path should be absolute or ~/: %s", i+1, it.LocalPath))
		}
	}
	if cfg.Map.MapRoot != "" {
		errs = append(errs, ValidateItems(cfg.Map.Items, cfg.Map.MapRoot)...)
		errs = append(errs, ValidatePlacement(cfg.Map.Items, cfg.Map.GitRoot, cfg.Map.MapRoot)...)
	}
	return errs
}

// RequireMutableBase keeps an initialized mapping from being reinterpreted
// under a different repository, namespace or mode.
func RequireMutableBase(cfg *config.Config) error {
	if cfg == nil || len(cfg.Map.Items) == 0 || cfg.Map.MapRoot == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(WorktreeDir(cfg.Map.MapRoot), ".git")); err == nil {
		return errors.New("remove all mappings before changing git-root, map-root or mode")
	}
	return nil
}

func lockMap(env *Env) (func(), error) {
	if env == nil {
		return func() {}, nil
	}
	if err := ensureStateDir(env); err != nil {
		return nil, err
	}
	unlock, err := lock.Acquire(env.State)
	if err != nil {
		return nil, fmt.Errorf("map: %w", err)
	}
	return unlock, nil
}

// ---------- [[map.items]] textual editing ----------

func mapItemBlock(item config.MapItem) string {
	return fmt.Sprintf("\n[[map.items]]\nscope = %q\npath = %q\nlocal_path = %q\n",
		item.Scope, item.Path, item.LocalPath)
}

// AddItem appends one mapping to the user config and — when the worktree is
// already initialized — immediately materializes it and disarms .syncable
// (spec §4.5). cfg must be the freshly loaded config for cfgPath.
func AddItem(cfgPath string, cfg *config.Config, scope, repoPath, local string, env *Env) error {
	if strings.TrimSpace(local) == "" {
		return errors.New("local path must not be empty")
	}
	item := config.MapItem{
		Scope:     scope,
		Path:      filepath.ToSlash(filepath.Clean(strings.TrimSpace(repoPath))),
		LocalPath: expandHomeTilde(local),
	}
	if item.Scope != config.ScopeMapRoot && item.Scope != config.ScopeGitRoot {
		return fmt.Errorf("invalid scope %q (use -a for map-root scope, -A for git-root scope)", scope)
	}
	if cfg.Map.MapRoot == "" {
		return errors.New("set map-root first (`gnm config map-root <name>`)")
	}
	if _, err := RepoPathOf(item, cfg.Map.MapRoot); err != nil {
		return err
	}
	norm := NormalizeLocal(item.LocalPath)
	key := LocalKey(norm)
	for _, ex := range cfg.Map.Items {
		if LocalKey(NormalizeLocal(ex.LocalPath)) == key {
			return fmt.Errorf("local path already mapped: %s", ex.LocalPath)
		}
		rpEx, _ := RepoPathOf(ex, cfg.Map.MapRoot)
		rpNew, _ := RepoPathOf(item, cfg.Map.MapRoot)
		if repoKey(rpEx) == repoKey(rpNew) {
			return fmt.Errorf("repo path already mapped: %s", rpNew)
		}
	}
	if errs := ValidateItems(append(append([]config.MapItem{}, cfg.Map.Items...), item), cfg.Map.MapRoot); len(errs) > 0 {
		return errs[0]
	}
	if errs := ValidatePlacement(append(append([]config.MapItem{}, cfg.Map.Items...), item), cfg.Map.GitRoot, cfg.Map.MapRoot); len(errs) > 0 {
		return errs[0]
	}
	unlock, err := lockMap(env)
	if err != nil {
		return err
	}
	defer unlock()
	oldItems := append([]config.MapItem(nil), cfg.Map.Items...)

	// Disarm the gate BEFORE writing config: if the process crashes after
	// the new mapping is on disk but before disarm, the scheduler would run
	// the new mapping under the still-armed gate, bypassing manual
	// confirmation (§4.5). On any subsequent failure the gate stays
	// disarmed — the user must `gnm push` to re-arm after fixing.
	if env != nil {
		if err := RemoveSyncable(env); err != nil {
			return fmt.Errorf("disarm .syncable: %w", err)
		}
	}

	f, err := os.OpenFile(cfgPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		if env != nil {
			env.logf("map %s: .syncable removed; fix error then `gnm push` to re-arm", env.MapRoot)
		}
		return err
	}
	if _, err := f.WriteString(mapItemBlock(item)); err != nil {
		f.Close()
		if env != nil {
			env.logf("map %s: .syncable removed; fix error then `gnm push` to re-arm", env.MapRoot)
		}
		return err
	}
	if err := f.Close(); err != nil {
		if env != nil {
			env.logf("map %s: .syncable removed; fix error then `gnm push` to re-arm", env.MapRoot)
		}
		return err
	}
	if env != nil { // initialized: apply now (§4.5)
		rollbackConfig := func() error {
			_, rollbackErr := removeItemBlocksWhere(cfgPath, func(existing config.MapItem) bool {
				return existing.Scope == item.Scope && existing.Path == item.Path && existing.LocalPath == item.LocalPath
			})
			cfg.Map.Items = oldItems
			return rollbackErr
		}
		if err := applyMappingItem(env, item); err != nil {
			if rollbackErr := rollbackConfig(); rollbackErr != nil {
				return fmt.Errorf("apply mapping: %w (config rollback failed: %v); .syncable stays disarmed", err, rollbackErr)
			}
			return fmt.Errorf("apply mapping: %w; .syncable stays disarmed — push again to re-arm", err)
		}
		cfg.Map.Items = append(oldItems, item)
		env.logf("map %s: mapping applied; .syncable removed — push again to re-arm", env.MapRoot)
		return nil
	}
	cfg.Map.Items = append(oldItems, item)
	return nil
}

// expandHomeTilde keeps ~/ local paths portable across machines (§4.3);
// anything else is stored normalized.
func expandHomeTilde(local string) string {
	norm := NormalizeLocal(local)
	if home, err := os.UserHomeDir(); err == nil {
		homeNorm := NormalizeLocal(home)
		homeKey := LocalKey(homeNorm)
		normKey := LocalKey(norm)
		if within(normKey, homeKey) {
			if normKey == homeKey {
				return "~"
			}
			// Preserve original casing: the suffix after homeKey+"/" in
			// normKey maps to the same suffix in norm (same length, same
			// separators — LocalKey only changes case on Windows).
			suffix := strings.TrimPrefix(normKey, homeKey+"/")
			origRel := filepath.ToSlash(norm[len(norm)-len(suffix):])
			return "~/" + origRel
		}
	}
	return norm
}

// RemoveItems deletes mappings by exact local path (the unique identity),
// or all of them with all=true; initialized worktrees get each removal
// applied and lose .syncable (spec §4.5).
func RemoveItems(cfgPath string, cfg *config.Config, locals []string, all bool, env *Env) error {
	targets := map[string]bool{} // comparison keys
	var removedDefs []config.MapItem
	for _, l := range locals {
		targets[LocalKey(NormalizeLocal(l))] = true
	}
	matched := func(it config.MapItem) bool {
		if all {
			return true
		}
		return targets[LocalKey(NormalizeLocal(it.LocalPath))]
	}

	var keep []config.MapItem
	for _, it := range cfg.Map.Items {
		if matched(it) {
			removedDefs = append(removedDefs, it)
		} else {
			keep = append(keep, it)
		}
	}
	if len(removedDefs) == 0 {
		if all {
			return nil // nothing to remove is fine for --all
		}
		return fmt.Errorf("no mapping for %v", locals)
	}
	if !all && len(removedDefs) != len(targets) {
		return errors.New("one or more paths are not exact mappings; nothing removed")
	}
	unlock, err := lockMap(env)
	if err != nil {
		return err
	}
	defer unlock()

	if env != nil {
		if err := RemoveSyncable(env); err != nil {
			return fmt.Errorf("disarm .syncable: %w", err)
		}
		for i, it := range removedDefs {
			if err := removeMappingItem(env, it); err != nil {
				for j := i; j >= 0; j-- {
					_ = applyMappingItem(env, removedDefs[j])
				}
				return fmt.Errorf("unmap %s: %w", it.LocalPath, err)
			}
		}
	}
	if _, err := removeItemBlocksWhere(cfgPath, matched); err != nil {
		if env != nil {
			var rollbackErr error
			for i := len(removedDefs) - 1; i >= 0; i-- {
				if rerr := applyMappingItem(env, removedDefs[i]); rerr != nil && rollbackErr == nil {
					rollbackErr = rerr
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("remove config: %w (filesystem rollback failed: %v)", err, rollbackErr)
			}
		}
		return err
	}
	cfg.Map.Items = keep
	if env != nil {
		// When all mappings are removed, retire the worktree and branch so
		// the user can switch git-root or re-init cleanly (spec §4.5).
		if all && len(keep) == 0 {
			g := env.gitRunner()
			if err := g.WorktreeRemove(env.Worktree); err != nil {
				env.logf("map %s: worktree remove failed — retire incomplete; run `git -C %s worktree remove --force %s` manually, then `git -C %s branch -D %s`", env.MapRoot, env.GitRoot, env.Worktree, env.GitRoot, BranchName(env.MapRoot))
			} else {
				if err := g.DeleteBranch(BranchName(env.MapRoot)); err != nil {
					env.logf("map %s: branch delete failed (clean up manually): %v", env.MapRoot, err)
				}
				env.logf("map %s: all mappings removed; worktree retired — change git-root and run `gnm init` to start fresh", env.MapRoot)
			}
		} else {
			env.logf("map %s: %d mapping(s) removed; .syncable removed — review with `gnm status`", env.MapRoot, len(removedDefs))
		}
	}
	return nil
}

// removeItemBlocksWhere drops every `[[map.items]]` block whose parsed item
// satisfies match. Returns how many blocks were removed. A missing config
// file simply has nothing to remove.
func removeItemBlocksWhere(cfgPath string, match func(config.MapItem) bool) (int, error) {
	content, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	lines := strings.Split(string(content), "\n")
	out := make([]string, 0, len(lines))
	removed := 0
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "[[map.items]]" {
			j := i + 1
			var it config.MapItem
			for j < len(lines) {
				t := strings.TrimSpace(lines[j])
				if strings.HasPrefix(t, "[") {
					break // next header ends this block
				}
				if v, ok := parseItemKV(t, "scope"); ok {
					it.Scope = v
				}
				if v, ok := parseItemKV(t, "path"); ok {
					it.Path = v
				}
				if v, ok := parseItemKV(t, "local_path"); ok {
					it.LocalPath = v
				}
				j++
			}
			if match(it) {
				i = j
				removed++
				continue
			}
		}
		out = append(out, lines[i])
		i++
	}
	if removed == 0 {
		return 0, nil
	}
	text := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	return removed, os.WriteFile(cfgPath, []byte(text), 0o644)
}

// parseItemKV extracts `key = "value"` from a TOML line (repos.parseKV twin).
func parseItemKV(line, key string) (string, bool) {
	rest := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(rest, key) {
		return "", false
	}
	rest = strings.TrimLeft(rest[len(key):], " \t")
	if !strings.HasPrefix(rest, "=") {
		return "", false
	}
	rest = strings.TrimSpace(rest[1:])
	var value map[string]string
	if _, err := toml.Decode(key+" = "+rest, &value); err != nil {
		return "", false
	}
	return value[key], value[key] != ""
}

// ---------- snapshot (save / load) ----------

type snapshotFile struct {
	Map config.Map `toml:"map"`
}

// SaveSnapshot writes the current user [map] section into the worktree as
// `.gns/map/<map-root>.toml`. It never stages, commits or pushes (§4.6).
func SaveSnapshot(env *Env) error {
	path := filepath.Join(env.Worktree, filepath.FromSlash(SnapshotRel(env.MapRoot)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	buf.WriteString("# machine map snapshot (managed by `gnm config save`)\n")
	section := snapshotSection(*env.Cfg)
	section.MapRoot = env.MapRoot
	if err := toml.NewEncoder(&buf).Encode(snapshotFile{Map: section}); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

// snapshotSection copies only the fields that belong in a snapshot (drop the
// scheduler switch: whether THIS machine auto-syncs is not shared state).
func snapshotSection(c config.Config) config.Map {
	items := make([]config.MapItem, len(c.Map.Items))
	copy(items, c.Map.Items)
	return config.Map{
		GitRoot: c.Map.GitRoot,
		MapRoot: c.Map.MapRoot,
		Mode:    c.Map.Mode,
		Items:   items,
	}
}

// LoadSnapshot imports `.gns/map/<map-root>.toml` from git-root HEAD into
// the user config. Only legal before init; never touches real files (§4.6).
func LoadSnapshot(cfgPath string, cfg *config.Config, mapRootArg string, logf func(string, ...any)) error {
	if cfg.Map.GitRoot == "" {
		return errors.New("map.git_root is not set; point it at the repo first")
	}
	effective := mapRootArg
	if effective == "" {
		effective = cfg.Map.MapRoot
	}
	if effective == "" {
		return errors.New("no map-root given and none configured; run `gnm config map-root <name>`")
	}
	if err := ValidMapRoot(effective); err != nil {
		return err
	}
	g := newRunner(NormalizeLocal(cfg.Map.GitRoot), cfg)
	blob, err := g.ShowHeadFile(SnapshotRel(effective))
	if err != nil {
		return fmt.Errorf("read %s from git-root HEAD: %w", SnapshotRel(effective), err)
	}
	var snap snapshotFile
	if _, err := toml.Decode(blob, &snap); err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}
	// Repository location and scheduler preference belong to this machine;
	// the selected snapshot supplies mappings, mode and the target map-root.
	snap.Map.GitRoot = cfg.Map.GitRoot
	snap.Map.MapRoot = effective
	snap.Map.Sync = cfg.Map.Sync
	if snap.Map.Mode == "" {
		snap.Map.Mode = "auto"
	}
	if snap.Map.Mode != "auto" && snap.Map.Mode != "link" && snap.Map.Mode != "copy" {
		return fmt.Errorf("snapshot mode is invalid: %q", snap.Map.Mode)
	}
	for i := range snap.Map.Items {
		snap.Map.Items[i].LocalPath = expandHomeTilde(snap.Map.Items[i].LocalPath)
	}
	if errs := ValidateItems(snap.Map.Items, effective); len(errs) > 0 {
		return fmt.Errorf("snapshot mappings: %v", errs[0])
	}
	if errs := ValidatePlacement(snap.Map.Items, cfg.Map.GitRoot, effective); len(errs) > 0 {
		return fmt.Errorf("snapshot mappings: %v", errs[0])
	}

	// scalars via the standard editor (line-level, comments preserved)
	for _, kv := range [][2]string{
		{"git_root", snap.Map.GitRoot},
		{"map_root", snap.Map.MapRoot},
		{"mode", snap.Map.Mode},
	} {
		if kv[1] == "" {
			continue
		}
		if err := config.SetKey(cfgPath, "map", kv[0], kv[1]); err != nil {
			return err
		}
	}
	// items: replace wholesale
	if _, err := removeItemBlocksWhere(cfgPath, func(config.MapItem) bool { return true }); err != nil {
		return err
	}
	f, err := os.OpenFile(cfgPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	for _, it := range snap.Map.Items {
		if _, err := f.WriteString(mapItemBlock(it)); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}

	loaded := snapshotSection(config.Config{Map: snap.Map})
	loaded.Sync = cfg.Map.Sync
	cfg.Map = loaded
	if logf != nil {
		logf("map %s: imported %d item(s) from snapshot", snap.Map.MapRoot, len(snap.Map.Items))
		logf("next: `gnm init` applies every mapping")
	}
	return nil
}
