// mapcmd.go implements `gns map` (`gnm`) and `gns map-config`
// (`gnm config`) — see doc/git-notes-sync_map.md.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/aweyonhub/git-notes-sync/internal/config"
	"github.com/aweyonhub/git-notes-sync/internal/mapsync"
)

const mapUsage = `gns map — map local files into a dedicated git repo (alias: gnm)

usage:
  gnm init                    create the machine worktree and apply mappings
  gnm status                  state, mapping facts and next commands
  gnm add <path|pattern...> | -A   select the LOCAL version and stage it
  gnm get <path|pattern...> | -A   select the HEAD version and deploy it
  gnm commit [-m <message>]   commit staged content only
  gnm pull [-f | --force]     rebase machine onto git-root (files untouched)
  gnm push                    manual confirm entry; arms .syncable
  gnm sync                    automatic entry; requires .syncable

flags:
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

// loadUserConfig loads the map user config, tolerating a not-yet-created
// file (first run: defaults + the path for later SetKey persistence).
// Returns the merged config and the effective file path.
func loadUserConfig(cfgPath string) (*config.Config, string, error) {
	p := resolveCfgPath(cfgPath)
	if _, err := os.Stat(p); err != nil {
		return config.Defaults(), p, nil
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
		env, err := loadMapEnv(cfgPath)
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
		commonValueFlags := map[string]bool{"c": true, "m": true, "message": true}
		fs.StringVar(&cfgPath, "c", "", "config file path")
		fs.StringVar(&msg, "m", "", "commit message")
		fs.StringVar(&msg, "message", "", "commit message (long form)")
		if err := fs.Parse(normalizeArgs(rest, commonValueFlags)); err != nil {
			return err
		}
		if msg != "" && len(fs.Args()) > 0 {
			return errors.New("usage: gnm commit [-m <message>]")
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
		env, err := loadMapEnv(cfgPath)
		if err != nil {
			return err
		}
		return mapsync.Sync(env)

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
		env := initializedEnvOrNil(p, cfg)
		return mapsync.AddItem(p, cfg, scope, scopeVal, args[0], env)

	case "remove":
		var cfgPath string
		removeAll := hasBoolFlag(rest, "A") || hasBoolFlag(rest, "all")
		args, err := parseKVArgs(rest, map[string]*string{"c": &cfgPath})
		if err != nil {
			return err
		}
		if !removeAll && len(args) == 0 {
			return errors.New("usage: gnm config remove <local-path...> | -A")
		}
		cfg, p, err := loadUserConfig(cfgPath)
		if err != nil {
			return err
		}
		env := initializedEnvOrNil(p, cfg)
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
		// effective map-root: CLI arg wins over the configured one (§4.6);
		// the snapshot content is always the current user [map] section
		if len(args) == 1 && args[0] != env.MapRoot {
			snapEnv := *env
			snapEnv.MapRoot = args[0]
			snapEnv.Worktree = mapsync.WorktreeDir(args[0])
			if !mapsync.IsInitialized(&snapEnv) {
				return fmt.Errorf("worktree for %q not initialized", args[0])
			}
			return mapsync.SaveSnapshot(&snapEnv)
		}
		if !mapsync.IsInitialized(env) {
			fmt.Println("worktree not initialized yet; the snapshot will be written by `gnm init`")
			return nil
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
		if rerr == nil && mapsync.IsInitialized(env) {
			return errors.New("worktree already initialized; use `gnm config add/remove` instead")
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
func initializedEnvOrNil(cfgPath string, cfg *config.Config) *mapsync.Env {
	env, err := mapsync.ResolveEnv(cfg, cfgPath, mapLogf)
	if err != nil || !mapsync.IsInitialized(env) {
		return nil
	}
	return env
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
			*used = key
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

// hasBoolFlag reports whether a boolean-style flag appears anywhere.
func hasBoolFlag(args []string, name string) bool {
	for _, a := range args {
		if trimDashes(a) == name {
			return true
		}
	}
	return false
}
