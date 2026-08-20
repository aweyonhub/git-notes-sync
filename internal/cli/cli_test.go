package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeArgs(t *testing.T) {
	vf := map[string]bool{"c": true, "p": true, "repo": true}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"flags first", []string{"-c", "f.toml", "notes"}, []string{"-c", "f.toml", "notes"}},
		{"positional first", []string{"notes", "-c", "f.toml"}, []string{"-c", "f.toml", "notes"}},
		{"mixed", []string{"-p", "/x", "notes", "-c", "f"}, []string{"-p", "/x", "-c", "f", "notes"}},
		{"flag with equals", []string{"notes", "-c=f.toml"}, []string{"-c=f.toml", "notes"}},
		{"boolean flag no value", []string{"notes", "-force"}, []string{"-force", "notes"}},
		{"flag value looks like flag", []string{"-c", "-x", "notes"}, []string{"-c", "-x", "notes"}},
		{"no args", nil, nil},
	}
	for _, c := range cases {
		got := normalizeArgs(c.in, vf)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: normalizeArgs(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestLogStamp(t *testing.T) {
	// under `go test`, stdout is a pipe, not a tty → timestamps are enabled
	if stdoutIsTerminal() {
		t.Skip("stdout is a terminal; cannot test redirected mode")
	}
	s := logStamp()
	// format: YYYY-MM-DD HH:MM:SS + space
	if len(s) != 20 {
		t.Fatalf("logStamp() = %q, want 20-char timestamp + space", s)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(s)); err != nil {
		t.Fatalf("logStamp() = %q not parseable: %v", s, err)
	}
}

func TestResolveInterval(t *testing.T) {
	// explicit -interval wins over anything
	if got := resolveInterval(120, "/nonexistent/x.toml"); got != 120 {
		t.Errorf("explicit interval: got %d", got)
	}

	// unreadable config → 600s default
	if got := resolveInterval(0, "/nonexistent/x.toml"); got != 600 {
		t.Errorf("fallback default: got %d", got)
	}

	// config sync_interval wins when no explicit flag
	dir := t.TempDir()
	p := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(p, []byte("sync_interval = 45\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveInterval(0, p); got != 45 {
		t.Errorf("config sync_interval: got %d", got)
	}

	// empty cfgPath resolves via the global config (GNS_CONFIG override)
	t.Setenv("GNS_CONFIG", p)
	if got := resolveInterval(0, ""); got != 45 {
		t.Errorf("global config via GNS_CONFIG: got %d", got)
	}
}

func TestResolveTarget(t *testing.T) {
	// no config file: positional is a raw path
	dir, err := resolveTarget("", "", "/some/path")
	if err != nil || dir != "/some/path" {
		t.Fatalf("raw path: %q, %v", dir, err)
	}

	// flag wins over positional
	dir, err = resolveTarget("", "/flag/path", "name")
	if err != nil || dir != "/flag/path" {
		t.Fatalf("flag priority: %q, %v", dir, err)
	}

	// empty: current directory
	dir, err = resolveTarget("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected cwd")
	}
}

func TestCmdResolveMutuallyExclusive(t *testing.T) {
	cases := [][]string{
		{"--ours", "--theirs"},
		{"--ours", "--ai"},
		{"--theirs", "--ai"},
		{"--ours", "--theirs", "--ai"},
	}
	for _, c := range cases {
		if err := cmdResolve(c); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("cmdResolve(%v) = %v, want mutual-exclusion error", c, err)
		}
	}
}

// runGit runs git in dir and returns trimmed combined output (fatal on error).
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initTestRepo creates an empty git repo with deterministic config.
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(dir, "seed.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "seed")
}

func TestSyncAllMergesRepoConfig(t *testing.T) {
	base := t.TempDir()
	r1 := filepath.Join(base, "r1")
	r2 := filepath.Join(base, "r2")
	initTestRepo(t, r1)
	initTestRepo(t, r2)

	// r1: repo-level auto_commit=false must be honored by sync-all
	if err := os.WriteFile(filepath.Join(r1, ".notes-sync.toml"), []byte("auto_commit = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// r2: no repo config → global auto_commit commits
	for _, r := range []string{r1, r2} {
		if err := os.WriteFile(filepath.Join(r, "note.md"), []byte("edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := `auto_commit = true
commit_debounce = 0
commit_max_wait = 0
[[repos]]
name = "r1"
path = "` + filepath.ToSlash(r1) + `"
[[repos]]
name = "r2"
path = "` + filepath.ToSlash(r2) + `"
`
	cfgPath := filepath.Join(base, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cmdSyncAll([]string{"-c", cfgPath}); err != nil {
		t.Fatalf("sync-all: %v", err)
	}
	if runGit(t, r1, "status", "--porcelain") == "" {
		t.Fatal("r1: auto_commit=false must be honored (worktree should stay dirty)")
	}
	if runGit(t, r2, "status", "--porcelain") != "" {
		t.Fatal("r2: global auto_commit should have committed everything")
	}
}
