// mapcmd.go implements `gns map` (`gnm`) and `gns map-config`
// (`gnm config`) — see doc/git-notes-sync_map.md.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/lock"
	"github.com/aweyonhub/git-notes-sync/internal/mapsync"
)

const mapUsage = `gns map — map local files into a dedicated git repo (alias: gnm)

usage:
  gnm init                    create the machine worktree and apply mappings
  gnm status                  state, mapping facts and next commands
  gnm add <path|pattern...> | -A   select the LOCAL version and stage it
  gnm get <path|pattern...> | -A   select the HEAD version and deploy it
  gnm commit [-m <message>]   commit staged content only
  gnm pull [-f | --force]     move the machine baseline to git-root (files untouched)
  gnm push                    manual confirm entry; arms .syncable
  gnm sync                    automatic entry; requires .syncable
  gnm cd [-p|--path] <worktree|git-root>  print a copyable cd command (path only with -p)

flags:
  -c path      config file (default: global config)
`

const mapCdUsage = `gnm cd — print a platform-ready directory command or its target path

usage:
  gnm cd <worktree|git-root>             print a command to copy and run (Windows: pushd)
  gnm cd -p <worktree|git-root>          print the absolute path only
  gnm cd --path <worktree|git-root>      print the absolute path only

flags:
  -p, --path   print the directory path only
  -c path      config file (default: global config)
`

const mapConfigUsage = `gns map-config — configure gns map mappings (alias: gnm config)

usage:
  gnm config git-root <path>          point at the integration repo
  gnm config map-root <name>          this machine's namespace
  gnm config add -a <repo> <local>    scope = map-root (machine namespace)
  gnm config add -A <repo> <local>    scope = git-root (shared)
  gnm config remove <local...>        unmap by exact local path
  gnm config remove -A | --all        unmap everything
  gnm config list                     show effective [map] section
  gnm config validate                 report problems
  gnm config save [<map-root>]        write snapshot into the worktree
  gnm config load [<map-root>]        import snapshot from git-root (pre-init)

flags:
  -c path      config file (default: global config)
`

// mapLogf prints prefixed progress lines using the shared log stamp.
func mapLogf(f string, a ...any) {
	fmt.Printf("%s  %s\n", logStamp(), fmt.Sprintf(f, a...))
}

// shellQuoteCdArg quotes an argument for the platform's usual interactive
// shell. Both POSIX shells and PowerShell use single quotes for literal text,
// but escape an embedded single quote differently.
func shellQuoteCdArg(s string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func mapCdCommand(target, targetPath, cfgPath string) string {
	// CMD has no $(...) command substitution. pushd with a quoted absolute
	// path works in both CMD and PowerShell and also changes drive letters.
	if runtime.GOOS == "windows" {
		return `pushd "` + targetPath + `"`
	}
	args := []string{"gnm", "cd", "-p"}
	if cfgPath != "" {
		args = append(args, "-c", shellQuoteCdArg(cfgPath))
	}
	args = append(args, target)
	return `cd "$(` + strings.Join(args, " ") + `)"`
}

// loadUserConfig loads the map user config, tolerating a not-yet-created
// file (first run: defaults + the path for later SetKey persistence).
// Returns the merged config and the effective file path.
func loadUserConfig(cfgPath string) (*config.Config, string, error) {
	p := resolveCfgPath(cfgPath)
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return config.Defaults(), p, nil
		}
		return nil, p, err
	}
	cfg, err := config.Load(p, "")
	if err != nil {
		return nil, p, err
	}
	return cfg, p, nil
}

// loadMapEnv loads the user config and resolves the map environment.
func loadMapEnv(cfgPath string) (*mapsync.Env, error) {
	cfg, p, err := loadUserConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	return mapsync.ResolveEnv(cfg, p, mapLogf)
}

