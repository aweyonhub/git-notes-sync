package cli

import (
	"reflect"
	"testing"
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
