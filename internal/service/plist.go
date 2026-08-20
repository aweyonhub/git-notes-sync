// Package service registers gns as an OS service / launch agent.
//
// All three platforms are implemented: macOS launchd LaunchAgent, Linux
// systemd user units (or a managed crontab block via --cron), and Windows
// Task Scheduler (schtasks with a wscript/.vbe wrapper to avoid console
// windows). The generators in this file are pure and platform-independent
// so they can be unit-tested everywhere.
package service

import (
	"encoding/xml"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Mode selects how the agent is scheduled.
type Mode int

const (
	// ModeInterval: launchd fires `gns sync-all` every N seconds (stateless,
	// the documented macOS recommendation). One shot per tick, no resident
	// process.
	ModeInterval Mode = iota
	// ModeDaemon: a resident `gns daemon` process kept alive by launchd
	// (KeepAlive). Sync cadence is controlled by the daemon's own
	// sync_interval config (default 600s).
	ModeDaemon
)

// Backend selects the scheduling backend on Linux (ignored on macOS).
type Backend int

const (
	// BackendAuto: default — systemd user units on Linux (timer for interval
	// mode, service for daemon mode).
	BackendAuto Backend = iota
	// BackendSystemd: explicitly use systemd user units.
	BackendSystemd
	// BackendCron: manage a block in the user's crontab instead.
	BackendCron
)

// LaunchOptions describes a macOS LaunchAgent to install.
type LaunchOptions struct {
	Label    string // launchd label / systemd unit name, e.g. com.git-notes-sync
	Exe      string // absolute path of the program launchd will run
	Mode     Mode
	Backend  Backend // Linux only: systemd (default) or crontab
	Interval int     // seconds, ModeInterval only
	Config   string  // global config path, ModeDaemon only (passed as -c)
	Home     string  // HOME for the agent environment
	LogDir   string  // where stdout/stderr logs go (e.g. ~/Library/Logs)
	Force    bool    // overwrite an existing plist
}

// PlistPath returns the absolute LaunchAgent plist path.
func (o LaunchOptions) PlistPath() string {
	return o.agentDir() + o.Label + ".plist"
}

// agentDir returns the LaunchAgents directory.
func (o LaunchOptions) agentDir() string {
	// ~/Library/LaunchAgents is the user-level agents directory.
	return homeDir(o.Home) + "/Library/LaunchAgents/"
}

func (o LaunchOptions) logPath(suffix string) string {
	return strings.TrimRight(o.LogDir, "/") + "/" + o.Label + suffix
}

// buildPlist renders the LaunchAgent plist XML. Values are XML-escaped; paths
// are left as provided (caller must pass absolute paths).
func buildPlist(o LaunchOptions) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")

	writeKeyString(&b, "Label", o.Label)

	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	if o.Mode == ModeDaemon {
		writeArrayString(&b, o.Exe, "daemon")
		if o.Config != "" {
			writeArrayString(&b, "-c", o.Config)
		}
	} else {
		writeArrayString(&b, o.Exe, "sync-all")
		if o.Config != "" {
			writeArrayString(&b, "-c", o.Config)
		}
	}
	writeArrayString(&b, "--log", o.logPath(".log"))
	b.WriteString("\t</array>\n")

	if o.Mode == ModeDaemon {
		// resident daemon: keep alive, restart if it exits
		writeKeyBool(&b, "RunAtLoad", true)
		writeKeyBool(&b, "KeepAlive", true)
	} else {
		// stateless: fire every N seconds; also run once at load
		writeKeyInt(&b, "StartInterval", o.Interval)
		writeKeyBool(&b, "RunAtLoad", true)
	}

	// launchd's environment is nearly empty: PATH must include the gns
	// launcher dir (npm installs gns as a node shim) and HOME must point at
	// the user directory (credential helpers / ssh look there).
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	writeKeyString(&b, "PATH", o.ExeDir()+":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	writeKeyString(&b, "HOME", homeDir(o.Home))
	b.WriteString("\t</dict>\n")

	writeKeyString(&b, "ProcessType", "Background")

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// ExeDir returns the directory containing the program; launchd needs it in
// PATH to resolve `#!/usr/bin/env node` shims (npm-installed gns).
func (o LaunchOptions) ExeDir() string {
	dir := o.Exe
	if i := strings.LastIndexByte(dir, '/'); i >= 0 {
		dir = dir[:i]
	}
	if dir == "" {
		dir = "/"
	}
	return dir
}

func writeKeyString(b *strings.Builder, key, val string) {
	b.WriteString("\t<key>")
	b.WriteString(key)
	b.WriteString("</key>\n\t<string>")
	b.WriteString(xmlEscape(val))
	b.WriteString("</string>\n")
}

func writeArrayString(b *strings.Builder, vals ...string) {
	for _, v := range vals {
		b.WriteString("\t\t<string>")
		b.WriteString(xmlEscape(v))
		b.WriteString("</string>\n")
	}
}

func writeKeyInt(b *strings.Builder, key string, val int) {
	b.WriteString("\t<key>")
	b.WriteString(key)
	b.WriteString("</key>\n\t<integer>")
	b.WriteString(strconv.Itoa(val))
	b.WriteString("</integer>\n")
}

func writeKeyBool(b *strings.Builder, key string, val bool) {
	b.WriteString("\t<key>")
	b.WriteString(key)
	b.WriteString("</key>\n\t<")
	if val {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteString("/>\n")
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// homeDir expands a HOME value, falling back to an empty placeholder if the
// user home cannot be determined.
func homeDir(home string) string {
	if home != "" {
		return strings.TrimRight(home, "/")
	}
	return ""
}

// Preflight inspects the environment the agent will run in and returns
// user-facing warnings. It does not fail the install — the agent may still
// work (e.g. SSH remotes) — but silent failures later are the common failure
// mode, so surface risks up front.
func Preflight(home string, repoPaths []string) []string {
	var warns []string

	// 1. HTTPS credential persistence: background services (launchd/systemd/
	// cron) have no terminal, so interactive auth can never succeed.
	helper := gitCredentialHelper()
	switch {
	case helper == "":
		warns = append(warns, "warn: git credential.helper is not configured — background services have no terminal and HTTPS push would fail. Run: git config --global credential.helper store, then `git push` once to save credentials")
	case runtime.GOOS == "darwin" && strings.Contains(helper, "osxkeychain"):
		warns = append(warns, "note: credential helper is osxkeychain — the first launchd run may pop a Keychain authorization dialog; click \"Always Allow\"")
	case strings.Contains(helper, "store"):
		// credentials are in ~/.git-credentials; nothing to do
	}

	// 2. macOS-only: repos under TCC-protected folders. launchd-launched
	// processes are silently denied access to Desktop / Documents / Downloads
	// (TCC does not exist on Linux/Windows, so skip the check elsewhere).
	if runtime.GOOS == "darwin" {
		for _, p := range repoPaths {
			if d := tccProtectedDir(p, home); d != "" {
				warns = append(warns, fmt.Sprintf("warn: repo %s is inside the TCC-protected ~/%s folder — launchd may silently deny access; move it to a plain path (e.g. ~/notes)", p, d))
			}
		}
	}

	return warns
}

// gitCredentialHelper returns git's credential helper at any config scope.
func gitCredentialHelper() string {
	out, err := exec.Command("git", "config", "--get", "credential.helper").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// tccProtectedDir returns the TCC-protected folder name containing path, or
// "" if it is not under one.
func tccProtectedDir(path, home string) string {
	if home == "" {
		return ""
	}
	p := filepath.ToSlash(path)
	for _, d := range []string{"Desktop", "Documents", "Downloads"} {
		base := filepath.ToSlash(filepath.Join(home, d))
		if p == base || strings.HasPrefix(p, base+"/") {
			return d
		}
	}
	return ""
}

// Validate checks options before writing anything.
func (o LaunchOptions) Validate() error {
	if err := validateLabel(o.Label); err != nil {
		return err
	}
	if o.Exe == "" {
		return fmt.Errorf("program path must not be empty")
	}
	if o.Mode == ModeInterval && o.Interval < 5 {
		return fmt.Errorf("interval must be at least 5 seconds")
	}
	if o.Mode == ModeDaemon && o.Config == "" {
		return fmt.Errorf("daemon mode needs a global config path (-c)")
	}
	return nil
}

// requireHome guards Install/Uninstall against writing to or deleting system
// paths (agentDir() degenerates to /Library/LaunchAgents when HOME is
// unknown). Uninstall needs this but not the full Validate (no Exe/Mode).
func requireHome(o LaunchOptions) error {
	if o.Home == "" {
		return fmt.Errorf("cannot determine user home directory")
	}
	return nil
}

// validateLabel checks a launchd label without requiring the install-only
// fields (Exe / Mode / Interval) — used by Uninstall.
func validateLabel(label string) error {
	if label == "" {
		return fmt.Errorf("label must not be empty")
	}
	if strings.ContainsAny(label, " /") {
		return fmt.Errorf("invalid label %q: must not contain spaces or slashes", label)
	}
	return nil
}
