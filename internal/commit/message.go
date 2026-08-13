package commit

import (
	"fmt"
	"strings"

	"github.com/aweyonhub/git-notes-sync/internal/git"
)

// maxFileLines caps per-file lines in the summary message.
const maxFileLines = 20

// buildSummary renders the diff summary for a staged state, e.g.:
//
//	files: 3 changed, +42, -8
//	- docroot/10-note/mac/aerospace.md (+20, -3)
//	- docroot/10-note/tools/brew.md (+15, -5)
//	- docroot/20-collect/draft.md (+7)
func buildSummary(g *git.Runner) (string, error) {
	ns, err := g.CachedNumstat()
	if err != nil {
		return "", err
	}
	files, add, del := 0, 0, 0
	for _, n := range ns {
		files++
		if !n.Binary {
			add += n.Added
			del += n.Deleted
		}
	}
	lines := []string{fmt.Sprintf(" files: %d changed, +%d, -%d", files, add, del)}
	shown := 0
	for _, n := range ns {
		if shown >= maxFileLines {
			break
		}
		if n.Binary {
			lines = append(lines, fmt.Sprintf(" - %s (binary)", n.Path))
		} else {
			lines = append(lines, fmt.Sprintf(" - %s (+%d, -%d)", n.Path, n.Added, n.Deleted))
		}
		shown++
	}
	if len(ns) > maxFileLines {
		lines = append(lines, fmt.Sprintf(" - ... and %d more", len(ns)-maxFileLines))
	}
	return strings.Join(lines, "\n"), nil
}
