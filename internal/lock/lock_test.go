package lock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseRemovesOwnLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "git-notes-sync.lock")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	unlock()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed, err=%v", err)
	}
}

func TestReleaseDoesNotDeleteForeignLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "git-notes-sync.lock")
	// simulate our lock being replaced by a successor's
	if err := os.WriteFile(p, []byte("pid=9\nat=2026-01-01T00:00:00Z\ntoken=other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("foreign lock must not be deleted, err=%v", err)
	}
	_ = os.Remove(p)
}

func TestStaleLockStealAndForeignRelease(t *testing.T) {
	dir := t.TempDir()
	unlockA, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "git-notes-sync.lock")
	// age A's lock beyond staleness (crashed/hung holder whose heartbeat died)
	old := time.Now().Add(-staleAfter - time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	// B steals the stale lock
	unlockB, err := Acquire(dir)
	if err != nil {
		t.Fatalf("B should steal the stale lock: %v", err)
	}
	// A finishes: must NOT delete B's lock (token mismatch)
	unlockA()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("B's lock must survive A's release, err=%v", err)
	}
	unlockB()
}

func TestHeartbeatRefreshesMtime(t *testing.T) {
	prev := heartbeatEvery
	heartbeatEvery = 40 * time.Millisecond
	defer func() { heartbeatEvery = prev }()

	dir := t.TempDir()
	unlock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	p := filepath.Join(dir, "git-notes-sync.lock")
	st1, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	st2, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.ModTime().After(st1.ModTime()) {
		t.Fatalf("heartbeat should refresh mtime: %v → %v", st1.ModTime(), st2.ModTime())
	}
}

func TestBusyLockError(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Acquire(dir); err == nil || !strings.Contains(err.Error(), "another sync is running") {
		t.Fatalf("second acquire should report busy, got %v", err)
	}
}

func TestTokensUnique(t *testing.T) {
	// rapid successive acquires in one process must never share a token
	// (a time-based token could collide on coarse Windows clocks, letting
	// an old holder delete the new holder's lock). Only the token field is
	// compared — the at= timestamp may legitimately repeat.
	dir := t.TempDir()
	p := filepath.Join(dir, "git-notes-sync.lock")
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		unlock, err := Acquire(dir)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		tok := ""
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "token=") {
				tok = strings.TrimPrefix(line, "token=")
			}
		}
		if tok == "" {
			t.Fatalf("acquire %d: no token field in lock file %q", i, string(b))
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q at acquire %d", tok, i)
		}
		seen[tok] = true
		unlock()
	}
}

func TestOwns(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "git-notes-sync.lock")
	if err := os.WriteFile(p, []byte("pid=1\nat=x\ntoken=abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !owns(p, "abc") {
		t.Error("matching token should be owned")
	}
	if owns(p, "xyz") {
		t.Error("different token must not be owned")
	}
	if owns(filepath.Join(dir, "missing"), "abc") {
		t.Error("missing file must not be owned")
	}
}
