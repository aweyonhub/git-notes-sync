package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeArgs(t *testing.T) {
	vf := map[string]bool{"c": true, "p": true, "repo": true}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"flags first", []string{"-c", "f.toml", "notes"}, []string{"-c", "f.toml", "notes"}},
		{"positional first", []string{"notes", "-c", "f.toml"}, []string{"-c", "f.toml", "notes"}},
		{"mixed", []string{"-p", "/x", "notes", "-c", "f"}, []string{"-p", "/x", "-c", "f", "notes"}},
		{"flag with equals", []string{"notes", "-c=f.toml"}, []string{"-c=f.toml", "notes"}},
		{"boolean flag no value", []string{"notes", "-force"}, []string{"-force", "notes"}},
		{"flag value looks like flag", []string{"-c", "-x", "notes"}, []string{"-c", "-x", "notes"}},
		{"no args", nil, nil},
	}
	for _, c := range cases {
		got := normalizeArgs(c.in, vf)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: normalizeArgs(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestLogStamp(t *testing.T) {
	// under `go test`, stdout is a pipe, not a tty → timestamps are enabled
	if stdoutIsTerminal() {
		t.Skip("stdout is a terminal; cannot test redirected mode")
	}
	s := logStamp()
	// format: YYYY-MM-DD HH:MM:SS + space
	if len(s) != 20 {
		t.Fatalf("logStamp() = %q, want 20-char timestamp + space", s)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(s)); err != nil {
		t.Fatalf("logStamp() = %q not parseable: %v", s, err)
	}
}

func TestResolveInterval(t *testing.T) {
	// explicit -interval wins over anything
	if got := resolveInterval(120, "/nonexistent/x.toml"); got != 120 {
		t.Errorf("explicit interval: got %d", got)
	}

	// unreadable config → 600s default
	if got := resolveInterval(0, "/nonexistent/x.toml"); got != 600 {
		t.Errorf("fallback default: got %d", got)
	}

	// config sync_interval wins when no explicit flag
	dir := t.TempDir()
	p := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(p, []byte("sync_interval = 45\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveInterval(0, p); got != 45 {
		t.Errorf("config sync_interval: got %d", got)
	}

	// empty cfgPath resolves via the global config (GNS_CONFIG override)
	t.Setenv("GNS_CONFIG", p)
	if got := resolveInterval(0, ""); got != 45 {
		t.Errorf("global config via GNS_CONFIG: got %d", got)
	}
}

func TestResolveTarget(t *testing.T) {
	// no config file: positional is a raw path
	dir, err := resolveTarget("", "", "/some/path")
	if err != nil || dir != "/some/path" {
		t.Fatalf("raw path: %q, %v", dir, err)
	}

	// flag wins over positional
	dir, err = resolveTarget("", "/flag/path", "name")
	if err != nil || dir != "/flag/path" {
		t.Fatalf("flag priority: %q, %v", dir, err)
	}

	// empty: current directory
	dir, err = resolveTarget("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected cwd")
	}
}
