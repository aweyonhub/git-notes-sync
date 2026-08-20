package service

import (
	"fmt"
	"strings"
)

// systemdUnitPath returns ~/.config/systemd/user/<label>.timer|service.
func systemdUnitPath(o LaunchOptions, kind string) string {
	return homeDir(o.Home) + "/.config/systemd/user/" + o.Label + "." + kind
}

// systemdService builds the user unit for the given mode:
//   - ModeInterval: Type=oneshot, runs `gns sync-all` (paired with a timer)
//   - ModeDaemon:   Type=simple resident `gns daemon -c <config>` with
//     Restart=always so systemd keeps it alive
func systemdService(o LaunchOptions) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=git-notes-sync " + modeWord(o) + " (" + o.Label + ")\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")

	if o.Mode == ModeDaemon {
		b.WriteString("[Service]\n")
		b.WriteString("Type=simple\n")
		b.WriteString("ExecStart=" + o.Exe + " daemon -c " + o.Config + "\n")
		b.WriteString("Restart=always\n")
		b.WriteString("RestartSec=10\n")
	} else {
		exec := o.Exe + " sync-all"
		if o.Config != "" {
			exec += " -c " + o.Config
		}
		b.WriteString("[Service]\n")
		b.WriteString("Type=oneshot\n")
		b.WriteString("ExecStart=" + exec + "\n")
	}
	b.WriteString("Environment=PATH=" + o.ExeDir() + ":/usr/local/bin:/usr/bin:/bin\n")
	b.WriteString("Environment=HOME=" + homeDir(o.Home) + "\n\n")

	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// systemdTimer builds the timer unit for ModeInterval (not used for daemon).
// OnUnitActiveSec mirrors launchd's StartInterval semantics: fire N seconds
// after the previous run; AccuracySec tightens the drift.
func systemdTimer(o LaunchOptions) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=git-notes-sync interval timer (" + o.Label + ")\n\n")
	b.WriteString("[Timer]\n")
	b.WriteString("OnBootSec=1min\n")
	b.WriteString("OnUnitActiveSec=" + fmt.Sprintf("%ds", o.Interval) + "\n")
	b.WriteString("AccuracySec=5s\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=timers.target\n")
	return b.String()
}

func modeWord(o LaunchOptions) string {
	if o.Mode == ModeDaemon {
		return "daemon"
	}
	return "sync"
}

// cronLogPath is where crontab entries redirect output (systemd units use the
// journal instead).
func cronLogPath(o LaunchOptions) string {
	return strings.TrimRight(o.LogDir, "/") + "/" + o.Label + ".log"
}

// cronMarkerOpen / cronMarkerClose delimit a managed block inside crontab.
// The label is embedded in the markers so multiple registrations (different
// labels) can coexist in one crontab and be managed independently.
func cronMarkerOpen(label string) string {
	return "# >>> gns-sync " + label + " (do not edit) >>>"
}

func cronMarkerClose(label string) string {
	return "# <<< gns-sync " + label + " <<<"
}

// cronBlock renders the crontab lines for the given options:
//   - ModeInterval: `*/N * * * * <exe> sync-all >> <log> 2>&1`
//   - ModeDaemon:   `@reboot <exe> daemon -c <config>` (cron has no
//     keep-alive; @reboot approximates "start at boot")
func cronBlock(o LaunchOptions) []string {
	var lines []string
	lines = append(lines, cronMarkerOpen(o.Label))
	if o.Mode == ModeDaemon {
		lines = append(lines, "@reboot "+o.Exe+" daemon -c "+o.Config+" --log "+cronLogPath(o))
	} else {
		cmd := o.Exe + " sync-all"
		if o.Config != "" {
			cmd += " -c " + o.Config
		}
		lines = append(lines, cronSchedule(o.Interval)+" "+cmd+" --log "+cronLogPath(o))
	}
	lines = append(lines, cronMarkerClose(o.Label))
	return lines
}

// cronSchedule converts a seconds interval to a cron schedule expression,
// minute-granular (ceil) up to 59 minutes, then hourly (ceil) up to 23h,
// then daily at 00:00 — cron has no sub-minute or arbitrary-period support.
func cronSchedule(interval int) string {
	min := (interval + 59) / 60 // ceil to minutes
	switch {
	case min <= 59:
		return fmt.Sprintf("*/%d * * * *", min)
	case min <= 23*60:
		h := (min + 59) / 60 // ceil to hours
		return fmt.Sprintf("0 */%d * * *", h)
	default:
		return "0 0 * * *"
	}
}

// mergeCrontab inserts the managed block for the given label, replacing any
// previous block carrying the same label. Blocks of other labels and
// non-managed lines are preserved.
func mergeCrontab(existing string, block []string, label string) string {
	return stripCrontab(existing, label) + strings.Join(block, "\n") + "\n"
}

// stripCrontab removes every managed block whose markers carry the given
// label; other labels' blocks and non-managed lines are preserved. A block
// missing its close marker is removed up to the end of the file.
func stripCrontab(existing string, label string) string {
	open := cronMarkerOpen(label)
	close := cronMarkerClose(label)
	var out strings.Builder
	rest := existing
	for {
		idx := strings.Index(rest, open)
		if idx < 0 {
			out.WriteString(rest)
			return out.String()
		}
		out.WriteString(rest[:idx])
		after := rest[idx+len(open):]
		end := strings.Index(after, close)
		if end < 0 {
			return out.String() // unterminated: drop everything to EOF
		}
		rest = after[end+len(close):]
		if strings.HasPrefix(rest, "\n") {
			rest = rest[1:]
		}
	}
}

// crontabHasManaged reports whether a managed block for the given label exists.
func crontabHasManaged(existing string, label string) bool {
	return strings.Contains(existing, cronMarkerOpen(label))
}
