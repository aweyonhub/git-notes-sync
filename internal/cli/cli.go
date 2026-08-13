// Package cli implements the `gns` command line interface.
package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/git-notes-sync/git-notes-sync/internal/ai"
	"github.com/git-notes-sync/git-notes-sync/internal/commit"
	"github.com/git-notes-sync/git-notes-sync/internal/config"
	"github.com/git-notes-sync/git-notes-sync/internal/daemon"
	"github.com/git-notes-sync/git-notes-sync/internal/sync"
	"github.com/git-notes-sync/git-notes-sync/internal/version"
)

const usageText = `git-notes-sync %s — auto-sync for notes-style git workspaces

usage:
  gns sync [flags]        sync repo: commit → fetch → merge → push
  gns commit [flags]      commit current changes immediately
  gns commit-ai [flags]   commit with an AI-generated message
  gns status [flags]      show worktree / remote / conflict status
  gns resolve [flags]     list or resolve persisted conflict markers
  gns daemon [flags]      run the lightweight timer daemon
  gns version

(alias: notes-sync)

flags (per command):
  -c path      config file (default: global config + ./.notes-sync.toml)
  -repo path   target repository (default: current directory)

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
	case "commit":
		return cmdCommit(rest, "")
	case "commit-ai":
		return cmdCommit(rest, config.MessageAI)
	case "status":
		return cmdStatus(rest)
	case "resolve":
		return cmdResolve(rest)
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
	fs.StringVar(repo, "repo", "", "target repository (default: current directory)")
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

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	var cfgPath, repo string
	commonFlags(fs, &cfgPath, &repo)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := repoDir(repo)
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

func cmdCommit(args []string, forcedMode string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	var cfgPath, repo, message string
	var force bool
	commonFlags(fs, &cfgPath, &repo)
	fs.BoolVar(&force, "force", false, "ignore debounce / max_wait timing")
	fs.StringVar(&message, "message", "", "custom commit message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := repoDir(repo)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := repoDir(repo)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := repoDir(repo)
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
	gen := ai.NewGenerator(&cfg.AI)
	n, err := sync.Resolve(dir, mode, cfg, gen, func(f string, a ...any) {
		fmt.Printf("  %s\n", fmt.Sprintf(f, a...))
	})
	if err != nil {
		return err
	}
	fmt.Printf("resolved %d file(s)\n", n)
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
	fmt.Printf("daemon started (config: %s, ctrl-c to stop)\n", cfgPath)
	return daemon.Run(cfgPath, once)
}
