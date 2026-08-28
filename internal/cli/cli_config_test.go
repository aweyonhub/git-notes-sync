package cli

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aweyonhub/git-notes-sync/internal/mapsync"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	return string(out)
}

func newCfgFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCmdConfig_SetTopLevel(t *testing.T) {
	p := newCfgFile(t, "auto_commit = true\n")
	if err := cmdConfig([]string{"set", "sync_interval", "600", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "sync_interval = 600") {
		t.Errorf("value not written:\n%s", raw)
	}
}

func TestMapStatusBeforeConfigurationIsActionable(t *testing.T) {
	p := newCfgFile(t, "")
	out := captureStdout(t, func() error {
		return cmdMap([]string{"status", "-c", p})
	})
	if !strings.Contains(out, "state:      NOT_INITIALIZED") || !strings.Contains(out, "gnm config git-root") {
		t.Fatalf("unconfigured map status = %q", out)
	}
}

func TestMapAllRejectsPositionalPath(t *testing.T) {
	p := newCfgFile(t, "")
	err := cmdMap([]string{"add", "-A", "some/path", "-c", p})
	if err == nil || !strings.Contains(err.Error(), "usage: gnm add") {
		t.Fatalf("add -A with path error = %v", err)
	}
}

func TestMapCdPrintsCopyableCommand(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GNS_APP_DATA", filepath.Join(base, "app data"))
	p := newCfgFile(t, "[map]\ngit_root = \""+filepath.ToSlash(filepath.Join(base, "git root"))+"\"\nmap_root = \"tm\"\n")
	t.Setenv("GNS_CONFIG", p)

	out := captureStdout(t, func() error {
		return cmdMap([]string{"cd", "worktree"})
	})
	want := `cd "$(gnm cd -p worktree)"`
	if runtime.GOOS == "windows" {
		want = `pushd "` + mapsync.WorktreeDir("tm") + `"`
	}
	if got := strings.TrimSpace(out); got != want {
		t.Fatalf("gnm cd worktree = %q, want %q", got, want)
	}
}

func TestMapCdPathFlagsPrintRawPaths(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GNS_APP_DATA", filepath.Join(base, "app data"))
	gitRoot := filepath.Join(base, "git root")
	p := newCfgFile(t, "[map]\ngit_root = \""+filepath.ToSlash(gitRoot)+"\"\nmap_root = \"tm\"\n")

	gitOut := captureStdout(t, func() error {
		return cmdMap([]string{"cd", "--path", "git-root", "-c", p})
	})
	if got := strings.TrimSpace(gitOut); got != filepath.Clean(gitRoot) {
		t.Fatalf("gnm cd --path git-root = %q, want %q", got, filepath.Clean(gitRoot))
	}

	worktreeOut := captureStdout(t, func() error {
		return cmdMap([]string{"cd", "worktree", "-p", "-c", p})
	})
	wantWorktree := mapsync.WorktreeDir("tm")
	if got := strings.TrimSpace(worktreeOut); got != wantWorktree {
		t.Fatalf("gnm cd -p worktree = %q, want %q", got, wantWorktree)
	}
}

func TestMapCdHelpDescribesCommandAndPathModes(t *testing.T) {
	out := captureStdout(t, func() error {
		return cmdMap([]string{"cd", "-h"})
	})
	for _, want := range []string{
		"gnm cd <worktree|git-root>",
		"gnm cd -p <worktree|git-root>",
		"gnm cd --path <worktree|git-root>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("gnm cd -h missing %q:\n%s", want, out)
		}
	}

	topLevelOut := captureStdout(t, func() error {
		return cmdMap([]string{"-h"})
	})
	if !strings.Contains(topLevelOut, "gnm cd [-p|--path] <worktree|git-root>") {
		t.Fatalf("gnm -h missing cd path flags:\n%s", topLevelOut)
	}
}

func TestCmdConfig_SetNested(t *testing.T) {
	p := newCfgFile(t, `[ai]
type = "api"
`)
	if err := cmdConfig([]string{"set", "ai.timeout", "90", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "timeout = 90") {
		t.Errorf("nested value not written:\n%s", raw)
	}
}

func TestCmdConfig_SetStringQuoted(t *testing.T) {
	p := newCfgFile(t, "")
	if err := cmdConfig([]string{"set", "commit_message", "ai", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), `commit_message = "ai"`) {
		t.Errorf("string not quoted:\n%s", raw)
	}
}

func TestCmdConfig_SetTypeError(t *testing.T) {
	p := newCfgFile(t, "")
	if err := cmdConfig([]string{"set", "sync_interval", "abc", "-c", p}); err == nil {
		t.Fatal("expected type error")
	}
}

func TestCmdConfig_SetUnknownKey(t *testing.T) {
	p := newCfgFile(t, "")
	if err := cmdConfig([]string{"set", "no_such_key", "1", "-c", p}); err == nil {
		t.Fatal("expected unknown-key error")
	}
}

func TestCmdConfig_SetReposRejected(t *testing.T) {
	p := newCfgFile(t, "")
	err := cmdConfig([]string{"set", "repos", "x", "-c", p})
	if err == nil || !strings.Contains(err.Error(), "gns repos") {
		t.Fatalf("expected repos hint, got: %v", err)
	}
}

func TestCmdConfig_SetArrayRejected(t *testing.T) {
	p := newCfgFile(t, "")
	err := cmdConfig([]string{"set", "conflict.text_extensions", "x", "-c", p})
	if err == nil || !strings.Contains(err.Error(), "array") {
		t.Fatalf("expected array hint, got: %v", err)
	}
}

func TestCmdConfig_Get(t *testing.T) {
	p := newCfgFile(t, "sync_interval = 120\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"get", "sync_interval", "-c", p})
	})
	if strings.TrimSpace(out) != "120" {
		t.Errorf("get = %q, want 120", out)
	}
}

func TestCmdConfig_GetStringQuoted(t *testing.T) {
	p := newCfgFile(t, `commit_message = "static"`+"\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"get", "commit_message", "-c", p})
	})
	if strings.TrimSpace(out) != `"static"` {
		t.Errorf("get string = %q, want \"static\"", out)
	}
}

func TestCmdConfig_GetDefaultsWhenNoFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.toml")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"get", "sync_interval", "-c", p})
	})
	if strings.TrimSpace(out) != "600" {
		t.Errorf("default get = %q, want 600", out)
	}
}

func TestCmdConfig_List(t *testing.T) {
	p := newCfgFile(t, "sync_interval = 30\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"list", "-c", p})
	})
	if !strings.Contains(out, "sync_interval") {
		t.Errorf("list missing sync_interval:\n%s", out)
	}
	if !strings.Contains(out, "[default: 600]") {
		t.Errorf("list should flag overridden default:\n%s", out)
	}
	if !strings.Contains(out, "auto_commit") {
		t.Errorf("list missing other keys:\n%s", out)
	}
}

func TestCmdConfig_Unset(t *testing.T) {
	p := newCfgFile(t, "auto_commit = true\nsync_interval = 30\n")
	if err := cmdConfig([]string{"unset", "sync_interval", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "sync_interval") {
		t.Errorf("key not removed:\n%s", raw)
	}
	if !strings.Contains(string(raw), "auto_commit = true") {
		t.Errorf("sibling key lost:\n%s", raw)
	}
}

func TestCmdConfig_UnsetAbsent(t *testing.T) {
	p := newCfgFile(t, "auto_commit = true\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"unset", "sync_interval", "-c", p})
	})
	if !strings.Contains(out, "not set") {
		t.Errorf("absent unset should say 'not set': %q", out)
	}
}
