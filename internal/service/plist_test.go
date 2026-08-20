package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testOptions() LaunchOptions {
	return LaunchOptions{
		Label:    "com.git-notes-sync",
		Exe:      "/Users/alice/.local/bin/gns",
		Mode:     ModeInterval,
		Interval: 300,
		Home:     "/Users/alice",
		LogDir:   "/Users/alice/Library/Logs",
	}
}

func TestBuildPlistInterval(t *testing.T) {
	got := buildPlist(testOptions())
	for _, want := range []string{
		"<key>Label</key>",
		"<string>com.git-notes-sync</string>",
		"<string>/Users/alice/.local/bin/gns</string>",
		"<string>sync-all</string>",
		"<key>StartInterval</key>",
		"<integer>300</integer>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>PATH</key>",
		"<string>/Users/alice/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>",
		"<key>HOME</key>",
		"<string>/Users/alice</string>",
		"<string>--log</string>",
		"<string>/Users/alice/Library/Logs/com.git-notes-sync.log</string>",
		"<key>ProcessType</key>",
		"<string>Background</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q\n--- plist ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "daemon") {
		t.Errorf("interval mode must not reference daemon\n%s", got)
	}
	if strings.Contains(got, "KeepAlive") {
		t.Errorf("interval mode must not set KeepAlive\n%s", got)
	}
}

func TestBuildPlistDaemon(t *testing.T) {
	o := testOptions()
	o.Mode = ModeDaemon
	o.Config = "/Users/alice/.config/git-notes-sync/config.toml"
	got := buildPlist(o)
	for _, want := range []string{
		"<string>/Users/alice/.local/bin/gns</string>",
		"<string>daemon</string>",
		"<string>-c</string>",
		"<string>/Users/alice/.config/git-notes-sync/config.toml</string>",
		"<key>KeepAlive</key>",
		"<true/>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q\n--- plist ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "StartInterval") {
		t.Errorf("daemon mode must not set StartInterval\n%s", got)
	}
	if strings.Contains(got, "sync-all") {
		t.Errorf("daemon mode must not run sync-all\n%s", got)
	}
}

func TestBuildPlistEscapesXML(t *testing.T) {
	o := testOptions()
	o.Exe = "/Users/a&b/My Notes/gns <v2>"
	got := buildPlist(o)
	for _, want := range []string{
		"/Users/a&amp;b/My Notes/gns &lt;v2&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing escaped %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "a&b/My") {
		t.Errorf("raw & must be escaped\n%s", got)
	}
}

func TestValidate(t *testing.T) {
	base := testOptions()
	if err := base.Validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*LaunchOptions)
		want string
	}{
		{"empty label", func(o *LaunchOptions) { o.Label = "" }, "label"},
		{"label with space", func(o *LaunchOptions) { o.Label = "com.a b" }, "label"},
		{"empty exe", func(o *LaunchOptions) { o.Exe = "" }, "program"},
		{"interval too small", func(o *LaunchOptions) { o.Interval = 2 }, "interval"},
		{"daemon without config", func(o *LaunchOptions) { o.Mode = ModeDaemon; o.Config = "" }, "config"},
	}
	for _, c := range cases {
		o := base
		c.mut(&o)
		if err := o.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
	}
}

func TestExeDir(t *testing.T) {
	o := testOptions()
	if got := o.ExeDir(); got != "/Users/alice/.local/bin" {
		t.Errorf("ExeDir = %q", got)
	}
	o.Exe = "/gns"
	if got := o.ExeDir(); got != "/" {
		t.Errorf("ExeDir root = %q", got)
	}
}

func TestRequireHome(t *testing.T) {
	base := testOptions()
	if err := requireHome(base); err != nil {
		t.Fatalf("valid home rejected: %v", err)
	}
	if err := requireHome(LaunchOptions{}); err == nil {
		t.Fatal("empty home must be rejected (protects against system-path writes)")
	}
}

// writeGitConfig points git's global config at an isolated temp file and
// disables system-level config (Apple Git ships osxkeychain at system scope).
func writeGitConfig(t *testing.T, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", p)
}

func hasWarn(warns []string, sub string) bool {
	for _, w := range warns {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestPreflightCredentialHelper(t *testing.T) {
	home := "/Users/alice"
	repos := []string{home + "/notes"} // plain path, no TCC warning

	writeGitConfig(t, "")
	warns := Preflight(home, repos)
	if !hasWarn(warns, "credential.helper is not configured") {
		t.Errorf("expected credential warning, got %v", warns)
	}

	writeGitConfig(t, "[credential]\n\thelper = osxkeychain\n")
	warns = Preflight(home, repos)
	if runtime.GOOS == "darwin" {
		if !hasWarn(warns, "osxkeychain") {
			t.Errorf("expected osxkeychain note, got %v", warns)
		}
	} else if hasWarn(warns, "osxkeychain") {
		t.Errorf("osxkeychain note is macOS-only, got %v", warns)
	}
	if hasWarn(warns, "not configured") {
		t.Errorf("osxkeychain must not trigger 'not configured', got %v", warns)
	}

	writeGitConfig(t, "[credential]\n\thelper = store\n")
	warns = Preflight(home, repos)
	if hasWarn(warns, "credential.helper") {
		t.Errorf("store helper must not warn, got %v", warns)
	}
}

func TestPreflightTCCPaths(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("TCC-protected folder checks are macOS-only")
	}
	writeGitConfig(t, "[credential]\n\thelper = store\n") // silence credential warnings
	home := "/Users/alice"

	cases := []struct {
		path, want string
	}{
		{home + "/Documents/notes", "Documents"},
		{home + "/Documents", "Documents"},
		{home + "/Desktop/wiki", "Desktop"},
		{home + "/Downloads/x", "Downloads"},
		{home + "/notes", ""},
		{home + "/DocumentsBackup/x", ""},
	}
	for _, c := range cases {
		warns := Preflight(home, []string{c.path})
		if c.want == "" {
			if hasWarn(warns, "TCC-protected") {
				t.Errorf("%s: unexpected TCC warning %v", c.path, warns)
			}
			continue
		}
		if !hasWarn(warns, "~/"+c.want) {
			t.Errorf("%s: expected TCC warning for %s, got %v", c.path, c.want, warns)
		}
	}
}
