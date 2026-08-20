// Package cli implements the `gns` command line interface.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aweyonhub/git-notes-sync/internal/ai"
	"github.com/aweyonhub/git-notes-sync/internal/commit"
	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/daemon"
	reposPkg "github.com/aweyonhub/git-notes-sync/internal/repos"
	"github.com/aweyonhub/git-notes-sync/internal/service"
	"github.com/aweyonhub/git-notes-sync/internal/sync"
	"github.com/aweyonhub/git-notes-sync/internal/version"
)

const usageText = `git-notes-sync %s — auto-sync for notes-style git workspaces

usage:
  gns sync [flags]        sync repo: commit → fetch → merge → push
  gns sync-all [flags]    sync every repo in the config repos list
  gns commit [flags]      commit current changes immediately
  gns commit-ai [flags]   commit with an AI-generated message
  gns status [flags]      show worktree / remote / conflict status
  gns resolve [flags]     list or resolve persisted conflict markers
  gns repos <cmd>         manage the repo list: list | add | del
  gns config <cmd>        inspect / edit config: list | get | set | unset
  gns logs [flags]        show scheduler logs (launchd / systemd / Task Scheduler)
  gns install [flags]     install launchd (macOS) / systemd-cron (Linux) / Task Scheduler (Windows)
  gns uninstall [flags]   remove the registered service
  gns daemon [flags]      run the lightweight timer daemon
  gns version

(alias: notes-sync)

flags (per command):
  -c path      config file (default: global config + ./.notes-sync.toml)
  -p path      target repository (default: current directory)

launcher flag (any command):
  --log path   redirect output to a log file with rotation ([log] max_size_kb / max_backups);
               gns install injects it into scheduler registrations automatically

sync flags:
  -p path      sync one repo (default: current directory)

repos subcommands:
  gns repos list                     list configured repos
  gns repos add <path> [-name n]     add a repo (default name: dir basename)
  gns repos del <name|path>          remove a repo

resolve flags:
  -ours        keep local side, drop markers, commit & push
  -theirs      keep remote side, drop markers, commit & push
  -ai          semantic merge via AI (config [ai] required)
  (no flag     lists conflicted files)

config subcommands:
  gns config list                 show effective values (merged) vs defaults
  gns config get <key>            print one value (e.g. sync_interval, ai.timeout)
  gns config set <key> <value>    write a value to the global config file
  gns config unset <key>          remove a key (fall back to default)
  -c path                        target config file (default: global config)
  note: repos use 'gns repos'; arrays (text_extensions) edit by hand

commit flags:
  -message s   custom commit message (overrides configured mode)

daemon flags:
  -once        run a single tick then exit

install flags (macOS launchd / Linux systemd-cron / Windows Task Scheduler):
  -interval N  tick seconds for interval mode (default: config sync_interval, else 600)
  -daemon      resident mode: keep 'gns daemon' alive instead of interval ticks
               (cadence = config sync_interval)
  -cron        Linux: use crontab instead of systemd user units
  -exe path    program to launch (default: this binary)
  -label s     launchd label / systemd unit / task name (default: com.git-notes-sync)
  -force       overwrite an existing plist

uninstall flags:
  -label s     launchd label (default: com.git-notes-sync)
`

