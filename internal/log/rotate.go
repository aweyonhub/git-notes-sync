// Package log provides simple log rotation for git-notes-sync.
package log

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Rotate checks if the log file exceeds maxSizeKB and rotates it if needed.
// It keeps maxBackups historical copies (.log.1, .log.2, ...).
func Rotate(path string, maxSizeKB int, maxBackups int) error {
	if maxSizeKB <= 0 {
		maxSizeKB = 500
	}
	if maxBackups < 0 {
		maxBackups = 0
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to rotate
		}
		return err
	}

	maxSize := int64(maxSizeKB) * 1024
	if info.Size() < maxSize {
		return nil // under threshold
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Shift existing backups down (.N-1 → .N), dropping the oldest beyond
	// maxBackups. Iterate from highest index down so renames never collide.
	for i := maxBackups; i > 0; i-- {
		if i == maxBackups {
			old := filepath.Join(dir, fmt.Sprintf("%s.%d", base, i))
			if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if i > 1 {
			src := filepath.Join(dir, fmt.Sprintf("%s.%d", base, i-1))
			dst := filepath.Join(dir, fmt.Sprintf("%s.%d", base, i))
			if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	// Move the current log to .1 (or drop it entirely when maxBackups == 0).
	if maxBackups > 0 {
		if err := os.Rename(path, filepath.Join(dir, base+".1")); err != nil {
			return err
		}
	} else {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// Cleanup removes orphaned backup files that exceed maxBackups. Only files
// whose suffix is a pure integer are considered backups, so unrelated files
// like "gns.log.1.txt" are never touched.
func Cleanup(path string, maxBackups int) error {
	if maxBackups < 0 {
		maxBackups = 0
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	prefix := base + "."
	var backups []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		if _, err := strconv.Atoi(suffix); err == nil {
			backups = append(backups, name)
		}
	}

	// sort numerically so cleanup removes the highest indexes first
	sort.Slice(backups, func(i, j int) bool {
		ni, _ := strconv.Atoi(strings.TrimPrefix(backups[i], prefix))
		nj, _ := strconv.Atoi(strings.TrimPrefix(backups[j], prefix))
		return ni < nj
	})

	for i := maxBackups; i < len(backups); i++ {
		if err := os.Remove(filepath.Join(dir, backups[i])); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