// cmdMap dispatches `gns map` / `gnm`.
func cmdMap(args []string) error {
	if len(args) == 0 {
		fmt.Printf(mapUsage)
		return nil
	}
	// `gns map config …` forwards to map-config so both spellings work
	if args[0] == "config" {
		return cmdMapConfig(args[1:])
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "init":
		fs := flag.NewFlagSet("map init", flag.ContinueOnError)
		var cfgPath string
		fs.StringVar(&cfgPath, "c", "", "config file path")
		if err := fs.Parse(normalizeArgs(rest, map[string]bool{"c": true})); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errors.New("gnm <sub> takes no arguments")
		}
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		return mapsync.Init(env)

	case "status":
		fs := flag.NewFlagSet("map status", flag.ContinueOnError)
		var cfgPath string
		fs.StringVar(&cfgPath, "c", "", "config file path")
		if err := fs.Parse(normalizeArgs(rest, map[string]bool{"c": true})); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errors.New("gnm <sub> takes no arguments")
		}
		cfg, p, err := loadUserConfig(cfgPath)
		if err != nil {
			return err
		}
		if cfg.Map.GitRoot == "" || cfg.Map.MapRoot == "" {
			fmt.Print(mapsync.StatusUninitialized(cfg))
			return nil
		}
		env, err := mapsync.ResolveEnv(cfg, p, mapLogf)
		if err != nil {
			return err
		}
		out, err := mapsync.Status(env)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil

	case "add", "get":
		fs := flag.NewFlagSet("map "+sub, flag.ContinueOnError)
		var cfgPath string
		all := fs.Bool("A", false, "select every mapping")
		fs.BoolVar(all, "all", false, "-A long form")
		fs.StringVar(&cfgPath, "c", "", "config file path")
		if err := fs.Parse(normalizeArgs(rest, map[string]bool{"c": true})); err != nil {
			return err
		}
		if *all && len(fs.Args()) > 0 {
			return errors.New("usage: gnm " + sub + " <path|pattern...> | -A")
		}
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		if sub == "add" {
			return mapsync.Add(env, fs.Args(), *all)
		}
		return mapsync.Get(env, fs.Args(), *all)

	case "commit":
		fs := flag.NewFlagSet("map commit", flag.ContinueOnError)
		var cfgPath, msg string
		// sentinel distinguishes an omitted -m (use the default) from an
		// explicitly empty value, which the design rejects (spec §6.6)
		const msgUnset = "\x00unset"
		commonValueFlags := map[string]bool{"c": true, "m": true, "message": true}
		fs.StringVar(&cfgPath, "c", "", "config file path")
		fs.StringVar(&msg, "m", msgUnset, "commit message")
		fs.StringVar(&msg, "message", msgUnset, "commit message (long form)")
		if err := fs.Parse(normalizeArgs(rest, commonValueFlags)); err != nil {
			return err
		}
		if len(fs.Args()) > 0 {
			return errors.New("usage: gnm commit [-m <message>]")
		}
		if msg == "" {
			return errors.New("map commit: message must not be empty (omit -m for the default)")
		}
		if msg == msgUnset {
			msg = ""
		}
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		return mapsync.Commit(env, msg)

	case "pull":
		fs := flag.NewFlagSet("map pull", flag.ContinueOnError)
		var cfgPath string
		force := fs.Bool("f", false, "force-align git-root to its upstream")
		fs.BoolVar(force, "force", false, "-f long form")
		fs.StringVar(&cfgPath, "c", "", "config file path")
		if err := fs.Parse(normalizeArgs(rest, map[string]bool{"c": true})); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errors.New("gnm <sub> takes no arguments")
		}
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		return mapsync.Pull(env, *force)

	case "push":
		fs := flag.NewFlagSet("map push", flag.ContinueOnError)
		var cfgPath string
		fs.StringVar(&cfgPath, "c", "", "config file path")
		if err := fs.Parse(normalizeArgs(rest, map[string]bool{"c": true})); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errors.New("gnm <sub> takes no arguments")
		}
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		return mapsync.Push(env)

	case "sync":
		fs := flag.NewFlagSet("map sync", flag.ContinueOnError)
		var cfgPath string
		fs.StringVar(&cfgPath, "c", "", "config file path")
		if err := fs.Parse(normalizeArgs(rest, map[string]bool{"c": true})); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errors.New("gnm <sub> takes no arguments")
		}
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		return mapsync.Sync(env)

	case "cd":
		fs := flag.NewFlagSet("map cd", flag.ContinueOnError)
		var cfgPath string
		var pathOnly bool
		fs.SetOutput(os.Stdout)
		fs.Usage = func() { fmt.Fprint(fs.Output(), mapCdUsage) }
		fs.StringVar(&cfgPath, "c", "", "config file path")
		fs.BoolVar(&pathOnly, "p", false, "print the directory path only")
		fs.BoolVar(&pathOnly, "path", false, "print the directory path only")
		if err := fs.Parse(normalizeArgs(rest, map[string]bool{"c": true})); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: gnm cd [-p|--path] <worktree|git-root>")
		}
		target := fs.Arg(0)
		if target != "worktree" && target != "git-root" {
			return errors.New("usage: gnm cd [-p|--path] <worktree|git-root>")
		}
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		var targetPath string
		switch target {
		case "worktree":
			targetPath = env.Worktree
		case "git-root":
			targetPath = env.GitRoot
		}
		if pathOnly {
			fmt.Println(targetPath)
		} else {
			fmt.Println(mapCdCommand(target, targetPath, cfgPath))
		}
		return nil

	case "help", "-h", "--help":
		fmt.Printf(mapUsage)
		return nil
	default:
		return fmt.Errorf("unknown map command %q (see: gnm help)", sub)
	}
}