// Run dispatches a command line. Returns the process error, if any.
func Run(args []string) error {
	if len(args) == 0 {
		fmt.Printf(usageText, version.Version)
		return nil
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "sync":
		return cmdSync(rest)
	case "sync-all":
		return cmdSyncAll(rest)
	case "commit":
		return cmdCommit(rest, "")
	case "commit-ai":
		return cmdCommit(rest, config.MessageAI)
	case "status":
		return cmdStatus(rest)
	case "resolve":
		return cmdResolve(rest)
	case "repos":
		return cmdRepos(rest)
	case "config":
		return cmdConfig(rest)
	case "daemon":
		return cmdDaemon(rest)
	case "logs":
		return cmdLogs(rest)
	case "install":
		return cmdInstall(rest)
	case "uninstall":
		return cmdUninstall(rest)
	case "version", "--version", "-v":
		fmt.Println("git-notes-sync " + version.Version + " (commit " + version.Commit + ")")
		return nil
	case "help", "--help", "-h":
		fmt.Printf(usageText, version.Version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (see: gns help)", cmd)
	}
}

func commonFlags(fs *flag.FlagSet, cfgPath, repo *string) {
	fs.StringVar(cfgPath, "c", "", "config file path (default: global + repo)")
	fs.StringVar(repo, "p", "", "target repository (default: current directory)")
	fs.StringVar(repo, "repo", "", "alias of -p")
}

func repoDir(repo string) string {
	if repo != "" {
		return repo
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// resolveTarget resolves the sync target from, in priority order:
//  1. -p/-repo flag (a path)
//  2. positional arg: a configured repo name/path, else a raw path
//  3. current directory
func resolveTarget(cfgPath, repoFlag, positional string) (string, error) {
	if repoFlag != "" {
		return repoFlag, nil
	}
	if positional != "" {
		cfg, err := config.Load(cfgPath, "")
		if err != nil {
			return "", err
		}
		if r, ok := cfg.Repos.Find(positional); ok {
			return r.ExpandedPath(), nil
		}
		return positional, nil
	}
	return repoDir(""), nil
}

// stdoutIsTerminal reports whether stdout is a character device (a tty).
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// logStamp returns a per-line timestamp prefix when stdout is redirected to a
// file (cron / launchd logs), and nothing when writing to a terminal, so
// interactive output stays clean while background logs carry timestamps.
func logStamp() string {
	if stdoutIsTerminal() {
		return ""
	}
	return time.Now().Format("2006-01-02 15:04:05") + " "
}

var commonValueFlags = map[string]bool{
	"c": true, "p": true, "repo": true, "message": true, "name": true,
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	var cfgPath, repo string
	commonFlags(fs, &cfgPath, &repo)
	if err := fs.Parse(normalizeArgs(args, commonValueFlags)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: gns sync [-p path | repo-name | path]")
	}
	dir, err := resolveTarget(cfgPath, repo, fs.Arg(0))
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath, dir)
	if err != nil {
		return err
	}
	rep := sync.Sync(dir, cfg, func(f string, a ...any) {
		fmt.Printf("%s  %s\n", logStamp(), fmt.Sprintf(f, a...))
	})
	return rep.Err
}

// cmdSyncAll syncs every repo in the config repos list (like one daemon tick).
func cmdSyncAll(args []string) error {
	fs := flag.NewFlagSet("sync-all", flag.ContinueOnError)
	var cfgPath string
	fs.StringVar(&cfgPath, "c", "", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfgPath == "" {
		cfgPath = config.GlobalPath()
	}
	cfg, err := config.Load(cfgPath, "")
	if err != nil {
		return err
	}
	repos := cfg.Repos.All()
	if len(repos) == 0 {
		return errors.New("no repos configured (use `gns repos add <path>` or set repos in " + cfgPath + ")")
	}
	var failed bool
	for _, r := range repos {
		path := r.ExpandedPath()
		disp := r.DisplayName()
		// Merge the repo-level .notes-sync.toml on top of the global config
		// (same semantics as `gns sync <repo>` and the daemon). A broken
		// repo config falls back to the global values with a warning.
		rcfg := cfg
		if merged, err := config.Load(cfgPath, path); err == nil {
			rcfg = merged
		} else {
			fmt.Printf("%swarn: %s repo config skipped: %v\n", logStamp(), disp, err)
		}
		fmt.Printf("%s[%s] %s\n", logStamp(), disp, path)
		rep := sync.Sync(path, rcfg, func(f string, a ...any) {
			fmt.Printf("%s  %s\n", logStamp(), fmt.Sprintf(f, a...))
		})
		if rep.Err != nil {
			fmt.Printf("%s[%s] ERROR: %v\n", logStamp(), disp, rep.Err)
			failed = true
		}
	}
	if failed {
		return errors.New("one or more repos failed")
	}
	return nil
}

func cmdCommit(args []string, forcedMode string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	var cfgPath, repo, message string
	commonFlags(fs, &cfgPath, &repo)
	fs.StringVar(&message, "message", "", "custom commit message")
	if err := fs.Parse(normalizeArgs(args, commonValueFlags)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: gns commit [repo-name | path]")
	}
	dir, err := resolveTarget(cfgPath, repo, fs.Arg(0))
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath, dir)
	if err != nil {
		return err
	}
	cm := commit.New(dir, cfg, func(f string, a ...any) {
		fmt.Printf("  %s\n", fmt.Sprintf(f, a...))
	})
	made, err := cm.CommitNow(forcedMode, message)
	if err != nil {
		return err
	}
	if !made {
		fmt.Println("nothing to commit")
	}
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var cfgPath, repo string
	commonFlags(fs, &cfgPath, &repo)
	if err := fs.Parse(normalizeArgs(args, commonValueFlags)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: gns status [repo-name | path]")
	}
	dir, err := resolveTarget(cfgPath, repo, fs.Arg(0))
	if err != nil {
		return err
	}
	if _, err := config.Load(cfgPath, dir); err != nil {
		return err
	}
	out, err := sync.Status(dir)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	var cfgPath, repo string
	var ours, theirs, aiMode bool
	commonFlags(fs, &cfgPath, &repo)
	fs.BoolVar(&ours, "ours", false, "keep local side")
	fs.BoolVar(&theirs, "theirs", false, "keep remote side")
	fs.BoolVar(&aiMode, "ai", false, "semantic merge via AI")
	if err := fs.Parse(normalizeArgs(args, commonValueFlags)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: gns resolve [repo-name | path] [--ours|--theirs|--ai]")
	}
	if n := b2i(ours) + b2i(theirs) + b2i(aiMode); n > 1 {
		return errors.New("--ours / --theirs / --ai are mutually exclusive")
	}
	dir, err := resolveTarget(cfgPath, repo, fs.Arg(0))
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath, dir)
	if err != nil {
		return err
	}
	files, err := sync.FindConflicts(dir)
	if err != nil {
		return err
	}
	if !ours && !theirs && !aiMode {
		if len(files) == 0 {
			fmt.Println("no conflicts")
			return nil
		}
		fmt.Printf("%d conflicted file(s) (use --ours / --theirs / --ai):\n", len(files))
		for _, f := range files {
			fmt.Printf("  %s (%d block(s))\n", f.Path, f.Blocks)
		}
		return nil
	}
	mode := "ours"
	if theirs {
		mode = "theirs"
	}
	if aiMode {
		mode = "ai"
	}
	gen := ai.NewGenerator(&cfg.AI, dir)
	n, err := sync.Resolve(dir, mode, cfg, gen, func(f string, a ...any) {
		fmt.Printf("  %s\n", fmt.Sprintf(f, a...))
	})
	if err != nil {
		return err
	}
	fmt.Printf("resolved %d file(s)\n", n)
	return nil
}

// cmdRepos manages the repo list: list | add <path> | del <name|path>.
func cmdRepos(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gns repos list|add <path> [-name n]|del <name|path>")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		fs := flag.NewFlagSet("repos list", flag.ContinueOnError)
		var cfgPath string
		fs.StringVar(&cfgPath, "c", "", "config file path")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if cfgPath == "" {
			cfgPath = config.GlobalPath()
		}
		list, err := reposPkg.List(cfgPath)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("no repos configured (use: gns repos add <path>)")
			return nil
		}
		for i, r := range list {
			name := r.Name
			if name == "" {
				name = "-"
			}
			fmt.Printf("%d. %-12s %s\n", i+1, name, r.Path)
		}
		fmt.Printf("%d repo(s) in %s\n", len(list), cfgPath)
		return nil
	case "add":
		var cfgPath, name string
		args, err := parseKVArgs(rest, map[string]*string{
			"name": &name,
			"c":    &cfgPath,
		})
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New("usage: gns repos add <path> [-name n] [-c config]")
		}
		if cfgPath == "" {
			cfgPath = config.GlobalPath()
		}
		path := args[0]
		if err := reposPkg.Add(cfgPath, name, path); err != nil {
			return err
		}
		fmt.Printf("added %s → %s\n", nameOrDefault(name, path), path)
		return nil
	case "del":
		var cfgPath string
		args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New("usage: gns repos del <name|path> [-c config]")
		}
		if cfgPath == "" {
			cfgPath = config.GlobalPath()
		}
		if err := reposPkg.Del(cfgPath, args[0]); err != nil {
			return err
		}
		fmt.Printf("removed %s from %s\n", args[0], cfgPath)
		return nil
	default:
		return fmt.Errorf("unknown repos subcommand %q (list|add|del)", sub)
	}
}

