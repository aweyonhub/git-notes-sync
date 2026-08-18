//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux backends:
//   - systemd user units (default): ~/.config/systemd/user/<label>.service
//     (+ .timer for interval mode), enabled via `systemctl --user`.
//   - crontab: a managed marker block appended to the user's crontab
//     (`crontab -l` → edit → `crontab -`).

// Install registers the scheduling unit on Linux.
func Install(o LaunchOptions) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if err := requireHome(o); err != nil {
		return err
	}
	if o.Backend == BackendCron {
		return installCron(o)
	}
	return installSystemd(o)
}

// Uninstall removes the scheduling unit on Linux.
func Uninstall(o LaunchOptions) error {
	if err := validateLabel(o.Label); err != nil {
		return err
	}
	if err := requireHome(o); err != nil {
		return err
	}
	if o.Backend == BackendCron {
		return uninstallCron(o)
	}
	// BackendAuto or BackendSystemd: remove systemd units AND any leftover
	// cron block (user may have switched backends with -force without
	// uninstalling first).
	cronErr := uninstallCron(o)
	sdErr := uninstallSystemd(o)
	if sdErr != nil {
		return sdErr
	}
	return cronErr
}

// Loaded reports whether the unit / cron block is registered.
func Loaded(o LaunchOptions) bool {
	if o.Backend == BackendCron {
		existing, err := crontabDump()
		return err == nil && crontabHasManaged(existing)
	}
	// BackendAuto: check both systemd and cron
	existing, err := crontabDump()
	if err == nil && crontabHasManaged(existing) {
		return true
	}
	for _, kind := range []string{"timer", "service"} {
		if systemctl("is-active", "--quiet", o.Label+"."+kind) == nil {
			return true
		}
	}
	return false
}

// DefaultLogDir is the XDG state dir for cron-mode logs (systemd units use
// the journal).
func DefaultLogDir(home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".local", "state", "git-notes-sync")
}

// LogPath returns the log file for cron mode; systemd mode has no file
// (output goes to the user journal — `gns logs` uses journalctl there).
// Returns "" when the file does not exist.
func LogPath(label string) string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".local", "state", "git-notes-sync", label+".log")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// SystemdUnitExists reports whether a systemd user unit file exists on disk
// for the given label (either .service or .timer).
func SystemdUnitExists(label string) bool {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".config", "systemd", "user", label)
	for _, kind := range []string{".service", ".timer"} {
		if _, err := os.Stat(base + kind); err == nil {
			return true
		}
	}
	return false
}

// --- systemd backend ---

func installSystemd(o LaunchOptions) error {
	unitDir := homeDir(o.Home) + "/.config/systemd/user"
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", unitDir, err)
	}

	units := []struct{ kind, content string }{
		{"service", systemdService(o)},
	}
	if o.Mode == ModeInterval {
		units = append(units, struct{ kind, content string }{"timer", systemdTimer(o)})
	}
	for _, u := range units {
		p := systemdUnitPath(o, u.kind)
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, []byte(u.content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, p); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("rename to %s: %w", p, err)
		}
	}

	if err := systemctl("--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	enable := o.Label + ".service"
	if o.Mode == ModeInterval {
		enable = o.Label + ".timer"
	}
	if err := systemctl("--user", "enable", "--now", enable); err != nil {
		return fmt.Errorf("enable %s: %w (is systemd user session available? try `--cron` or `loginctl enable-linger` for headless setups)", enable, err)
	}
	return nil
}

func uninstallSystemd(o LaunchOptions) error {
	// disable+stop is idempotent; ignore errors for units that don't exist
	_ = systemctl("--user", "disable", "--now", o.Label+".timer")
	_ = systemctl("--user", "disable", "--now", o.Label+".service")
	removed := false
	for _, kind := range []string{"timer", "service"} {
		p := systemdUnitPath(o, kind)
		if _, err := os.Stat(p); err == nil {
			if err := os.Remove(p); err != nil {
				return fmt.Errorf("remove %s: %w", p, err)
			}
			removed = true
		}
	}
	if !removed {
		// nothing was there; still give systemd a chance to pick up state
		return nil
	}
	return systemctl("--user", "daemon-reload")
}

// --- crontab backend ---

func crontabDump() (string, error) {
	out, err := exec.Command("crontab", "-l").CombinedOutput()
	if err != nil {
		// "no crontab for user" is a normal first-run state (exit 1; message
		// on stderr varies by distro: "no crontab for <user>")
		msg := strings.TrimSpace(string(out))
		if len(msg) == 0 || strings.Contains(msg, "no crontab") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %s", msg)
	}
	return string(out), nil
}

func crontabWrite(content string) error {
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab -: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func installCron(o LaunchOptions) error {
	existing, err := crontabDump()
	if err != nil {
		return err
	}
	if crontabHasManaged(existing) && !o.Force {
		return fmt.Errorf("crontab already contains a gns-sync block (use -force to replace, or `gns uninstall` first)")
	}
	// ensure the log dir exists before cron redirects into it
	if err := os.MkdirAll(o.LogDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", o.LogDir, err)
	}
	return crontabWrite(mergeCrontab(existing, cronBlock(o)))
}

func uninstallCron(o LaunchOptions) error {
	existing, err := crontabDump()
	if err != nil {
		return err
	}
	if !crontabHasManaged(existing) {
		return nil
	}
	return crontabWrite(stripCrontab(existing))
}

// systemctl runs systemctl; when --user is implied we still pass it
// explicitly so the unit lookup is unambiguous.
func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}
