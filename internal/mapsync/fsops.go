// fsops.go: the copy engine shared by init / add / get / sync (spec §5.2–5.4).
//
// Direction is always explicit; both sides never merge. Convergence rules:
// size+mtime filtering, deletion propagation inside existing directories,
// atomic file replacement preserving mode bits and mtime, symlinks copied as
// links, special files refused loudly.
package mapsync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type entryKind int

const (
	kMissing entryKind = iota
	kFile
	kDir
	kSymlink
	kOther // socket / FIFO / device
)

func inspect(path string) (entryKind, os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return kMissing, nil, nil
		}
		return kOther, nil, err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return kSymlink, fi, nil
	case fi.IsDir():
		return kDir, fi, nil
	case fi.Mode().IsRegular():
		return kFile, fi, nil
	default:
		return kOther, fi, nil
	}
}

func kindOf(path string) entryKind {
	k, _, err := inspect(path)
	if err != nil {
		return kOther
	}
	return k
}

func kindName(k entryKind) string {
	switch k {
	case kFile:
		return "file"
	case kDir:
		return "dir"
	case kSymlink:
		return "link"
	case kOther:
		return "special"
	default:
		return "missing"
	}
}

// SpecialFileError reports a socket/FIFO/device met during a copy walk.
// Interactive commands abort with it; automatic sync additionally removes
// .syncable (spec §6.4).
type SpecialFileError struct{ Path string }

func (e *SpecialFileError) Error() string {
	return fmt.Sprintf("refusing to copy special file: %s", e.Path)
}

// ConcurrentChangeError means a real file changed after it was copied into
// the worktree. The caller stops and lets the user choose; no extra tree scan
// is needed because the baseline is collected during the normal copy pass.
type ConcurrentChangeError struct{ Path string }

func (e *ConcurrentChangeError) Error() string {
	return fmt.Sprintf("local path changed during synchronization: %s", e.Path)
}

type nodeStamp struct {
	Kind   entryKind
	Size   int64
	Mtime  int64
	Mode   os.FileMode
	Target string
}

type copyBaseline map[string]nodeStamp

// SyncTree converges dstRoot to srcRoot (spec §5.3):
//
//  1. missing/类型不同的目标 → 复制或替换；
//  2. 大小不同 → 复制；大小同 mtime 不同 → 复制；两者皆同 → 跳过；
//  3. 目录始终递归，源端已删除的子文件自动删除目标端对应子文件；
//  4. 复制走临时文件 + 原子替换，成功后同步源 mtime。
func SyncTree(srcRoot, dstRoot string) error {
	return syncTree(srcRoot, dstRoot, "", nil, nil)
}

// syncTreeTracked performs the normal local→worktree copy and remembers the
// local metadata already visited. syncTreeGuarded reuses that baseline while
// deploying worktree→local, so TOCTOU protection adds no full-tree pass.
func syncTreeTracked(srcRoot, dstRoot string) (copyBaseline, error) {
	baseline := copyBaseline{}
	err := syncTree(srcRoot, dstRoot, "", baseline, nil)
	return baseline, err
}

func syncTreeGuarded(srcRoot, dstRoot string, baseline copyBaseline) error {
	return syncTree(srcRoot, dstRoot, "", nil, baseline)
}

