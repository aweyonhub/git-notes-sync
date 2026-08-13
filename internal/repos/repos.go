// Package repos maintains the multi-repo list inside a TOML config file.
// It edits the file at block level so comments and unrelated keys survive.
package repos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/git-notes-sync/git-notes-sync/internal/config"
)

var (
	ErrNotFound = errors.New("repo not found")
	ErrExists   = errors.New("repo already exists")
)

// List returns the repos parsed from the config file (unexpanded).
// A missing config file yields an empty list, not an error.
func List(cfgPath string) ([]config.Repo, error) {
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return nil, nil
	}
	cfg, err := config.Load(cfgPath, "")
	if err != nil {
		return nil, err
	}
	return cfg.Repos.All(), nil
}

// Add appends a repo block, replacing an existing entry with the same name
// or path first (so add is idempotent).
func Add(cfgPath, name, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path is required")
	}
	if name == "" {
		name = filepath.Base(strings.TrimRight(path, `/\`))
	}
	existing, err := List(cfgPath)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.Name == name {
			if err := removeBlock(cfgPath, r); err != nil {
				return err
			}
		}
	}
	if err := ensureFile(cfgPath); err != nil {
		return err
	}
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	block := fmt.Sprintf("\n[[repos]]\nname = %q\npath = %q\n", name, path)
	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return nil
}

// Del removes the repo whose name or path matches.
func Del(cfgPath, nameOrPath string) error {
	existing, err := List(cfgPath)
	if err != nil {
		return err
	}
	var match *config.Repo
	for i := range existing {
		if existing[i].Name == nameOrPath || existing[i].Path == nameOrPath {
			match = &existing[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("%w: %s", ErrNotFound, nameOrPath)
	}
	return removeBlock(cfgPath, *match)
}

// removeBlock deletes one [[repos]] block from the file.
func removeBlock(cfgPath string, target config.Repo) error {
	content, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(content), "\n")
	out := make([]string, 0, len(lines))
	i := 0
	removed := false
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "[[repos]]" {
			j := i + 1
			var name, path string
			for j < len(lines) {
				trimmed := strings.TrimSpace(lines[j])
				if strings.HasPrefix(trimmed, "[") {
					break
				}
				if v, ok := parseKV(trimmed, "name"); ok {
					name = v
				}
				if v, ok := parseKV(trimmed, "path"); ok {
					path = v
				}
				j++
			}
			if name == target.Name && path == target.Path {
				i = j // drop the whole block
				removed = true
				continue
			}
		}
		out = append(out, lines[i])
		i++
	}
	if !removed {
		return ErrNotFound
	}
	// trim trailing blank lines before the new content is written
	text := strings.Join(out, "\n")
	text = strings.TrimRight(text, "\n") + "\n"
	return os.WriteFile(cfgPath, []byte(text), 0o644)
}

// parseKV extracts `key = "value"` from a TOML line.
func parseKV(line, key string) (string, bool) {
	if !strings.HasPrefix(line, key+" =") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, key+" ="))
	rest = strings.Trim(rest, `"`)
	return rest, true
}

func ensureFile(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cfgPath, []byte("# git-notes-sync config\n"), 0o644)
}
