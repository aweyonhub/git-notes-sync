package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTailLastLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := tailLastLines(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "line4" || lines[1] != "line5" {
		t.Errorf("tail 2: got %v", lines)
	}

	lines, err = tailLastLines(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Errorf("tail 10 (more than file): got %d lines", len(lines))
	}

	if _, err := tailLastLines(filepath.Join(dir, "nope.log"), 5); err == nil {
		t.Error("missing file should error")
	}
}

func TestTailLastLinesNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	if err := os.WriteFile(p, []byte("a\nb"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := tailLastLines(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[1] != "b" {
		t.Errorf("no trailing newline: got %v", lines)
	}
}

func TestTailLastLinesEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := tailLastLines(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "" {
		t.Errorf("empty file: got %v (want single empty line)", lines)
	}
}
