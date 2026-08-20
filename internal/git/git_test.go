package git

import "testing"

func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under limit", "abc", 10, "abc"},
		{"ascii cut", "abcdef", 4, "abcd"},
		// 你好世界 = 4 runes × 3 bytes; a 7-byte cut would split 世 (byte 2)
		{"multibyte boundary", "你好世界", 7, "你好"},
		{"exact boundary", "你好世界", 6, "你好"},
		{"split first rune", "你好世界", 4, "你"},
		{"empty", "", 3, ""},
	}
	for _, c := range cases {
		got := truncateUTF8(c.in, c.max)
		if got != c.want {
			t.Errorf("%s: truncateUTF8(%q, %d) = %q, want %q", c.name, c.in, c.max, got, c.want)
		}
		if len(got) > c.max {
			t.Errorf("%s: result exceeds max bytes: %d > %d", c.name, len(got), c.max)
		}
	}
}
