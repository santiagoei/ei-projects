package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/santiagoei/ei-projects/internal/agent"
	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/gitx"
	"github.com/santiagoei/ei-projects/internal/scan"
	"github.com/santiagoei/ei-projects/internal/vault"
)

func testReport() scan.Report {
	now := time.Now()
	return scan.Report{Projects: []scan.Project{
		{
			Name: "obsidian", Path: "/v/obsidian", RepoCount: 1, Pending: 1,
			Repos: []gitx.Repo{{Path: "/v/obsidian", Branch: "main", Dirty: true, Changes: 1, Pending: true}},
			Sessions: []agent.Session{
				{ID: "s1", Title: "Auditoría", Directory: "/v/obsidian", Updated: now.Add(-time.Hour)},
				{ID: "s2", Title: "Curaduría", Directory: "/v/obsidian", Updated: now.Add(-3 * time.Hour)},
			},
			Notes: []vault.Note{{Path: "/v/n/a.md", Rel: "a.md", Modified: now}},
		},
		{
			// the shape that bit the user: an inner directory with a broken
			// .git -- error repo, empty branch -- plus depth to overflow lists.
			Name: "icampus.fei.org", Path: "/p/icampus.fei.org", RepoCount: 6,
			Repos: []gitx.Repo{
				{Path: "/p/icampus.fei.org/icampus", Error: "fatal: detected dubious ownership in repository at '/p/icampus.fei.org/icampus'\n" +
					"To add an exception for this directory, call:\n\n\tgit config --global --add safe.directory /p/icampus.fei.org/icampus"},
				{Path: "/p/carryover400", Branch: "master"},
				{Path: "/p/moodle405", Branch: "master", Dirty: true, Changes: 3, Ahead: 2, Pending: true},
				{Path: "/p/moodle401", Branch: "master"},
				{Path: "/p/x", Branch: "feature/emailotp-queue-apply", Dirty: true, Changes: 3, Ahead: 12, Behind: 7, Pending: true},
				{Path: "/p/y", Branch: "master"},
			},
			Sessions: []agent.Session{
				{ID: "s1", Title: "Retomar pendientes en local_analytics", Directory: "/p/icampus.fei.org", Updated: now.Add(-2 * time.Hour), Cost: 1.234},
				{ID: "s2", Title: strings.Repeat("sesión larguísima ", 6), Directory: "/p/sub/dir", Updated: now.Add(-26 * time.Hour), Cost: 0.01},
			},
			Notes: []vault.Note{
				{Path: "/v/50 area.tr/tr.fei/upgrade.md", Rel: "50 area.tr/tr.fei/tr.fei.upgrade_a_4.5_OK.md", Modified: now.Add(-2 * time.Hour)},
				{Path: "/v/b.md", Rel: strings.Repeat("nota/reciente/", 6) + "b.md", Modified: now.Add(-4 * time.Hour)},
			},
		},
		{
			Name: "gone", Path: "/missing", Missing: true,
		},
	}}
}

func drive(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var nm tea.Model
		nm, cmd = m.Update(msg)
		m = nm.(Model)
	}
	_ = cmd
	return m
}

// The frame must never exceed the window: the alt-screen has no room for a
// 31st row and the terminal eats the top border to make it.
func TestViewNeverExceedsWindowSize(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // never let a j press touch the real state
	for _, size := range []struct{ w, h int }{
		{120, 30}, {150, 42}, {80, 24}, {200, 50}, {100, 22}, {300, 60},
	} {
		m := New(&config.Config{})
		m = drive(t, m, tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = drive(t, m, scanDoneMsg{report: testReport()})
		check := func(state string) {
			t.Helper()
			view := m.View()
			n := strings.Count(view, "\n") + 1
			// one spare row: a frame that ends on the last terminal row
			// leaves the renderer no cursor line and the top scrolls away
			if n > size.h-1 {
				t.Errorf("%dx%d %s: view is %d lines, window %d\n%s",
					size.w, size.h, state, n, size.h, view)
			}
		}
		check("projects focus")
		for i := 0; i < 3; i++ {
			m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		}
		check("after 3 downs")
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		check("context focus")
		m = drive(t, m, commitsDoneMsg{repoPath: "/p/icampus.fei.org/icampus", commits: nil})
		check("commits loaded")
		// a multi-line git failure must not leak rows into the status line
		m.status = "commits: fatal: detected dubious ownership\nTo add an exception for this directory, call:\n\n\tgit config ..."
		check("multiline status")
		m.help = true
		check("help open")
	}
}

// A click on a pane's row focuses the pane and selects exactly that row,
// using the same geometry the renderer just painted with.
func TestMouseClickSelectsRow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})

	g := m.geom()

	// wheel over the sessions pane moves only its cursor
	r := g.rects[paneSessions]
	before := m.cursors[paneSessions]
	m = drive(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown,
		X: r.x + 10, Y: r.y + 3})
	if m.cursors[paneSessions] != before+1 {
		t.Fatalf("wheel: %d -> %d", before, m.cursors[paneSessions])
	}
	if m.focus == paneSessions {
		t.Fatalf("the wheel must not steal focus")
	}

	r = g.rects[paneProjects]
	click := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
		X: r.x + 5, Y: r.y + 1 + 2} // third visible project row
	m = drive(t, m, click)
	if m.focus != paneProjects || m.project != 2 {
		t.Fatalf("click mapped wrong: focus=%d project=%d (rect %+v)", m.focus, m.project, r)
	}
}

// The selection round-trips through the state file by path.
func TestSelectionPersistsByPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1)
	m.repo = 2
	m.persistSelection()

	p, r := loadSelection()
	if p != "/p/icampus.fei.org" || r != "/p/moodle405" {
		t.Fatalf("roundtrip: %q %q", p, r)
	}

	// a fresh model restores the pair on its first scan
	fresh := New(&config.Config{})
	fresh = drive(t, fresh, tea.WindowSizeMsg{Width: 150, Height: 40})
	fresh = drive(t, fresh, scanDoneMsg{report: testReport()})
	if fresh.project != 1 || fresh.repo != 2 {
		t.Fatalf("restore: project=%d repo=%d", fresh.project, fresh.repo)
	}
}
