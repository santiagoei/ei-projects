package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/santiagoei/ei-projects/internal/config"
)

// q is not a quit key while a search is open, so the seat must be saved by
// that quit too: otherwise closing a filter with ctrl+c silently throws away
// where the user was standing.
func TestQuitFromTheFilterInputSaves(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1)
	m.focus = paneSessions
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("moo")})
	if !m.filtering {
		t.Fatal("the input did not open")
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if s := loadState(); s.Focus != "sessions" || s.Project != "/p/icampus.fei.org" {
		t.Fatalf("ctrl+c from the filter lost the seat: %+v", s)
	}
}