func nameOrDefault(name, path string) string {
	if name != "" {
		return name
	}
	return filepath.Base(strings.TrimRight(path, `/\`))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cmdConfig inspects / edits scalar config keys (list | get | set | unset).
// It operates on the global config (or -c file). repos and arrays are not
// editable here — see `gns repos` / hand-edit.
func cmdConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gns config list|get <key>|set <key> <value>|unset <key>")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return cmdConfigList(rest)
	case "get":
		return cmdConfigGet(rest)
	case "set":
		return cmdConfigSet(rest)
	case "unset":
		return cmdConfigUnset(rest)
	default:
		return fmt.Errorf("unknown config subcommand %q (list|get|set|unset)", sub)
	}
}

// resolveCfgPath returns the config path from -c or the global default.
func resolveCfgPath(flag string) string {
	if flag == "" {
		return config.GlobalPath()
	}
	return flag
}

// loadEffective loads the merged config for display; a missing file yields
// Defaults() so `list`/`get` work before any config exists.
func loadEffective(cfgPath string) (*config.Config, error) {
	if _, err := os.Stat(cfgPath); err != nil {
		return config.Defaults(), nil
	}
	return config.Load(cfgPath, "")
}

func cmdConfigList(rest []string) error {
	var cfgPath string
	args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return errors.New("usage: gns config list [-c config]")
	}
	p := resolveCfgPath(cfgPath)
	merged, err := loadEffective(p)
	if err != nil {
		return err
	}
	defaults := config.Defaults()
	for _, f := range config.AllFields() {
		val, _ := config.FieldValue(merged, f.Section, f.Key)
		def, _ := config.FieldValue(defaults, f.Section, f.Key)
		display := val
		if f.Kind == "string" {
			display = `"` + val + `"`
		}
		line := fmt.Sprintf("%-26s = %s", f.Dotted(), display)
		if val != def {
			d := def
			if f.Kind == "string" {
				d = `"` + def + `"`
			}
			line += fmt.Sprintf("   [default: %s]", d)
		}
		fmt.Println(line)
	}
	fmt.Println("\nrepos: use `gns repos list`")
	return nil
}

func cmdConfigGet(rest []string) error {
	var cfgPath string
	args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: gns config get <key> [-c config]")
	}
	dotted := args[0]
	f, ok := config.LookupField(dotted)
	if !ok {
		return configKeyError(dotted)
	}
	p := resolveCfgPath(cfgPath)
	merged, err := loadEffective(p)
	if err != nil {
		return err
	}
	val, _ := config.FieldValue(merged, f.Section, f.Key)
	if f.Kind == "string" {
		val = `"` + val + `"`
	}
	fmt.Println(val)
	return nil
}

func cmdConfigSet(rest []string) error {
	var cfgPath string
	args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
	if err != nil {
		return err
	}
	if len(args) != 2 {
		return errors.New("usage: gns config set <key> <value> [-c config]")
	}
	dotted, value := args[0], args[1]
	if hint := config.UnsettableHint(dotted); hint != "" {
		return errors.New(hint)
	}
	f, ok := config.LookupField(dotted)
	if !ok {
		return configKeyError(dotted)
	}
	p := resolveCfgPath(cfgPath)
	if err := config.SetKey(p, f.Section, f.Key, value); err != nil {
		return err
	}
	fmt.Printf("set %s = %s\n", dotted, value)
	return nil
}

func cmdConfigUnset(rest []string) error {
	var cfgPath string
	args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: gns config unset <key> [-c config]")
	}
	dotted := args[0]
	f, ok := config.LookupField(dotted)
	if !ok {
		return configKeyError(dotted)
	}
	p := resolveCfgPath(cfgPath)
	removed, err := config.UnsetKey(p, f.Section, f.Key)
	if err != nil {
		return err
	}
	if removed {
		fmt.Printf("unset %s\n", dotted)
	} else {
		fmt.Printf("%s was not set (using default)\n", dotted)
	}
	return nil
}

// configKeyError reports an unknown key, hinting at `config list`.
func configKeyError(dotted string) error {
	return fmt.Errorf("unknown config key %q (run `gns config list` to see available keys)", dotted)
}

// normalizeArgs moves flags to the front so flag.FlagSet can parse them,
// allowing positional args (e.g. a repo name) to appear anywhere:
//
//	gns sync notes -c file   →   gns sync -c file notes
//
// valueFlags lists flags that consume the next argument.
//
// Known edge: `--` (end-of-flags terminator) is not supported — every
// argument starting with "-" is treated as a flag, so a repository path
// beginning with "-" cannot be expressed positionally. Accepted trade-off
// for a small CLI; such paths can still be passed via -p/-repo.
func normalizeArgs(args []string, valueFlags map[string]bool) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			key := strings.TrimLeft(a, "-")
			if valueFlags[key] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

// parseKVArgs extracts -key value flags (in any position) and returns the
// remaining positional arguments. Standard flag.FlagSet stops at the first
// positional arg, so repos subcommands parse manually.
func parseKVArgs(args []string, flags map[string]*string) ([]string, error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			key := strings.TrimLeft(a, "-")
			dst, ok := flags[key]
			if !ok {
				return nil, fmt.Errorf("unknown flag %q", a)
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", a)
			}
			*dst = args[i+1]
			i++
			continue
		}
		positional = append(positional, a)
	}
	return positional, nil
}

// resolveInterval returns the effective tick interval for interval mode:
// an explicit -interval wins; otherwise the global config's sync_interval is
// used; if the config cannot be read, fall back to the 600s default (the same
// default as sync_interval).
func resolveInterval(explicit int, cfgPath string) int {
	if explicit > 0 {
		return explicit
	}
	cfg, err := config.Load(cfgPath, "")
	if err != nil {
		return 600
	}
	return cfg.SyncInterval
}

// cmdInstall registers a launchd LaunchAgent / systemd unit / crontab block:
// either a stateless timer running `gns sync-all` (default) or a resident
// `gns daemon` kept alive (--daemon).
func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	var interval int
	var daemonMode, force, cronMode bool
	var exe, label string
	fs.IntVar(&interval, "interval", 0, "tick seconds (interval mode; default: config sync_interval, else 600)")
	fs.BoolVar(&daemonMode, "daemon", false, "resident daemon mode")
	fs.BoolVar(&cronMode, "cron", false, "Linux: use crontab instead of systemd units")
	fs.StringVar(&exe, "exe", "", "program to launch (default: this binary)")
	fs.StringVar(&label, "label", "com.git-notes-sync", "launchd label / systemd unit name")
	fs.BoolVar(&force, "force", false, "overwrite existing service/task")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: gns install [-interval N] [-daemon] [-cron] [-exe path] [-label s] [-force]")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w (pass -exe)", err)
		}
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}

	mode := service.ModeInterval
	cfgPath := config.GlobalPath() // resolved at install time (honors GNS_CONFIG)
	if daemonMode {
		mode = service.ModeDaemon
	}
	// interval resolution: explicit -interval > global config sync_interval > 600s
	interval = resolveInterval(interval, cfgPath)
	backend := service.BackendAuto
	if cronMode {
		if runtime.GOOS != "linux" {
			return errors.New("--cron is Linux-only (crontab backend); on macOS use the default launchd backend, on Windows the default Task Scheduler backend")
		}
		backend = service.BackendCron
	}
	opts := service.LaunchOptions{
		Label:    label,
		Exe:      exe,
		Mode:     mode,
		Backend:  backend,
		Interval: interval,
		Config:   cfgPath,
		Home:     home,
		LogDir:   service.DefaultLogDir(home),
		Force:    force,
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := service.Install(opts); err != nil {
		return err
	}

	modeDesc := fmt.Sprintf("interval: every %ds, runs `gns sync-all -c %s` (stateless)", interval, cfgPath)
	if daemonMode {
		modeDesc = fmt.Sprintf("daemon: resident `gns daemon -c %s` (cadence = sync_interval config)", cfgPath)
	}
	// platform-aware summary: launchd on macOS, systemd/cron on Linux
	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("installed launchd agent %s\n", label)
		fmt.Printf("  plist: %s\n", opts.PlistPath())
		fmt.Printf("  mode:  %s\n", modeDesc)
		fmt.Printf("  logs:  %s.log\n", opts.LogDir+"/"+label)
		fmt.Println("verify:  launchctl list | grep " + label)
		fmt.Println("logs:    gns logs [-label " + label + "]")
	case "linux":
		fmt.Printf("installed gns scheduler %s\n", label)
		fmt.Printf("  mode:  %s\n", modeDesc)
		if cronMode {
			fmt.Println("  backend: crontab (managed marker block)")
			fmt.Printf("  crontab: crontab -l | grep gns-sync\n")
			fmt.Printf("  logs:    %s\n", opts.LogDir+"/"+label+".log")
			if daemonMode {
				fmt.Println("  note: cron @reboot has no keep-alive — a crash won't restart;")
				fmt.Println("        systemd daemon mode (no --cron) uses Restart=always")
			}
		} else {
			fmt.Println("  backend: systemd user units")
			fmt.Printf("  units:   ~/.config/systemd/user/%s.{service,timer}\n", label)
			fmt.Printf("  logs:    journalctl --user -u %s\n", label)
		}
		fmt.Println("verify:  systemctl --user list-timers | grep " + label)
		fmt.Println("logs:    gns logs [-label " + label + "]")
	case "windows":
		fmt.Printf("installed gns scheduler %s\n", label)
		fmt.Printf("  mode:  %s\n", modeDesc)
		fmt.Printf("  task:  %s (Task Scheduler, no admin needed)\n", label)
		fmt.Printf("  logs:  %s\\%s.log\n", opts.LogDir, label)
		fmt.Println("verify:  schtasks /Query /TN \"" + label + "\"")
		fmt.Println("logs:    gns logs [-label " + label + "]")
		if daemonMode {
			fmt.Println("  note: Task Scheduler has no keep-alive — a crash won't restart;")
			fmt.Println("        the task runs once per logon, keep `gns daemon` alive via other means")
		}
	default:
		fmt.Printf("installed gns scheduler %s\n", label)
	}
	fmt.Println("remove:  gns uninstall")

	// environment preflight: surface launchd-specific risks (credentials,
	// empty repo list, TCC-protected paths) right after a successful install.
	paths := []string{}
	if cfg, err := config.Load(cfgPath, ""); err != nil {
		fmt.Printf("warn: cannot read global config (%v) — preflight repo checks skipped\n", err)
	} else {
		for _, r := range cfg.Repos.All() {
			paths = append(paths, r.ExpandedPath())
		}
	}
	if len(paths) == 0 {
		fmt.Println("warn: no repos in global config — `gns sync-all` will fail until you add one (gns repos add <path>)")
	}
	for _, w := range service.Preflight(home, paths) {
		fmt.Println(w)
	}
	return nil
}

// cmdUninstall stops and removes the launchd LaunchAgent / systemd unit /
// crontab block.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	var label string
	fs.StringVar(&label, "label", "com.git-notes-sync", "launchd label / systemd unit name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: gns uninstall [-label s]")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	opts := service.LaunchOptions{Label: label, Home: home}
	if err := service.Uninstall(opts); err != nil {
		return err
	}
	if service.Loaded(opts) {
		fmt.Println("note: agent is still registered — check the platform output above")
	}
	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("uninstalled launchd agent %s\n", label)
	case "linux":
		fmt.Printf("uninstalled gns scheduler %s (systemd units / crontab block removed)\n", label)
	case "windows":
		fmt.Printf("uninstalled gns scheduler %s (Task Scheduler task removed)\n", label)
	default:
		fmt.Printf("uninstalled %s\n", label)
	}
	return nil
}

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	var cfgPath string
	var once bool
	fs.StringVar(&cfgPath, "c", "", "config file path")
	fs.BoolVar(&once, "once", false, "run a single tick then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfgPath == "" {
		cfgPath = config.GlobalPath()
	}
	fmt.Printf("%sdaemon started (config: %s, ctrl-c to stop)\n", logStamp(), cfgPath)
	return daemon.Run(cfgPath, once)
}
