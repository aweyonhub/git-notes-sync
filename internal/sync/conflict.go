package sync

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/git-notes-sync/git-notes-sync/internal/config"
	"github.com/git-notes-sync/git-notes-sync/internal/git"
)

// handleConflicts turns a failed merge into a committed, pushable state:
//
//	text conflict   → keep both sides + markers, stage, merge commit
//	binary conflict → keep local copy (ours) or abort, per binary_strategy
func handleConflicts(g *git.Runner, paths []string, cfg *config.Config, rep *Report) error {
	if cfg.Conflict.Strategy == config.StrategyAbort {
		_ = g.MergeAbort()
		return fmt.Errorf("conflicts in %d file(s); conflict.strategy=abort, merge reverted", len(paths))
	}

	var texts, binaries []string
	for _, p := range paths {
		if isText(p, cfg) {
			texts = append(texts, p)
		} else {
			binaries = append(binaries, p)
		}
	}

	if len(binaries) > 0 {
		switch cfg.BinaryStrategy {
		case config.BinaryOurs:
			for _, p := range binaries {
				if err := g.CheckoutOurs(p); err != nil {
					return fmt.Errorf("binary %s: %w", p, err)
				}
				if err := g.Add(p); err != nil {
					return err
				}
			}
			rep.logf("binary conflicts resolved keeping local copy: %s", strings.Join(binaries, ", "))
		case config.BinaryAbort:
			_ = g.MergeAbort()
			return fmt.Errorf("binary conflict in %s; binary_strategy=abort, merge reverted", binaries[0])
		default:
			_ = g.MergeAbort()
			return fmt.Errorf("invalid binary_strategy %q", cfg.BinaryStrategy)
		}
	}

	if len(texts) > 0 {
		for _, p := range texts {
			// staging keeps the conflict markers in the file: conflict
			// becomes a persisted, resolvable state instead of a blocker
			if err := g.Add(p); err != nil {
				return err
			}
		}
		rep.logf("text conflicts preserved (markers kept, resolve later with `notes resolve`): %s", strings.Join(texts, ", "))
	}

	if err := g.CommitMerge(); err != nil {
		return fmt.Errorf("merge commit: %w", err)
	}
	rep.logf("merge commit created (%d conflicted file(s))", len(paths))
	return nil
}

// isText decides text vs binary: extension preset first, then NUL-byte sniff.
func isText(path string, cfg *config.Config) bool {
	if cfg.IsTextExt(path) {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return true // unreadable → treat as text, keep markers path
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	return bytes.IndexByte(buf[:n], 0) < 0
}
