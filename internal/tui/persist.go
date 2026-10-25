package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The cockpit reopens where it left closed: the last project/repo pair is
// remembered by PATH (the watched list can grow, indices must not be the
// thing persisted) under XDG state home.

type persisted struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
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

func loadSelection() (string, string) {
	path, ok := statePath()
	if !ok {
		return "", ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var s persisted
	if json.Unmarshal(data, &s) != nil {
		return "", ""
	}
	return s.Project, s.Repo
}

// persistSelection writes the current pair; failures are silent by design:
// the worst case is reopening at the default selection, never a broken app.
func (m Model) persistSelection() {
	p := m.proj()
	if p.Path == "" {
		return
	}
	s := persisted{Project: p.Path}
	if repo, ok := m.selectedRepo(); ok {
		s.Repo = repo.Path
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
