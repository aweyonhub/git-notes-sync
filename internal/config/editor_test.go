package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// readCfg reads a config file back as a merged Config.
func loadCfg(t *testing.T, p string) *Config {
	t.Helper()
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// collectScalarFields walks a config struct and returns dotted-key → kind
// for every toml-tagged scalar field (bool/int/string). Arrays and blocks
// with no scalar leaves are skipped (they are not settable via `gns config`).
func collectScalarFields(typ reflect.Type, section string, out map[string]string) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		dotted := tag
		if section != "" {
			dotted = section + "." + tag
		}
		switch f.Type.Kind() {
		case reflect.Bool:
			out[dotted] = "bool"
		case reflect.Int:
			out[dotted] = "int"
		case reflect.String:
			out[dotted] = "string"
		case reflect.Slice:
			// arrays (e.g. conflict.text_extensions) are intentionally not
			// editable via `gns config set` — hand-edited
		case reflect.Struct:
			// nested table — recurse (the repos block has no scalar leaves)
			collectScalarFields(f.Type, tag, out)
		}
	}
}

// TestConfigFieldsMatchStruct guards the configFields registry against
// drift: every scalar struct field must be registered (and with the right
// kind), and every registered entry must map to a real struct field.
func TestConfigFieldsMatchStruct(t *testing.T) {
	fields := map[string]string{}
	collectScalarFields(reflect.TypeOf(Config{}), "", fields)

	registered := map[string]string{}
	for _, f := range configFields {
		dotted := f.Key
		if f.Section != "" {
			dotted = f.Section + "." + f.Key
		}
		registered[dotted] = f.Kind
	}

	for dotted, kind := range fields {
		if rk, ok := registered[dotted]; !ok {
			t.Errorf("struct field %q has no configFields entry (register it, or it will be invisible to `gns config`)", dotted)
		} else if rk != kind {
			t.Errorf("field %q kind mismatch: struct=%s registry=%s", dotted, kind, rk)
		}
	}
	for dotted := range registered {
		if _, ok := fields[dotted]; !ok {
			t.Errorf("configFields entry %q has no matching struct field (typo or renamed?)", dotted)
		}
	}
}

