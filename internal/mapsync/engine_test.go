// engine_test.go: real-git integration coverage of the map lifecycle —
// init → choose → commit → push → sync → conflict → pull → recover,
// plus the .syncable gate semantics (spec §7/§8).
package mapsync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aweyonhub/git-notes-sync/internal/config"
)

// ---------- helpers ----------

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitCmdAllowFail runs git tolerating non-zero exit (conflicted merges,
// refused operations), returning combined output for assertions.
func gitCmdAllowFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func pinGitIdentity(t *testing.T, dir string) {
	t.Helper()
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "core.autocrlf", "false")
	gitCmd(t, dir, "config", "pull.rebase", "false")
}

func mappedLocal(t *testing.T, home string) string {
	t.Helper()
	p := filepath.Join(home, "file.txt")
	writeFile(t, p, "V1\n")
	return p
}

func mappedWtFile(env *Env) string {
	return filepath.Join(env.Worktree, "tm", "dot", "file.txt")
}

func gitRootMappedPath(env *Env) string {
	return filepath.Join(env.GitRoot, "tm", "dot", "file.txt")
}

// newTestEnv builds a bare remote + cloned git-root with an initial commit,
// and an Env in copy mode rooted under a hermetic GNS_APP_DATA. Returns the
// bare remote path too, so failure-path tests can break and repair origin.
func newTestEnv(t *testing.T) (*Env, string, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("GNS_APP_DATA", filepath.Join(tmp, "app"))

	remote := filepath.Join(tmp, "remote.git")
	gitCmd(t, tmp, "init", "--bare", "-b", "main", "remote.git")

	gitRoot := filepath.Join(tmp, "gitroot")
	gitCmd(t, tmp, "-c", "core.autocrlf=false", "clone", "remote.git", "gitroot")
	pinGitIdentity(t, gitRoot)
	writeFile(t, filepath.Join(gitRoot, "README.md"), "# map repo\n")
	gitCmd(t, gitRoot, "add", "-A")
	gitCmd(t, gitRoot, "commit", "-m", "init")
	gitCmd(t, gitRoot, "push", "-u", "origin", "main")

	cfg := config.Defaults()
	cfg.RetryAttempts = 1 // keep failure-path tests fast
	cfg.Map.GitRoot = gitRoot
	cfg.Map.MapRoot = "tm"
	cfg.Map.Mode = "copy" // link mode has its own opt-in test

	env, err := ResolveEnv(cfg, filepath.Join(tmp, "user-config.toml"), func(f string, a ...any) { t.Logf(f, a...) })
	if err != nil {
		t.Fatal(err)
	}
	return env, remote, filepath.Join(tmp, "home")
}

// setupMapped builds an Env with one map-root scoped mapping and runs Init.
// The gate stays unarmed; callers drive the confirm flow themselves. The
// bare remote path is returned for peers pushing concurrent changes.
func setupMapped(t *testing.T, mode string) (env *Env, remote, local, home string) {
	t.Helper()
	env, remote, home = newTestEnv(t)
	if mode != "" {
		env.Mode = ResolveMode(mode)
		env.Cfg.Map.Mode = mode
	}
	local = mappedLocal(t, home)
	env.Cfg.Map.Items = []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "dot/file.txt", LocalPath: local},
	}
	if err := Init(env); err != nil {
		t.Fatalf("init: %v", err)
	}
	return env, remote, local, home
}

// peerClone creates a second machine from the BARE remote (never from the
// non-bare git-root: pushing into its checked-out branch is rejected).
func peerClone(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	peer := filepath.Join(dir, "peer")
	gitCmd(t, dir, "-c", "core.autocrlf=false", "clone", remote, "peer")
	pinGitIdentity(t, peer)
	return peer
}

// armGate runs the manual confirm flow: `add -A` chooses local content and
// also stages the .gns snapshot init wrote; commit; push arms the gate.
func armGate(t *testing.T, env *Env) {
	t.Helper()
	if err := Add(env, nil, true); err != nil {
		t.Fatalf("add -A: %v", err)
	}
	if err := Commit(env, ""); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := Push(env); err != nil {
		t.Fatalf("push: %v", err)
	}
	if !HasSyncable(env) {
		t.Fatal(".syncable missing after a fully successful push")
	}
}

