// Package ai provides pluggable, non-blocking AI enhancement: commit message
// generation and conflict resolution. Every failure degrades to a fallback.
package ai

import (
	"errors"
	"fmt"

	"github.com/git-notes-sync/git-notes-sync/internal/config"
)

// ErrNotConfigured is returned when ai.type is unset.
var ErrNotConfigured = errors.New("ai not configured")

// Generator wraps an ai config and dispatches to api or command backends.
type Generator struct {
	cfg *config.AI
}

func NewGenerator(cfg *config.AI) *Generator { return &Generator{cfg: cfg} }

func (g *Generator) Enabled() bool {
	return g != nil && g.cfg != nil && g.cfg.Type != ""
}

// CommitMessage asks the AI for a commit message from a staged diff.
func (g *Generator) CommitMessage(diff string) (string, error) {
	if !g.Enabled() {
		return "", ErrNotConfigured
	}
	system := "You write concise git commit messages for a notes/documentation repository. " +
		"Summarize what changed using the diff. First line: short imperative subject (<= 72 chars). " +
		"Optionally follow with a blank line and bullet points. Output only the commit message."
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
	prompt := fmt.Sprintf("Resolve the conflict markers in file %s:\n\n```\n%s\n```", path, content)
	return g.complete(system, prompt)
}
