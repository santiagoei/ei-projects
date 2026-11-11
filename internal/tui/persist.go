package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The cockpit reopens where it left closed: the last project/repo pair is
// remembered by PATH (the watched list can grow, indices must not be the
// thing persisted) under XDG state home, and so are the focused pane and each
// pane's cursor -- by pane NAME, because a saved number would land on a
// different pane the day the order changes.

type persisted struct {
	Project string         `json:"project"`
	Repo    string         `json:"repo"`
	Focus   string         `json:"focus,omitempty"`
	Cursors map[string]int `json:"cursors,omitempty"`
}

// paneNames are the persisted identity of a focusable pane: stable strings,
// never the iota.
var paneNames = map[paneFocus]string{
	paneProjects: "projects",
	paneRepos:    "repos",
	paneEnv:      "env",
	paneCommits:  "commits",
	paneSessions: "sessions",
	paneNotes:    "notes",
}

func paneNamed(name string) (paneFocus, bool) {
	for p, n := range paneNames {
		if n == name {
			return p, true
		}
	}
	return 0, false
}

func statePath() (string, bool) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "ei-projects", "state.json"), true
}

func loadState() persisted {
	var s persisted
	path, ok := statePath()
	if !ok {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if json.Unmarshal(data, &s) != nil {
		return persisted{}
	}
	return s
}

// persist writes the whole state; failures are silent by design: the worst
// case is reopening at the default selection, never a broken app.
func (m Model) persist() {
	p := m.proj()
	if p.Path == "" {
		return
	}
	s := persisted{Project: p.Path, Focus: paneNames[m.focus]}
	if repo, ok := m.selectedRepo(); ok {
		s.Repo = repo.Path
	}
	s.Cursors = map[string]int{}
	for f, name := range paneNames {
		// projects and repos are not in here: they round-trip by path, which
		// survives a watched list that grew or reordered between runs
		if f == paneProjects || f == paneRepos {
			continue
		}
		if pos := m.cursors[f]; pos != 0 {
			s.Cursors[name] = pos
		}
	}
	path, ok := statePath()
	if !ok {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

// restoreCursors puts every pane back on the row the user left it, as far as
// the rows it has right now allow. A pane whose list still arrives (the
// repo's history, the compose state) keeps its restored number waiting: the
// clamp runs again when the feed lands.
func (m *Model) restoreCursors(s persisted) {
	for name, pos := range s.Cursors {
		p, ok := paneNamed(name)
		if !ok {
			continue
		}
		if p == paneProjects || p == paneRepos {
			continue // they round-trip by path, which is sturdier than a number
		}
		if pos > 0 {
			m.cursors[p] = pos
		}
	}
	if f, ok := paneNamed(s.Focus); ok {
		m.focus = f
	}
	// the ENV pane only exists for a project with a compose file, and the
	// layout drops it on a short window: focus must not sit on a pane that
	// is not painted. Nothing is painted before the first size arrives, so
	// the check has to wait for one too.
	if m.width > 0 && !m.focusVisible() {
		m.focus = paneProjects
	}
	m.clampCursors()
}

// LastSelection is the repository the cockpit had selected the last time it
// ran -- the same path it would restore. The CLI asks it when the user types
// "ei-projects code" with no argument: the keybind and the command should
// reopen the thing they were working on, not the first folder alphabetically.
func LastSelection() (string, bool) {
	s := loadState()
	if s.Repo != "" {
		return s.Repo, true
	}
	return s.Project, s.Project != ""
}
