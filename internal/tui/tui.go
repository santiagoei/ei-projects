// Package tui is the cockpit: a dashboard of projects, and a per-project
// view over its repositories, opencode sessions and vault notes. It renders
// a scan.Report and fires actions; it owns no other logic.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/santiagoei/ei-projects/internal/actions"
	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/scan"
)

const (
	viewDashboard = iota
	viewProject
)

const scanDeadline = 90 * time.Second

type scanDoneMsg struct {
	report scan.Report
	err    error
}

type rowKind int

const (
	rowRepo rowKind = iota
	rowSession
	rowNote
)

type row struct {
	kind rowKind
	idx  int
}

type Model struct {
	cfg     *config.Config
	report  scan.Report
	view    int
	cursor  int // dashboard: project index; project view: row index
	focused int // which project the project view shows
	loading bool
	spinner spinner.Model
	width   int
	height  int
	status  string
}

func New(cfg *config.Config) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
	return Model{cfg: cfg, spinner: sp}
}

func (m Model) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(m.scanNow(), m.spinner.Tick)
}

func (m Model) scanNow() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), scanDeadline)
		defer cancel()
		return scanDoneMsg{report: scan.Run(ctx, cfg)}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case scanDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.status = "scan failed: " + msg.err.Error()
			return m, nil
		}
		m.report = msg.report
		if m.view == viewProject {
			m.cursor = clamp(m.cursor, 0, len(m.rows())-1)
		} else {
			m.cursor = clamp(m.cursor, 0, len(m.report.Projects)-1)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.loading {
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
		return m, nil
	}
	if m.view == viewDashboard {
		return m.dashboardKey(msg)
	}
	return m.projectKey(msg)
}

func (m Model) dashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.report.Projects)
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		m.cursor = clamp(m.cursor-1, 0, n-1)
	case "down", "j":
		m.cursor = clamp(m.cursor+1, 0, n-1)
	case "enter":
		if n > 0 {
			m.view = viewProject
			m.focused = m.cursor
			m.cursor = 0
		}
	case "r":
		m.loading = true
		return m, tea.Batch(m.scanNow(), m.spinner.Tick)
	}
	return m, nil
}

func (m Model) projectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.rows()
	switch msg.String() {
	case "esc", "h", "left":
		m.view = viewDashboard
		m.cursor = m.focused
		m.status = ""
		return m, nil
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.cursor = clamp(m.cursor-1, 0, len(rows)-1)
	case "down", "j":
		m.cursor = clamp(m.cursor+1, 0, len(rows)-1)
	case "r":
		m.loading = true
		return m, tea.Batch(m.scanNow(), m.spinner.Tick)
	case "enter":
		return m.actPrimary(rows)
	case "g":
		return m.actRepo(rows, "lazygit", actions.Lazygit)
	case "t":
		return m.actRepo(rows, "terminal", actions.Terminal)
	case "c":
		return m.actRepo(rows, "editor", actions.Editor)
	case "a":
		return m.actRepo(rows, "agent", actions.Agent)
	case "e":
		return m.actRepo(rows, "files", actions.Explore)
	case "y":
		return m.actCopy(rows)
	case "s":
		return m.actSession(rows)
	case "d":
		return m.actNote(rows)
	}
	return m, nil
}

// rows is the current project's actionable rows in visual order; the cursor
// only ever walks things it can act on, headers are decoration.
func (m Model) rows() []row {
	if m.view != viewProject {
		return nil
	}
	p := m.project()
	var rows []row
	for i := range p.Repos {
		rows = append(rows, row{rowRepo, i})
	}
	for i := range p.Sessions {
		rows = append(rows, row{rowSession, i})
	}
	for i := range p.Notes {
		rows = append(rows, row{rowNote, i})
	}
	return rows
}

func (m Model) project() scan.Project {
	if m.focused < 0 || m.focused >= len(m.report.Projects) {
		return scan.Project{}
	}
	return m.report.Projects[m.focused]
}

