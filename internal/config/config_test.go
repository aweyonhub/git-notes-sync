package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReposSimpleArray(t *testing.T) {
	p := writeTemp(t, "repos = [\"~/notes\", \"/work/wiki\"]\n")
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos.Len() != 2 {
		t.Fatalf("expected 2 repos, got %d", cfg.Repos.Len())
	}
	all := cfg.Repos.All()
	if all[0].Path != "~/notes" || all[1].Path != "/work/wiki" {
		t.Fatalf("unexpected paths: %+v", all)
	}
	if all[0].DisplayName() != "~/notes" {
		t.Fatalf("simple repo should fall back to path as name")
	}
}

func TestReposNamedTables(t *testing.T) {
	p := writeTemp(t, `
[[repos]]
name = "notes"
path = "~/notes"

[[repos]]
name = "wiki"
path = "/work/wiki"
`)
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	all := cfg.Repos.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(all))
	}
	if all[0].Name != "notes" || all[0].Path != "~/notes" {
		t.Fatalf("unexpected repo0: %+v", all[0])
	}
	if all[0].DisplayName() != "notes" {
		t.Fatalf("named repo should use name")
	}
	if got, ok := cfg.Repos.Find("wiki"); !ok || got.Path != "/work/wiki" {
		t.Fatalf("Find by name failed: %+v", got)
	}
}

func TestReposMergedWithOtherKeys(t *testing.T) {
	p := writeTemp(t, `auto_commit = false
commit_debounce = 10

[[repos]]
name = "notes"
path = "~/notes"
`)
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoCommit {
		t.Fatal("auto_commit should be false")
	}
	if cfg.CommitDebounce != 10 {
		t.Fatalf("commit_debounce should be 10, got %d", cfg.CommitDebounce)
	}
	if cfg.Repos.Len() != 1 {
		t.Fatalf("expected 1 repo")
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	// "/abs/p" is absolute on Unix but not on Windows (no drive letter);
	// expandPath resolves it against the current drive there. Mirror that
	// so the expected value follows the platform's filepath semantics.
	absP := "/abs/p"
	if !filepath.IsAbs(absP) {
		if a, err := filepath.Abs(absP); err == nil {
			absP = a
		}
	}
	cases := map[string]string{
		"~/notes":  filepath.Join(home, "notes"),
		"~":        home,
		"/abs/p":   absP,
		"rel/path": mustAbs(t, "rel/path"),
	}
	for in, want := range cases {
		got := expandPath(in)
		// Normalize path separators for cross-platform comparison.
		if filepath.ToSlash(got) != filepath.ToSlash(want) {
			t.Errorf("expandPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestExpandPathHomePrefix(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := expandPath("~/a/b")
	// Normalize separators so prefix/suffix checks are cross-platform.
	if g, h := filepath.ToSlash(got), filepath.ToSlash(home); !strings.HasPrefix(g, h) || !strings.HasSuffix(g, "a/b") {
		t.Fatalf("unexpected expansion: %q", got)
	}
}

func TestGlobalPathGNSConfig(t *testing.T) {
	origHome, _ := os.UserHomeDir()
	defer t.Setenv("HOME", origHome)

	// default: platform config dir + git-notes-sync/config.toml
	p := GlobalPath()
	if !strings.HasSuffix(p, "git-notes-sync"+string(os.PathSeparator)+"config.toml") {
		t.Fatalf("default GlobalPath = %q, want suffix git-notes-sync/config.toml", p)
	}

	// GNS_CONFIG (absolute) overrides
	t.Setenv("GNS_CONFIG", "/tmp/custom.toml")
	if got := GlobalPath(); got != "/tmp/custom.toml" {
		t.Fatalf("GNS_CONFIG absolute: GlobalPath = %q", got)
	}

	// GNS_CONFIG with ~ expansion
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GNS_CONFIG", "~/custom.toml")
	if got := GlobalPath(); got != filepath.Join(home, "custom.toml") {
		t.Fatalf("GNS_CONFIG with ~: GlobalPath = %q", got)
	}

	// empty GNS_CONFIG falls back to the original default (restore HOME first)
	t.Setenv("HOME", origHome)
	t.Setenv("GNS_CONFIG", "")
	if got := GlobalPath(); got != p {
		t.Fatalf("empty GNS_CONFIG: GlobalPath = %q, want %q", got, p)
	}
}
