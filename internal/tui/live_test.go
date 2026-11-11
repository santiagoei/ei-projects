package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/herdr"
)

// The fixture: project 1 is icampus. Its session is the project folder itself,
// and repository 0 lives in a subfolder of it.
func liveModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1)
	m.repo = 0
	return m
}

func TestLivePanesShowWhereTheyAreAndWhatTheyWant(t *testing.T) {
	m := liveModel(t)
	repo := m.proj().Repos[0].Path // /p/icampus.fei.org/icampus
	m = drive(t, m, liveDoneMsg{panes: []herdr.Pane{
		{ID: "w5:p1", Agent: "opencode", Status: "working", Cwd: repo + "/mod", Title: "OC | moodle fix"},
		{ID: "w6:p2", Agent: "claude", Status: "blocked", Cwd: "/p/icampus.fei.org", TermTitle: "claude"},
	}})

	if !strings.Contains(m.breadcrumb(), "⚠1 blocked") {
		t.Fatalf("a blocked agent must reach the breadcrumb from any pane:\n%s", m.breadcrumb())
	}
	// the session row carries the mark of the agent running in it
	m.focus = paneSessions
	if !strings.Contains(m.paneSessions(m.geom()), "◉") {
		t.Fatal("the session with a live agent has no mark")
	}
	// the DETAIL block of the selected repository lists its own agent only
	detail := strings.Join(m.detailLines(60), "\n")
	if !strings.Contains(detail, "agents") || !strings.Contains(detail, "working") {
		t.Fatalf("DETAIL lost the repository's agent:\n%s", detail)
	}
	if strings.Contains(detail, "blocked") {
		t.Fatalf("DETAIL shows an agent of another folder:\n%s", detail)
	}
}

func TestLiveFeedIsSilentWithoutHerdr(t *testing.T) {
	old := herdr.Bin
	herdr.Bin = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { herdr.Bin = old })
	m := liveModel(t)
	if m.liveCmd() != nil || liveTick() != nil {
		t.Fatal("no herdr binary: nothing is asked and no tick is armed")
	}
	if strings.Contains(m.breadcrumb(), "blocked") {
		t.Fatal("a machine without herdr shows herdr")
	}
}

func TestLiveFeedSaysWhenHerdrDoesNotAnswer(t *testing.T) {
	m := liveModel(t)
	m = drive(t, m, liveDoneMsg{err: os.ErrNotExist})
	if !strings.Contains(strings.Join(m.detailLines(60), "\n"), "no answer") {
		t.Fatal("an installed herdr that does not answer must say so, not look empty")
	}
}

// enter on a session with a live agent goes to the agent, and does not restart
// the session: that is the one behaviour the key changes.
func TestEnterJumpsToTheLiveAgent(t *testing.T) {
	bin, log := fakeHerdrForTUI(t)
	old := herdr.Bin
	herdr.Bin = bin
	t.Cleanup(func() { herdr.Bin = old })

	m := liveModel(t)
	m = drive(t, m, liveDoneMsg{panes: []herdr.Pane{
		{ID: "w6:p2", Agent: "opencode", Status: "idle", Cwd: "/p/icampus.fei.org", Title: "OC"},
	}})
	m.focus = paneSessions
	m.cursors[paneSessions] = 0
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	b, _ := os.ReadFile(log)
	if strings.TrimSpace(string(b)) != "w6:p2" {
		t.Fatalf("enter did not focus the agent's pane: %q", b)
	}
	if !strings.Contains(m.status, "focused") {
		t.Fatalf("the jump said nothing: %q", m.status)
	}
}

func fakeHerdrForTUI(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "focus.log")
	bin = filepath.Join(dir, "herdr")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n  'agent focus') echo \"$3\" >> '" + log + "' ;;\n  *) exit 2 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}
