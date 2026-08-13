// Package daemon is the optional lightweight timer daemon (Windows-first):
// it only ticks on a timer and runs sync per repo. No watcher, no state
// persistence beyond what sync itself uses.
package daemon

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/sync"
)

// Run loops sync over all configured repos every sync_interval seconds.
// Global config is cached and reloaded when its mtime changes.
func Run(globalPath string, once bool) error {
	logf := log.Printf
	cfg := config.Defaults()
	var lastMtime time.Time

	for {
		if globalPath != "" {
			if st, err := os.Stat(globalPath); err == nil && !st.ModTime().Equal(lastMtime) {
				loaded, lerr := config.Load(globalPath, "")
				if lerr != nil {
					logf("config error (keeping previous): %v", lerr)
				} else {
					cfg = loaded
					lastMtime = st.ModTime()
					logf("config loaded: %s", globalPath)
				}
			}
		}

		repos := cfg.Repos.All()
		if len(repos) == 0 {
			if wd, err := os.Getwd(); err == nil {
				repos = []config.Repo{{Path: wd}}
			}
		}
		for _, r := range repos {
			repoPath := r.ExpandedPath()
			disp := r.DisplayName()
			rcfg := cfg
			if merged, err := config.Load(globalPath, repoPath); err == nil {
				rcfg = merged
			}
			rep := sync.Sync(repoPath, rcfg, func(f string, a ...any) {
				logf("[%s] %s", disp, fmt.Sprintf(f, a...))
			})
			for _, s := range rep.Steps {
				logf("[%s] %s", disp, s)
			}
			if rep.Err != nil {
				logf("[%s] ERROR: %v", disp, rep.Err)
			}
		}
		if once {
			return nil
		}
		time.Sleep(time.Duration(cfg.SyncInterval) * time.Second)
	}
}
