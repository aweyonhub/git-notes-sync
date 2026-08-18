package service

import (
	"path/filepath"
	"testing"
)

func TestTaskCommandInterval(t *testing.T) {
	o := LaunchOptions{
		Label:    "com.git-notes-sync",
		Exe:      `C:\Program Files\git-notes-sync\gns.exe`,
		Mode:     ModeInterval,
		Interval: 600,
		LogDir:   `C:\Users\me\AppData\Local\git-notes-sync`,
	}
	cmd := taskCommand(o)
	want := `cmd /c "C:\Program Files\git-notes-sync\gns.exe" sync-all >> "C:\Users\me\AppData\Local\git-notes-sync\com.git-notes-sync.log" 2>&1`
	if cmd != want {
		t.Errorf("taskCommand interval:\n got: %s\nwant: %s", cmd, want)
	}
}

func TestTaskCommandDaemon(t *testing.T) {
	o := LaunchOptions{
		Label:  "com.git-notes-sync",
		Exe:    `C:\gns.exe`,
		Mode:   ModeDaemon,
		Config: `C:\Users\me\.config\git-notes-sync\config.toml`,
		LogDir: `C:\Users\me\AppData\Local\git-notes-sync`,
	}
	cmd := taskCommand(o)
	want := `cmd /c "C:\gns.exe" daemon -c "C:\Users\me\.config\git-notes-sync\config.toml" >> "C:\Users\me\AppData\Local\git-notes-sync\com.git-notes-sync.log" 2>&1`
	if cmd != want {
		t.Errorf("taskCommand daemon:\n got: %s\nwant: %s", cmd, want)
	}
}

func TestTaskCreateArgs(t *testing.T) {
	o := LaunchOptions{
		Label:    "com.git-notes-sync",
		Exe:      `C:\gns.exe`,
		Mode:     ModeInterval,
		Interval: 60,
		LogDir:   filepath.Join(`C:\Users\me\AppData\Local\git-notes-sync`),
	}
	args := taskCreateArgs(o)
	want := []string{"/Create", "/TN", "com.git-notes-sync", "/TR",
		`cmd /c "C:\gns.exe" sync-all >> "C:\Users\me\AppData\Local\git-notes-sync\com.git-notes-sync.log" 2>&1`,
		"/F", "/SC", "MINUTE", "/MO", "1"}
	if len(args) != len(want) {
		t.Fatalf("arg count: got %d want %d (%v)", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg[%d]: got %q want %q", i, args[i], want[i])
		}
	}
}

func TestTaskCreateArgsSubMinuteClamp(t *testing.T) {
	o := LaunchOptions{
		Label:    "com.git-notes-sync",
		Exe:      `C:\gns.exe`,
		Mode:     ModeInterval,
		Interval: 30, // < 60s → clamp to 1 minute
		LogDir:   `C:\logs`,
	}
	args := taskCreateArgs(o)
	if args[len(args)-1] != "1" {
		t.Errorf("sub-minute interval should clamp to 1 minute, got %q", args[len(args)-1])
	}
}

func TestTaskCreateArgsDaemonOnLogon(t *testing.T) {
	o := LaunchOptions{
		Label:  "com.git-notes-sync",
		Exe:    `C:\gns.exe`,
		Mode:   ModeDaemon,
		Config: `C:\cfg.toml`,
	}
	args := taskCreateArgs(o)
	if args[len(args)-1] != "ONLOGON" {
		t.Errorf("daemon mode should use ONLOGON trigger, got %v", args)
	}
}