func syncTree(srcRoot, dstRoot, rel string, baseline, guard copyBaseline) error {
	sk, si, err := inspect(srcRoot)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", srcRoot, err)
	}
	dk, di, err := inspect(dstRoot)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dstRoot, err)
	}
	if baseline != nil {
		stamp, err := makeStamp(srcRoot, sk, si)
		if err != nil {
			return err
		}
		baseline[rel] = stamp
	}
	if guard != nil {
		if _, err := checkStamp(dstRoot, rel, guard); err != nil {
			return err
		}
	}
	if sk == kOther {
		return &SpecialFileError{srcRoot}
	}
	if dk == kOther {
		return &SpecialFileError{dstRoot}
	}
	if sk == kMissing {
		if dk == kMissing {
			return nil
		}
		if guard != nil {
			if err := checkSubtree(dstRoot, rel, guard); err != nil {
				return err
			}
		}
		return os.RemoveAll(dstRoot)
	}
	if sk != dk && dk != kMissing {
		if guard != nil && dk == kDir {
			if err := checkSubtree(dstRoot, rel, guard); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(dstRoot); err != nil {
			return err
		}
		dk = kMissing
		di = nil
	}
	switch sk {
	case kFile:
		return copyFileIfNeeded(srcRoot, dstRoot, si, di)
	case kSymlink:
		tgt, err := os.Readlink(srcRoot)
		if err != nil {
			return err
		}
		if dk == kSymlink {
			if cur, e2 := os.Readlink(dstRoot); e2 == nil && cur == tgt {
				return nil
			}
		}
		if err := os.RemoveAll(dstRoot); err != nil {
			return err
		}
		if err := ensureParent(dstRoot); err != nil {
			return err
		}
		return os.Symlink(tgt, dstRoot)
	case kDir:
		if err := os.MkdirAll(dstRoot, 0o755); err != nil {
			return err
		}
		ents, err := os.ReadDir(srcRoot)
		if err != nil {
			return err
		}
		srcNames := make(map[string]bool, len(ents))
		for _, e := range ents {
			srcNames[e.Name()] = true
			childRel := e.Name()
			if rel != "" {
				childRel = rel + "/" + e.Name()
			}
			if err := syncTree(filepath.Join(srcRoot, e.Name()), filepath.Join(dstRoot, e.Name()), childRel, baseline, guard); err != nil {
				return err
			}
		}
		dents, err := os.ReadDir(dstRoot)
		if err != nil {
			return err
		}
		for _, de := range dents {
			if !srcNames[de.Name()] {
				childRel := de.Name()
				if rel != "" {
					childRel = rel + "/" + de.Name()
				}
				child := filepath.Join(dstRoot, de.Name())
				if guard != nil {
					if err := checkSubtree(child, childRel, guard); err != nil {
						return err
					}
				}
				if err := os.RemoveAll(child); err != nil {
					return err
				}
			}
		}
		// Directories carry meaningful permissions too (for example, private
		// configuration directories). Apply their metadata after walking so
		// child creation and deletion do not immediately change the mtime.
		if err := os.Chmod(dstRoot, si.Mode().Perm()); err != nil {
			return err
		}
		mt := si.ModTime()
		if err := os.Chtimes(dstRoot, mt, mt); err != nil {
			return err
		}
	}
	return nil
}

func makeStamp(path string, kind entryKind, info os.FileInfo) (nodeStamp, error) {
	s := nodeStamp{Kind: kind}
	if info != nil && kind == kFile {
		s.Size, s.Mtime, s.Mode = info.Size(), info.ModTime().UnixNano(), info.Mode().Perm()
	}
	if info != nil && kind == kDir {
		// Directory mtime changes as children are edited and is not a stable
		// content signal. Permission bits are meaningful and must still match.
		s.Mode = info.Mode().Perm()
	}
	if kind == kSymlink {
		target, err := os.Readlink(path)
		if err != nil {
			return s, err
		}
		s.Target = target
	}
	return s, nil
}

func checkStamp(path, rel string, baseline copyBaseline) (entryKind, error) {
	expected, ok := baseline[rel]
	kind, info, err := inspect(path)
	if err != nil {
		return kind, err
	}
	current, err := makeStamp(path, kind, info)
	if err != nil {
		return kind, err
	}
	if !ok {
		expected = nodeStamp{Kind: kMissing}
	}
	if current != expected {
		return kind, &ConcurrentChangeError{path}
	}
	return kind, nil
}

