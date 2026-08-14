//go:build !darwin

package service

import "fmt"

func LaunchdDomain() string { return "" }

func Install(o LaunchOptions) error {
	return fmt.Errorf("gns install is currently macOS-only (launchd); Linux systemd and Windows Task Scheduler are planned — build/run on macOS, or configure cron (Linux) / `gns daemon` + Task Scheduler (Windows) manually")
}

func Uninstall(o LaunchOptions) error {
	return fmt.Errorf("gns uninstall is currently macOS-only (launchd); nothing to remove on this platform")
}

func DefaultLogDir(home string) string { return "" }

func Loaded(o LaunchOptions) bool { return false }
