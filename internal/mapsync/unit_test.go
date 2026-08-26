// unit_test.go: pure-logic coverage — glob matching, mapping validation,
// copy filter rules and the [[map.items]] textual editor.
package mapsync

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

func TestGlobRegexp(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/h/.pi/*", "/h/.pi/skills", true},
		{"/h/.pi/*", "/h/.pi/.hidden", true}, // dotfiles match
		{"/h/.pi/*", "/h/.pi/a/b", false},    // never crosses a separator
		{"/h/.pi/*/x", "/h/.pi/sub/x", true}, // star mid-path
		{"/h/f.txt", "/h/f.txt", true},       // literal
		{"/h/f.txt", "/h/g.txt", false},
	}
	for _, c := range cases {
		re, err := globRegexp(c.pattern)
		if err != nil {
			t.Fatalf("globRegexp(%q): %v", c.pattern, err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("globRegexp(%q).Match(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestCollapseDescendants(t *testing.T) {
	got := collapseDescendants([]string{"a/b/c", "a/b", "z", "a"})
	want := []string{"a", "z"} // "a" covers everything under it
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("collapseDescendants = %v, want %v", got, want)
	}
}

func TestValidateItems(t *testing.T) {
	ok := []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: ".bashrc", LocalPath: "~/.bashrc"},
		{Scope: config.ScopeGitRoot, Path: "common/gitconfig", LocalPath: "~/.gitconfig"},
	}
	if errs := ValidateItems(ok, "winTx"); len(errs) != 0 {
		t.Fatalf("valid items rejected: %v", errs)
	}

	dupLocal := append(append([]config.MapItem{}, ok...),
		config.MapItem{Scope: config.ScopeMapRoot, Path: "other", LocalPath: "~/.bashrc"})
	if errs := ValidateItems(dupLocal, "winTx"); len(errs) == 0 {
		t.Fatal("duplicate local path not rejected")
	}

	nested := []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "pi-skill", LocalPath: "~/.pi/skills"},
		{Scope: config.ScopeGitRoot, Path: "shared", LocalPath: "~/.pi"},
	}
	if errs := ValidateItems(nested, "winTx"); len(errs) == 0 {
		t.Fatal("nested local mappings not rejected")
	}

	badPath := []config.MapItem{{Scope: config.ScopeGitRoot, Path: "../escape", LocalPath: "~/.x"}}
	if errs := ValidateItems(badPath, "winTx"); len(errs) == 0 {
		t.Fatal(`".."-escaping repo path not rejected`)
	}
}

func TestRepoPathOf(t *testing.T) {
	mr := config.MapItem{Scope: config.ScopeMapRoot, Path: "pi-skill", LocalPath: "~"}
	got, err := RepoPathOf(mr, "winTx")
	if err != nil || got != "winTx/pi-skill" {
		t.Fatalf("RepoPathOf map-root = %q, %v", got, err)
	}
	gr := config.MapItem{Scope: config.ScopeGitRoot, Path: "common/x", LocalPath: "~"}
	got, err = RepoPathOf(gr, "winTx")
	if err != nil || got != "common/x" {
		t.Fatalf("RepoPathOf git-root = %q, %v", got, err)
	}
}

func TestMapPathSafety(t *testing.T) {
	for _, path := range []string{".git/config", ".gns/map/other.toml", "CON/file", `C:\\absolute`} {
		item := config.MapItem{Scope: config.ScopeGitRoot, Path: path, LocalPath: "~/.x"}
		if _, err := RepoPathOf(item, "tm"); err == nil {
			t.Fatalf("unsafe repo path accepted: %s", path)
		}
	}

	t.Setenv("GNS_APP_DATA", t.TempDir())
	cfg := config.Defaults()
	cfg.Map.MapRoot = "tm"
	cfg.Map.Items = []config.MapItem{{Scope: config.ScopeMapRoot, Path: "x", LocalPath: "~/.x"}}
	if err := os.MkdirAll(filepath.Join(WorktreeDir("tm"), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RequireMutableBase(cfg); err == nil {
		t.Fatal("initialized base configuration was mutable")
	}
}

func writeF(t *testing.T, path, content string) time.Time {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.ModTime()
}

func TestSyncTreeFilterRules(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	writeF(t, src+"/a.txt", "AAA")
	writeF(t, dst+"/a.txt", "AAA")
	// equal size + mtime → skip (mtime preserved by earlier copies)
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	si, _ := os.Stat(src + "/a.txt")
	di, _ := os.Stat(dst + "/a.txt")
	if !si.ModTime().Equal(di.ModTime()) {
		t.Fatal("mtime not converged after first copy")
	}

	// size differs → copy
	writeF(t, src+"/a.txt", "AAAB")
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst + "/a.txt"); string(b) != "AAAB" {
		t.Fatalf("size-diff copy failed: %q", b)
	}

	// same size, mtime differs → copy
	writeF(t, src+"/b.txt", "X")
	writeF(t, dst+"/b.txt", "Y")
	future := time.Now().Add(time.Hour)
	os.Chtimes(dst+"/b.txt", future, future)
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst + "/b.txt"); string(b) != "X" {
		t.Fatalf("mtime-diff copy failed: %q", b)
	}

	// deletion propagation inside an existing directory
	os.Remove(src + "/b.txt")
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dst + "/b.txt"); !os.IsNotExist(err) {
		t.Fatal("deletion did not propagate")
	}

	// type change file→dir is replaced
	os.MkdirAll(src+"/c", 0o755)
	writeF(t, src+"/c/inner.txt", "in")
	writeF(t, dst+"/c", "file-content")
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst + "/c/inner.txt")
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatal("type change to dir not handled")
	}

	// missing source removes the whole destination subtree
	os.RemoveAll(src + "/c")
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dst + "/c"); !os.IsNotExist(err) {
		t.Fatal("subtree deletion did not propagate")
	}
}

func TestSyncTreePreservesExecBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "s.sh")
	if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, "d.sh")
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Mode()&0o111 == 0 {
		t.Fatal("executable bit lost in copy")
	}

	// Permission-only changes are part of copy freshness even when size and
	// mtime still match.
	mtime := fi.ModTime()
	if err := os.Chmod(src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("permission-only change not copied: mode=%v", fi.Mode().Perm())
	}
}

func TestSyncTreePreservesDirectoryMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix directory permission bits")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(src, "item"), "value")
	if err := os.Chmod(src, 0o700); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(src, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}
	if err := SyncTree(src, dst); err != nil {
		t.Fatal(err)
	}
	si, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != si.Mode().Perm() || !di.ModTime().Equal(si.ModTime()) {
		t.Fatalf("directory metadata differs: src=%v/%v dst=%v/%v",
			si.Mode().Perm(), si.ModTime(), di.Mode().Perm(), di.ModTime())
	}
}

func TestTrackedCopyBlocksConcurrentLocalChange(t *testing.T) {
	tmp := t.TempDir()
	local := filepath.Join(tmp, "local")
	worktree := filepath.Join(tmp, "worktree")
	writeF(t, filepath.Join(local, "a.txt"), "local")

	baseline, err := syncTreeTracked(local, worktree)
	if err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(local, "a.txt"), "edited while merging")
	writeF(t, filepath.Join(worktree, "a.txt"), "remote")

	err = syncTreeGuarded(worktree, local, baseline)
	var changed *ConcurrentChangeError
	if !errors.As(err, &changed) {
		t.Fatalf("expected ConcurrentChangeError, got %v", err)
	}
	if got := string(mustRead(t, filepath.Join(local, "a.txt"))); got != "edited while merging" {
		t.Fatalf("concurrent local edit overwritten: %q", got)
	}
}

func TestEnsureSymlinkRefusesExistingPath(t *testing.T) {
	tmp := t.TempDir()
	local := filepath.Join(tmp, "local.txt")
	target := filepath.Join(tmp, "target.txt")
	writeF(t, local, "keep")
	writeF(t, target, "target")
	if err := EnsureSymlink(local, target); err == nil {
		t.Fatal("EnsureSymlink replaced an unmanaged file")
	}
	if got := string(mustRead(t, local)); got != "keep" {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestWildcardCanSelectMappingRoots(t *testing.T) {
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "pi")
	items := []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "skills", LocalPath: filepath.Join(parent, "skills")},
		{Scope: config.ScopeMapRoot, Path: "agents", LocalPath: filepath.Join(parent, "agents")},
	}
	for _, item := range items {
		writeF(t, filepath.Join(item.LocalPath, "x.txt"), "x")
	}
	env := &Env{Cfg: &config.Config{Map: config.Map{Items: items}}, MapRoot: "tm", Worktree: filepath.Join(tmp, "wt")}
	nodes, err := selectNodes(env, []string{filepath.Join(parent, "*")}, false, sideWorktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || len(nodes[0]) != 1 || nodes[0][0] != "" || len(nodes[1]) != 1 || nodes[1][0] != "" {
		t.Fatalf("root wildcard selection = %#v", nodes)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRemoveItemBlocksWhereKeepsOtherContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "# my config\nauto_commit = true\n\n[[repos]]\nname = \"notes\"\npath = \"~/notes\"\n" +
		"\n[[map.items]]\nscope = \"map-root\"\npath = \"keep-me\"\nlocal_path = \"~/.keep\"\n" +
		"\n[[map.items]]\nscope = \"git-root\"\npath = \"drop\"\nlocal_path = \"~/.drop\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := removeItemBlocksWhere(path, func(it config.MapItem) bool {
		return LocalKey(NormalizeLocal(it.LocalPath)) == LocalKey(NormalizeLocal("~/.drop"))
	})
	if err != nil || n != 1 {
		t.Fatalf("removed=%d err=%v", n, err)
	}
	out, _ := os.ReadFile(path)
	s := string(out)
	for _, keep := range []string{"# my config", "[[repos]]", "keep-me", "~/.keep"} {
		if !strings.Contains(s, keep) {
			t.Fatalf("lost content %q in:\n%s", keep, s)
		}
	}
	if strings.Contains(s, "drop") {
		t.Fatalf("target block survived:\n%s", s)
	}

	// AddItem appends and rejects duplicates / containment
	cfg, err := config.Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Map.MapRoot = "tm"
	if err := AddItem(path, cfg, config.ScopeGitRoot, "other", "~/.keep", nil); err == nil {
		t.Fatal("AddItem accepted a duplicate local path")
	}
	if err := AddItem(path, cfg, config.ScopeGitRoot, "fresh", "~/.fresh", nil); err != nil {
		t.Fatalf("AddItem fresh: %v", err)
	}
	cfg2, err := config.Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Map.Items) != 2 {
		t.Fatalf("expected 2 items after add, got %d", len(cfg2.Map.Items))
	}
}