func checkSubtree(root, rel string, baseline copyBaseline) error {
	seen := map[string]bool{}
	var walk func(string, string) error
	walk = func(path, key string) error {
		seen[key] = true
		kind, err := checkStamp(path, key, baseline)
		if err != nil {
			return err
		}
		if kind != kDir {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childKey := entry.Name()
			if key != "" {
				childKey = key + "/" + entry.Name()
			}
			if err := walk(filepath.Join(path, entry.Name()), childKey); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, rel); err != nil {
		return err
	}
	prefix := rel
	if prefix != "" {
		prefix += "/"
	}
	for key := range baseline {
		if (key == rel || strings.HasPrefix(key, prefix)) && !seen[key] {
			return &ConcurrentChangeError{root}
		}
	}
	return nil
}

// snapshotMetadata is intentionally used by the explicit status command.
// Automatic synchronization obtains the same metadata during its required
// copy traversal and does not pay for these extra diagnostic walks.
func snapshotMetadata(root string) (copyBaseline, error) {
	out := copyBaseline{}
	var walk func(string, string) error
	walk = func(path, rel string) error {
		kind, info, err := inspect(path)
		if err != nil {
			return err
		}
		if kind == kOther {
			return &SpecialFileError{path}
		}
		stamp, err := makeStamp(path, kind, info)
		if err != nil {
			return err
		}
		out[rel] = stamp
		if kind != kDir {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childRel := entry.Name()
			if rel != "" {
				childRel = rel + "/" + entry.Name()
			}
			if err := walk(filepath.Join(path, entry.Name()), childRel); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return out, nil
}

func treesSameMetadata(a, b string) (bool, error) {
	as, err := snapshotMetadata(a)
	if err != nil {
		return false, err
	}
	bs, err := snapshotMetadata(b)
	if err != nil {
		return false, err
	}
	if len(as) != len(bs) {
		return false, nil
	}
	for path, left := range as {
		if right, ok := bs[path]; !ok || right != left {
			return false, nil
		}
	}
	return true, nil
}

// copyFileIfNeeded copies via a temp file + atomic rename, preserving the
// source permission bits (executable bit included) and mtime.
func copyFileIfNeeded(src, dst string, si, di os.FileInfo) error {
	if di != nil && di.Mode().IsRegular() && si.Size() == di.Size() && si.ModTime().Equal(di.ModTime()) && si.Mode().Perm() == di.Mode().Perm() {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".gns-copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); _ = os.Remove(tmpName) }

	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, si.Mode().Perm()); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	mt := si.ModTime()
	if err := os.Chtimes(tmpName, mt, mt); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// Windows can reject replacement of a read-only destination. Temporarily
	// make that file writable and retry, restoring it if replacement still
	// fails. The temp file already carries the source permissions.
	if err := os.Rename(tmpName, dst); err != nil {
		if di != nil && di.Mode().IsRegular() {
			oldMode := di.Mode().Perm()
			if chmodErr := os.Chmod(dst, oldMode|0o200); chmodErr == nil {
				if retryErr := os.Rename(tmpName, dst); retryErr == nil {
					return nil
				}
				_ = os.Chmod(dst, oldMode)
			}
		}
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// EnsureSymlink points linkPath at target, replacing any existing node there
// (spec §4.4: link creation failure reports an error, never silently copies).
func EnsureSymlink(linkPath, target string) error {
	if linkPointsTo(linkPath, target) {
		return nil
	}
	if kindOf(linkPath) != kMissing {
		return fmt.Errorf("refusing to replace unmanaged path: %s", linkPath)
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}

// ReplaceLocalWithLink performs the link-mode swap of spec §6.3: sync local
// content into the worktree first (syncFn), set the original aside, create
// the symlink, verify it resolves, then drop the backup. Any failure rolls
// the original path back — no lost files, no half-built mappings.
func ReplaceLocalWithLink(localAbs, wtPath string, syncFn func() error) error {
	if linkPointsTo(localAbs, wtPath) {
		return nil
	}
	if syncFn != nil {
		if err := syncFn(); err != nil {
			return fmt.Errorf("copy into worktree: %w", err)
		}
	}
	bak, err := moveAside(localAbs)
	if err != nil {
		return fmt.Errorf("set aside %s: %w", localAbs, err)
	}
	if err := os.MkdirAll(filepath.Dir(localAbs), 0o755); err != nil {
		_ = os.Rename(bak, localAbs)
		return err
	}
	if err := os.Symlink(wtPath, localAbs); err != nil {
		_ = os.Remove(localAbs)
		_ = os.Rename(bak, localAbs) // rollback
		return fmt.Errorf("create symlink: %w", err)
	}
	if _, err := os.Stat(localAbs); err != nil { // must resolve through the link
		_ = os.Remove(localAbs)
		_ = os.Rename(bak, localAbs)
		return fmt.Errorf("verify symlink: %w", err)
	}
	// The mapping is already live; a leftover backup is safer than turning a
	// cleanup warning into a partial-init failure.
	_ = os.RemoveAll(bak)
	return nil
}

func moveAside(path string) (string, error) {
	if kindOf(path) == kMissing {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".gns-bak-*")
	if err != nil {
		return "", err
	}
	bak := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(bak)
		return "", err
	}
	if err := os.Remove(bak); err != nil {
		return "", err
	}
	if err := os.Rename(path, bak); err != nil {
		return "", err
	}
	return bak, nil
}

func linkPointsTo(linkPath, target string) bool {
	if kindOf(linkPath) != kSymlink {
		return false
	}
	current, err := os.Readlink(linkPath)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(current) {
		current = filepath.Join(filepath.Dir(linkPath), current)
	}
	return LocalKey(NormalizeLocal(current)) == LocalKey(NormalizeLocal(target))
}

// walkNodes lists every node (files, links, dirs — relative slash paths)
// under root without following symlinks; "" is never returned for root
// itself. A special file aborts the walk.
func walkNodes(root string) ([]string, error) {
	var out []string
	var walk func(abs, rel string) error
	walk = func(abs, rel string) error {
		switch kindOf(abs) {
		case kMissing:
			return nil
		case kOther:
			return &SpecialFileError{abs}
		case kDir:
			if rel != "" {
				out = append(out, rel)
			}
			ents, err := os.ReadDir(abs)
			if err != nil {
				return err
			}
			for _, e := range ents {
				childRel := e.Name()
				if rel != "" {
					childRel = rel + "/" + e.Name()
				}
				if err := walk(filepath.Join(abs, e.Name()), childRel); err != nil {
					return err
				}
			}
		default: // file or symlink — symlinks are nodes, never descended into
			out = append(out, rel)
		}
		return nil
	}
	if kindOf(root) == kMissing {
		return nil, nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	if len(out) > 0 && out[0] == "" {
		out = out[1:] // drop the root itself when it was recorded
	}
	return out, nil
}

// touchDir makes sure the parent chain of path exists.
func ensureParent(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}
