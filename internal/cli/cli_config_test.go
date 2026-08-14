package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	return string(out)
}

func newCfgFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCmdConfig_SetTopLevel(t *testing.T) {
	p := newCfgFile(t, "auto_commit = true\n")
	if err := cmdConfig([]string{"set", "sync_interval", "600", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "sync_interval = 600") {
		t.Errorf("value not written:\n%s", raw)
	}
}

func TestCmdConfig_SetNested(t *testing.T) {
	p := newCfgFile(t, `[ai]
type = "api"
`)
	if err := cmdConfig([]string{"set", "ai.timeout", "90", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "timeout = 90") {
		t.Errorf("nested value not written:\n%s", raw)
	}
}

func TestCmdConfig_SetStringQuoted(t *testing.T) {
	p := newCfgFile(t, "")
	if err := cmdConfig([]string{"set", "commit_message", "ai", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), `commit_message = "ai"`) {
		t.Errorf("string not quoted:\n%s", raw)
	}
}

func TestCmdConfig_SetTypeError(t *testing.T) {
	p := newCfgFile(t, "")
	if err := cmdConfig([]string{"set", "sync_interval", "abc", "-c", p}); err == nil {
		t.Fatal("expected type error")
	}
}

func TestCmdConfig_SetUnknownKey(t *testing.T) {
	p := newCfgFile(t, "")
	if err := cmdConfig([]string{"set", "no_such_key", "1", "-c", p}); err == nil {
		t.Fatal("expected unknown-key error")
	}
}

func TestCmdConfig_SetReposRejected(t *testing.T) {
	p := newCfgFile(t, "")
	err := cmdConfig([]string{"set", "repos", "x", "-c", p})
	if err == nil || !strings.Contains(err.Error(), "gns repos") {
		t.Fatalf("expected repos hint, got: %v", err)
	}
}

func TestCmdConfig_SetArrayRejected(t *testing.T) {
	p := newCfgFile(t, "")
	err := cmdConfig([]string{"set", "conflict.text_extensions", "x", "-c", p})
	if err == nil || !strings.Contains(err.Error(), "array") {
		t.Fatalf("expected array hint, got: %v", err)
	}
}

func TestCmdConfig_Get(t *testing.T) {
	p := newCfgFile(t, "sync_interval = 120\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"get", "sync_interval", "-c", p})
	})
	if strings.TrimSpace(out) != "120" {
		t.Errorf("get = %q, want 120", out)
	}
}

func TestCmdConfig_GetStringQuoted(t *testing.T) {
	p := newCfgFile(t, `commit_message = "static"` + "\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"get", "commit_message", "-c", p})
	})
	if strings.TrimSpace(out) != `"static"` {
		t.Errorf("get string = %q, want \"static\"", out)
	}
}

func TestCmdConfig_GetDefaultsWhenNoFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.toml")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"get", "sync_interval", "-c", p})
	})
	if strings.TrimSpace(out) != "600" {
		t.Errorf("default get = %q, want 600", out)
	}
}

func TestCmdConfig_List(t *testing.T) {
	p := newCfgFile(t, "sync_interval = 30\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"list", "-c", p})
	})
	if !strings.Contains(out, "sync_interval") {
		t.Errorf("list missing sync_interval:\n%s", out)
	}
	if !strings.Contains(out, "[default: 600]") {
		t.Errorf("list should flag overridden default:\n%s", out)
	}
	if !strings.Contains(out, "auto_commit") {
		t.Errorf("list missing other keys:\n%s", out)
	}
}

func TestCmdConfig_Unset(t *testing.T) {
	p := newCfgFile(t, "auto_commit = true\nsync_interval = 30\n")
	if err := cmdConfig([]string{"unset", "sync_interval", "-c", p}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "sync_interval") {
		t.Errorf("key not removed:\n%s", raw)
	}
	if !strings.Contains(string(raw), "auto_commit = true") {
		t.Errorf("sibling key lost:\n%s", raw)
	}
}

func TestCmdConfig_UnsetAbsent(t *testing.T) {
	p := newCfgFile(t, "auto_commit = true\n")
	out := captureStdout(t, func() error {
		return cmdConfig([]string{"unset", "sync_interval", "-c", p})
	})
	if !strings.Contains(out, "not set") {
		t.Errorf("absent unset should say 'not set': %q", out)
	}
}
