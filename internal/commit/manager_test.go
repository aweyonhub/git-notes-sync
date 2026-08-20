package commit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

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

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "core.autocrlf", "false")
	return dir
}

func writeChange(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommitNowCustomMessage(t *testing.T) {
	dir := initRepo(t)
	writeChange(t, dir)

	m := New(dir, config.Defaults(), nil)
	made, err := m.CommitNow("", "custom subject")
	if err != nil || !made {
		t.Fatalf("CommitNow: made=%v err=%v", made, err)
	}
	if got := runGit(t, dir, "log", "-1", "--format=%s"); got != "custom subject" {
		t.Fatalf("commit subject = %q, want \"custom subject\"", got)
	}
}

func TestCommitNowCustomMessageSkipsAI(t *testing.T) {
	dir := initRepo(t)
	writeChange(t, dir)

	cfg := config.Defaults()
	cfg.CommitMessage = config.MessageAI
	cfg.AI = config.AI{
		Type:       "api",
		BaseURL:    "http://127.0.0.1:1/v1", // unreachable: must not be called
		Model:      "m",
		APIKeyEnv:  "NOTES_TEST_KEY",
		TimeoutSec: 1,
	}
	t.Setenv("NOTES_TEST_KEY", "k")

	m := New(dir, cfg, nil)
	made, err := m.CommitNow(config.MessageAI, "custom subject")
	if err != nil || !made {
		t.Fatalf("CommitNow: made=%v err=%v", made, err)
	}
	if got := runGit(t, dir, "log", "-1", "--format=%s"); got != "custom subject" {
		t.Fatalf("explicit message must win over AI mode, got %q", got)
	}
}
