package ai

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/git-notes-sync/git-notes-sync/internal/config"
)

// ErrNotConfigured is returned when ai.type is unset.
var ErrNotConfigured = errors.New("ai not configured")

// maxAgentBytes caps the agent instructions file sent to the model.
const maxAgentBytes = 16 * 1024

// Generator wraps an ai config and dispatches to api or command backends.
type Generator struct {
	cfg     *config.AI
	repoDir string
}

func NewGenerator(cfg *config.AI, repoDir string) *Generator {
	return &Generator{cfg: cfg, repoDir: repoDir}
}

func (g *Generator) Enabled() bool {
	return g != nil && g.cfg != nil && g.cfg.Type != ""
}

// agentContent loads the agent instructions file (default AGENTS.md) from
// the repository root. A missing file is not an error.
func (g *Generator) agentContent() string {
	file := g.cfg.AgentFile
	if file == "" {
		file = "AGENTS.md"
	}
	if g.repoDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(g.repoDir, file))
	if err != nil {
		return ""
	}
	if len(b) > maxAgentBytes {
		b = b[:maxAgentBytes]
	}
	return string(b)
}

// agentSection renders the agent instructions block for the system prompt.
func (g *Generator) agentSection() string {
	content := g.agentContent()
	if content == "" {
		return ""
	}
	return "\nFollow these repository agent instructions (from AGENTS.md):\n" + content + "\n"
}

// CommitMessage asks the AI for a commit message from a staged diff.
func (g *Generator) CommitMessage(diff string) (string, error) {
	if !g.Enabled() {
		return "", ErrNotConfigured
	}
	system := "You write concise git commit messages for a notes/documentation repository. " +
		"Summarize what changed using the diff. First line: short imperative subject (<= 72 chars). " +
		"Optionally follow with a blank line and bullet points. Output only the commit message."
	system += g.agentSection()
	prompt := fmt.Sprintf("Diff (git diff --cached):\n\n%s", diff)
	return g.complete(system, prompt)
}

// ResolveConflict asks the AI to semantically merge one conflicted file.
func (g *Generator) ResolveConflict(path, content string) (string, error) {
	if !g.Enabled() {
		return "", ErrNotConfigured
	}
	system := "You resolve git merge conflict markers in files. " +
		"Keep both sides when both contain unique information; prefer the better side when they are alternatives. " +
		"Remove all conflict markers. Reply with ONLY the resolved file content, no commentary, no markdown fences."
	system += g.agentSection()
	prompt := fmt.Sprintf("Resolve the conflict markers in file %s:\n\n```\n%s\n```", path, content)
	return g.complete(system, prompt)
}
