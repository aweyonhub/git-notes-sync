// Package mapsync implements the `gns map` feature (see
// doc/git-notes-sync_map.md): mapping local files (dotfiles, configs,
// skills, scripts) into a dedicated git-root repository through a
// per-machine worktree branch, with a `.syncable` gate guarding automatic
// synchronization.
//
// Data flow (spec §1.5):
//
//	remote ←pull--ff-only— git-root ←ff-only merge— worktree HEAD
//	                                   ↕ add/get/commit
//	                        worktree files ←link/copy— local real files
package mapsync

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AppDataDir returns the GNS application data directory holding per-machine
// map state (worktrees, .syncable, blocked state). GNS_APP_DATA overrides it
// (tests use this to stay hermetic); the default follows os.UserConfigDir()
// like the global config: macOS ~/Library/Application Support, Linux
// ~/.config, Windows %AppData% — all under git-notes-sync/.
func AppDataDir() string {
	if p := os.Getenv("GNS_APP_DATA"); p != "" {
		return expandHome(p)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "git-notes-sync")
}

// MapBaseDir is <app-data>/map, the parent of all per-machine map state.
func MapBaseDir() string { return filepath.Join(AppDataDir(), "map") }

// StateDir is the per-map-root state directory (<app>/map/<map-root>)
// holding .syncable, blocked.json and the map lock.
func StateDir(mapRoot string) string { return filepath.Join(MapBaseDir(), mapRoot) }

// WorktreeDir is the fixed worktree location <app>/map/<map-root>-worktree.
func WorktreeDir(mapRoot string) string {
	return filepath.Join(MapBaseDir(), mapRoot+"-worktree")
}

// BranchName is the fixed machine worktree branch: gns/map/<map-root>-worktree.
func BranchName(mapRoot string) string { return "gns/map/" + mapRoot + "-worktree" }

// SnapshotRel is the repo-relative config snapshot path for a map root
// (.gns/map/<map-root>.toml).
func SnapshotRel(mapRoot string) string { return ".gns/map/" + mapRoot + ".toml" }

// BackupRef is the ref keeping the pre-reset worktree HEAD recoverable
// (spec §9.5: old HEAD must stay reachable).
func BackupRef(mapRoot string) string { return "refs/gns/map/" + mapRoot + "-backup" }

// GitRootBackupRef keeps the integration branch reachable before force pull.
func GitRootBackupRef(mapRoot string) string {
	return "refs/gns/map/" + mapRoot + "-git-root-backup"
}

var mapRootRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidMapRoot enforces path- and Git-ref-safe names: letters/digits/-/_/.,
// starting alphanumeric, no "..", no trailing ".lock", 64 chars max.
func ValidMapRoot(name string) error {
	if !mapRootRe.MatchString(name) ||
		strings.Contains(name, "..") ||
		strings.HasSuffix(name, ".") ||
		strings.HasSuffix(strings.ToLower(name), ".lock") ||
		windowsReservedName(name) {
		return fmt.Errorf("invalid map-root %q: use a portable letters/digits/._- name", name)
	}
	return nil
}

func windowsReservedName(name string) bool {
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

// expandHome resolves a leading ~ without depending on $HOME semantics that
// differ across platforms.
func expandHome(p string) string {
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
