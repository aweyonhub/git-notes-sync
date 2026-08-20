// Package lock guards a repository against concurrent sync runs.
package lock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// staleAfter is how long a lock file may live untouched before being
// considered stale (the holder's heartbeat refreshes the mtime).
const staleAfter = 10 * time.Minute

// heartbeatEvery is how often a held lock's mtime is refreshed. A variable
// so tests can shorten it.
var heartbeatEvery = time.Minute

// Acquire creates <gitDir>/git-notes-sync.lock (O_EXCL) and returns a release
// function. While held, a heartbeat refreshes the file's mtime so a healthy
// long-running holder (network retries, AI resolve) is never mistaken for
// stale; if the holder crashes, the heartbeat dies with it and the 10-minute
// staleness window eventually reclaims the lock. Stale locks are removed and
// retried once. The release function verifies token ownership before
// removing the file, so a holder that outlived its lock never deletes a
// successor's lock; release also waits for the heartbeat to exit before
// removing, closing the last-tick window.
func Acquire(gitDir string) (func(), error) {
	path := filepath.Join(gitDir, "git-notes-sync.lock")
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid=%d\nat=%s\ntoken=%s\n", os.Getpid(), time.Now().Format(time.RFC3339), token)
			f.Close()

			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				t := time.NewTicker(heartbeatEvery)
				defer t.Stop()
				for {
					select {
					case now := <-t.C:
						// The lock may have been stolen while we were
						// suspended: never touch a file we no longer own.
						if !owns(path, token) {
							return
						}
						_ = os.Chtimes(path, now, now)
					case <-stop:
						return
					}
				}
			}()
			// sync.Once makes double release a safe no-op (callers may
			// combine defer unlock() with an explicit unlock()).
			var once sync.Once
			return func() {
				once.Do(func() {
					close(stop)
					<-done
					release(path, token)
				})
			}, nil
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
		return nil, fmt.Errorf("another sync is running (lock: %s; stale locks expire after %d min)", path, int(staleAfter/time.Minute))
	}
	return nil, errors.New("could not acquire lock")
}

// newToken returns a per-acquire unique token (crypto/rand hex). A failure
// of the random source is returned to the caller — the lock is a safety
// mechanism and must not fall back to a collidable token.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate lock token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// owns reports whether the lock file currently carries exactly our token.
func owns(path, token string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "token=") {
			return strings.TrimPrefix(line, "token=") == token
		}
	}
	return false
}

// release removes the lock file only when it still carries our token — a
// holder that outlived its lock must never delete a successor's lock.
func release(path, token string) {
	if !owns(path, token) {
		return
	}
	os.Remove(path)
}
