// Package git wraps the system git binary. The tool deliberately does not
// re-implement git (see spec §1.3): all operations shell out to `git`.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Runner executes git commands inside a working directory.
type Runner struct {
	Dir     string
	Env     []string
	Timeout time.Duration // per-command deadline; 0 = no timeout
}

func NewRunner(dir string) *Runner { return &Runner{Dir: dir} }

// CmdError carries the failing command and git's stderr.
type CmdError struct {
	Args   []string
	Stderr string
	Code   int
	Err    error
}

func (e *CmdError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *CmdError) Unwrap() error { return e.Err }

// IsTransient reports whether a git error is likely transient and worth
// retrying. Permanent failures (auth, permissions, missing repo) are
// reported as non-transient so callers can fail fast instead of sleeping
// through pointless retries. Unknown error kinds default to transient —
// the safe direction. Used by retry.Do's classifier.
func IsTransient(err error) bool {
	var ce *CmdError
	if !errors.As(err, &ce) {
		return true
	}
	s := strings.ToLower(ce.Stderr)
	for _, p := range []string{
		"authentication failed",
		"permission denied",
		"not a git repository",
		"does not appear to be a git repository",
		"repository not found",
		"invalid username or password",
		"returned error: 403",
		"could not read username",
	} {
		if strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func (r *Runner) run(args ...string) (string, string, error) {
	ctx := context.Background()
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	// core.quotepath=off keeps path output raw on every command: the git
	// default (true) escapes non-ASCII names to `"\344..."` octal blobs,
	// which would corrupt anything downstream that matches or stores paths
	// against real filesystem names (CJK notes repos are a primary use).
	full := append([]string{"-c", "core.quotepath=off"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = r.Dir
	if len(r.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Env...)
	}
	// After the process is killed (timeout) or exits, an orphaned
	// grandchild (ssh, credential helper) may still hold the output pipes —
	// without WaitDelay, Wait blocks until it exits, re-hanging the caller.
	// WaitDelay closes the pipes and returns ErrWaitDelay after the bound.
	// (Modern git redirects its background gc output to gc.log, so lingering
	// pipes on successful commands are not expected.)
	cmd.WaitDelay = 5 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		// The deadline killed git (network hang, credential prompt, etc.):
		// surface a clear timeout error instead of the opaque kill status.
		stderr := strings.TrimSpace(errb.String())
		if stderr != "" {
			stderr += "; "
		}
		stderr += fmt.Sprintf("timed out after %ds", int(r.Timeout/time.Second))
		return out.String(), errb.String(), cmdErr(args, stderr, err)
	}
	return out.String(), errb.String(), err
}

func cmdErr(args []string, stderr string, err error) error {
	code := -1
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return &CmdError{Args: args, Stderr: stderr, Code: code, Err: err}
}

// Out runs git and returns trimmed stdout; non-zero exit yields *CmdError.
func (r *Runner) Out(args ...string) (string, error) {
	out, stderr, err := r.run(args...)
	if err != nil {
		return "", cmdErr(args, stderr, err)
	}
	return strings.TrimSpace(out), nil
}

// OutErr runs git and returns trimmed stdout/stderr even on failure.
func (r *Runner) OutErr(args ...string) (string, string, error) {
	out, stderr, err := r.run(args...)
	if err != nil {
		return strings.TrimSpace(out), strings.TrimSpace(stderr), cmdErr(args, stderr, err)
	}
	return strings.TrimSpace(out), strings.TrimSpace(stderr), nil
}

// IsRepo reports whether Dir is inside a git work tree.
func (r *Runner) IsRepo() bool {
	_, err := r.Out("rev-parse", "--git-dir")
	return err == nil
}

func (r *Runner) TopLevel() (string, error) {
	s, err := r.Out("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if abs, aerr := filepath.Abs(s); aerr == nil {
		s = abs
	}
	return s, nil
}

func (r *Runner) GitDir() (string, error) {
	s, err := r.Out("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return s, nil
}

func (r *Runner) CurrentBranch() string {
	s, err := r.Out("branch", "--show-current")
	if err != nil || s == "" {
		return ""
	}
	return s
}

// Upstream parses @{u} into (remote, branch).
func (r *Runner) Upstream() (remote, branch string, ok bool) {
	s, err := r.Out("rev-parse", "--abbrev-ref", "@{u}")
	if err != nil {
		return "", "", false
	}
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// Count returns the number of commits in revA..revB.
func (r *Runner) Count(revA, revB string) (int, error) {
	s, err := r.Out("rev-list", "--count", revA+".."+revB)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

func (r *Runner) Fetch(remote string) error {
	_, err := r.Out("fetch", "--prune", remote)
	return err
}

func (r *Runner) Merge(ref string) error {
	_, err := r.Out("merge", "--no-edit", ref)
	return err
}

func (r *Runner) MergeAbort() error {
	_, err := r.Out("merge", "--abort")
	return err
}

// MergeInProgress reports whether git is mid-merge/rebase/cherry-pick
// (returns the marker name, or "" when idle).
func (r *Runner) MergeInProgress() (string, error) {
	gd, err := r.GitDir()
	if err != nil {
		return "", err
	}
	markers := []string{
		filepath.Join(gd, "MERGE_HEAD"),
		filepath.Join(gd, "CHERRY_PICK_HEAD"),
		filepath.Join(gd, "REVERT_HEAD"),
		filepath.Join(gd, "rebase-merge"),
		filepath.Join(gd, "rebase-apply"),
	}
	for _, m := range markers {
		if _, err := os.Stat(m); err == nil {
			return filepath.Base(m), nil
		}
	}
	return "", nil
}

// Entry is one row of `git status --porcelain -z`.
type Entry struct {
	Status string // "XY" or "??"
	Path   string
}

func (r *Runner) Status() ([]Entry, error) {
	out, _, err := r.run("status", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "" {
			continue
		}
		if len(f) < 4 {
			continue
		}
		xy := f[:2]
		path := f[3:]
		if (xy[0] == 'R' || xy[0] == 'C' || xy[1] == 'R' || xy[1] == 'C') && i+1 < len(fields) {
			// porcelain v1 -z rename/copy record: "XY <new>\0<old>\0" —
			// the first field already carries the destination path (kept
			// above); skip the source-path field that follows.
			i++
		}
		entries = append(entries, Entry{Status: xy, Path: path})
	}
	return entries, nil
}

// Unmerged returns paths currently in conflict (stages 1/2/3). -z keeps
// raw names under default core.quotepath=true (CJK etc).
func (r *Runner) Unmerged() ([]string, error) {
	out, err := r.Out("ls-files", "-u", "-z")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, entry := range strings.Split(out, "\x00") {
		tab := strings.IndexByte(entry, '\t')
		if tab < 0 {
			continue
		}
		p := entry[tab+1:]
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Numstat is one row of `git diff --cached --numstat`.
type Numstat struct {
	Path    string
	Added   int
	Deleted int
	Binary  bool
}

func (r *Runner) CachedNumstat() ([]Numstat, error) {
	out, err := r.Out("diff", "--cached", "--numstat")
	if err != nil {
		return nil, err
	}
	var ns []Numstat
	for _, ln := range strings.Split(out, "\n") {
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		n := Numstat{Path: parts[2]}
		if parts[0] == "-" {
			n.Binary = true
		} else {
			n.Added, _ = strconv.Atoi(parts[0])
			n.Deleted, _ = strconv.Atoi(parts[1])
		}
		ns = append(ns, n)
	}
	return ns, nil
}

// CachedDiff returns `git diff --cached`, truncated to maxBytes on a UTF-8
// rune boundary (a byte cut could split a multi-byte character mid-sequence).
func (r *Runner) CachedDiff(maxBytes int) (string, error) {
	out, _, err := r.run("diff", "--cached")
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && len(out) > maxBytes {
		out = truncateUTF8(out, maxBytes) + "\n...[truncated]"
	}
	return out, nil
}

// truncateUTF8 cuts s to at most max bytes without splitting a UTF-8 rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func (r *Runner) AddAll() error { _, err := r.Out("add", "-A"); return err }

func (r *Runner) Add(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	_, err := r.Out(args...)
	return err
}

func (r *Runner) Commit(msg string) error {
	_, err := r.Out("commit", "-m", msg)
	return err
}

// CommitMerge finishes an in-progress merge (used after conflict staging).
func (r *Runner) CommitMerge() error {
	_, err := r.Out("commit", "--no-edit")
	return err
}

// Push pushes the current branch to remote:refs/heads/branch.
func (r *Runner) Push(remote, branch string) error {
	_, err := r.Out("push", remote, "HEAD:refs/heads/"+branch)
	return err
}

func (r *Runner) CheckoutOurs(path string) error {
	_, err := r.Out("checkout", "--ours", "--", path)
	return err
}

func (r *Runner) CheckoutTheirs(path string) error {
	_, err := r.Out("checkout", "--theirs", "--", path)
	return err
}

// MarkerFiles returns tracked files containing conflict markers
// (`<<<<<<< ` / `>>>>>>> ` lines). Empty when none.
func (r *Runner) MarkerFiles() ([]string, error) {
	// -z keeps raw paths under default core.quotepath=true (CJK etc)
	out, _, err := r.run("grep", "-l", "-z", "-e", "^<<<<<<< ", "-e", "^>>>>>>> ", "--", ":/")
	if err != nil {
		// git grep exits 1 when nothing matches
		if ce, ok := err.(*exec.ExitError); ok && ce.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, ln := range strings.Split(out, "\x00") {
		if ln != "" {
			files = append(files, ln)
		}
	}
	return files, nil
}

// ---------- primitives for the map (worktree) feature ----------

// Head returns the current HEAD commit hash ("" when the repo has no commits).
func (r *Runner) Head() string {
	s, err := r.Out("rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return s
}

// BranchExists reports whether a local branch exists.
func (r *Runner) BranchExists(branch string) bool {
	_, err := r.Out("show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	// show-ref --quiet prints nothing; exit 0 = exists
	return err == nil
}

// HasStaged reports whether the index differs from HEAD.
func (r *Runner) HasStaged() (bool, error) {
	_, _, err := r.run("diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}
	var ce *exec.ExitError
	if errors.As(err, &ce) && ce.ExitCode() == 1 {
		return true, nil
	}
	return false, cmdErr([]string{"diff", "--cached", "--quiet"}, "", err)
}

// PathsChanged reports whether two commits differ under the selected paths.
func (r *Runner) PathsChanged(from, to string, paths ...string) (bool, error) {
	args := []string{"diff", "--quiet", from, to}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	_, _, err := r.run(args...)
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, cmdErr(args, "", err)
}

// MergeFFOnly fast-forwards to ref, refusing to create a merge commit.
func (r *Runner) MergeFFOnly(ref string) error {
	_, err := r.Out("merge", "--ff-only", ref)
	return err
}

// PullFFOnly runs `git pull --ff-only` in Dir.
func (r *Runner) PullFFOnly() error {
	_, err := r.Out("pull", "--ff-only")
	return err
}

// ResetHard resets --hard to rev (branch tip or commit hash).
func (r *Runner) ResetHard(rev string) error {
	_, err := r.Out("reset", "--hard", rev)
	return err
}

// ResetMixed resets HEAD and index to rev, keeping working files intact.
func (r *Runner) ResetMixed(rev string) error {
	_, err := r.Out("reset", "--mixed", rev)
	return err
}

// ResetMerge moves HEAD while preserving unrelated working-tree changes.
func (r *Runner) ResetMerge(rev string) error {
	_, err := r.Out("reset", "--merge", rev)
	return err
}

// ResetPaths restores selected index entries without touching working files.
func (r *Runner) ResetPaths(rev string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"reset", "-q", rev, "--"}, paths...)
	_, err := r.Out(args...)
	return err
}

// UpdateRefBestEffort points ref at rev, ignoring failures (backup refs are
// a safety net, not a requirement).
func (r *Runner) UpdateRefBestEffort(ref, rev string) {
	_, _ = r.Out("update-ref", ref, rev)
}

// WorktreeAdd creates a linked worktree at dir with a new branch based on base.
func (r *Runner) WorktreeAdd(branch, dir, base string) error {
	_, err := r.Out("worktree", "add", "-b", branch, dir, base)
	return err
}

func (r *Runner) WorktreeRemove(dir string) error {
	_, err := r.Out("worktree", "remove", "--force", dir)
	return err
}

func (r *Runner) DeleteBranch(branch string) error {
	_, err := r.Out("branch", "-D", branch)
	return err
}

// LsTreeHead lists file paths under sub (repo-relative, "" = whole tree)
// from HEAD. Empty when the path has no tracked files.
//
// -z keeps names raw: without it git quotes non-ASCII (`"\344\270\255..."`)
// and names containing quotes under default core.quotepath=true, which
// would break matching on stock environments (CI runners included).
func (r *Runner) LsTreeHead(sub string) ([]string, error) {
	args := []string{"ls-tree", "-r", "--name-only", "-z", "HEAD"}
	if sub != "" {
		args = append(args, "--", sub)
	}
	out, _, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, ln := range strings.Split(out, "\x00") {
		if ln != "" {
			paths = append(paths, ln)
		}
	}
	return paths, nil
}

// ShowHeadFile prints a blob at path from HEAD ("HEAD:<path>").
func (r *Runner) ShowHeadFile(path string) (string, error) {
	return r.Out("show", "HEAD:"+path)
}
