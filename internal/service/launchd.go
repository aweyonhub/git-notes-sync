//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LaunchdDomain is the per-user launchd domain (GUI session of the current
// user). Agents in this domain run when the user is logged in.
func LaunchdDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// launchctl runs launchctl with the given args, returning a descriptive error
// on failure.
func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("launchctl %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// isLoaded reports whether the agent is currently registered with launchd.
func isLoaded(label string) bool {
	return launchctl("print", LaunchdDomain()+"/"+label) == nil
}

// Install registers the LaunchAgent: writes the plist atomically, unloads any
// previous registration, then bootstraps the new one.
func Install(o LaunchOptions) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if err := requireHome(o); err != nil {
		return err
	}
	plist := o.PlistPath()
	if _, err := os.Stat(plist); err == nil && !o.Force {
		return fmt.Errorf("already installed at %s (use -force to overwrite, or `gns uninstall` first)", plist)
	}

	if err := os.MkdirAll(o.agentDir(), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", o.agentDir(), err)
	}
	if err := os.MkdirAll(o.LogDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", o.LogDir, err)
	}

	// atomic write: temp file in the same dir, then rename
	tmp := plist + ".tmp"
	if err := os.WriteFile(tmp, []byte(buildPlist(o)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, plist); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename to %s: %w", plist, err)
	}

	// unload a stale registration first (bootout of a non-loaded agent is an
	// error we ignore), then load the new plist
	_ = launchctl("bootout", LaunchdDomain()+"/"+o.Label)
	if err := launchctl("bootstrap", LaunchdDomain(), plist); err != nil {
		return fmt.Errorf("bootstrap failed (plist written at %s): %w", plist, err)
	}
	return nil
}

// Uninstall stops the agent and removes the plist.
func Uninstall(o LaunchOptions) error {
	if err := validateLabel(o.Label); err != nil {
		return err
	}
	if err := requireHome(o); err != nil {
		return err
	}
	plist := o.PlistPath()
	if _, err := os.Stat(plist); os.IsNotExist(err) {
		// already gone; still try to unload a leftover registration
		_ = launchctl("bootout", LaunchdDomain()+"/"+o.Label)
		return nil
	}
	_ = launchctl("bootout", LaunchdDomain()+"/"+o.Label)
	if err := os.Remove(plist); err != nil {
		return fmt.Errorf("remove %s: %w", plist, err)
	}
	return nil
}

// DefaultLogDir is where launchd writes the agent's stdout/stderr.
func DefaultLogDir(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, "Library", "Logs")
}

// Loaded reports whether the agent is registered (for `install --status`).
func Loaded(o LaunchOptions) bool {
	return isLoaded(o.Label)
}
