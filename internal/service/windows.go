//go:build windows

package service

// Windows backend: Task Scheduler (schtasks.exe).
//
//   - ModeInterval: a per-user task fires `gns sync-all` every N minutes
//     (schtasks granularity is 1 minute; sub-minute intervals are clamped to
//     1). Output is redirected to a log file via cmd /c.
//   - ModeDaemon: an ONLOGON task starts the resident `gns daemon`; the
//     daemon's own sync_interval controls the cadence.
//
// Tasks are created in the current user's session (no admin required), which
// matches the launchd LaunchAgent / systemd user-unit semantics: they run
// only while the user is logged on.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// schtasks runs schtasks.exe with args, mapping failure to a descriptive
// error. Task Scheduler output is localized (GBK on zh-CN), so success/failure
// is judged by the exit code, never by parsing output.
func schtasks(args ...string) error {
	cmd := exec.Command("schtasks", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("schtasks %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// Install registers the Task Scheduler task.
func Install(o LaunchOptions) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if err := requireHome(o); err != nil {
		return err
	}
	if o.Mode == ModeDaemon && o.Config == "" {
		return fmt.Errorf("daemon mode needs a global config path")
	}
	// match launchd/systemd semantics: refuse to overwrite unless -force
	if Loaded(o) && !o.Force {
		return fmt.Errorf("task %q already registered (use -force to overwrite, or `gns uninstall` first)", taskName(o.Label))
	}
	if err := os.MkdirAll(o.LogDir, 0o755); err != nil {
		return fmt.Errorf("create log dir %s: %w", o.LogDir, err)
	}
	return schtasks(taskCreateArgs(o)...)
}

// Uninstall removes the scheduled task.
func Uninstall(o LaunchOptions) error {
	if err := validateLabel(o.Label); err != nil {
		return err
	}
	return schtasks("/Delete", "/TN", taskName(o.Label), "/F")
}

// Loaded reports whether the task is registered.
func Loaded(o LaunchOptions) bool {
	return schtasks("/Query", "/TN", taskName(o.Label)) == nil
}

// DefaultLogDir is %LOCALAPPDATA%\git-notes-sync (falls back to
// <home>\AppData\Local\git-notes-sync when LOCALAPPDATA is unset, e.g. some
// enterprise redirects).
func DefaultLogDir(home string) string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "git-notes-sync")
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, "AppData", "Local", "git-notes-sync")
}

