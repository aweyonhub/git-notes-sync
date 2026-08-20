// genversion regenerates internal/version/version.go from the root
// package.json so the checked-in default version has a single source
// (builds inject the real value via -ldflags; this default only matters
// for plain `go build`).
//
// Usage (from anywhere, path-independent):
//
//	go run ./scripts/genversion        # or: go generate ./internal/version
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	version, err := readVersion(filepath.Join(root, "package.json"))
	if err != nil {
		fatal(err)
	}
	out := fmt.Sprintf(`package version

//go:generate go run ../../scripts/genversion

// Version is the default build version; the build pipeline injects the real
// value via -ldflags (see Makefile / CI). The default is generated from
// package.json by scripts/genversion (go generate ./internal/version), so
// the version lives in a single place.
var Version = %q

// Commit is the git commit the binary was built from (optional, ldflags).
var Commit = "unknown"
`, version)
	target := filepath.Join(root, "internal", "version", "version.go")
	if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("genversion: %s = %s\n", target, version)
}

// repoRoot locates the repository root by walking up from this file's own
// path until go.mod is found, so the generator works regardless of the
// caller's working directory.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot resolve generator source path")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", file)
		}
		dir = parent
	}
}

// readVersion extracts the "version" field from a package.json.
func readVersion(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if pkg.Version == "" {
		return "", fmt.Errorf("no version field in %s", path)
	}
	return pkg.Version, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "genversion:", err)
	os.Exit(1)
}
