// Package cli implements the `gns` command line interface.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/ai"
	"github.com/aweyonhub/git-notes-sync/internal/commit"
	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/daemon"
	reposPkg "github.com/aweyonhub/git-notes-sync/internal/repos"
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
  gns daemon [flags]      run the lightweight timer daemon
  gns version

(alias: notes-sync)

flags (per command):
  -c path      config file (default: global config + ./.notes-sync.toml)
  -p path      target repository (default: current directory)

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

commit flags:
  -force       ignore debounce / max_wait timing
  -message s   custom commit message (overrides configured mode)

daemon flags:
  -once        run a single tick then exit
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
	case "daemon":
		return cmdDaemon(rest)
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
		fmt.Printf("  %s\n", fmt.Sprintf(f, a...))
	})
	for _, s := range rep.Steps {
		fmt.Println(" " + s)
	}
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
		fmt.Printf("[%s] %s\n", disp, path)
		rep := sync.Sync(path, cfg, func(f string, a ...any) {
			fmt.Printf("  %s\n", fmt.Sprintf(f, a...))
		})
		for _, s := range rep.Steps {
			fmt.Println(" " + s)
		}
		if rep.Err != nil {
			fmt.Printf("[%s] ERROR: %v\n", disp, rep.Err)
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
	var force bool
	commonFlags(fs, &cfgPath, &repo)
	fs.BoolVar(&force, "force", false, "ignore debounce / max_wait timing")
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
	made, err := cm.CommitNow(forcedMode)
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

// normalizeArgs moves flags to the front so flag.FlagSet can parse them,
// allowing positional args (e.g. a repo name) to appear anywhere:
//
//	gns sync notes -c file   →   gns sync -c file notes
//
// valueFlags lists flags that consume the next argument.
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
	fmt.Printf("daemon started (config: %s, ctrl-c to stop)\n", cfgPath)
	return daemon.Run(cfgPath, once)
}
