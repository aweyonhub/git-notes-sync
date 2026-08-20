// Task Scheduler command construction (platform-independent pure functions,
// unit-tested on any GOOS). The schtasks invocations live in windows.go.

package service

import (
	"strconv"
	"strings"
)

// taskName returns the Task Scheduler task name for the label.
func taskName(label string) string { return label }

// taskCommand builds the command string stored in the task's /TR value.
// Task Scheduler runs wscript.exe on a wrapper .vbe. wscript.exe is a GUI
// program, so no console window flashes when the task fires; the .vbe then
// launches gns hidden via WshShell.Run(..., 0). Windows path semantics:
// always backslash (filepath.Join would use / on non-Windows test hosts).
func taskCommand(o LaunchOptions) string {
	vbe := o.LogDir + `\` + o.Label + ".vbe"
	return `wscript.exe "` + vbe + `"`
}

// taskVbeContent builds the wrapper .vbe that wscript.exe executes. It runs
// `gns sync-all --log <path>` (or `gns daemon -c <config> --log <path>`) with
// the window hidden (Run's 2nd arg 0 = SW_HIDE). gns handles log rotation
// internally via --log.
func taskVbeContent(o LaunchOptions) string {
	logPath := o.LogDir + `\` + o.Label + ".log"
	var cmd string
	if o.Mode == ModeInterval {
		cmd = `"` + o.Exe + `" sync-all`
		if o.Config != "" {
			cmd += ` -c "` + o.Config + `"`
		}
		cmd += ` --log "` + logPath + `"`
	} else {
		cmd = `"` + o.Exe + `" daemon -c "` + o.Config + `" --log "` + logPath + `"`
	}
	// VBScript string literal: a literal double quote is written as "".
	vbs := strings.ReplaceAll(cmd, `"`, `""`)
	return `CreateObject("WScript.Shell").Run "` + vbs + `", 0, False` + "\r\n"
}

// taskCreateArgs returns the schtasks /Create argument list. Pure, so it can
// be unit-tested without invoking schtasks.
func taskCreateArgs(o LaunchOptions) []string {
	args := []string{"/Create", "/TN", taskName(o.Label), "/TR", taskCommand(o), "/F"}
	if o.Mode == ModeInterval {
		min := o.Interval / 60
		if min < 1 {
			min = 1
		}
		args = append(args, "/SC", "MINUTE", "/MO", strconv.Itoa(min))
	} else {
		args = append(args, "/SC", "ONLOGON")
	}
	return args
}
