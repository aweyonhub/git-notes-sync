// Package config: editor.go provides key-level TOML editing for `gns config`.
//
// It edits config files at the line level so comments, unrelated keys, and
// [[repos]] blocks survive. SetKey/UnsetKey only handle scalar leaf fields
// registered in configFields — arrays (text_extensions) and the repos block
// are intentionally not settable here (use `gns repos add/del` / hand-edit).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// FieldSpec describes one settable scalar config field.
type FieldSpec struct {
	Section string // "" = top-level, "ai"/"conflict" = nested table
	Key     string // TOML key
	Kind    string // "bool" | "int" | "string"
	Desc    string
}

// configFields is the registry of settable scalar fields.
// repos (block) and conflict.text_extensions (array) are deliberately absent.
var configFields = []FieldSpec{
	{"", "auto_commit", "bool", "commit worktree changes before syncing"},
	{"", "commit_debounce", "int", "skip commit while newest change < N seconds"},
	{"", "commit_max_wait", "int", "force commit N seconds after first seen"},
	{"", "commit_message", "string", "timestamp | static | ai"},
	{"", "commit_static_message", "string", "static mode message text"},
	{"", "ai_fallback", "string", "timestamp | static (AI failure fallback)"},
	{"", "binary_strategy", "string", "ours | abort (binary conflict)"},
	{"", "sync_interval", "int", "daemon tick, seconds (min 5)"},
	{"", "retry_attempts", "int", "fetch/push retries"},
	{"", "git_timeout", "int", "git command timeout, seconds (0 = no timeout)"},
	{"conflict", "strategy", "string", "preserve | abort (text conflict)"},
	{"ai", "type", "string", "api | command"},
	{"ai", "base_url", "string", "OpenAI-compatible endpoint"},
	{"ai", "model", "string", "model name"},
	{"ai", "api_key_env", "string", "env var holding the API key"},
	{"ai", "command", "string", "command-mode CLI"},
	{"ai", "timeout", "int", "AI call timeout, seconds"},
	{"ai", "max_diff_bytes", "int", "cap diff bytes sent to AI"},
	{"ai", "agent_file", "string", "repo-relative agent instructions file"},
	{"log", "max_size_kb", "int", "rotate log when exceeds this size (KB)"},
	{"log", "max_backups", "int", "number of historical log copies to keep"},
	{"map", "git_root", "string", "map feature: integration repo path"},
	{"map", "map_root", "string", "map feature: machine namespace in the repo"},
	{"map", "mode", "string", "map feature: auto | link | copy"},
	{"map", "sync", "bool", "map feature: run `gns map sync` from the scheduler"},
}

// AllFields returns the list of settable scalar fields.
func AllFields() []FieldSpec {
	out := make([]FieldSpec, len(configFields))
	copy(out, configFields)
	return out
}

// LookupField finds a FieldSpec by dotted notation ("sync_interval" or
// "ai.timeout"). Returns ok=false for unknown keys and for arrays/blocks.
func LookupField(dotted string) (FieldSpec, bool) {
	section, key := splitDotted(dotted)
	for _, f := range configFields {
		if f.Section == section && f.Key == key {
			return f, true
		}
	}
	return FieldSpec{}, false
}

// UnsettableHint returns a non-empty hint when dotted names a known config
// key that `gns config set` refuses to edit (arrays / the repos block).
func UnsettableHint(dotted string) string {
	section, key := splitDotted(dotted)
	switch {
	case section == "" && key == "repos":
		return "use `gns repos add/del` to manage the repo list"
	case section == "conflict" && key == "text_extensions":
		return "text_extensions is an array; edit the config file directly"
	case section == "map" && key == "items":
		return "use `gnm config add/remove` to manage map items"
	}
	return ""
}

// splitDotted splits "ai.timeout" → ("ai", "timeout"); "sync_interval" → ("", "sync_interval").
func splitDotted(dotted string) (section, key string) {
	if i := strings.IndexByte(dotted, '.'); i >= 0 {
		return dotted[:i], dotted[i+1:]
	}
	return "", dotted
}

// Dotted returns the dotted display form of a field.
func (f FieldSpec) Dotted() string {
	if f.Section == "" {
		return f.Key
	}
	return f.Section + "." + f.Key
}

// EncodeValue converts a raw CLI string into a TOML literal per kind.
func EncodeValue(kind, raw string) (string, error) {
	switch kind {
	case "bool":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return "", fmt.Errorf("expected true/false, got %q", raw)
		}
		return strconv.FormatBool(b), nil
	case "int":
		n, err := strconv.Atoi(raw)
		if err != nil {
			return "", fmt.Errorf("expected integer, got %q", raw)
		}
		return strconv.Itoa(n), nil
	case "string":
		return tomlQuote(raw), nil
	}
	return "", fmt.Errorf("unsupported kind %q", kind)
}

// DecodeValue renders a reflected value as a display string (no TOML quoting).
func formatReflect(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.String:
		return v.String(), true
	}
	return "", false
}

// FieldValue reads the effective value of (section, key) from a merged Config
// via reflection on TOML tags. Returns ok=false if the field is unknown.
func FieldValue(cfg *Config, section, key string) (string, bool) {
	if cfg == nil {
		return "", false
	}
	parent := reflect.ValueOf(cfg).Elem()
	if section != "" {
		f := findFieldByTag(parent, section)
		if !f.IsValid() {
			return "", false
		}
		parent = f
	}
	f := findFieldByTag(parent, key)
	if !f.IsValid() {
		return "", false
	}
	return formatReflect(f)
}