// actPrimary: enter means "do the obvious thing with this row".
func (m Model) actPrimary(rows []row) (tea.Model, tea.Cmd) {
	r, ok := selected(rows, m.cursor)
	if !ok {
		return m, nil
	}
	switch r.kind {
	case rowRepo:
		return m.actRepo(rows, "lazygit", actions.Lazygit)
	case rowSession:
		return m.actSession(rows)
	case rowNote:
		return m.actNote(rows)
	}
	return m, nil
}

func (m Model) actRepo(rows []row, name string, fn func(string) error) (tea.Model, tea.Cmd) {
	r, ok := selected(rows, m.cursor)
	if !ok || r.kind != rowRepo {
		return m, nil
	}
	repos := m.project().Repos
	if r.idx >= len(repos) {
		return m, nil
	}
	m.reported(fn(repos[r.idx].Path), name, repos[r.idx].Path)
	return m, nil
}

func (m Model) actSession(rows []row) (tea.Model, tea.Cmd) {
	r, ok := selected(rows, m.cursor)
	if !ok || r.kind != rowSession {
		return m, nil
	}
	sessions := m.project().Sessions
	if r.idx >= len(sessions) {
		return m, nil
	}
	s := sessions[r.idx]
	m.reported(actions.ResumeSession(s.Directory, s.ID), "resume", s.Title)
	return m, nil
}

func (m Model) actNote(rows []row) (tea.Model, tea.Cmd) {
	r, ok := selected(rows, m.cursor)
	if !ok || r.kind != rowNote {
		return m, nil
	}
	notes := m.project().Notes
	if r.idx >= len(notes) {
		return m, nil
	}
	m.reported(actions.OpenFile(notes[r.idx].Path), "open", notes[r.idx].Rel)
	return m, nil
}

func (m Model) actCopy(rows []row) (tea.Model, tea.Cmd) {
	r, ok := selected(rows, m.cursor)
	if !ok {
		return m, nil
	}
	var path string
	p := m.project()
	switch r.kind {
	case rowRepo:
		if r.idx < len(p.Repos) {
			path = p.Repos[r.idx].Path
		}
	case rowSession:
		if r.idx < len(p.Sessions) {
			path = p.Sessions[r.idx].Directory
		}
	case rowNote:
		if r.idx < len(p.Notes) {
			path = p.Notes[r.idx].Path
		}
	}
	if path == "" {
		return m, nil
	}
	m.reported(actions.Copy(path), "copy", path)
	return m, nil
}

func (m *Model) reported(err error, what, target string) {
	if err != nil {
		m.status = fmt.Sprintf("%s failed: %v", what, err)
		return
	}
	if len(target) > 60 {
		target = filepath.Base(target)
	}
	m.status = fmt.Sprintf("%s: %s", what, target)
}

// ---------------------------------------------------------------- rendering
//
// Padding happens on the plain string and the style wraps the padded result:
// lipgloss codes are invisible to the terminal but visible to %-Ns, so
// styling first would shift every column of the selected row.