// ---------- tests ----------

func TestInitPublishesLocalThenPushSyncRoundtrip(t *testing.T) {
	env, _, local, _ := setupMapped(t, "")

	// init published the local version into the worktree; gate stays off
	if got := readFile(t, mappedWtFile(env)); got != "V1\n" {
		t.Fatalf("worktree after init = %q", got)
	}
	if HasSyncable(env) {
		t.Fatal(".syncable created by init — spec forbids it")
	}

	armGate(t, env)
	if got := readFile(t, gitRootMappedPath(env)); got != "V1\n" {
		t.Fatalf("git-root after push = %q", got)
	}

	// automatic round: edit locally → sync converges remote, keeps the gate
	writeFile(t, local, "V2\n")
	if err := Sync(env); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := readFile(t, gitRootMappedPath(env)); got != "V2\n" {
		t.Fatalf("git-root after sync = %q", got)
	}
	if !HasSyncable(env) {
		t.Fatal(".syncable lost after successful sync")
	}
	if b, _ := ReadBlocked(env); b != nil {
		t.Fatalf("blocked state left behind: %+v", b)
	}
}

func TestAddAllConfirmsMissingLocalRootDeletion(t *testing.T) {
	env, _, local, _ := setupMapped(t, "")
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := Add(env, nil, true); err != nil {
		t.Fatalf("add -A: %v", err)
	}
	if _, err := os.Lstat(mappedWtFile(env)); !os.IsNotExist(err) {
		t.Fatalf("add -A did not remove worktree content: %v", err)
	}
	staged, err := env.wtRunner().HasStaged()
	if err != nil {
		t.Fatal(err)
	}
	if !staged {
		t.Fatal("add -A did not stage the confirmed deletion")
	}
}

func TestPullRejectsDirtyGitRoot(t *testing.T) {
	env, _, _, _ := setupMapped(t, "")
	writeFile(t, filepath.Join(env.GitRoot, "uncommitted.txt"), "dirty\n")
	if err := Pull(env, false); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty git-root pull error = %v", err)
	}
}

func TestGetDirectoryRemovesFilesAbsentFromHead(t *testing.T) {
	env, _, home := newTestEnv(t)
	localDir := filepath.Join(home, "mapped")
	writeFile(t, filepath.Join(localDir, "kept.txt"), "kept")
	env.Cfg.Map.Items = []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "dot", LocalPath: localDir},
	}
	if err := Init(env); err != nil {
		t.Fatal(err)
	}
	armGate(t, env)
	wtDir := filepath.Join(env.Worktree, "tm", "dot")
	writeFile(t, filepath.Join(localDir, "extra.txt"), "extra")
	writeFile(t, filepath.Join(wtDir, "extra.txt"), "extra")

	if err := Get(env, []string{localDir}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("get kept a local file absent from HEAD")
	}
	if entries, err := env.wtRunner().Status(); err != nil || len(entries) != 0 {
		t.Fatalf("get left worktree changes: %v, %v", entries, err)
	}
}

func TestInitFailureCleansWorktreeAndKeepsLocal(t *testing.T) {
	env, _, home := newTestEnv(t)
	first := filepath.Join(home, "first.txt")
	writeFile(t, first, "keep")
	writeFile(t, filepath.Join(env.GitRoot, "tm", "second", "file.txt"), "repo")
	gitCmd(t, env.GitRoot, "add", "-A")
	gitCmd(t, env.GitRoot, "commit", "-m", "add second")

	blocker := filepath.Join(home, "blocker")
	writeFile(t, blocker, "not a directory")
	env.Cfg.Map.Items = []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "first.txt", LocalPath: first},
		{Scope: config.ScopeMapRoot, Path: "second", LocalPath: filepath.Join(blocker, "child")},
	}
	if err := Init(env); err == nil {
		t.Fatal("init unexpectedly succeeded")
	}
	if got := readFile(t, first); got != "keep" {
		t.Fatalf("first local changed: %q", got)
	}
	if _, err := os.Stat(env.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree survived failed init: %v", err)
	}
	if env.gitRunner().BranchExists(BranchName(env.MapRoot)) {
		t.Fatal("machine branch survived failed init")
	}
}

