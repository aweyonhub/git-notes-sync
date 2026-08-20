package sync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/ai"
	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
	"github.com/aweyonhub/git-notes-sync/internal/retry"
)

// ConflictFile is a file holding persisted conflict markers.
type ConflictFile struct {
	Path   string
	Blocks int
}

// FindConflicts lists files with conflict markers (committed or mid-merge).
func FindConflicts(repo string) ([]ConflictFile, error) {
	g := git.NewRunner(repo)
	seen := map[string]bool{}
	var out []ConflictFile
	add := func(p string, blocks int) {
		if !seen[p] {
			seen[p] = true
			out = append(out, ConflictFile{Path: p, Blocks: blocks})
		}
	}
	if files, err := g.MarkerFiles(); err == nil {
		for _, f := range files {
			if content, err := os.ReadFile(filepath.Join(repo, f)); err == nil {
				add(f, countMarkers(string(content)))
			} else {
				add(f, 0)
			}
		}
	}
	if unmerged, err := g.Unmerged(); err == nil {
		for _, p := range unmerged {
			add(p, 0)
		}
	}
	return out, nil
}

// Resolve rewrites conflicted files dropping markers per mode
// (ours | theirs | ai), commits and pushes. Returns files resolved.
func Resolve(repo, mode string, cfg *config.Config, gen *ai.Generator, logf func(string, ...any)) (int, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	g := git.NewRunner(repo)
	if cfg.GitTimeoutSec > 0 {
		g.Timeout = time.Duration(cfg.GitTimeoutSec) * time.Second
	}
	gd, err := g.GitDir()
	if err != nil {
		return 0, fmt.Errorf("git dir: %w", err)
	}
	// resolve mutates the repo (rewrite → commit → push), so it takes the
	// same lock as the sync engine to avoid colliding with a running tick.
	unlock, err := lock.Acquire(gd)
	if err != nil {
		return 0, err
	}
	defer unlock()

	files, err := FindConflicts(repo)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	resolved := 0
	for _, f := range files {
		path := filepath.Join(repo, f.Path)
		content, err := os.ReadFile(path)
		if err != nil {
			return resolved, err
		}
		var out string
		var oerr error
		switch mode {
		case "ours", "theirs":
			out, oerr = applyMode(string(content), mode)
		case "ai":
			if gen == nil || !gen.Enabled() {
				oerr = errors.New("ai not configured (set [ai] in config)")
			} else {
				out, oerr = gen.ResolveConflict(f.Path, string(content))
			}
		default:
			return resolved, fmt.Errorf("unknown resolve mode %q", mode)
		}
		if oerr != nil {
			logf("skipped %s: %v (markers kept)", f.Path, oerr)
			continue
		}
		if out == string(content) {
			logf("skipped %s: no change", f.Path)
			continue
		}
		if err := writePreservingMode(path, out); err != nil {
			return resolved, err
		}
		if err := g.Add(f.Path); err != nil {
			return resolved, err
		}
		resolved++
		logf("resolved %s", f.Path)
	}
	if resolved == 0 {
		return 0, nil
	}
	msg := fmt.Sprintf("resolve conflicts: %d file(s)", resolved)
	if err := g.Commit(msg); err != nil {
		return resolved, fmt.Errorf("commit: %w", err)
	}
	if remote, branch, ok := g.Upstream(); ok {
		if err := retry.Do(cfg.RetryAttempts, func() error {
			return g.Push(remote, branch)
		}, 2*time.Second, git.IsTransient); err != nil {
			return resolved, fmt.Errorf("push: %w", err)
		}
		logf("pushed %s/%s", remote, branch)
	}
	return resolved, nil
}

func countMarkers(content string) int {
	n := 0
	for _, ln := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimRight(ln, "\r"), "<<<<<<< ") {
			n++
		}
	}
	return n
}

// writePreservingMode rewrites path keeping its existing permission bits
// (resolving an executable script must not silently drop the +x bit).
func writePreservingMode(path, content string) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return os.WriteFile(path, []byte(content), mode)
}
