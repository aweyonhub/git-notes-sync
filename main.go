// Command notes — Git Notes Sync: automatic sync for notes-style git
// workspaces. See README.md and git-nodes-sync.md (spec).
package main

import (
	"fmt"
	"os"

	"github.com/git-notes-sync/git-notes-sync/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