func TestSetKey_ReplaceExisting(t *testing.T) {
	p := writeCfg(t, `# header comment
auto_commit = true
sync_interval = 60   # tick

[ai]
type = "api"
timeout = 30
`)
	if err := SetKey(p, "", "sync_interval", "600"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	if !strings.Contains(got, "sync_interval = 600") || !strings.Contains(got, "# tick") {
		t.Errorf("value not replaced / comment not kept:\n%s", got)
	}
	if !strings.Contains(got, "# header comment") {
		t.Errorf("header comment lost:\n%s", got)
	}
	if !strings.Contains(got, `type = "api"`) {
		t.Errorf("unrelated key lost:\n%s", got)
	}
	if loadCfg(t, p).SyncInterval != 600 {
		t.Error("Load did not round-trip the new value")
	}
}

func TestSetKey_AddTopLevelKey(t *testing.T) {
	p := writeCfg(t, `auto_commit = true

[ai]
type = "api"
`)
	if err := SetKey(p, "", "sync_interval", "600"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	// new key must appear before the [ai] header
	idxKey := strings.Index(got, "sync_interval = 600")
	idxHdr := strings.Index(got, "[ai]")
	if idxKey < 0 || idxHdr < 0 || idxKey > idxHdr {
		t.Errorf("top-level key not inserted before header:\n%s", got)
	}
	if loadCfg(t, p).SyncInterval != 600 {
		t.Error("round-trip failed")
	}
}

func TestSetKey_AddNestedKeyExistingSection(t *testing.T) {
	p := writeCfg(t, `[ai]
type = "api"
timeout = 30

[conflict]
strategy = "preserve"
`)
	if err := SetKey(p, "ai", "model", `gpt-x`); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	// model must appear inside [ai], before [conflict]
	idxModel := strings.Index(got, `model = "gpt-x"`)
	idxAI := strings.Index(got, "[ai]")
	idxConflict := strings.Index(got, "[conflict]")
	if idxModel < 0 || idxModel < idxAI || idxModel > idxConflict {
		t.Errorf("nested key not placed inside [ai]:\n%s", got)
	}
	if loadCfg(t, p).AI.Model != "gpt-x" {
		t.Error("round-trip failed")
	}
}

func TestSetKey_AddNestedKeyNewSection(t *testing.T) {
	p := writeCfg(t, `auto_commit = true
`)
	if err := SetKey(p, "ai", "timeout", "90"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	if !strings.Contains(got, "\n[ai]\ntimeout = 90\n") {
		t.Errorf("new section not created:\n%s", got)
	}
	if loadCfg(t, p).AI.TimeoutSec != 90 {
		t.Error("round-trip failed")
	}
}

func TestSetKey_CreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.toml")
	if err := SetKey(p, "", "sync_interval", "600"); err != nil {
		t.Fatal(err)
	}
	if loadCfg(t, p).SyncInterval != 600 {
		t.Error("new file did not round-trip")
	}
}

func TestSetKey_Types(t *testing.T) {
	p := writeCfg(t, `auto_commit = false
commit_message = "static"
commit_debounce = 10
`)
	cases := []struct {
		section, key, raw, wantSubstr string
	}{
		{"", "auto_commit", "true", "auto_commit = true"},
		{"", "commit_message", "ai", `commit_message = "ai"`},
		{"", "commit_debounce", "42", "commit_debounce = 42"},
	}
	for _, c := range cases {
		if err := SetKey(p, c.section, c.key, c.raw); err != nil {
			t.Fatalf("SetKey(%s.%s=%q): %v", c.section, c.key, c.raw, err)
		}
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	for _, c := range cases {
		if !strings.Contains(got, c.wantSubstr) {
			t.Errorf("missing %q in:\n%s", c.wantSubstr, got)
		}
	}
}

func TestSetKey_TypeError(t *testing.T) {
	p := writeCfg(t, `auto_commit = true
`)
	if err := SetKey(p, "", "sync_interval", "abc"); err == nil {
		t.Fatal("expected error for non-integer")
	}
	if err := SetKey(p, "", "auto_commit", "yes"); err == nil {
		t.Fatal("expected error for non-bool")
	}
	// file must be untouched on type error
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "auto_commit = true") {
		t.Error("file modified despite type error")
	}
}

func TestSetKey_UnknownKey(t *testing.T) {
	p := writeCfg(t, `auto_commit = true
`)
	if err := SetKey(p, "", "no_such_key", "1"); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestSetKey_RejectsRepos(t *testing.T) {
	if hint := UnsettableHint("repos"); hint == "" {
		t.Fatal("repos should be flagged unsettable")
	}
}

func TestSetKey_PreservesReposBlock(t *testing.T) {
	p := writeCfg(t, `sync_interval = 600

[[repos]]
name = "notes"
path = "~/notes"

[ai]
type = "api"
`)
	if err := SetKey(p, "", "auto_commit", "false"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	if !strings.Contains(got, `[[repos]]`) || !strings.Contains(got, `path = "~/notes"`) {
		t.Errorf("repos block damaged:\n%s", got)
	}
}

func TestUnsetKey_RemovesLine(t *testing.T) {
	p := writeCfg(t, `auto_commit = true
sync_interval = 600
commit_debounce = 30
`)
	removed, err := UnsetKey(p, "", "sync_interval")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected removed=true")
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	if strings.Contains(got, "sync_interval") {
		t.Errorf("key not removed:\n%s", got)
	}
	if !strings.Contains(got, "auto_commit = true") || !strings.Contains(got, "commit_debounce = 30") {
		t.Errorf("other keys lost:\n%s", got)
	}
}

func TestUnsetKey_Nested(t *testing.T) {
	p := writeCfg(t, `[ai]
type = "api"
timeout = 90
`)
	removed, err := UnsetKey(p, "ai", "timeout")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected removed")
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "timeout") {
		t.Errorf("key not removed:\n%s", raw)
	}
	if !strings.Contains(string(raw), `type = "api"`) {
		t.Errorf("sibling key lost:\n%s", raw)
	}
}

func TestUnsetKey_AbsentIsIdempotent(t *testing.T) {
	p := writeCfg(t, `auto_commit = true
`)
	removed, err := UnsetKey(p, "", "sync_interval")
	if err != nil || removed {
		t.Fatalf("expected removed=false, nil; got %v, %v", removed, err)
	}
}

func TestUnsetKey_MissingFile(t *testing.T) {
	removed, err := UnsetKey(filepath.Join(t.TempDir(), "none.toml"), "", "sync_interval")
	if err != nil || removed {
		t.Fatalf("expected removed=false, nil; got %v, %v", removed, err)
	}
}

func TestFieldValue_Defaults(t *testing.T) {
	cfg := Defaults()
	cases := []struct {
		section, key, want string
	}{
		{"", "sync_interval", "600"},
		{"", "auto_commit", "true"},
		{"", "commit_message", "timestamp"},
		{"ai", "timeout", "60"},
		{"ai", "agent_file", "AGENTS.md"},
		{"conflict", "strategy", "preserve"},
	}
	for _, c := range cases {
		got, ok := FieldValue(cfg, c.section, c.key)
		if !ok {
			t.Errorf("FieldValue(%s.%s) not found", c.section, c.key)
			continue
		}
		if got != c.want {
			t.Errorf("FieldValue(%s.%s) = %q, want %q", c.section, c.key, got, c.want)
		}
	}
}

func TestFieldValue_Unknown(t *testing.T) {
	if _, ok := FieldValue(Defaults(), "", "no_such_key"); ok {
		t.Fatal("expected not-found for unknown key")
	}
}

func TestLookupField(t *testing.T) {
	if _, ok := LookupField("sync_interval"); !ok {
		t.Error("top-level key not found")
	}
	if _, ok := LookupField("ai.timeout"); !ok {
		t.Error("nested key not found")
	}
	if _, ok := LookupField("repos"); ok {
		t.Error("repos must not be in registry")
	}
	if _, ok := LookupField("conflict.text_extensions"); ok {
		t.Error("array must not be in registry")
	}
}

func TestEncodeValue(t *testing.T) {
	cases := []struct {
		kind, raw, want string
	}{
		{"bool", "true", "true"},
		{"bool", "1", "true"},
		{"int", "600", "600"},
		{"string", "hi", `"hi"`},
		{"string", `a"b\c`, `"a\"b\\c"`},
	}
	for _, c := range cases {
		got, err := EncodeValue(c.kind, c.raw)
		if err != nil || got != c.want {
			t.Errorf("EncodeValue(%s,%q) = %q,%v; want %q", c.kind, c.raw, got, err, c.want)
		}
	}
	if _, err := EncodeValue("int", "x"); err == nil {
		t.Error("expected error for bad int")
	}
	if _, err := EncodeValue("bool", "maybe"); err == nil {
		t.Error("expected error for bad bool")
	}
}
