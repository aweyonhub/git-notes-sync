package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aweyonhub/git-notes-sync/internal/ai"
	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
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

func TestApplyModeCRLF(t *testing.T) {
	// core.autocrlf checkouts carry \r\n line endings; marker lines must
	// still match (trailing \r stripped) and untouched lines keep their \r.
	src := strings.Join([]string{
		"# title", "", "<<<<<<< HEAD", "ours line", "=======",
		"theirs line", ">>>>>>> abc123", "", "tail", "",
	}, "\r\n")
	got, err := applyMode(src, "ours")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# title\r\n\r\nours line\r\n\r\ntail\r\n"; got != want {
		t.Fatalf("ours mismatch:\n%q\nvs\n%q", got, want)
	}
	got, err = applyMode(src, "theirs")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# title\r\n\r\ntheirs line\r\n\r\ntail\r\n"; got != want {
		t.Fatalf("theirs mismatch:\n%q\nvs\n%q", got, want)
	}
}

func TestWritePreservingMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(p, []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePreservingMode(p, "new\n"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("perm changed: %v → %v (executable bit must survive resolve)", before.Mode().Perm(), after.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); string(b) != "new\n" {
		t.Fatalf("content = %q, want new\\n", string(b))
	}
}