func TestInitializedRequiresExpectedWorktreeBranch(t *testing.T) {
	env, _, _, _ := setupMapped(t, "")
	gitCmd(t, env.Worktree, "switch", "-c", "other")
	initd, ierr := IsInitialized(env)
	if initd || ierr == nil || !strings.Contains(ierr.Error(), "unexpected branch") {
		t.Fatalf("wrong branch must report broken, got initd=%v err=%v", initd, ierr)
	}
}

func TestSyncConflictBlocksAndRecoveryFlow(t *testing.T) {
	env, remote, local, _ := setupMapped(t, "")
	armGate(t, env)

	// another machine moves the same content forward
	peer := peerClone(t, remote)
	writeFile(t, filepath.Join(peer, "tm", "dot", "file.txt"), "B-remote\n")
	gitCmd(t, peer, "add", "-A")
	gitCmd(t, peer, "commit", "-m", "b change")
	gitCmd(t, peer, "push")

	// local diverges on the same lines
	writeFile(t, local, "C-local\n")

	err := Sync(env)
	if err == nil || !strings.Contains(err.Error(), "merge conflict") {
		t.Fatalf("expected merge conflict error, got %v", err)
	}
	if HasSyncable(env) {
		t.Fatal(".syncable survived a confirmed conflict")
	}
	blocked, rerr := ReadBlocked(env)
	if rerr != nil || blocked == nil || blocked.Reason != "merge-conflict" || len(blocked.Conflicts) == 0 {
		t.Fatalf("blocked record missing/incomplete: %+v, %v", blocked, rerr)
	}
	// worktree restored to its committed pre-merge content; the real file
	// was never touched by the failed merge
	for _, p := range []string{mappedWtFile(env), local} {
		if got := readFile(t, p); got != "C-local\n" {
			t.Fatalf("pre-merge restore failed for %s: %q", p, got)
		}
	}

	// recovery: pull re-bases onto git-root without touching any working file
	if err := Pull(env, false); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got := readFile(t, local); got != "C-local\n" {
		t.Fatalf("pull touched the real file: %q", got)
	}

	// get adopts HEAD (the peer's version), deploying it everywhere; commit
	// becomes a harmless no-op when get already converged index and worktree
	if err := Get(env, []string{local}, false); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := readFile(t, local); got != "B-remote\n" {
		t.Fatalf("get did not deploy HEAD to local: %q", got)
	}
	if err := Commit(env, "resolve map conflict"); err != nil {
		t.Fatal(err)
	}
	if err := Push(env); err != nil {
		t.Fatalf("push after recovery: %v", err)
	}
	if !HasSyncable(env) {
		t.Fatal(".syncable not re-created after recovery push")
	}
	if got := readFile(t, gitRootMappedPath(env)); got != "B-remote\n" {
		t.Fatalf("git-root after recovery = %q", got)
	}
}

func TestAddConfirmsDeletionAndGetRestoresFromHead(t *testing.T) {
	env, _, local, _ := setupMapped(t, "")
	armGate(t, env)

	// phase A: delete locally, add stages it — but get can still restore
	// from HEAD before the deletion is committed
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := Add(env, []string{local}, false); err != nil {
		t.Fatalf("add deletion: %v", err)
	}
	if _, err := os.Lstat(mappedWtFile(env)); !os.IsNotExist(err) {
		t.Fatal("add deletion left the worktree copy behind")
	}
	if err := Get(env, []string{local}, false); err != nil {
		t.Fatalf("get restore: %v", err)
	}
	if got := readFile(t, local); got != "V1\n" {
		t.Fatalf("get did not resurrect the local file: %q", got)
	}
	if got := readFile(t, mappedWtFile(env)); got != "V1\n" {
		t.Fatalf("get did not restore the worktree copy: %q", got)
	}

	// phase B: this time carry the deletion through commit + push
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := Add(env, []string{local}, false); err != nil {
		t.Fatal(err)
	}
	if err := Commit(env, "drop mapped file"); err != nil {
		t.Fatal(err)
	}
	if err := Push(env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(gitRootMappedPath(env)); !os.IsNotExist(err) {
		t.Fatal("deletion did not reach git-root")
	}
}

func TestSyncKeepsSyncableOnTransientFailure(t *testing.T) {
	env, remote, home := newTestEnv(t)
	local := mappedLocal(t, home)
	env.Cfg.Map.Items = []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "dot/file.txt", LocalPath: local},
	}
	if err := Init(env); err != nil {
		t.Fatal(err)
	}
	armGate(t, env)

	// break the origin: fetch fails fast and permanently for git — but this
	// is NOT divergence, so §3.2 says the gate survives
	gitCmd(t, env.GitRoot, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "not-a-repo"))
	writeFile(t, local, "V3\n")
	err := Sync(env)
	if err == nil {
		t.Fatal("sync over a broken origin must fail")
	}
	if !HasSyncable(env) {
		t.Fatal(".syncable removed on a non-divergence failure")
	}

	// repair and converge
	gitCmd(t, env.GitRoot, "remote", "set-url", "origin", remote)
	if err := Sync(env); err != nil {
		t.Fatalf("sync after repair: %v", err)
	}
	if !HasSyncable(env) {
		t.Fatal(".syncable lost after repaired sync")
	}
}

