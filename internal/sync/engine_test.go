package sync

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
)

// ---------- helpers: real git fixtures ----------

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitCmdAllowFail runs git and tolerates non-zero exit (e.g. a pull that
// leaves the repo mid-merge), returning combined output.
func gitCmdAllowFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

func gitInitBare(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "init", "--bare", "-b", "main")
}

func gitClone(t *testing.T, src, dst string) {
	t.Helper()
	// -c core.autocrlf=false keeps the clone checkout free of CRLF noise
	// regardless of the host's global git config
	gitCmd(t, filepath.Dir(dst), "-c", "core.autocrlf=false", "clone", src, filepath.Base(dst))
	gitConfig(t, dst)
}

// gitConfig pins deterministic behavior for tests (identity + line endings).
func gitConfig(t *testing.T, dir string) {
	t.Helper()
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "core.autocrlf", "false")
	gitCmd(t, dir, "config", "pull.rebase", "false")
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", msg)
}

func gitPush(t *testing.T, dir string) {
	t.Helper()
	gitCmd(t, dir, "push", "origin", "HEAD")
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gitLog(t *testing.T, dir string) []string {
	t.Helper()
	out := gitCmd(t, dir, "log", "--oneline")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q to contain %q", haystack, needle)
	}
}

func newTestConfig() *config.Config {
	cfg := config.Defaults()
	cfg.CommitDebounce = 0
	cfg.CommitMaxWait = 0
	return cfg
}

// setupRemote returns (remoteBare, a, b) with a main branch containing note.md.
func setupRemote(t *testing.T) (remote, a, b string) {
	t.Helper()
	tmp := t.TempDir()
	remote = filepath.Join(tmp, "remote.git")
	gitInitBare(t, remote)
	a = filepath.Join(tmp, "a")
	b = filepath.Join(tmp, "b")
	gitClone(t, remote, a) // a pushes the initial commit
	writeFile(t, a, "note.md", "line1\n")
	gitCommitAll(t, a, "init")
	gitPush(t, a)
	gitClone(t, remote, b) // b clones the non-empty remote (upstream set)
	return remote, a, b
}

// ---------- tests ----------

func TestSyncFastForwardAndPush(t *testing.T) {
	remote, _, b := setupRemote(t)
	cfg := newTestConfig()

	// b is up to date: no-op
	rep := Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("sync no-op: %v", rep.Err)
	}

	// b writes a note and syncs: auto-commit + push
	writeFile(t, b, "note.md", "line1\nfrom b\n")
	rep = Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("sync push: %v", rep.Err)
	}
	if len(gitCmd(t, b, "status", "--porcelain")) > 0 {
		t.Fatal("b should be clean after sync")
	}
	log := gitLog(t, remote)
	if len(log) < 2 {
		t.Fatalf("expected >= 2 commits on remote, got %v", log)
	}
	assertContains(t, log[0], "notes:")
}

func TestSyncConflictPreservedAndResolved(t *testing.T) {
	_, a, b := setupRemote(t)
	cfg := newTestConfig()

	// b picks up base state
	if rep := Sync(b, cfg, nil); rep.Err != nil {
		t.Fatalf("initial sync: %v", rep.Err)
	}

	// both sides edit note.md differently
	writeFile(t, a, "note.md", "line1\nA-change\n")
	gitCommitAll(t, a, "a change")
	gitPush(t, a)

	writeFile(t, b, "note.md", "line1\nB-change\n")
	rep := Sync(b, cfg, nil) // auto-commits B-change, then merge conflicts
	if rep.Err != nil {
		t.Fatalf("sync with conflict should not fail: %v", rep.Err)
	}

	// conflict preserved: markers committed, merge commit pushed, no unmerged paths
	content := readFile(t, b, "note.md")
	assertContains(t, content, "<<<<<<<")
	assertContains(t, content, ">>>>>>>")
	if len(gitCmd(t, b, "status", "--porcelain")) > 0 {
		t.Fatal("b should be clean (conflict already committed)")
	}
	g := newGitRunner(b)
	if un, _ := g.Unmerged(); len(un) > 0 {
		t.Fatalf("no unmerged paths expected, got %v", un)
	}
	if merges := gitCmd(t, b, "rev-list", "--count", "--merges", "HEAD"); merges == "0" {
		t.Fatal("expected a merge commit")
	}

	// remote received the merge commit
	gitCmd(t, a, "pull", "--no-edit")
	assertContains(t, readFile(t, a, "note.md"), "<<<<<<<")

	// resolve with --theirs: markers dropped, remote side kept, pushed
	// (b is local/ours, a is remote/theirs → A-change wins)
	n, err := Resolve(b, "theirs", cfg, nil, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 file resolved, got %d", n)
	}
	if got := readFile(t, b, "note.md"); got != "line1\nA-change\n" {
		t.Fatalf("unexpected content after resolve: %q", got)
	}
	gitCmd(t, a, "pull", "--no-edit")
	if got := readFile(t, a, "note.md"); got != "line1\nA-change\n" {
		t.Fatalf("remote should have resolved content: %q", got)
	}
}

