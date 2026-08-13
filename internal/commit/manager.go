// Package commit decides WHEN to commit (debounce / max_wait) and what the
// commit message looks like (timestamp / static / ai).
package commit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/git-notes-sync/git-notes-sync/internal/ai"
	"github.com/git-notes-sync/git-notes-sync/internal/config"
	"github.com/git-notes-sync/git-notes-sync/internal/git"
)

// state remembers when pending changes were first noticed so that max_wait
// works across stateless cron invocations. Stored in .git/git-notes-sync.state.
type state struct {
	FirstSeen int64 `json:"first_seen"`
}

type Manager struct {
	Repo string
	Cfg  *config.Config
	Logf func(format string, args ...any)
}

func New(repo string, cfg *config.Config, logf func(string, ...any)) *Manager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Manager{Repo: repo, Cfg: cfg, Logf: logf}
}

// CommitIfNeeded commits pending changes, honoring debounce / max_wait when
// automatic is true (called from sync). Returns whether a commit was made.
func (m *Manager) CommitIfNeeded(automatic bool) (bool, error) {
	g := git.NewRunner(m.Repo)
	entries, err := g.Status()
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		m.clearState(g)
		return false, nil
	}

	if automatic {
		lastMod, ok := latestModification(m.Repo, entries)
		now := time.Now()
		if ok && now.Sub(lastMod) < time.Duration(m.Cfg.CommitDebounce)*time.Second {
			firstSeen := m.firstSeen(g)
			if firstSeen == 0 {
				firstSeen = now.Unix()
				m.saveState(g, firstSeen)
			}
			if now.Unix()-firstSeen < int64(m.Cfg.CommitMaxWait) {
				m.Logf("deferring: last change %s ago (< debounce %ds), pending %ds (< max_wait %ds)",
					now.Sub(lastMod).Round(time.Second), m.Cfg.CommitDebounce,
					now.Unix()-firstSeen, m.Cfg.CommitMaxWait)
				return false, nil
			}
			m.Logf("max_wait reached (%ds), forcing commit", m.Cfg.CommitMaxWait)
		} else if ok {
			m.clearState(g)
		}
	}

	return m.commit(g, "")
}

// CommitNow explicitly commits regardless of debounce, with an optional
// message mode override ("ai" for `notes commit-ai`).
func (m *Manager) CommitNow(mode string) (bool, error) {
	g := git.NewRunner(m.Repo)
	entries, err := g.Status()
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	m.clearState(g)
	return m.commit(g, mode)
}

// commit stages everything, builds the message and commits.
func (m *Manager) commit(g *git.Runner, mode string) (bool, error) {
	entries, err := g.Status()
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	if err := g.AddAll(); err != nil {
		return false, err
	}
	summary, err := buildSummary(g)
	if err != nil {
		return false, err
	}
	msg, err := m.message(g, mode, summary)
	if err != nil {
		return false, err
	}
	m.Logf("committing %d change(s)", len(entries))
	if err := g.Commit(msg); err != nil {
		return false, err
	}
	m.clearState(g)
	return true, nil
}

// message builds the commit message per config (timestamp | static | ai),
// falling back on any AI failure.
func (m *Manager) message(g *git.Runner, mode, summary string) (string, error) {
	if mode == "" {
		mode = m.Cfg.CommitMessage
	}
	switch mode {
	case config.MessageAI:
		gen := ai.NewGenerator(&m.Cfg.AI)
		if gen.Enabled() {
			diff, err := g.CachedDiff(m.Cfg.AI.MaxDiffBytes)
			if err == nil && strings.TrimSpace(diff) != "" {
				if msg, aerr := gen.CommitMessage(diff); aerr == nil && strings.TrimSpace(msg) != "" {
					return msg, nil
				} else {
					m.Logf("ai message failed (%v), falling back to %q", aerr, m.Cfg.AIFallback)
				}
			}
		} else {
			m.Logf("ai not configured, falling back to %q", m.Cfg.AIFallback)
		}
		if m.Cfg.AIFallback == config.MessageStatic {
			return m.Cfg.CommitStaticMsg + "\n\n" + summary, nil
		}
		return fmt.Sprintf("notes: %s\n\n%s", time.Now().Format("2006-01-02 15:04"), summary), nil
	case config.MessageStatic:
		return m.Cfg.CommitStaticMsg + "\n\n" + summary, nil
	default: // timestamp
		return fmt.Sprintf("notes: %s\n\n%s", time.Now().Format("2006-01-02 15:04"), summary), nil
	}
}

// latestModification returns the newest mtime among changed files.
// Deletions have no mtime and never block the debounce window.
func latestModification(root string, entries []git.Entry) (time.Time, bool) {
	var latest time.Time
	ok := false
	for _, e := range entries {
		if e.Status[0] == 'D' || e.Status[1] == 'D' {
			continue
		}
		st, err := os.Stat(filepath.Join(root, e.Path))
		if err != nil {
			continue
		}
		if !ok || st.ModTime().After(latest) {
			latest = st.ModTime()
			ok = true
		}
	}
	return latest, ok
}

func (m *Manager) statePath(g *git.Runner) (string, error) {
	gd, err := g.GitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "git-notes-sync.state"), nil
}

func (m *Manager) firstSeen(g *git.Runner) int64 {
	p, err := m.statePath(g)
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	var s state
	if json.Unmarshal(b, &s) != nil {
		return 0
	}
	return s.FirstSeen
}

func (m *Manager) saveState(g *git.Runner, ts int64) {
	p, err := m.statePath(g)
	if err != nil {
		return
	}
	if b, err := json.Marshal(state{FirstSeen: ts}); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

func (m *Manager) clearState(g *git.Runner) {
	p, err := m.statePath(g)
	if err != nil {
		return
	}
	_ = os.Remove(p)
}