func TestPullForceAdoptsRemoteBaseline(t *testing.T) {
	env, remote, local, _ := setupMapped(t, "")
	armGate(t, env)

	// stale local history directly on git-root (as if another tool had
	// committed there outside the map flow)
	writeFile(t, filepath.Join(env.GitRoot, "stray.txt"), "zombie\n")
	gitCmd(t, env.GitRoot, "add", "-A")
	gitCmd(t, env.GitRoot, "commit", "-m", "stray local commit")

	// meanwhile the remote moved via a peer
	peer := peerClone(t, remote)
	writeFile(t, filepath.Join(peer, "tm", "dot", "file.txt"), "Y-remote\n")
	gitCmd(t, peer, "add", "-A")
	gitCmd(t, peer, "commit", "-m", "y change")
	gitCmd(t, peer, "push")

	// The failed ff-only pull is classified from the commit graph, not from
	// Git's localized stderr: both HEAD and upstream now have unique commits.
	diverged, err := pullFFOnly(env.gitRunner(), 1)
	if err == nil || !diverged {
		t.Fatalf("two-sided history split classified as diverged=%v err=%v", diverged, err)
	}

	if err := Pull(env, true); err != nil {
		t.Fatalf("pull --force: %v", err)
	}
	// git-root aligned to the remote baseline; the stray commit left the tip
	if _, err := os.Lstat(filepath.Join(env.GitRoot, "stray.txt")); err == nil {
		t.Fatal("stray file survived force alignment")
	}
	if got := readFile(t, gitRootMappedPath(env)); got != "Y-remote\n" {
		t.Fatalf("force did not adopt remote baseline: %q", got)
	}
	// the real file was untouched by pull --force
	if got := readFile(t, local); got != "V1\n" {
		t.Fatalf("pull --force touched the real file: %q", got)
	}
	// the rewound machine tip stays reachable through the backup ref
	if out := gitCmd(t, env.GitRoot, "rev-parse", "--verify", BackupRef(env.MapRoot)); out == "" {
		t.Fatal("backup ref missing after force pull")
	}

	// choose local content again and converge everything
	writeFile(t, local, "Z-final\n")
	if err := Add(env, []string{local}, false); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := Commit(env, "resolve map divergence"); err != nil {
		t.Fatal(err)
	}
	if err := Push(env); err != nil {
		t.Fatalf("push after force recovery: %v", err)
	}
	if got := readFile(t, gitRootMappedPath(env)); got != "Z-final\n" {
		t.Fatalf("final convergence failed: %q", got)
	}
}