func TestBinaryConflictKeepsLocalCopy(t *testing.T) {
	_, a, b := setupRemote(t)
	cfg := newTestConfig()

	writeFile(t, a, "bin.dat", "\x00\x01binaryA\n")
	gitCommitAll(t, a, "add binary")
	gitPush(t, a)

	if rep := Sync(b, cfg, nil); rep.Err != nil {
		t.Fatalf("sync binary base: %v", rep.Err)
	}

	// b changes the binary but does NOT push yet
	writeFile(t, b, "bin.dat", "\x00\x01binaryB\n")
	gitCommitAll(t, b, "b binary")

	// a pushes a conflicting change
	writeFile(t, a, "bin.dat", "\x00\x01binaryA2\n")
	gitCommitAll(t, a, "a binary")
	gitPush(t, a)

	// b syncs: merge conflicts on the binary → keeps local copy (ours),
	// merge commit is pushed
	rep := Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("binary conflict sync: %v", rep.Err)
	}
	if got := readFile(t, b, "bin.dat"); got != "\x00\x01binaryB\n" {
		t.Fatalf("expected local copy kept, got %q", got)
	}
	if len(gitCmd(t, b, "status", "--porcelain")) > 0 {
		t.Fatal("b should be clean")
	}
	// the merge commit reached the remote
	gitCmd(t, a, "pull", "--no-edit")
	if got := readFile(t, a, "bin.dat"); got != "\x00\x01binaryB\n" {
		t.Fatalf("remote should have the merge result, got %q", got)
	}
}

func TestDebounceSkipsAndMaxWaitForces(t *testing.T) {
	_, _, b := setupRemote(t)

	// debounce large: fresh edit defers commit and writes first-seen state
	cfg := config.Defaults()
	cfg.CommitDebounce = 3600
	cfg.CommitMaxWait = 3600

	writeFile(t, b, "note.md", "line1\nfresh edit\n")
	rep := Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("sync defer: %v", rep.Err)
	}
	if len(gitCmd(t, b, "status", "--porcelain")) == 0 {
		t.Fatal("expected commit to be deferred")
	}

	// max_wait forces the commit even while the file is still fresh
	gd := filepath.Join(b, ".git")
	statePath := filepath.Join(gd, "git-notes-sync.state")
	buf, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	old := fmt.Sprintf("{\"first_seen\": %d}", time.Now().Unix()-3600-10)
	if err := os.WriteFile(statePath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = buf
	rep = Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("sync force: %v", rep.Err)
	}
	if len(gitCmd(t, b, "status", "--porcelain")) > 0 {
		t.Fatal("expected forced commit to have happened")
	}
}

func TestDebouncePassesAfterQuietPeriod(t *testing.T) {
	_, _, b := setupRemote(t)
	cfg := config.Defaults()
	cfg.CommitDebounce = 60
	cfg.CommitMaxWait = 600

	writeFile(t, b, "note.md", "line1\nquiet edit\n")
	// make the edit look old: debounce passes on next check
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(filepath.Join(b, "note.md"), old, old); err != nil {
		t.Fatal(err)
	}
	rep := Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("sync: %v", rep.Err)
	}
	if len(gitCmd(t, b, "status", "--porcelain")) > 0 {
		t.Fatal("expected commit after quiet period")
	}
}

