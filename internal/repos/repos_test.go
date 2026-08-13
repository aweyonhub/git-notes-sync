package repos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-notes-sync/git-notes-sync/internal/config"
)

const sample = `# my config
auto_commit = true

[[repos]]
name = "notes"
path = "~/notes"

# keep this comment
commit_debounce = 10

[[repos]]
name = "wiki"
path = "/work/wiki"
`

func setup(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestList(t *testing.T) {
	p := setup(t)
	list, err := List(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d: %+v", len(list), list)
	}
	if list[0].Name != "notes" || list[1].Name != "wiki" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestAddAppendsBlockAndKeepsComments(t *testing.T) {
	p := setup(t)
	if err := Add(p, "blog", "/work/blog"); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(p)
	text := string(content)
	if !strings.Contains(text, "auto_commit = true") {
		t.Fatal("existing content lost")
	}
	if !strings.Contains(text, "# keep this comment") {
		t.Fatal("comment lost")
	}
	if !strings.Contains(text, `name = "blog"`) || !strings.Contains(text, `path = "/work/blog"`) {
		t.Fatal("new block not appended")
	}
	list, err := List(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(list))
	}
}

func TestAddDefaultName(t *testing.T) {
	p := setup(t)
	if err := Add(p, "", "/work/scratch"); err != nil {
		t.Fatal(err)
	}
	list, _ := List(p)
	if list[2].Name != "scratch" {
		t.Fatalf("default name should be basename, got %q", list[2].Name)
	}
}

func TestAddReplacesDuplicateName(t *testing.T) {
	p := setup(t)
	if err := Add(p, "notes", "/new/notes"); err != nil {
		t.Fatal(err)
	}
	list, err := List(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 after replace, got %d", len(list))
	}
	got, ok := findByName(list, "notes")
	if !ok || got.Path != "/new/notes" {
		t.Fatalf("expected updated path, got %+v", got)
	}
}

func findByName(list []config.Repo, name string) (config.Repo, bool) {
	for _, r := range list {
		if r.Name == name {
			return r, true
		}
	}
	return config.Repo{}, false
}

func TestDelByNameAndPath(t *testing.T) {
	p := setup(t)
	if err := Del(p, "wiki"); err != nil {
		t.Fatal(err)
	}
	list, _ := List(p)
	if len(list) != 1 || list[0].Name != "notes" {
		t.Fatalf("del by name failed: %+v", list)
	}

	if err := Del(p, "/work/wiki"); err == nil {
		t.Fatal("expected ErrNotFound for already-deleted repo")
	}
	content, _ := os.ReadFile(p)
	if strings.Contains(string(content), "wiki") {
		t.Fatal("wiki block should be gone")
	}
	if !strings.Contains(string(content), "# keep this comment") {
		t.Fatal("comment lost after del")
	}
}

func TestDelCreatesEmptyConfigWhenMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := Add(p, "x", "/x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("config file should be created: %v", err)
	}
	cfg, err := config.Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos.Len() != 1 {
		t.Fatalf("expected 1 repo")
	}
}
