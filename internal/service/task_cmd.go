// Task Scheduler command construction (platform-independent pure functions,
// unit-tested on any GOOS). The schtasks invocations live in windows.go.

package service

import (
	"strconv"
)

// taskName returns the Task Scheduler task name for the label.
func taskName(label string) string { return label }

// taskCommand builds the command string stored in the task's /TR value.
// Both modes wrap the exe in cmd /c with log redirection so stdout/stderr
// survive (scheduled tasks have no console). Windows path semantics:
// always backslash (filepath.Join would use / on non-Windows test hosts).
func taskCommand(o LaunchOptions) string {
	exe := `"` + o.Exe + `"`
	log := o.LogDir + `\` + o.Label + ".log"
	if o.Mode == ModeInterval {
		return `cmd /c ` + exe + ` sync-all >> "` + log + `" 2>&1`
	}
	return `cmd /c ` + exe + ` daemon -c "` + o.Config + `" >> "` + log + `" 2>&1`
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

