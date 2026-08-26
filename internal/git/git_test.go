package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsTransient(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"fatal: Authentication failed for 'https://github.com'", false},
		{"git@github.com: Permission denied (publickey).", false},
		{"fatal: not a git repository", false},
		{"fatal: does not appear to be a git repository", false},
		{"remote: Repository not found.", false},
		{"fatal: could not read Username for 'https://github.com'", false},
		{"fatal: Could not resolve host: github.com", true},
		{"Connection timed out", true},
		{"fetch origin: timed out after 120s", true},
		{"fatal: early EOF", true},
		{"fatal: the remote end hung up unexpectedly", true},
		{"", true},
	}
	for _, c := range cases {
		err := &CmdError{Args: []string{"fetch"}, Stderr: c.stderr, Err: errors.New("exit")}
		if got := IsTransient(err); got != c.want {
			t.Errorf("IsTransient(%q) = %v, want %v", c.stderr, got, c.want)
		}
	}

	// unknown error kinds default to transient (the safe direction)
	if !IsTransient(errors.New("something")) {
		t.Error("unknown error type should default to transient")
	}

	// wrapped CmdError still classified via errors.As
	wrapped := fmt.Errorf("push: %w", &CmdError{Stderr: "Authentication failed", Err: errors.New("x")})
	if IsTransient(wrapped) {
		t.Error("wrapped CmdError should be classified via errors.As")
	}
}

func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under limit", "abc", 10, "abc"},
		{"ascii cut", "abcdef", 4, "abcd"},
		// 你好世界 = 4 runes × 3 bytes; a 7-byte cut would split 世 (byte 2)
		{"multibyte boundary", "你好世界", 7, "你好"},
		{"exact boundary", "你好世界", 6, "你好"},
		{"split first rune", "你好世界", 4, "你"},
		{"empty", "", 3, ""},
	}
	for _, c := range cases {
		got := truncateUTF8(c.in, c.max)
		if got != c.want {
			t.Errorf("%s: truncateUTF8(%q, %d) = %q, want %q", c.name, c.in, c.max, got, c.want)
		}
		if len(got) > c.max {
			t.Errorf("%s: result exceeds max bytes: %d > %d", c.name, len(got), c.max)
		}
	}
}

// fakeSleepingGit installs a "git" executable on PATH that sleeps longer
// than the runner deadline, so a timed-out command can be exercised for real.
func fakeSleepingGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		// ~1s of ping-based sleep, then a clean exit (never reached with a
		// short deadline — the runner kills the process first)
		if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@echo off\r\nping -n 2 127.0.0.1 >nul\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		// `exec` replaces the shell with sleep, so the kill lands on the
		// sleeper itself (no orphaned grandchild holding the pipes — the
		// scenario WaitDelay guards against in production). A 30s sleep
		// proves the deadline interrupts it rather than waiting it out.
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestRunnerTimeout(t *testing.T) {
	fakeSleepingGit(t)
	r := &Runner{Dir: t.TempDir(), Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := r.Out("fetch", "origin")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error should mention the timeout, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout should have fired quickly, took %s", elapsed)
	}
}

func TestRunnerNoTimeoutByDefault(t *testing.T) {
	// Timeout 0 means no deadline: the command must run to completion.
	dir := fakeSleepingGit(t)
	// overwrite the fake with a fast one (same name)
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@echo off\r\necho fake\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{Dir: t.TempDir()} // Timeout 0
	out, err := r.Out("status")
	if err != nil {
		t.Fatalf("no-timeout run failed: %v", err)
	}
	if out != "fake" {
		t.Fatalf("out = %q, want fake", out)
	}
}

// TestLsTreeHeadRawNames pins -z behavior: non-ASCII and quote-containing
// names must come back raw even when core.quotepath is at its default
// (true) — stock environments would otherwise feed escaped `"\344..."`
// strings into wildcard matching.
func TestLsTreeHeadRawNames(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
	} {
		if _, err := r.Out(args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	names := []string{"中文笔记.md", "plain.md"}
	if runtime.GOOS != "windows" {
		// `"` is illegal in Windows filenames; POSIX covers the quoted-name
		// branch of the -z fix
		names = append(names, `we"ird.md`)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Out("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Out("commit", "-m", "names"); err != nil {
		t.Fatal(err)
	}

	got, err := r.LsTreeHead("")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	if len(got) != len(want) {
		t.Fatalf("LsTreeHead = %v, want %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected/escaped name: %q (want set %v)", g, want)
		}
	}

	sub, err := r.LsTreeHead("中文笔记.md")
	if err != nil || len(sub) != 1 || sub[0] != "中文笔记.md" {
		t.Fatalf("subtree query by CJK name failed: %v, %v", sub, err)
	}
}

// TestCachedNumstatRawPaths pins the runner-level core.quotepath=off:
// staged CJK paths must come back raw for AI summaries, even on hosts
// where git would otherwise escape them.
func TestCachedNumstatRawPaths(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@example.com"},
	} {
		if _, err := r.Out(args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	p := filepath.Join(dir, "中文.md")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Out("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Out("commit", "-m", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Out("add", "-A"); err != nil {
		t.Fatal(err)
	}

	ns, err := r.CachedNumstat()
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 {
		t.Fatalf("numstat rows = %d, want 1", len(ns))
	}
	if !strings.Contains(ns[0].Path, "中文.md") || strings.Contains(ns[0].Path, `"`) {
		t.Fatalf("CJK path not raw: %q", ns[0].Path)
	}
	if ns[0].Added != 1 || ns[0].Deleted != 1 {
		t.Fatalf("unexpected counts: %+v", ns[0])
	}
}