func TestAIMessageFallsBack(t *testing.T) {
	_, _, b := setupRemote(t)
	cfg := newTestConfig()
	cfg.CommitMessage = config.MessageAI
	cfg.AI = config.AI{
		Type:       "api",
		BaseURL:    "http://127.0.0.1:1/v1", // unreachable
		Model:      "test",
		APIKeyEnv:  "NOTES_TEST_KEY",
		TimeoutSec: 2,
	}
	t.Setenv("NOTES_TEST_KEY", "k")

	writeFile(t, b, "note.md", "line1\nai fallback edit\n")
	rep := Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("sync with ai fallback: %v", rep.Err)
	}
	msg := gitCmd(t, b, "log", "-1", "--format=%s")
	assertContains(t, msg, "notes:")
	if len(gitCmd(t, b, "status", "--porcelain")) > 0 {
		t.Fatal("b should be clean")
	}
}

func TestResolveAIUnconfiguredKeepsMarkers(t *testing.T) {
	_, _, b := setupRemote(t)
	cfg := newTestConfig()

	writeFile(t, b, "note.md", "line1\nB-change\n")
	rep := Sync(b, cfg, nil) // commit + push base
	if rep.Err != nil {
		t.Fatalf("sync: %v", rep.Err)
	}
	// fabricate a marker block directly (no merge needed for this test)
	content := "<<<<<<< HEAD\nline1\nA-change\n=======\nline1\nB-change\n>>>>>>> abc123\n"
	writeFile(t, b, "note.md", content)
	gitCommitAll(t, b, "manual conflict")

	// resolve --ai without AI configured: keeps markers, resolves nothing
	gen := newAIGen(nil)
	n, err := Resolve(b, "ai", cfg, gen, nil)
	if err != nil {
		t.Fatalf("resolve ai unconfigured: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 resolved, got %d", n)
	}
	assertContains(t, readFile(t, b, "note.md"), "<<<<<<<")

	// --ours still works
	n, err = Resolve(b, "ours", cfg, gen, nil)
	if err != nil {
		t.Fatalf("resolve ours: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 resolved, got %d", n)
	}
	assertContains(t, readFile(t, b, "note.md"), "A-change")
}

func TestNoUpstreamSkipsCleanly(t *testing.T) {
	tmp := t.TempDir()
	b := filepath.Join(tmp, "b")
	gitCmd(t, tmp, "init", "-b", "main", filepath.Base(b))
	gitCmd(t, b, "config", "user.name", "test")
	gitCmd(t, b, "config", "user.email", "test@example.com")
	writeFile(t, b, "note.md", "hello\n")
	gitCommitAll(t, b, "init")

	cfg := newTestConfig()
	rep := Sync(b, cfg, nil)
	if rep.Err != nil {
		t.Fatalf("no-upstream sync should not error: %v", rep.Err)
	}
}

func TestNotARepository(t *testing.T) {
	cfg := newTestConfig()
	rep := Sync(t.TempDir(), cfg, nil)
	if rep.Err == nil {
		t.Fatal("expected error for non-repo")
	}
}

func TestStatusRenameReportsNewPath(t *testing.T) {
	_, _, b := setupRemote(t)
	writeFile(t, b, "old.md", "hello\n")
	gitCommitAll(t, b, "add old")
	gitCmd(t, b, "mv", "old.md", "new.md") // stages the rename

	g := newGitRunner(b)
	entries, err := g.Status()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Path == "old.md" {
			t.Fatalf("Status reported the rename source instead of the destination: %+v", entries)
		}
		if e.Path == "new.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Status did not report the rename destination: %+v", entries)
	}
}

func TestResolveRespectsLock(t *testing.T) {
	_, _, b := setupRemote(t)
	cfg := newTestConfig()

	// fabricate a committed marker block (no merge needed for this test)
	writeFile(t, b, "note.md", "<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> x\n")
	gitCommitAll(t, b, "manual conflict")

	g := newGitRunner(b)
	gd, err := g.GitDir()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lock.Acquire(gd)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	// lock held: resolve must refuse, not collide with the running "sync"
	gen := newAIGen(nil)
	if _, err := Resolve(b, "ours", cfg, gen, nil); err == nil || !strings.Contains(err.Error(), "another sync is running") {
		t.Fatalf("Resolve with held lock = %v, want lock error", err)
	}

	// lock released: resolve proceeds normally
	unlock()
	n, err := Resolve(b, "ours", cfg, gen, nil)
	if err != nil {
		t.Fatalf("resolve after unlock: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 resolved, got %d", n)
	}
	if got := readFile(t, b, "note.md"); got != "A\n" {
		t.Fatalf("unexpected content after resolve: %q", got)
	}
}
