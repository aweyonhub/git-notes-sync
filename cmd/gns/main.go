// Main entry: gns — Git Notes Sync. The launcher parses a leading --log flag
// (redirected output with rotation) before dispatching to the CLI.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/cli"
	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/log"
)

func main() {
	logPath, filteredArgs := parseLogFlag(os.Args[1:])

	if logPath != "" {
		// Load rotation settings from the global config (GNS_CONFIG or the
		// platform default — macOS: ~/Library/Application Support, Linux:
		// ~/.config, Windows: %AppData%). A broken config falls back to
		// defaults; log redirection must never block the sync.
		cfg := config.Defaults()
		if loaded, err := config.Load(config.GlobalPath(), ""); err == nil {
			cfg = loaded
		}

		// Expose the log file to child commands (daemon rotates every tick).
		os.Setenv("GNS_LOG_FILE", logPath)

		// Ensure the log directory exists.
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: create log dir: %v\n", err)
			os.Exit(1)
		}

		// Rotate (best-effort; a rotation failure must not break the run).
		if err := log.Rotate(logPath, cfg.Log.MaxSizeKB, cfg.Log.MaxBackups); err != nil {
			fmt.Fprintf(os.Stderr, "warn: log rotate: %v\n", err)
		}
		if err := log.Cleanup(logPath, cfg.Log.MaxBackups); err != nil {
			fmt.Fprintf(os.Stderr, "warn: log cleanup: %v\n", err)
		}

		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: open log file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()

		// Redirect stdout/stderr to the log file.
		os.Stdout = f
		os.Stderr = f
	}

	if err := cli.Run(filteredArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseLogFlag extracts a --log <path> or --log=<path> flag from anywhere in
// the arguments, returning the log path and the remaining args. --log without
// a value is a usage error.
func parseLogFlag(args []string) (string, []string) {
	var logPath string
	var filtered []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--log="):
			logPath = strings.TrimPrefix(a, "--log=")
			if logPath == "" {
				fmt.Fprintln(os.Stderr, "error: --log requires a path argument")
				os.Exit(2)
			}
		case a == "--log":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --log requires a path argument")
				os.Exit(2)
			}
			logPath = args[i+1]
			i++
		default:
			filtered = append(filtered, a)
		}
	}
	return logPath, filtered
}
