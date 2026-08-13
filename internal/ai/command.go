package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// completeCommand runs an arbitrary CLI (codex / opencode / pi / ollama /
// custom). Convention: stdin = content (diff or conflicted file),
// stdout = result. Non-zero exit or timeout is an error → caller falls back.
func (g *Generator) completeCommand(system, user string) (string, error) {
	if strings.TrimSpace(g.cfg.Command) == "" {
		return "", errors.New("ai.command is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout())
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", g.cfg.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", g.cfg.Command)
	}
	cmd.Stdin = strings.NewReader(user)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ai command failed: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return cleanOutput(out.String()), nil
}
