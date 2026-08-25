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
)

type entryKind int

const (
	kMissing entryKind = iota
	kFile
	kDir
	kSymlink
	kOther // socket / FIFO / device
)

func kindOf(path string) entryKind {
	fi, err := os.Lstat(path)
	if err != nil {
		return kMissing
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return kSymlink
	case fi.IsDir():
		return kDir
	case fi.Mode().IsRegular():
		return kFile
	default:
		return kOther
	}
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

// SyncTree converges dstRoot to srcRoot (spec §5.3):
//
//  1. missing/类型不同的目标 → 复制或替换；
//  2. 大小不同 → 复制；大小同 mtime 不同 → 复制；两者皆同 → 跳过；
//  3. 目录始终递归，源端已删除的子文件自动删除目标端对应子文件；
//  4. 复制走临时文件 + 原子替换，成功后同步源 mtime。
func SyncTree(srcRoot, dstRoot string) error {
	sk, dk := kindOf(srcRoot), kindOf(dstRoot)
	if sk == kMissing {
		if dk == kMissing {
			return nil
		}
		return os.RemoveAll(dstRoot) // whole-subtree deletion propagates
	}
	if sk == kOther {
		return &SpecialFileError{srcRoot}
	}
	if dk == kOther {
		if err := os.RemoveAll(dstRoot); err != nil {
			return err
		}
		dk = kMissing
	}
	if sk != kDir && dk == kDir || sk == kDir && dk != kDir && dk != kMissing {
		// type change: clear the destination before re-converging
		if err := os.RemoveAll(dstRoot); err != nil {
			return err
		}
		dk = kMissing
	}
	switch sk {
	case kFile:
		return copyFileIfNeeded(srcRoot, dstRoot)
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
			if err := SyncTree(filepath.Join(srcRoot, e.Name()), filepath.Join(dstRoot, e.Name())); err != nil {
				return err
			}
		}
		// prune destination children absent from source (deletion propagation)
		dents, err := os.ReadDir(dstRoot)
		if err != nil {
			return err
		}
		for _, de := range dents {
			if !srcNames[de.Name()] {
				if err := os.RemoveAll(filepath.Join(dstRoot, de.Name())); err != nil {
					return err
				}
			}
		}
		// keep the directory's own mtime in step so parent-level filters converge
		if si, err := os.Lstat(srcRoot); err == nil {
			mt := si.ModTime()
			_ = os.Chtimes(dstRoot, mt, mt)
		}
	}
	return nil
}

// needCopy implements the §5.3 filter: skip only when both sides are regular
// files of equal size and mtime — content is never read and no hash computed.
func needCopy(src, dst string) bool {
	si, err1 := os.Lstat(src)
	di, err2 := os.Lstat(dst)
	if err1 != nil || err2 != nil {
		return true
	}
	if !si.Mode().IsRegular() || !di.Mode().IsRegular() {
		return true
	}
	if si.Size() != di.Size() {
		return true
	}
	return !si.ModTime().Equal(di.ModTime())
}

// copyFileIfNeeded copies via a temp file + atomic rename, preserving the
// source permission bits (executable bit included) and mtime.
func copyFileIfNeeded(src, dst string) error {
	if !needCopy(src, dst) {
		return nil
	}
	si, err := os.Lstat(src)
	if err != nil {
		return err
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
	// os.Rename replaces an existing destination on every supported platform
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// EnsureSymlink points linkPath at target, replacing any existing node there
// (spec §4.4: link creation failure reports an error, never silently copies).
func EnsureSymlink(linkPath, target string) error {
	if kindOf(linkPath) == kSymlink {
		if cur, err := os.Readlink(linkPath); err == nil && cur == target {
			return nil
		}
	}
	if err := os.RemoveAll(linkPath); err != nil {
		return err
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
	bak := localAbs + ".gns-bak"
	_ = os.RemoveAll(bak)
	if syncFn != nil {
		if err := syncFn(); err != nil {
			return fmt.Errorf("copy into worktree: %w", err)
		}
	}
	if err := os.Rename(localAbs, bak); err != nil {
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
	if err := os.RemoveAll(bak); err != nil {
		return fmt.Errorf("remove backup %s: %w", bak, err)
	}
	return nil
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