func TestStatusRendersStatesAndHints(t *testing.T) {
	env, _, local, _ := setupMapped(t, "")

	out, err := Status(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MANUAL_REQUIRED", "staged=", "unstaged=", "untracked=", "[missing]", "[TO add]", "gnm add", "gnm push"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
	armGate(t, env)
	out, err = Status(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SYNCABLE", "(map-root) dot/file.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}

	writeFile(t, local, "changed outside the worktree\n")
	out, err = Status(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local-dirty=1", "Next: gnm sync"} {
		if !strings.Contains(out, want) {
			t.Fatalf("copy status missing %q after local edit:\n%s", want, out)
		}
	}
}

func TestMappingConfigChangeDisarmsGate(t *testing.T) {
	env, _, _, home := setupMapped(t, "")

	extra := filepath.Join(home, "extra.sh")
	writeFile(t, extra, "#!/bin/sh\necho hi\n")

	// arm first WITHOUT the extra mapping so the later add is a pure
	// mapping change on top of an armed gate
	armGate(t, env)

	if err := AddItem(env.ConfigPath, env.Cfg, config.ScopeGitRoot, "common/extra.sh", extra, env); err != nil {
		t.Fatalf("AddItem initialized: %v", err)
	}
	if HasSyncable(env) {
		t.Fatal(".syncable survived a mapping change (spec §4.5)")
	}
	// the new mapping was applied immediately: worktree holds the content
	if got := readFile(t, filepath.Join(env.Worktree, "common", "extra.sh")); !strings.Contains(got, "echo hi") {
		t.Fatalf("mapping not applied to worktree: %q", got)
	}

	// remove-all clears every definition, unmaps the machine namespace,
	// disarms the gate, and retires the worktree (spec §4.5: when the last
	// mapping is removed the machine returns to pre-init state).
	if err := RemoveItems(env.ConfigPath, env.Cfg, nil, true, env); err != nil {
		t.Fatalf("RemoveItems all: %v", err)
	}
	if len(env.Cfg.Map.Items) != 0 {
		t.Fatalf("items not cleared: %d", len(env.Cfg.Map.Items))
	}
	if HasSyncable(env) {
		t.Fatal(".syncable survived remove-all")
	}
	if _, err := os.Lstat(mappedWtFile(env)); !os.IsNotExist(err) {
		t.Fatal("map-root scoped content survived remove-all")
	}
	// worktree is retired: the directory is gone
	if _, err := os.Lstat(env.Worktree); !os.IsNotExist(err) {
		t.Fatal("worktree directory survived remove-all (should be retired)")
	}
}

func TestLinkModeLifecycleWhenSupported(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "probe-link")
	if err := os.Symlink(probe, probe+".lnk"); err != nil {
		t.Skip("symlinks unavailable on this platform/account")
	}
	os.Remove(probe + ".lnk")

	env, _, local, _ := setupMapped(t, "link")

	// init replaced the local path with a symlink onto the worktree file
	fi, err := os.Lstat(local)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("local path not converted to symlink: %v", err)
	}
	// edits through the link are instantly visible in the worktree
	writeFile(t, local, "L2\n")
	if got := readFile(t, mappedWtFile(env)); got != "L2\n" {
		t.Fatalf("link does not expose worktree content: %q", got)
	}
	armGate(t, env)

	// removing the mapping materializes a real file back (§4.5)
	if err := RemoveItems(env.ConfigPath, env.Cfg, []string{local}, false, env); err != nil {
		t.Fatalf("remove mapping: %v", err)
	}
	fi, err = os.Lstat(local)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unmap did not materialize a real file: %v", err)
	}
	if got := readFile(t, local); got != "L2\n" {
		t.Fatalf("materialized file lost content: %q", got)
	}
}

