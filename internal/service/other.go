//go:build !darwin && !linux && !windows

package service

import "fmt"

func Install(o LaunchOptions) error {
	return fmt.Errorf("gns install is supported on macOS (launchd), Linux (systemd/cron) and Windows (Task Scheduler); nothing to do on this platform")
}

func Uninstall(o LaunchOptions) error {
	return fmt.Errorf("gns uninstall is supported on macOS (launchd), Linux (systemd/cron) and Windows (Task Scheduler); nothing to remove on this platform")
}

func DefaultLogDir(home string) string { return "" }

func Loaded(o LaunchOptions) bool { return false }
