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
		b.WriteString("[Service]\n")
		b.WriteString("Type=oneshot\n")
		b.WriteString("ExecStart=" + o.Exe + " sync-all\n")
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

// cronMarkerOpen / cronMarkerClose delimit the managed block inside crontab.
const (
	cronMarkerOpen  = "# >>> gns-sync managed by gns install (do not edit) >>>"
	cronMarkerClose = "# <<< gns-sync <<<"
)

// cronBlock renders the crontab lines for the given options:
//   - ModeInterval: `*/N * * * * <exe> sync-all >> <log> 2>&1`
//   - ModeDaemon:   `@reboot <exe> daemon -c <config>` (cron has no
//     keep-alive; @reboot approximates "start at boot")
func cronBlock(o LaunchOptions) []string {
	var lines []string
	lines = append(lines, cronMarkerOpen)
	if o.Mode == ModeDaemon {
		lines = append(lines, "@reboot "+o.Exe+" daemon -c "+o.Config+" --log "+cronLogPath(o))
	} else {
		lines = append(lines, cronSchedule(o.Interval)+" "+o.Exe+" sync-all --log "+cronLogPath(o))
	}
	lines = append(lines, cronMarkerClose)
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

// mergeCrontab inserts the managed block, replacing any previous managed
// block (detected by the open marker). Non-managed lines are preserved.
func mergeCrontab(existing string, block []string) string {
	return stripCrontab(existing) + strings.Join(block, "\n") + "\n"
}

// stripCrontab removes a previously managed block (from the open marker to
// the close marker inclusive); a block missing its close marker is removed
// up to the end of the file. The newline right after the close marker is
// dropped too, so stripping restores the exact pre-merge content.
func stripCrontab(existing string) string {
	idx := strings.Index(existing, cronMarkerOpen)
	if idx < 0 {
		return existing
	}
	rest := existing[idx+len(cronMarkerOpen):]
	end := strings.Index(rest, cronMarkerClose)
	if end >= 0 {
		end += len(cronMarkerClose)
	} else {
		end = len(rest)
	}
	tail := rest[end:]
	if strings.HasPrefix(tail, "\n") {
		tail = tail[1:]
	}
	return existing[:idx] + tail
}

// crontabHasManaged reports whether a managed block exists.
func crontabHasManaged(existing string) bool {
	return strings.Contains(existing, cronMarkerOpen)
}
