// Package daemon is the optional lightweight timer daemon (Windows-first):
// it only ticks on a timer and runs sync per repo. No watcher, no state
// persistence beyond what sync itself uses.
package daemon

import (
	"fmt"
	"os"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	logPkg "github.com/aweyonhub/git-notes-sync/internal/log"
	"github.com/aweyonhub/git-notes-sync/internal/sync"
)

// Run loops sync over all configured repos every sync_interval seconds.
// Global config is cached and reloaded when its mtime changes.
func Run(globalPath string, once bool) error {
	// logf writes to stderr with the same timestamp format as the interval
	// mode's redirected output (2006-01-02 15:04:05), so both scheduling
	// styles produce uniform logs (launchd sends stderr to <label>.err.log).
	logf := func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(f, a...))
	}
	cfg := config.Defaults()
	var lastMtime time.Time

	for {
		if globalPath != "" {
			if st, err := os.Stat(globalPath); err == nil && !st.ModTime().Equal(lastMtime) {
				loaded, lerr := config.Load(globalPath, "")
				// Record the mtime on failure too: a config typo would
				// otherwise be re-logged (and re-attempted) every tick.
				lastMtime = st.ModTime()
				if lerr != nil {
					logf("config error (keeping previous): %v", lerr)
				} else {
					cfg = loaded
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
			if rep.Err != nil {
				logf("[%s] ERROR: %v", disp, rep.Err)
			}
		}
		// Rotate the log every tick when running with --log redirection
		// (GNS_LOG_FILE is set by the launcher). RotateAndReopen closes the
		// current stdout/stderr handles first — Windows cannot rename an open
		// file (Go opens without FILE_SHARE_DELETE), and on POSIX the open
		// handle would keep writing into the renamed backup — then reopens
		// and re-points the process output at the fresh file.
		if lp := os.Getenv("GNS_LOG_FILE"); lp != "" {
			f, err := logPkg.RotateAndReopen(lp, cfg.Log.MaxSizeKB, cfg.Log.MaxBackups, os.Stdout, os.Stderr)
			if err != nil {
				logf("log rotate: %v", err)
			} else {
				os.Stdout = f
				os.Stderr = f
			}
		}

		if once {
			return nil
		}
		time.Sleep(time.Duration(cfg.SyncInterval) * time.Second)
	}
}
