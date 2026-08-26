// mapcmd_guard_test.go: CLI-level guard rails that need no full map setup.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aweyonhub/git-notes-sync/internal/mapsync"
)

// TestCommitRejectsExplicitEmptyMessage pins spec §6.6: an explicitly empty
// -m/--message is a usage error, while omitting the flag keeps the default.
func TestCommitRejectsExplicitEmptyMessage(t *testing.T) {
	err := cmdMap([]string{"commit", "-m", ""})
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("expected explicit-empty rejection, got %v", err)
	}
	err = cmdMap([]string{"commit", "--message", ""})
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("expected long-form empty rejection, got %v", err)
	}
}

// TestConfigSaveRequiresInitBeforeOtherMapRoot guards against the orphan
// .gns tree: saving under another map-root before init must be a no-op,
// otherwise the leftover directory makes a later `gnm init` fail.
func TestConfigSaveRequiresInitBeforeOtherMapRoot(t *testing.T) {
	tmp := t.TempDir()
	appDir := filepath.Join(tmp, "app")
	t.Setenv("GNS_APP_DATA", appDir)
	cfgPath := filepath.Join(tmp, "config.toml")
	t.Setenv("GNS_CONFIG", cfgPath)

	gitRoot := filepath.ToSlash(filepath.Join(tmp, "norepo"))
	cfg := "[map]\ngit_root = \"" + gitRoot + "\"\nmap_root = \"tm\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() error { return cmdMapConfig([]string{"save", "other"}) })
	if !strings.Contains(out, "not initialized") {
		t.Fatalf("expected not-initialized notice, got %q", out)
	}
	for _, mr := range []string{"tm", "other"} {
		if _, err := os.Stat(mapsync.WorktreeDir(mr)); !os.IsNotExist(err) {
			t.Fatalf("orphan worktree tree created for %q", mr)
		}
	}
}
