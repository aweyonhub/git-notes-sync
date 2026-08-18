package service

import (
	"strings"
	"testing"
)

func linuxTestOptions() LaunchOptions {
	return LaunchOptions{
		Label:    "com.git-notes-sync",
		Exe:      "/home/alice/.local/bin/gns",
		Mode:     ModeInterval,
		Interval: 300,
		Home:     "/home/alice",
		LogDir:   "/home/alice/.local/state/git-notes-sync",
	}
}

func TestSystemdServiceInterval(t *testing.T) {
	got := systemdService(linuxTestOptions())
	for _, want := range []string{
		"[Unit]",
		"Description=git-notes-sync sync (com.git-notes-sync)",
		"[Service]",
		"Type=oneshot",
		"ExecStart=/home/alice/.local/bin/gns sync-all",
		"Environment=PATH=/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin",
		"Environment=HOME=/home/alice",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("service unit missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "Restart=") {
		t.Errorf("oneshot unit must not set Restart\n%s", got)
	}
}

func TestSystemdServiceDaemon(t *testing.T) {
	o := linuxTestOptions()
	o.Mode = ModeDaemon
	o.Config = "/home/alice/.config/git-notes-sync/config.toml"
	got := systemdService(o)
	for _, want := range []string{
		"Description=git-notes-sync daemon (com.git-notes-sync)",
		"Type=simple",
		"ExecStart=/home/alice/.local/bin/gns daemon -c /home/alice/.config/git-notes-sync/config.toml",
		"Restart=always",
		"RestartSec=10",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("service unit missing %q\n---\n%s", want, got)
		}
	}
}

func TestSystemdTimer(t *testing.T) {
	got := systemdTimer(linuxTestOptions())
	for _, want := range []string{
		"[Timer]",
		"OnBootSec=1min",
		"OnUnitActiveSec=300s",
		"AccuracySec=5s",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timer unit missing %q\n---\n%s", want, got)
		}
	}
}

func TestSystemdUnitPath(t *testing.T) {
	o := linuxTestOptions()
	if got := systemdUnitPath(o, "timer"); got != "/home/alice/.config/systemd/user/com.git-notes-sync.timer" {
		t.Errorf("timer path = %q", got)
	}
}

func TestCronBlockInterval(t *testing.T) {
	o := linuxTestOptions()
	lines := cronBlock(o)
	want := []string{
		cronMarkerOpen,
		"*/5 * * * * /home/alice/.local/bin/gns sync-all >> /home/alice/.local/state/git-notes-sync/com.git-notes-sync.log 2>&1",
		cronMarkerClose,
	}
	if len(lines) != len(want) {
		t.Fatalf("cronBlock = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestCronBlockIntervalCeiling(t *testing.T) {
	o := linuxTestOptions()
	o.Interval = 90 // ceil(90/60) = 2 → */2 (runs every 120s)
	if got := cronBlock(o)[1]; !strings.HasPrefix(got, "*/2 * * * *") {
		t.Errorf("90s interval → %q, want */2 prefix", got)
	}
	o.Interval = 30 // < 60s → */1
	if got := cronBlock(o)[1]; !strings.HasPrefix(got, "*/1 * * * *") {
		t.Errorf("30s interval → %q, want */1 prefix", got)
	}
}

func TestCronScheduleRanges(t *testing.T) {
	cases := []struct {
		interval int
		want     string
	}{
		{30, "*/1 * * * *"},
		{300, "*/5 * * * *"},
		{7200, "0 */2 * * *"},  // 2h (e.g. config sync_interval=7200)
		{3600, "0 */1 * * *"},  // 1h
		{23 * 3600, "0 */23 * * *"},
		{24 * 3600, "0 0 * * *"}, // ≥24h → daily
	}
	for _, c := range cases {
		if got := cronSchedule(c.interval); got != c.want {
			t.Errorf("cronSchedule(%d) = %q, want %q", c.interval, got, c.want)
		}
	}
}

func TestCronBlockDaemon(t *testing.T) {
	o := linuxTestOptions()
	o.Mode = ModeDaemon
	o.Config = "/home/alice/.config/git-notes-sync/config.toml"
	lines := cronBlock(o)
	if got := lines[1]; got != "@reboot /home/alice/.local/bin/gns daemon -c /home/alice/.config/git-notes-sync/config.toml" {
		t.Errorf("daemon cron line = %q", got)
	}
}

func TestMergeStripCrontab(t *testing.T) {
	o := linuxTestOptions()
	block := cronBlock(o)
	existing := "SHELL=/bin/bash\n0 9 * * * backup-job\n"

	merged := mergeCrontab(existing, block)
	if !strings.Contains(merged, "SHELL=/bin/bash") || !strings.Contains(merged, "backup-job") {
		t.Errorf("merge must preserve existing lines\n%s", merged)
	}
	if !strings.Contains(merged, "*/5 * * * *") {
		t.Errorf("merge must add the managed block\n%s", merged)
	}

	// strip must restore the original content
	if got := stripCrontab(merged); got != existing {
		t.Errorf("strip = %q, want %q", got, existing)
	}
}

func TestStripCrontabReplacesOldBlock(t *testing.T) {
	o := linuxTestOptions()
	old := mergeCrontab("user-line\n", cronBlock(o))
	// re-install with a different interval → old block replaced, user line kept
	o.Interval = 600
	re := mergeCrontab(old, cronBlock(o))
	if strings.Count(re, cronMarkerOpen) != 1 {
		t.Errorf("exactly one managed block expected\n%s", re)
	}
	if !strings.Contains(re, "*/10 * * * *") {
		t.Errorf("old block must be replaced with */10\n%s", re)
	}
	if !strings.Contains(re, "user-line\n") {
		t.Errorf("user line must survive re-install\n%s", re)
	}
}

func TestStripCrontabNoBlock(t *testing.T) {
	in := "0 9 * * * job\n"
	if got := stripCrontab(in); got != in {
		t.Errorf("strip without block must be a no-op, got %q", got)
	}
}

func TestStripCrontabUnterminatedBlock(t *testing.T) {
	// a block whose close marker was lost (e.g. hand-edited) is removed to EOF
	in := "keep\n" + cronMarkerOpen + "\n*/5 * * * * x\n"
	got := stripCrontab(in)
	if got != "keep\n" {
		t.Errorf("unterminated block must be removed to EOF, got %q", got)
	}
}
