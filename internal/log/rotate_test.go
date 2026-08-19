package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, path string, sizeKB int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := strings.Repeat("x", sizeKB*1024)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func backupsOnDisk(t *testing.T, dir, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+".") {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestRotateNoFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gns.log")
	if err := Rotate(p, 10, 1); err != nil {
		t.Fatalf("missing file must be a no-op: %v", err)
	}
}

func TestRotateUnderThreshold(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gns.log")
	writeLog(t, p, 1) // 1KB < 10KB
	if err := Rotate(p, 10, 1); err != nil {
		t.Fatal(err)
	}
	if got := backupsOnDisk(t, filepath.Dir(p), "gns.log"); len(got) != 0 {
		t.Errorf("under-threshold file must not rotate, backups = %v", got)
	}
}

func TestRotateOverThreshold(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gns.log")
	writeLog(t, p, 20) // 20KB > 10KB
	if err := Rotate(p, 10, 1); err != nil {
		t.Fatal(err)
	}
	got := backupsOnDisk(t, dir, "gns.log")
	if len(got) != 1 || got[0] != "gns.log.1" {
		t.Fatalf("expected exactly gns.log.1, got %v", got)
	}
}

func TestRotateKeepsMaxBackups(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gns.log")
	writeLog(t, p, 20)
	// simulate two prior rotations: .1 and .2 on disk
	os.WriteFile(filepath.Join(dir, "gns.log.1"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "gns.log.2"), []byte("b"), 0o644)

	if err := Rotate(p, 10, 2); err != nil {
		t.Fatal(err)
	}
	got := backupsOnDisk(t, dir, "gns.log")
	// after rotating with maxBackups=2: .1 = old current, .2 = old .1; old .2 dropped
	if len(got) != 2 {
		t.Fatalf("want 2 backups, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "gns.log.3")); !os.IsNotExist(err) {
		t.Errorf("gns.log.3 must not exist")
	}
}

func TestRotateMaxBackupsZeroDeletes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gns.log")
	writeLog(t, p, 20)
	if err := Rotate(p, 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("maxBackups=0 must delete the log, not keep it")
	}
}

func TestRotateShiftOrder(t *testing.T) {
	// with pre-existing .1/.2, rotation must shift .1→.2 and current→.1
	dir := t.TempDir()
	p := filepath.Join(dir, "gns.log")
	writeLog(t, p, 20)
	os.WriteFile(filepath.Join(dir, "gns.log.1"), []byte("old-1"), 0o644)
	os.WriteFile(filepath.Join(dir, "gns.log.2"), []byte("old-2"), 0o644)

	if err := Rotate(p, 10, 3); err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(filepath.Join(dir, "gns.log.2"))
	if err != nil || string(b2) != "old-1" {
		t.Errorf("gns.log.2 should hold old .1 content, got %q err=%v", b2, err)
	}
	b1, err := os.ReadFile(filepath.Join(dir, "gns.log.1"))
	if err != nil || len(b1) != 20*1024 {
		t.Errorf("gns.log.1 should hold rotated content, got len=%d err=%v", len(b1), err)
	}
}

func TestCleanupRemovesExtra(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gns.log")
	writeLog(t, p, 1)
	os.WriteFile(filepath.Join(dir, "gns.log.1"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(dir, "gns.log.2"), []byte("2"), 0o644)
	os.WriteFile(filepath.Join(dir, "gns.log.3"), []byte("3"), 0o644)

	if err := Cleanup(p, 1); err != nil {
		t.Fatal(err)
	}
	got := backupsOnDisk(t, dir, "gns.log")
	if len(got) != 1 || got[0] != "gns.log.1" {
		t.Errorf("Cleanup(1) should keep only .1, got %v", got)
	}
}

func TestCleanupIgnoresNonNumeric(t *testing.T) {
	// "gns.log.1.txt" and "gns.log.keep" must never be treated as backups
	dir := t.TempDir()
	p := filepath.Join(dir, "gns.log")
	writeLog(t, p, 1)
	os.WriteFile(filepath.Join(dir, "gns.log.1"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(dir, "gns.log.1.txt"), []byte("keep me"), 0o644)
	os.WriteFile(filepath.Join(dir, "gns.log.5"), []byte("5"), 0o644)

	if err := Cleanup(p, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gns.log.1.txt")); err != nil {
		t.Errorf("non-numeric-suffixed file must be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gns.log.5")); !os.IsNotExist(err) {
		t.Errorf("numeric backup beyond maxBackups must be removed")
	}
}

func TestRotateDefaults(t *testing.T) {
	// maxSizeKB<=0 → 500KB default; a 200KB file must not rotate under defaults
	dir := t.TempDir()
	p := filepath.Join(dir, "gns.log")
	writeLog(t, p, 200)
	if err := Rotate(p, 0, 1); err != nil {
		t.Fatal(err)
	}
	if got := backupsOnDisk(t, dir, "gns.log"); len(got) != 0 {
		t.Errorf("200KB under 500KB default must not rotate, got %v", got)
	}
}
