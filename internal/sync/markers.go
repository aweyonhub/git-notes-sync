package sync

import (
	"errors"
	"fmt"
	"strings"
)

// Block holds the two sides of one conflict marker region.
type Block struct {
	Ours   []string
	Theirs []string
}

var errUnterminated = errors.New("unterminated conflict marker block")

// parseBlocks splits lines into marker regions. Lines outside regions are
// returned implicitly by applyMode which walks the original lines.
// Marker lines are matched after stripping a trailing \r so CRLF checkouts
// (core.autocrlf) are handled correctly.
func parseBlocks(lines []string) ([]Block, error) {
	var blocks []Block
	var cur *Block
	var side *[]string
	for _, ln := range lines {
		tln := strings.TrimRight(ln, "\r")
		switch {
		case cur == nil && strings.HasPrefix(tln, "<<<<<<< "):
			cur = &Block{}
			side = &cur.Ours
		case cur != nil && tln == "=======":
			side = &cur.Theirs
		case cur != nil && strings.HasPrefix(tln, ">>>>>>> "):
			blocks = append(blocks, *cur)
			cur = nil
			side = nil
		case side != nil:
			*side = append(*side, ln)
		}
	}
	if cur != nil {
		return nil, errUnterminated
	}
	return blocks, nil
}

// applyMode returns file content with markers removed, keeping the chosen
// side: "ours" (top) or "theirs" (bottom).
func applyMode(content, mode string) (string, error) {
	lines := strings.Split(content, "\n")
	blocks, err := parseBlocks(lines)
	if err != nil {
		return "", fmt.Errorf("malformed markers: %w", err)
	}
	if len(blocks) == 0 {
		return content, nil
	}
	var out []string
	bi := 0
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if strings.HasPrefix(strings.TrimRight(ln, "\r"), "<<<<<<< ") {
			b := blocks[bi]
			bi++
			if mode == "theirs" {
				out = append(out, b.Theirs...)
			} else {
				out = append(out, b.Ours...)
			}
			// skip the whole marker region
			for i < len(lines) && !strings.HasPrefix(strings.TrimRight(lines[i], "\r"), ">>>>>>> ") {
				i++
			}
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n"), nil
}