// cmdMapConfig dispatches `gns map-config` / `gnm config`.
func cmdMapConfig(args []string) error {
	if len(args) == 0 {
		fmt.Printf(mapConfigUsage)
		return nil
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "git-root", "map-root":
		var cfgPath string
		args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: gnm config %s <value> [-c config]", sub)
		}
		key := "git_root"
		if sub == "map-root" {
			if err := mapsync.ValidMapRoot(args[0]); err != nil {
				return err
			}
			key = "map_root"
		}
		cfgPath2 := resolveCfgPath(cfgPath)
		if err := guardMapBaseChange(cfgPath2, key, args[0], false); err != nil {
			return err
		}
		if err := config.SetKey(cfgPath2, "map", key, args[0]); err != nil {
			return err
		}
		fmt.Printf("set map.%s = %s\n", key, args[0])
		return nil

	case "add":
		var cfgPath, scopeVal string
		given := ""
		args, err := parseKVArgsMulti(rest, map[string]*string{
			"a": &scopeVal,
			"A": &scopeVal,
			"c": &cfgPath,
		}, &given)
		if err != nil {
			return err
		}
		if scopeVal == "" || len(args) != 1 {
			return errors.New("usage: gnm config add -a <repo-path> <local-path> | -A <repo-path> <local-path>")
		}
		scope := config.ScopeMapRoot
		if given == "A" {
			scope = config.ScopeGitRoot
		}
		cfg, p, err := loadUserConfig(cfgPath)
		if err != nil {
			return err
		}
		env, err := initializedEnvOrNil(p, cfg)
		if err != nil {
			return err
		}
		return mapsync.AddItem(p, cfg, scope, scopeVal, args[0], env)

	case "remove":
		var cfgPath string
		filtered, removeAll := stripMapRemoveAll(rest)
		args, err := parseKVArgs(filtered, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if !removeAll && len(args) == 0 {
			return errors.New("usage: gnm config remove <local-path...> | -A")
		}
		if removeAll && len(args) != 0 {
			return errors.New("usage: gnm config remove <local-path...> | -A")
		}
		cfg, p, err := loadUserConfig(cfgPath)
		if err != nil {
			return err
		}
		env, err := initializedEnvOrNil(p, cfg)
		if err != nil {
			return err
		}
		return mapsync.RemoveItems(p, cfg, args, removeAll, env)

	case "list":
		var cfgPath string
		args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if len(args) > 0 {
			return errors.New("usage: gnm config list [-c config]")
		}
		cfg, _, err := loadUserConfig(cfgPath)
		if err != nil {
			return err
		}
		fmt.Print(mapsync.ListItems(cfg))
		return nil

	case "validate":
		var cfgPath string
		args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if len(args) > 0 {
			return errors.New("usage: gnm config validate [-c config]")
		}
		cfg, _, err := loadUserConfig(cfgPath)
		if err != nil {
			return err
		}
		errs := mapsync.ValidateReport(cfg)
		if len(errs) == 0 {
			fmt.Println("map config ok")
			return nil
		}
		for _, e := range errs {
			fmt.Println("error:", e)
		}
		return errors.New("map config has problems")

	case "save":
		var cfgPath string
		args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if len(args) > 1 {
			return errors.New("usage: gnm config save [<map-root>] [-c config]")
		}
		env, rerr := loadMapEnv(cfgPath)
		if rerr != nil {
			return rerr
		}
		// The initialization gate must come before the optional map-root
		// argument is honored: writing a snapshot for an uninitialized
		// worktree would create an orphan .gns tree that later blocks init.
		initd, ierr := mapsync.IsInitialized(env)
		if ierr != nil {
			fmt.Printf("warn: inspect worktree: %v\n", ierr)
			fmt.Println("worktree not initialized; no snapshot written now — `gnm init` will create the initial snapshot")
			return nil
		}
		if !initd {
			fmt.Println("worktree not initialized; no snapshot written now — `gnm init` will create the initial snapshot")
			return nil
		}
		if err := os.MkdirAll(env.State, 0o755); err != nil {
			return err
		}
		unlock, uerr := lock.Acquire(env.State)
		if uerr != nil {
			return fmt.Errorf("map: %w", uerr)
		}
		defer unlock()
		// effective map-root: CLI arg wins over the configured one (§4.6);
		// the snapshot content is always the current user [map] section
		if len(args) == 1 && args[0] != env.MapRoot {
			if err := mapsync.ValidMapRoot(args[0]); err != nil {
				return err
			}
			snapEnv := *env
			snapEnv.MapRoot = args[0]
			return mapsync.SaveSnapshot(&snapEnv)
		}
		return mapsync.SaveSnapshot(env)

	case "load":
		var cfgPath string
		args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if len(args) > 1 {
			return errors.New("usage: gnm config load [<map-root>] [-c config]")
		}
		cfg, p, err := loadUserConfig(cfgPath)
		if err != nil {
			return err
		}
		env, rerr := mapsync.ResolveEnv(safeCfg(cfg), p, mapLogf)
		if rerr == nil {
			initd, ierr := mapsync.IsInitialized(env)
			if ierr != nil {
				return fmt.Errorf("map: inspect worktree: %w (resolve it before loading a snapshot)", ierr)
			}
			if initd {
				return errors.New("worktree already initialized; use `gnm config add/remove` instead")
			}
		} else {
			// ResolveEnv failed. Plan B: only tolerate "nothing configured yet"
			// (git_root or map_root empty). If both are set, still check
			// worktree ownership independently — an initialized machine with
			// bad items must not be overwritten by load.
			if cfg.Map.GitRoot != "" && cfg.Map.MapRoot != "" {
				mr := cfg.Map.MapRoot
				if len(args) == 1 {
					mr = args[0]
				}
				owned, oerr := mapsync.WorktreeOwnedBy(mr, mapsync.NormalizeLocal(cfg.Map.GitRoot), cfg)
				if oerr != nil {
					return fmt.Errorf("map: %w (resolve it before loading a snapshot)", oerr)
				}
				if owned {
					return errors.New("worktree already initialized; use `gnm config add/remove` instead")
				}
				// Not owned — allow load to overwrite the bad config.
			}
		}
		var mr string
		if len(args) == 1 {
			mr = args[0]
		}
		return mapsync.LoadSnapshot(p, cfg, mr, mapLogf)

	case "help", "-h", "--help":
		fmt.Printf(mapConfigUsage)
		return nil
	default:
		return fmt.Errorf("unknown map-config command %q (see: gnm config help)", sub)
	}
}

