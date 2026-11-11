package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/santiagoei/ei-projects/internal/actions"
	"github.com/santiagoei/ei-projects/internal/herdr"
)

// liveRefresh is how often the agents herdr is running are asked about. Short
// on purpose: "is it blocked on me yet" is the question, and the answer is one
// local socket read. It is silent -- no spinner, no flag -- like every tick the
// cockpit runs on its own, because a panel open for seconds must not look busy.
const liveRefresh = 15 * time.Second

type liveDoneMsg struct {
	panes []herdr.Pane
	err   error
}

type liveTickMsg struct{}

// liveCmd asks herdr for its agent panes. Without a herdr binary there is no
// feed at all, and nothing is asked, so the cockpit on a machine without herdr
// is the cockpit it always was.
func (m Model) liveCmd() tea.Cmd {
	if !herdr.Available() {
		return nil
	}
	return func() tea.Msg {
		panes, err := herdr.Snapshot(context.Background())
		return liveDoneMsg{panes: panes, err: err}
	}
}

func liveTick() tea.Cmd {
	if !herdr.Available() {
		return nil
	}
	return tea.Tick(liveRefresh, func(time.Time) tea.Msg { return liveTickMsg{} })
}

// livePanesIn are the agent panes whose working directory is dir or sits under
// it, most urgent first. A project's agents are the ones working in its folder.
func (m Model) livePanesIn(dir string) []herdr.Pane {
	if dir == "" {
		return nil
	}
	sep := string(filepath.Separator)
	var out []herdr.Pane
	for _, p := range m.live {
		if p.Cwd == dir || strings.HasPrefix(p.Cwd, dir+sep) {
			out = append(out, p)
		}
	}
	// stable sort by rank: the order herdr listed them stays within a rank
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && herdr.Rank(out[j].Status) < herdr.Rank(out[j-1].Status); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// liveFor is the agent a session is running in, if one is open. A session is
// matched by its folder: that is where opencode was started, and where herdr
// says the pane's process works.
func (m Model) liveFor(dir string) (herdr.Pane, bool) {
	panes := m.livePanesIn(dir)
	if len(panes) == 0 {
		return herdr.Pane{}, false
	}
	return panes[0], true
}

// blockedAgents counts agents waiting on the user, across every watched project:
// the one number the breadcrumb carries, so a question nobody is looking at is
// still visible from any pane.
func (m Model) blockedAgents() int {
	n := 0
	for _, p := range m.live {
		if p.Status == "blocked" {
			n++
		}
	}
	return n
}

// liveMark is the glyph a session row carries while an agent runs in it. The
// colour is the state: red is a question, cyan is work in progress, dim is idle.
func liveMark(status string) string {
	switch status {
	case "blocked":
		return red("◉")
	case "working":
		return cyan("◉")
	case "done":
		return green("◉")
	}
	return dim("◉")
}

// liveWord is a state written out for the DETAIL block, coloured the same way.
func liveWord(status string) string {
	switch status {
	case "blocked":
		return red("blocked")
	case "working":
		return cyan("working")
	case "done":
		return green("done")
	case "idle":
		return dim("idle")
	}
	return dim("unknown")
}

// liveLines is the part of the DETAIL block about the selected repository's
// agents: a summary, then the two most urgent. Nothing is shown for a repository
// with no agent in it, and a herdr that is installed but not answering says so
// once, instead of looking like a repository with no agents.
func (m Model) liveLines(repoPath string, inner int) []string {
	panes := m.livePanesIn(repoPath)
	if len(panes) == 0 {
		if m.liveErr {
			return []string{dim("herdr: no answer, is the server running?")}
		}
		return nil
	}
	counts := map[string]int{}
	for _, p := range panes {
		counts[p.Status]++
	}
	var parts []string
	for _, s := range []string{"blocked", "working", "done", "idle"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	rows := []string{dim("agents ") + strings.Join(parts, dim(" · "))}
	for i, p := range panes {
		if i == 2 {
			break
		}
		name := clip(p.Name(), 14)
		label := clip(p.Label(), max(4, inner-lipgloss.Width(liveWord(p.Status))-lipgloss.Width(name)-6))
		rows = append(rows, liveWord(p.Status)+" "+name+" "+dim(label))
	}
	return rows
}

// jumpToAgent focuses the herdr pane of an open session and says what happened.
// It moves herdr's focus and raises its window; it writes nothing to the
// project, the session or the git repository.
func (m Model) jumpToAgent(p herdr.Pane) (tea.Model, tea.Cmd) {
	raised, err := actions.FocusAgent(p.ID)
	if err != nil {
		return m, m.setStatus("jump failed: " + firstLine(err.Error()))
	}
	if !raised {
		return m, tea.Batch(m.setStatus(fmt.Sprintf("agent %s focused in herdr; its window is not on screen", p.Name())), m.liveCmd())
	}
	return m, tea.Batch(m.setStatus("agent "+p.Name()+" focused"), m.liveCmd())
}
