// Package lock guards a repository against concurrent sync runs.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// staleAfter is how long a lock file may live before being considered stale.
const staleAfter = 10 * time.Minute

// Acquire creates <gitDir>/git-notes-sync.lock (O_EXCL) and returns a release
// function. Stale locks are removed and retried once.
func Acquire(gitDir string) (func(), error) {
	path := filepath.Join(gitDir, "git-notes-sync.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid=%d\nat=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		st, serr := os.Stat(path)
		if serr != nil {
			return nil, err
		}
		if time.Since(st.ModTime()) > staleAfter {
			os.Remove(path)
			continue
		}
		return nil, fmt.Errorf("another sync is running (lock: %s; stale locks expire after 10 min)", path)
	}
	return nil, errors.New("could not acquire lock")
}
