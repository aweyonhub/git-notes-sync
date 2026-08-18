//go:build !darwin && !linux

package service

import "fmt"

func Install(o LaunchOptions) error {
	return fmt.Errorf("gns install is currently macOS-only (launchd) or Linux-only (systemd/cron); nothing to do on this platform — configure cron manually, or build/run on macOS/Linux")
}

func Uninstall(o LaunchOptions) error {
	return fmt.Errorf("gns uninstall is currently macOS-only (launchd) or Linux-only (systemd/cron); nothing to remove on this platform")
}

func DefaultLogDir(home string) string { return "" }

func Loaded(o LaunchOptions) bool { return false }
