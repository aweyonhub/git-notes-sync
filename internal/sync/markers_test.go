package sync

import (
	"strings"
	"testing"

	"github.com/git-notes-sync/git-notes-sync/internal/ai"
	"github.com/git-notes-sync/git-notes-sync/internal/config"
	"github.com/git-notes-sync/git-notes-sync/internal/git"
)

func newGitRunner(dir string) *git.Runner { return git.NewRunner(dir) }

func newAIGen(cfg *config.AI) *ai.Generator { return ai.NewGenerator(cfg, "") }

func TestApplyMode(t *testing.T) {
	src := `# title

<<<<<<< HEAD
ours line
=======
theirs line
>>>>>>> abc123

tail
`
	ours := `# title

ours line

tail
`
	theirs := `# title

theirs line

tail
`
	gotOurs, err := applyMode(src, "ours")
	if err != nil {
		t.Fatal(err)
	}
	if gotOurs != ours {
		t.Fatalf("ours mismatch:\n%q\nvs\n%q", gotOurs, ours)
	}
	gotTheirs, err := applyMode(src, "theirs")
	if err != nil {
		t.Fatal(err)
	}
	if gotTheirs != theirs {
		t.Fatalf("theirs mismatch:\n%q\nvs\n%q", gotTheirs, theirs)
	}
}

func TestApplyModeMultipleBlocks(t *testing.T) {
	src := strings.Join([]string{
		"<<<<<<< HEAD", "a1", "=======", "b1", ">>>>>>> x",
		"middle",
		"<<<<<<< HEAD", "a2", "=======", "b2", ">>>>>>> y",
		"end",
	}, "\n")
	got, err := applyMode(src, "ours")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"a1", "middle", "a2", "end"}, "\n")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyModeUnterminated(t *testing.T) {
	if _, err := applyMode("<<<<<<< HEAD\nabc\n", "ours"); err == nil {
		t.Fatal("expected error for unterminated block")
	}
}

func TestApplyModeNoMarkers(t *testing.T) {
	got, err := applyMode("plain\ncontent\n", "ours")
	if err != nil {
		t.Fatal(err)
	}
	if got != "plain\ncontent\n" {
		t.Fatalf("expected unchanged, got %q", got)
	}
}
