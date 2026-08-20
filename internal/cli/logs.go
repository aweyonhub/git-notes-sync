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
// (cross-platform; no inotify dependency). It starts at fromBytes so a
// caller can print the tail first (`-n N -f` semantics) without losing writes
// that land between the tail and the follow loop.
func followFile(path string, fromBytes int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var size int64
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if fromBytes >= 0 && fromBytes <= st.Size() {
		size = fromBytes // resume from the tailed position
	} else {
		size = st.Size() // resume from EOF
	}
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
	var follow, pathOnly bool
	fs.StringVar(&label, "label", "com.git-notes-sync", "launchd label / systemd unit / task name")
	fs.IntVar(&n, "n", 50, "number of trailing lines (file mode only)")
	fs.BoolVar(&follow, "f", false, "follow new output (file mode: poll; systemd: journalctl -f)")
	fs.BoolVar(&pathOnly, "path", false, "print the log file path only, read nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: gns logs [--label s] [-n N] [-f] [--path]")
	}
	if n < 1 {
		n = 1
	}

	// An explicit -n wins; -f without -n defaults to a window of 20 lines
	// (tail -n 20 -f semantics); plain -n defaults to 50.
	explicitN := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "n" {
			explicitN = true
		}
	})
	n = resolveLogN(n, follow, explicitN)

	// Linux systemd backend: output lives in the user journal, not a file.
	// Check for systemd unit files on disk (not LogPath) to avoid misrouting
	// cron-mode installs that haven't run yet (file missing → would falsely
	// fall into journalctl, which then errors "No such unit").
	if runtime.GOOS == "linux" && service.SystemdUnitExists(label) {
		if pathOnly {
			fmt.Printf("journalctl --user -u %s.service (no file — systemd journal)\n", label)
			return nil
		}
		return journalctl(label+".service", n, follow)
	}

	path := service.LogPath(label)
	if path == "" {
		return fmt.Errorf("no log file for %q — install the scheduler first (`gns install`), or use --label to match it", label)
	}
	if pathOnly {
		fmt.Println(path)
		return nil
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
	// tail -n N -f semantics: print the last n lines first, then follow new
	// output from that position.
	start := int64(-1)
	if lines, err := tailLastLines(path, n); err == nil {
		for _, l := range lines {
			fmt.Println(l)
		}
		if st, err := os.Stat(path); err == nil {
			start = st.Size()
		}
	}
	return followFile(path, start)
}

// resolveLogN returns the effective line window: an explicit -n wins; -f
// without -n defaults to 20 (tail -n 20 -f); plain -n defaults to 50.
func resolveLogN(n int, follow bool, explicitN bool) int {
	if explicitN {
		return n // n is already clamped to ≥1 by the caller
	}
	if follow {
		return 20
	}
	return 50
}

func logsUsageLine() string {
	return "  gns logs [flags]       show scheduler logs (launchd / systemd / Task Scheduler)"
}