func TestSnapshotSaveLoadRoundtrip(t *testing.T) {
	env, _, _, _ := setupMapped(t, "")

	snapPath := filepath.Join(env.Worktree, ".gns", "map", "tm.toml")
	if _, err := os.Stat(snapPath); err != nil {
		t.Fatalf("init snapshot missing: %v", err)
	}

	// publish the snapshot to git-root the way users do: add -A picks up
	// .gns/ together with mapped content (spec §6.5)
	armGate(t, env)

	fresh := filepath.Join(t.TempDir(), "fresh.toml")
	if err := os.WriteFile(fresh, []byte("# fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	freshCfg := config.Defaults()
	freshCfg.Map.GitRoot = env.GitRoot
	freshCfg.Map.MapRoot = "" // no map-root yet: the argument selects the snapshot
	if err := LoadSnapshot(fresh, freshCfg, "tm", nil); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if freshCfg.Map.MapRoot != "tm" {
		t.Fatalf("snapshot map_root not imported: %q", freshCfg.Map.MapRoot)
	}
	if len(freshCfg.Map.Items) != 1 || freshCfg.Map.Items[0].Path != "dot/file.txt" {
		t.Fatalf("snapshot items not imported: %+v", freshCfg.Map.Items)
	}
}

// TestSyncRefusesDuringInterruptedMerge locks in the §"不自动解决冲突" rule:
// an unresolved merge left in the worktree must stop automatic sync with the
// gate intact — never be concluded by add -A + commit.
func TestSyncRefusesDuringInterruptedMerge(t *testing.T) {
	env, _, local, _ := setupMapped(t, "")
	armGate(t, env)

	// craft a conflicting second history purely inside the worktree
	wtBranch := BranchName(env.MapRoot)
	gitCmd(t, env.Worktree, "checkout", "-b", "tmp-conflict")
	writeFile(t, mappedWtFile(env), "B-side\n")
	gitCmd(t, env.Worktree, "add", "-A")
	gitCmd(t, env.Worktree, "commit", "-m", "b side")
	gitCmd(t, env.Worktree, "checkout", wtBranch)
	writeFile(t, local, "C-side\n")
	writeFile(t, mappedWtFile(env), "C-side\n")
	gitCmd(t, env.Worktree, "add", "-A")
	gitCmd(t, env.Worktree, "commit", "-m", "c side")

	// leave a merge unresolved mid-flight
	gitCmdAllowFail(t, env.Worktree, "merge", "--no-commit", "tmp-conflict")

	headBefore := gitCmd(t, env.Worktree, "rev-parse", "HEAD")
	err := Sync(env)
	if err == nil || !strings.Contains(err.Error(), "in-progress") {
		t.Fatalf("expected in-progress refusal, got %v", err)
	}
	if !HasSyncable(env) {
		t.Fatal(".syncable must survive an interrupted merge (spec §3.2)")
	}
	if got := gitCmd(t, env.Worktree, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("sync moved HEAD during an interrupted merge")
	}
	if un, _ := env.wtRunner().Unmerged(); len(un) == 0 {
		t.Fatal("conflict state was disturbed by sync")
	}
}

// TestLinkRemoteDeletionRequiresChoice pins the 规范派 decision: a remote
// deletion of the whole mapping root must NOT silently remove the managed
// local link — it blocks into MANUAL_REQUIRED for an explicit add/get.
func TestLinkRemoteDeletionRequiresChoice(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "probe-link")
	if err := os.Symlink(probe, probe+".lnk"); err != nil {
		t.Skip("symlinks unavailable on this platform/account")
	}
	os.Remove(probe + ".lnk")

	env, remote, local, _ := setupMapped(t, "link")
	armGate(t, env)

	peer := peerClone(t, remote)
	if err := os.Remove(filepath.Join(peer, "tm", "dot", "file.txt")); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, peer, "add", "-A")
	gitCmd(t, peer, "commit", "-m", "remote delete root")
	gitCmd(t, peer, "push")

	err := Sync(env)
	if err == nil || !strings.Contains(err.Error(), "mapping root diverged after merge") {
		t.Fatalf("expected post-merge root violation block, got %v", err)
	}
	if fi, lerr := os.Lstat(local); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("managed link was auto-removed by remote deletion: %v", lerr)
	}
	if HasSyncable(env) {
		t.Fatal(".syncable must be removed when a choice is required")
	}
}