// safeCfg guards ResolveEnv against configs whose map section is incomplete
// (load must stay usable before any map config exists).
func safeCfg(cfg *config.Config) *config.Config {
	c := config.Defaults()
	c.Map = cfg.Map
	return c
}

// initializedEnvOrNil resolves env only when the machine worktree exists,
// mirroring spec §4.1: pre-init edits touch config alone.
func initializedEnvOrNil(cfgPath string, cfg *config.Config) (*mapsync.Env, error) {
	env, err := mapsync.ResolveEnv(cfg, cfgPath, mapLogf)
	if err != nil {
		if cfg.Map.MapRoot == "" || cfg.Map.GitRoot == "" {
			return nil, nil
		}
		return nil, err
	}
	initd, ierr := mapsync.IsInitialized(env)
	if ierr != nil {
		// broken debris must not silently degrade to "pre-init edits only"
		return nil, ierr
	}
	if !initd {
		return nil, nil
	}
	return env, nil
}

// parseKVArgsMulti is parseKVArgs plus tracking which value-flag was used
// (needed because -a and -A are distinct scopes sharing one target var).
func parseKVArgsMulti(args []string, flags map[string]*string, used *string) ([]string, error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			key := trimDashes(a)
			dst, ok := flags[key]
			if !ok {
				return nil, fmt.Errorf("unknown flag %q", a)
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", a)
			}
			*dst = args[i+1]
			if (key == "a" || key == "A") && *used != "" && *used != key {
				return nil, errors.New("-a and -A are mutually exclusive")
			}
			if key == "a" || key == "A" {
				*used = key
			}
			i++
			continue
		}
		positional = append(positional, a)
	}
	return positional, nil
}

func trimDashes(a string) string {
	for len(a) > 0 && a[0] == '-' {
		a = a[1:]
	}
	return a
}

func stripMapRemoveAll(args []string) ([]string, bool) {
	filtered := make([]string, 0, len(args))
	all := false
	for _, arg := range args {
		if arg == "-A" || arg == "--all" {
			all = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, all
}
