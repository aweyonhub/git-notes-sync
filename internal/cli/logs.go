// Package cli: logs.go implements `gns logs` — view the service logs written
// by `gns install` (launchd / systemd-cron / Task Scheduler).
//
//   - macOS:     ~/Library/Logs/<label>.log
//   - Linux cron: ~/.local/state/git-notes-sync/<label>.log
//   - Linux systemd: user journal via `journalctl --user -u <label>`
//   - Windows:   %LOCALAPPDATA%\git-notes-sync\<label>.log

package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/service"
)

// tailLastLines returns the last n lines of the file at path. A simple
// whole-file read is fine: service logs are small; correctness over
// micro-optimization.
func tailLastLines(path string, n int) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) <= n {
		return lines, nil
	}
	return lines[len(lines)-n:], nil
}

// followFile polls the file every second and prints appended content
// (cross-platform; no inotify dependency).
func followFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	buf := make([]byte, 64*1024)
	for {
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if st.Size() > size {
			if _, err := f.Seek(size, 0); err != nil {
				return err
			}
			for {
				n, err := f.Read(buf)
				if n > 0 {
					os.Stdout.Write(buf[:n])
				}
				if err != nil {
					break
				}
				if n < len(buf) {
					break
				}
			}
			size = st.Size()
		}
		time.Sleep(time.Second)
	}
}

// journalctl runs journalctl --user for a systemd unit, printing its output.
func journalctl(unit string, n int, follow bool) error {
	args := []string{"--user", "-u", unit}
	if follow {
		args = append(args, "-f")
	} else {
		args = append(args, "-n", strconv.Itoa(n))
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	var label string
	var n int
	var follow bool
	fs.StringVar(&label, "label", "com.git-notes-sync", "launchd label / systemd unit / task name")
	fs.IntVar(&n, "n", 50, "number of trailing lines (file mode only)")
	fs.BoolVar(&follow, "f", false, "follow new output (file mode: poll; systemd: journalctl -f)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: gns logs [-label s] [-n N] [-f]")
	}
	if n < 1 {
		n = 1
	}

	// Linux systemd backend: output lives in the user journal, not a file.
	// Check for systemd unit files on disk (not LogPath) to avoid misrouting
	// cron-mode installs that haven't run yet (file missing → would falsely
	// fall into journalctl, which then errors "No such unit").
	if runtime.GOOS == "linux" && service.SystemdUnitExists(label) {
		return journalctl(label+".service", n, follow)
	}

	path := service.LogPath(label)
	if path == "" {
		return fmt.Errorf("no log file for %q — install the scheduler first (`gns install`), or use -label to match it", label)
	}
	if !follow {
		lines, err := tailLastLines(path, n)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("log file not found yet: %s (run `gns install` first, or wait for the first scheduled run)", path)
			}
			return err
		}
		for _, l := range lines {
			fmt.Println(l)
		}
		return nil
	}
	return followFile(path)
}

// cmdLogsUsage is used by the help text.
func logsUsageLine() string {
	return "  gns logs [flags]       show scheduler logs (launchd / systemd / Task Scheduler)"
}
