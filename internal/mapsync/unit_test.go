// unit_test.go: pure-logic coverage — glob matching, mapping validation,
// copy filter rules and the [[map.items]] textual editor.
package mapsync

import (
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