func findFieldByTag(v reflect.Value, tag string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("toml") == tag {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

// tomlQuote produces a TOML basic string literal.
func tomlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// SetKey writes rawValue (a CLI string) for (section, key) into cfgPath.
// It validates the key against the registry, encodes the value per kind,
// and edits the file at line level — preserving comments and other keys.
// Creates the file, section header, or key as needed.
func SetKey(cfgPath, section, key, rawValue string) error {
	spec, ok := findSpec(section, key)
	if !ok {
		return fmt.Errorf("unknown config key %q", FieldSpec{section, key, "", ""}.Dotted())
	}
	encoded, err := EncodeValue(spec.Kind, rawValue)
	if err != nil {
		return fmt.Errorf("%s: %w", spec.Dotted(), err)
	}
	return writeKeyValue(cfgPath, section, key, encoded)
}

// UnsetKey removes the key line for (section, key) from cfgPath.
// Returns removed=true if a line was deleted; false if the key was absent
// (idempotent). A missing file yields removed=false, nil.
func UnsetKey(cfgPath, section, key string) (bool, error) {
	content, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	lines := strings.Split(string(content), "\n")
	out := make([]string, 0, len(lines))
	curSection := ""
	removed := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if sec, isHdr := headerSection(t); isHdr {
			curSection = sec
			out = append(out, line)
			continue
		}
		if curSection == section && isKeyLine(t, key) {
			removed = true
			continue // drop
		}
		out = append(out, line)
	}
	if !removed {
		return false, nil
	}
	text := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	return true, os.WriteFile(cfgPath, []byte(text), 0o644)
}

func findSpec(section, key string) (FieldSpec, bool) {
	for _, f := range configFields {
		if f.Section == section && f.Key == key {
			return f, true
		}
	}
	return FieldSpec{}, false
}

// writeKeyValue performs the line-level edit for an encoded value.
func writeKeyValue(cfgPath, section, key, encoded string) error {
	content, err := os.ReadFile(cfgPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := ensureFile(cfgPath); err != nil {
			return err
		}
		content = []byte{}
	}
	lines := strings.Split(string(content), "\n")

	// 1. Try to replace an existing key line in the target section.
	curSection := ""
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if sec, isHdr := headerSection(t); isHdr {
			curSection = sec
			continue
		}
		if curSection == section && isKeyLine(t, key) {
			lines[i] = replaceValue(line, encoded)
			return writeLines(cfgPath, lines)
		}
	}

	// 2. Key not found — insert.
	newLine := key + " = " + encoded
	if section == "" {
		// top-level: insert before the first header line (or at EOF).
		insertAt := len(lines)
		for i, line := range lines {
			if _, isHdr := headerSection(strings.TrimSpace(line)); isHdr {
				insertAt = i
				break
			}
		}
		lines = insertLines(lines, insertAt, newLine)
	} else {
		// nested: find the section header, else create it.
		sectionIdx := -1
		for i, line := range lines {
			if sec, isHdr := headerSection(strings.TrimSpace(line)); isHdr && sec == section {
				sectionIdx = i
				break
			}
		}
		if sectionIdx >= 0 {
			// append after the section's last key (before next header / EOF)
			insertAt := len(lines)
			for i := sectionIdx + 1; i < len(lines); i++ {
				if _, isHdr := headerSection(strings.TrimSpace(lines[i])); isHdr {
					insertAt = i
					break
				}
			}
			lines = insertLines(lines, insertAt, newLine)
		} else {
			// create the section at the end
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, "["+section+"]", newLine)
		}
	}
	return writeLines(cfgPath, lines)
}

// headerSection returns (sectionName, true) for "[ai]" headers and
// ("__block__", true) for "[[repos]]" table-array headers (so top-level
// keys are never matched inside a block). Returns ("", false) otherwise.
func headerSection(trimmed string) (string, bool) {
	if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
		return "__block__", true
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return strings.TrimSpace(trimmed[1 : len(trimmed)-1]), true
	}
	return "", false
}

// isKeyLine reports whether trimmed is `<key> = ...`.
func isKeyLine(trimmed, key string) bool {
	if !strings.HasPrefix(trimmed, key) {
		return false
	}
	rest := strings.TrimLeft(trimmed[len(key):], " \t")
	return strings.HasPrefix(rest, "=")
}

// replaceValue swaps the value in a `key = value [# comment]` line,
// preserving leading indent, the key, and any trailing inline comment.
func replaceValue(line, encoded string) string {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return line
	}
	before := line[:eq+1]
	rest := line[eq+1:]
	_, comment := splitValueComment(rest)
	out := before + " " + encoded
	if comment != "" {
		out += "  " + comment
	}
	return out
}

// splitValueComment splits ` 600  # note` → ("600", "# note").
// Honors TOML basic-string quoting so a '#' inside a string is not a comment.
func splitValueComment(rest string) (value, comment string) {
	inQuote := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == '\\' && inQuote:
			i++ // skip escaped char
		case c == '"':
			inQuote = !inQuote
		case c == '#' && !inQuote:
			return strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i:])
		}
	}
	return strings.TrimSpace(rest), ""
}

func insertLines(lines []string, at int, newLines ...string) []string {
	out := make([]string, 0, len(lines)+len(newLines))
	out = append(out, lines[:at]...)
	out = append(out, newLines...)
	out = append(out, lines[at:]...)
	return out
}

func writeLines(path string, lines []string) error {
	text := strings.Join(lines, "\n")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func ensureFile(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cfgPath, []byte("# git-notes-sync config\n"), 0o644)
}
