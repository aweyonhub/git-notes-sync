package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

func TestAgentContent(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.AI{AgentFile: "AGENTS.md"}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("always use zh-CN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(cfg, dir)
	content := g.agentContent()
	if !strings.Contains(content, "always use zh-CN") {
		t.Fatalf("agent content not loaded: %q", content)
	}
	section := g.agentSection()
	if !strings.Contains(section, "repository agent instructions") || !strings.Contains(section, "always use zh-CN") {
		t.Fatalf("agent section malformed: %q", section)
	}
}

func TestAgentContentMissingFile(t *testing.T) {
	g := NewGenerator(&config.AI{AgentFile: "AGENTS.md"}, t.TempDir())
	if g.agentContent() != "" || g.agentSection() != "" {
		t.Fatal("missing agent file should be silent")
	}
}

func TestAgentContentDefaultAndTruncation(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxAgentBytes+1000)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	// AgentFile empty → default AGENTS.md
	g := NewGenerator(&config.AI{}, dir)
	if len(g.agentContent()) > maxAgentBytes {
		t.Fatalf("agent content not truncated: %d", len(g.agentContent()))
	}
}

func TestCommitMessageIncludesAgent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("use zh-CN"), 0o644); err != nil {
		t.Fatal(err)
	}
	// api generator: build the prompt without calling the network
	g := NewGenerator(&config.AI{Type: "api", AgentFile: "AGENTS.md"}, dir)
	system := "You write concise git commit messages for a notes/documentation repository. " +
		"Summarize what changed using the diff. First line: short imperative subject (<= 72 chars). " +
		"Optionally follow with a blank line and bullet points. Output only the commit message."
	system += g.agentSection()
	if !strings.Contains(system, "use zh-CN") {
		t.Fatal("agent instructions should be part of the system prompt")
	}
}

func TestBuildCommandInput(t *testing.T) {
	// empty system: input passes through unchanged
	if got := buildCommandInput("", "diff content"); got != "diff content" {
		t.Fatalf("empty system: got %q", got)
	}
	got := buildCommandInput("be concise", "diff content")
	for _, want := range []string{"### Instructions", "be concise", "### Input", "diff content"} {
		if !strings.Contains(got, want) {
			t.Fatalf("input missing %q: %q", want, got)
		}
	}
	// instructions must precede the actual input
	if strings.Index(got, "### Instructions") > strings.Index(got, "### Input") ||
		strings.Index(got, "### Input") > strings.Index(got, "diff content") {
		t.Fatalf("section order wrong: %q", got)
	}
}