// TestConflictPathsSurviveQuotepath drives a real modify/modify conflict on
// a CJK-named file and asserts the recorded conflict path stays raw even on
// hosts where core.quotepath defaults to true.
func TestConflictPathsSurviveQuotepath(t *testing.T) {
	env, remote, home := newTestEnv(t)
	local := filepath.Join(home, "测试说明.md")
	writeFile(t, local, "V1\n")
	env.Cfg.Map.Items = []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "dot/测试说明.md", LocalPath: local},
	}
	if err := Init(env); err != nil {
		t.Fatalf("init: %v", err)
	}
	armGate(t, env)

	peer := peerClone(t, remote)
	writeFile(t, filepath.Join(peer, "tm", "dot", "测试说明.md"), "B-remote\n")
	gitCmd(t, peer, "add", "-A")
	gitCmd(t, peer, "commit", "-m", "b change")
	gitCmd(t, peer, "push")

	writeFile(t, local, "C-local\n")
	err := Sync(env)
	if err == nil || !strings.Contains(err.Error(), "merge conflict") {
		t.Fatalf("expected merge conflict, got %v", err)
	}
	blocked, rerr := ReadBlocked(env)
	if rerr != nil || blocked == nil || len(blocked.Conflicts) == 0 {
		t.Fatalf("blocked conflicts missing: %+v %v", blocked, rerr)
	}
	for _, c := range blocked.Conflicts {
		if strings.Contains(c, "\\3") || strings.Contains(c, `"`) {
			t.Fatalf("conflict path looks escaped: %q", c)
		}
		if !strings.Contains(c, "测试说明.md") {
			t.Fatalf("unexpected conflict path: %q", c)
		}
	}
}

// ---------- RunSchedulerTick tests (#5) ----------

// TestSchedulerTickNotInitializedSkip verifies that RunSchedulerTick on a
// machine that has not run `gnm init` returns nil (silent skip).
func TestSchedulerTickNotInitializedSkip(t *testing.T) {
	env, _, _ := newTestEnv(t)
	env.Cfg.Map.Items = []config.MapItem{
		{Scope: config.ScopeMapRoot, Path: "dot/file.txt", LocalPath: filepath.Join(t.TempDir(), "f.txt")},
	}
	env.Cfg.Map.Sync = true
	if err := RunSchedulerTick(env.Cfg, func(string, ...any) {}); err != nil {
		t.Fatalf("RunSchedulerTick on uninitialized machine: %v", err)
	}
}

// TestSchedulerTickSyncDisabled verifies that map.sync=false is a no-op.
func TestSchedulerTickSyncDisabled(t *testing.T) {
	env, _, _, _ := setupMapped(t, "")
	armGate(t, env)
	env.Cfg.Map.Sync = false
	if err := RunSchedulerTick(env.Cfg, func(string, ...any) {}); err != nil {
		t.Fatalf("RunSchedulerTick with sync=false: %v", err)
	}
}

// TestSchedulerTickArmedSyncs verifies that an armed, initialized machine
// runs a real sync round via the scheduler entry point.
func TestSchedulerTickArmedSyncs(t *testing.T) {
	env, remote, _, _ := setupMapped(t, "")
	armGate(t, env)
	env.Cfg.Map.Sync = true

	// push a remote change so sync has something to fast-forward
	peer := peerClone(t, remote)
	writeFile(t, filepath.Join(peer, "tm", "dot", "file.txt"), "V1\nremote-v2\n")
	gitCmd(t, peer, "add", "-A")
	gitCmd(t, peer, "commit", "-m", "remote change")
	gitCmd(t, peer, "push")

	// sync should fast-forward (no local change since armGate)
	if err := RunSchedulerTick(env.Cfg, func(string, ...any) {}); err != nil {
		t.Fatalf("RunSchedulerTick armed: %v", err)
	}
	if !HasSyncable(env) {
		t.Fatal("sync should preserve .syncable on successful fast-forward")
	}
}

// TestSchedulerTickManualRequiredSkip verifies that an initialized but
// disarmed machine (MANUAL_REQUIRED) skips sync and returns nil.
func TestSchedulerTickManualRequiredSkip(t *testing.T) {
	env, _, _, _ := setupMapped(t, "")
	armGate(t, env)
	env.Cfg.Map.Sync = true

	// Simulate MANUAL_REQUIRED by removing .syncable directly
	if err := RemoveSyncable(env); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := RunSchedulerTick(env.Cfg, func(string, ...any) { ran = true }); err != nil {
		t.Fatalf("RunSchedulerTick MANUAL_REQUIRED: %v", err)
	}
	if !ran {
		t.Fatal("expected log output for MANUAL_REQUIRED skip")
	}
}

// ---------- safety tests (#6) ----------