var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("204"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func (m Model) View() string {
	var b strings.Builder
	switch {
	case m.loading:
		return fmt.Sprintf("%s scanning projects…", m.spinner.View())
	case m.view == viewProject:
		b.WriteString(m.projectView())
	default:
		b.WriteString(m.dashboardView())
	}
	if m.status != "" {
		b.WriteString("\n" + dimStyle.Render(m.status))
	}
	return b.String()
}

func (m Model) dashboardView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("ei-projects") + "  " +
		dimStyle.Render(fmt.Sprintf("%d projects", len(m.report.Projects))) + "\n\n")
	for i, p := range m.report.Projects {
		cursor := "  "
		plain := fmt.Sprintf("%-28s", p.Name)
		if i == m.cursor {
			cursor = "▸ "
			plain = selectedStyle.Render(plain)
		}
		line := cursor + plain
		switch {
		case p.Missing:
			line += warnStyle.Render("missing")
		default:
			repos := fmt.Sprintf("%d repos", p.RepoCount)
			if p.Pending > 0 {
				repos += fmt.Sprintf(" · %d pending", p.Pending)
			} else {
				repos += " · clean"
			}
			line += fmt.Sprintf("%-30s", repos)
			sess := "—"
			if len(p.Sessions) > 0 {
				sess = fmt.Sprintf("⧗ %s", ageStr(p.Sessions[0].Updated))
			}
			line += fmt.Sprintf("%-12s", sess)
			if len(p.Notes) > 0 {
				line += fmt.Sprintf("✎ %d", len(p.Notes))
			}
		}
		b.WriteString(line + "\n")
		if i == m.cursor && len(p.FeedErrors) > 0 {
			b.WriteString("  " + dimStyle.Render(strings.Join(p.FeedErrors, "; ")) + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("↑↓ select · enter open · r refresh · q quit"))
	return b.String()
}

func (m Model) projectView() string {
	p := m.project()
	rows := m.rows()
	var b strings.Builder
	b.WriteString(titleStyle.Render(p.Name) + "  " + dimStyle.Render(shortPath(p.Path)) + "\n\n")

	printed := map[rowKind]bool{}
	for i, r := range rows {
		if !printed[r.kind] {
			printed[r.kind] = true
			switch r.kind {
			case rowRepo:
				b.WriteString(sectionStyle.Render("REPOSITORIES") + "\n")
			case rowSession:
				b.WriteString("\n" + sectionStyle.Render("SESSIONS (opencode)") + "\n")
			case rowNote:
				b.WriteString("\n" + sectionStyle.Render("NOTES (vault)") + "\n")
			}
		}
		cursor := "  "
		line := m.rowText(p, r)
		if i == m.cursor {
			cursor = "▸ "
			line = selectedStyle.Render(line)
		}
		b.WriteString(cursor + line + "\n")
	}
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("nothing here yet") + "\n")
		if len(p.FeedErrors) > 0 {
			b.WriteString(dimStyle.Render(strings.Join(p.FeedErrors, "; ")) + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render(
		"enter act · g lazygit t term c code a agent e files y copy · s resume · d open · esc back · r refresh · q quit"))
	return b.String()
}

func (m Model) rowText(p scan.Project, r row) string {
	switch r.kind {
	case rowRepo:
		if r.idx >= len(p.Repos) {
			return "?"
		}
		repo := p.Repos[r.idx]
		state := "clean"
		switch {
		case repo.Error != "":
			state = repo.Error
		case repo.Dirty:
			state = fmt.Sprintf("dirty %d", repo.Changes)
		}
		tracking := ""
		if repo.Ahead > 0 {
			tracking = fmt.Sprintf(" ↑%d", repo.Ahead)
		}
		if repo.Behind > 0 {
			tracking += fmt.Sprintf(" ↓%d", repo.Behind)
		}
		return fmt.Sprintf("%-30s %s%s  %s", filepath.Base(repo.Path),
			repo.Branch+tracking, strings.Repeat(" ", max(0, 12-len(repo.Branch)-len(tracking))), state)
	case rowSession:
		if r.idx >= len(p.Sessions) {
			return "?"
		}
		s := p.Sessions[r.idx]
		title := clip(s.Title, 50)
		return fmt.Sprintf("%-52s %6s  $%.2f", title, ageStr(s.Updated), s.Cost)
	case rowNote:
		if r.idx >= len(p.Notes) {
			return "?"
		}
		n := p.Notes[r.idx]
		return fmt.Sprintf("%-52s %6s", clip(n.Rel, 50), ageStr(n.Modified))
	}
	return ""
}

// ------------------------------------------------------------------ helpers

func selected(rows []row, cursor int) (row, bool) {
	if cursor < 0 || cursor >= len(rows) {
		return row{}, false
	}
	return rows[cursor], true
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ageStr(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func shortPath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		p = "~" + strings.TrimPrefix(p, h)
	}
	return clip(p, 64)
}
