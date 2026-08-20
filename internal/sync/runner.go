package sync

import (
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/git"
)

// newRunner builds a git runner honoring the configured per-command timeout
// (git_timeout; 0 = no timeout). Every sync-package entry point uses it so
// no git invocation misses the deadline.
func newRunner(repo string, cfg *config.Config) *git.Runner {
	g := git.NewRunner(repo)
	if cfg != nil && cfg.GitTimeoutSec > 0 {
		g.Timeout = time.Duration(cfg.GitTimeoutSec) * time.Second
	}
	return g
}