// TestSpecialFileRejectsSync verifies that a special file (FIFO/socket/device)
// in the mapping root is rejected with SpecialFileError, not silently
// overwritten or deleted.
func TestSpecialFileRejectsSync(t *testing.T) {
	if IsWindows() {
		t.Skip("special files (FIFO) are POSIX-only")
	}
	env, _, local, _ := setupMapped(t, "")
	armGate(t, env)

	// replace the local file with a FIFO
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", local).Run(); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	err := Sync(env)
	if err == nil {
		t.Fatal("sync should reject special file")
	}
	// Either the direct SpecialFileError path or the earlier root-violation
	// block is acceptable: both gate sync off without touching the special
	// file. The invariant under test is safety, not which check fired first.
	var sfe *SpecialFileError
	if !errors.As(err, &sfe) && !strings.Contains(err.Error(), "mapping root needs manual choice") {
		t.Fatalf("expected SpecialFileError or root-violation block, got: %v", err)
	}
	if HasSyncable(env) {
		t.Fatal(".syncable should be removed on special-file block")
	}
	// the special file itself must survive untouched
	if fi, lerr := os.Lstat(local); lerr != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("special file was modified or removed: %v", lerr)
	}
}

// TestSymlinkNotDereferencedInCopy verifies that SyncTree in copy mode
// reproduces symlinks as symlinks on the destination side — it must NOT
// dereference them and copy the target content.
func TestSymlinkNotDereferencedInCopy(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "probe-target")
	writeFile(t, probe, "target-content\n")
	if err := os.Symlink(probe, probe+".lnk"); err != nil {
		t.Skip("symlinks unavailable on this platform/account")
	}
	os.Remove(probe + ".lnk")

	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	os.MkdirAll(src, 0o755)
	// create a symlink inside src
	if err := os.Symlink(filepath.Join(tmp, "target"), filepath.Join(src, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}

	if err := SyncTree(src, dst); err != nil {
		t.Fatalf("SyncTree: %v", err)
	}
	// dst/link must be a symlink, NOT a file with target's content
	fi, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("SyncTree dereferenced symlink into a regular file")
	}
}

// TestCopyModeRootViolationBlocks verifies that a type mismatch at the
// mapping root (e.g. local is a file, worktree is a dir) blocks sync
// instead of blindly deleting the local side.
func TestCopyModeRootViolationBlocks(t *testing.T) {
	env, _, local, _ := setupMapped(t, "")
	armGate(t, env)

	// replace local file with a directory — root type mismatch
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(local, "child.txt"), "child\n")

	err := Sync(env)
	if err == nil {
		t.Fatal("sync should block on root type mismatch")
	}
	// local content must survive
	if _, err := os.Stat(filepath.Join(local, "child.txt")); err != nil {
		t.Fatal("local content was deleted despite root violation block")
	}
}

// TestAddNonMappedFile verifies `gnm add` accepts a worktree-relative file
// outside any mapping (.gitignore), instead of rejecting it.
func TestAddNonMappedFile(t *testing.T) {
	env, _, _, _ := setupMapped(t, "")

	gi := filepath.Join(env.Worktree, ".gitignore")
	writeFile(t, gi, "# ignore\n")

	if err := Add(env, []string{".gitignore"}, false); err != nil {
		t.Fatalf("add non-mapped .gitignore: %v", err)
	}
	staged, err := env.wtRunner().HasStaged()
	if err != nil {
		t.Fatal(err)
	}
	if !staged {
		t.Fatal(".gitignore not staged after add")
	}
}

// TestGetNonMappedFile verifies `gnm get` restores the HEAD version of a
// non-mapped file, and confirms deletion when HEAD lacks it.
func TestGetNonMappedFile(t *testing.T) {
	env, _, _, _ := setupMapped(t, "")
	armGate(t, env)

	gi := filepath.Join(env.Worktree, ".gitignore")
	writeFile(t, gi, "# v1\n")
	if err := Add(env, []string{".gitignore"}, false); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := Commit(env, "add gitignore"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// modify then restore via get
	writeFile(t, gi, "# v2\n")
	if err := Get(env, []string{".gitignore"}, false); err != nil {
		t.Fatalf("get non-mapped .gitignore: %v", err)
	}
	if got := readFile(t, gi); got != "# v1\n" {
		t.Fatalf("get should restore HEAD version, got %q", got)
	}
}
