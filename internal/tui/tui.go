// Package tui is the cockpit: a lazygit-shaped frame of four panes over a
// scan.Report -- projects top-left, the selected project's repositories
// bottom-left, shortcuts top-right, and a tabbed context pane bottom-right
// (commits, opencode sessions, vault notes). Focus walks panes with
// tab/h/l; the git history loads on demand for the selected repo so the
// standing scan stays cheap.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/santiagoei/ei-projects/internal/actions"
	"github.com/santiagoei/ei-projects/internal/agent"
	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/gitx"
	"github.com/santiagoei/ei-projects/internal/scan"
)

const scanDeadline = 90 * time.Second
const commitDeadline = 20 * time.Second
const commitCount = 15

type paneFocus int

const (
	paneProjects paneFocus = iota
	paneRepos
	paneContext
)

type section int

const (
	sectionCommits section = iota
	sectionSessions
	sectionNotes
)

type scanDoneMsg struct {
	report scan.Report
	err    error
}

// commitsDoneMsg carries the pane it belongs to: a reply for a repo the
// user has already walked past is dropped, never rendered.
type commitsDoneMsg struct {
	repoPath string
	commits  []gitx.Commit
	err      error
}

type Model struct {
	cfg     *config.Config
	report  scan.Report
	focus   paneFocus
	sec     section
	project int
	repo    int
	cursors map[section]int

	loading        bool
	commits        []gitx.Commit
	commitsFor     string
	commitsLoading bool
	spinner        spinner.Model

	width  int
	height int
	status string
}

func New(cfg *config.Config) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
	return Model{cfg: cfg, spinner: sp, cursors: map[section]int{}, loading: true}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), m.spinner.Tick)
}

func (m Model) scanCmd() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), scanDeadline)
		defer cancel()
		return scanDoneMsg{report: scan.Run(ctx, cfg)}
	}
}

// commitsCmd fetches history for the repo the selection points at; nil
// when there is nothing to fetch.
func (m *Model) commitsCmd() tea.Cmd {
	repo, ok := m.selectedRepo()
	if !ok {
		m.commits, m.commitsFor, m.commitsLoading = nil, "", false
		return nil
	}
	if m.commitsFor == repo.Path {
		return nil // already cached for this repo
	}
	path := repo.Path
	m.commitsLoading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commitDeadline)
		defer cancel()
		c, err := gitx.Commits(ctx, path, commitCount)
		return commitsDoneMsg{repoPath: path, commits: c, err: err}
	}
}

// -------------------------------------------------------------- selection

func (m Model) proj() scan.Project {
	if m.project < 0 || m.project >= len(m.report.Projects) {
		return scan.Project{}
	}
	return m.report.Projects[m.project]
}

func (m Model) selectedRepo() (gitx.Repo, bool) {
	repos := m.proj().Repos
	if m.repo < 0 || m.repo >= len(repos) {
		return gitx.Repo{}, false
	}
	return repos[m.repo], true
}

// sessionsFor narrows the project's session list to the selected repo:
// a session belongs to the repo its opencode ran in (the same directory
// prefix rule the scan applied one level up).
func (m Model) sessionsFor() []agent.Session {
	repo, ok := m.selectedRepo()
	scope := m.proj().Path
	if ok {
		scope = repo.Path
	}
	var out []agent.Session
	for _, s := range m.proj().Sessions {
		if s.Directory == scope || strings.HasPrefix(s.Directory, scope+string(filepath.Separator)) {
			out = append(out, s)
		}
	}
	if len(out) == 0 && ok && len(m.proj().Sessions) > 0 {
		// the repo has no sessions of its own yet: show the project's
		return append(out, m.proj().Sessions...)
	}
	return out
}

func (m Model) selectProject(i int) {
	if i == m.project {
		return
	}
	m.project = clamp(i, 0, len(m.report.Projects)-1)
	m.repo = 0
	m.cursors = map[section]int{}
	m.commits = nil
	m.commitsFor = ""
}

// -------------------------------------------------------------- update

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case scanDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.status = "scan failed: " + msg.err.Error()
			return m, nil
		}
		m.report = msg.report
		m.project = clamp(m.project, 0, len(m.report.Projects)-1)
		if repos := m.proj().Repos; m.repo >= len(repos) {
			m.repo = clamp(m.repo, 0, len(repos)-1)
		}
		return m, m.commitsCmd()

	case commitsDoneMsg:
		if repo, ok := m.selectedRepo(); ok && repo.Path == msg.repoPath {
			m.commits = msg.commits
			m.commitsFor = msg.repoPath
			if msg.err != nil {
				m.status = "commits: " + msg.err.Error()
			}
		}
		m.commitsLoading = false
		return m, m.tickIfBusy()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m Model) tickIfBusy() tea.Cmd {
	if m.loading || m.commitsLoading {
		return m.spinner.Tick
	}
	return nil
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		m.loading = true
		return m, tea.Batch(m.scanCmd(), m.spinner.Tick)
	case "tab", "l", "right":
		m.focus = (m.focus + 1) % 3
		return m, nil
	case "shift+tab", "h", "left", "esc":
		if m.focus == paneProjects {
			if key == "esc" {
				return m, tea.Quit
			}
			m.focus = paneContext
		} else {
			m.focus--
		}
		return m, nil
	case "[":
		m.sec = (m.sec + 2) % 3
		return m, nil
	case "]":
		m.sec = (m.sec + 1) % 3
		return m, nil
	}

	// Project jump digits: global, like lazygit's global keys.
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		i := int(key[0] - '1')
		if i < len(m.report.Projects) {
			m.selectProject(i)
		}
		return m, m.commitsCmd()
	}

	// Global actions on the selected repo (the shortcuts pane's promise).
	if fn, ok := m.repoAction(key); ok {
		if repo, has := m.selectedRepo(); has {
			m.reported(fn(repo.Path), key, repo.Path)
		}
		return m, nil
	}

	switch key {
	case "up", "k":
		return m.moveCursor(-1)
	case "down", "j":
		return m.moveCursor(1)
	case "enter":
		return m.onEnter()
	case "s":
		return m.actSession()
	case "d":
		return m.actNote()
	case "y":
		return m.actCopy()
	}
	return m, nil
}

func (m Model) repoAction(key string) (func(string) error, bool) {
	switch key {
	case "g":
		return actions.Lazygit, true
	case "t":
		return actions.Terminal, true
	case "c":
		return actions.Editor, true
	case "a":
		return actions.Agent, true
	case "e":
		return actions.Explore, true
	}
	return nil, false
}

func (m Model) moveCursor(dy int) (tea.Model, tea.Cmd) {
	switch m.focus {
	case paneProjects:
		m.selectProject(m.project + dy)
		return m, m.commitsCmd()
	case paneRepos:
		n := len(m.proj().Repos)
		if n == 0 {
			return m, nil
		}
		m.repo = clamp(m.repo+dy, 0, n-1)
		return m, m.commitsCmd()
	default:
		m.cursors[m.sec] = clamp(m.cursors[m.sec]+dy, 0, m.sectionCount(m.sec)-1)
	}
	return m, nil
}

func (m Model) sectionCount(sec section) int {
	p := m.proj()
	switch sec {
	case sectionCommits:
		return len(m.commits)
	case sectionSessions:
		return len(m.sessionsFor())
	default:
		return len(p.Notes)
	}
}

func (m Model) onEnter() (tea.Model, tea.Cmd) {
	switch m.focus {
	case paneProjects:
		m.focus = paneRepos
	case paneRepos:
		if repo, ok := m.selectedRepo(); ok {
			m.reported(actions.Lazygit(repo.Path), "lazygit", repo.Path)
		}
	default:
		switch m.sec {
		case sectionCommits:
			return m.actCopy()
		case sectionSessions:
			return m.actSession()
		default:
			return m.actNote()
		}
	}
	return m, nil
}

func (m Model) actSession() (tea.Model, tea.Cmd) {
	list := m.sessionsFor()
	i := m.cursors[sectionSessions]
	if i < 0 || i >= len(list) {
		return m, nil
	}
	s := list[i]
	m.reported(actions.ResumeSession(s.Directory, s.ID), "resume", s.Title)
	return m, nil
}

func (m Model) actNote() (tea.Model, tea.Cmd) {
	notes := m.proj().Notes
	i := m.cursors[sectionNotes]
	if i < 0 || i >= len(notes) {
		return m, nil
	}
	m.reported(actions.OpenFile(notes[i].Path), "open", notes[i].Rel)
	return m, nil
}

func (m Model) actCopy() (tea.Model, tea.Cmd) {
	var what string
	switch {
	case m.focus == paneProjects:
		what = m.proj().Path
	case m.focus == paneRepos:
		if repo, ok := m.selectedRepo(); ok {
			what = repo.Path
		}
	case m.sec == sectionCommits:
		i := m.cursors[sectionCommits]
		if i >= 0 && i < len(m.commits) {
			what = m.commits[i].Hash
		}
	case m.sec == sectionSessions:
		list := m.sessionsFor()
		i := m.cursors[sectionSessions]
		if i >= 0 && i < len(list) {
			what = list[i].Directory
		}
	default:
		notes := m.proj().Notes
		i := m.cursors[sectionNotes]
		if i >= 0 && i < len(notes) {
			what = notes[i].Path
		}
	}
	if what == "" {
		return m, nil
	}
	m.reported(actions.Copy(what), "copy", what)
	return m, nil
}

func (m *Model) reported(err error, what, target string) {
	if err != nil {
		m.status = fmt.Sprintf("%s failed: %v", what, err)
		return
	}
	m.status = fmt.Sprintf("%s: %s", what, filepath.Base(target))
}

// -------------------------------------------------------------- view

var (
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("204"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func (m Model) View() string {
	if m.width < 60 || m.height < 14 {
		return fmt.Sprintf("ei-projects: resize the window (want >= 60x14, have %dx%d)", m.width, m.height)
	}
	if m.loading && m.report.Projects == nil {
		return fmt.Sprintf("%s scanning projects…", m.spinner.View())
	}

	leftW := clamp(m.width*36/100, 30, 52)
	rightW := m.width - leftW
	bodyH := m.height - 1 // status row

	projH := clamp(m.height*42/100, 7, m.height-11)
	repoH := bodyH - projH
	shortH := clamp(9, 6, bodyH-6)
	ctxPane := m.paneContext(rightW, bodyH-shortH)

	projects := m.paneProjects(leftW, projH)
	repos := m.paneRepos(leftW, repoH)
	shortcuts := m.paneShortcuts(rightW, shortH)

	left := lipgloss.JoinVertical(lipgloss.Left, projects, repos)
	right := lipgloss.JoinVertical(lipgloss.Left, shortcuts, ctxPane)
	frame := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	status := m.statusLine()
	return frame + "\n" + status
}

func (m Model) border(active bool, title string, w, h int) (lipgloss.Style, string) {
	fg := lipgloss.Color("244")
	if active {
		fg = lipgloss.Color("204")
	}
	return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(fg).
			Width(w - 2).Height(h - 2),
		lipgloss.NewStyle().Bold(true).Foreground(fg).Render(title)
}

func (m Model) paneProjects(w, h int) string {
	var lines []string
	for i, p := range m.report.Projects {
		label := fmt.Sprintf("%d %s", i+1, p.Name)
		switch {
		case p.Missing:
			label += "  " + warnStyle.Render("missing")
		case p.Pending > 0:
			label += "  " + fmt.Sprintf("(%d)", p.Pending)
		}
		if len(label) > w-4 {
			label = label[:w-4]
		}
		if i == m.project {
			lines = append(lines, selectedStyle.Render("> "+label))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	if len(lines) == 0 {
		lines = []string{dimStyle.Render("(no projects)")}
	}
	box, title := m.border(m.focus == paneProjects, fmt.Sprintf("PROJECTS · %d", len(m.report.Projects)), w, h)
	return box.Render(title + "\n" + listWindow(lines, m.project, h-4))
}

func (m Model) paneRepos(w, h int) string {
	repos := m.proj().Repos
	var lines []string
	for i, r := range repos {
		state := "clean"
		switch {
		case r.Error != "":
			state = "error"
		case r.Dirty:
			state = fmt.Sprintf("dirty %d", r.Changes)
		}
		if r.Ahead > 0 {
			state = fmt.Sprintf("%s ↑%d", state, r.Ahead)
		}
		label := clip(filepath.Base(r.Path), w-16) + " " + state
		if i == m.repo {
			lines = append(lines, selectedStyle.Render("> "+clip(label, w-4)))
		} else {
			lines = append(lines, "  "+clip(label, w-4))
		}
	}
	if len(lines) == 0 {
		lines = []string{dimStyle.Render("(no repositories)")}
	}
	title := "REPOSITORIES"
	if n := m.proj().Name; n != "" {
		title += " · " + n
	}
	box, t := m.border(m.focus == paneRepos, title, w, h)
	return box.Render(t + "\n" + listWindow(lines, m.repo, h-4))
}

func (m Model) paneShortcuts(w, h int) string {
	lines := []string{
		"tab/l focus · h/esc back · [ ] section",
		"1-9 project · r rescan · q quit",
		"",
		"g lazygit · t terminal · c editor",
		"a agent · e files · y copy",
	}
	switch m.focus {
	case paneProjects:
		lines = append(lines, "enter → repositories")
	case paneRepos:
		lines = append(lines, "enter → lazygit")
	default:
		switch m.sec {
		case sectionCommits:
			lines = append(lines, "enter/y → copy sha")
		case sectionSessions:
			lines = append(lines, "enter/s → resume session")
		default:
			lines = append(lines, "enter/d → open note")
		}
	}
	body := make([]string, 0, len(lines))
	for _, l := range lines {
		body = append(body, dimStyle.Render(clip(l, w-4)))
	}
	box, t := m.border(false, "SHORTCUTS", w, h)
	return box.Render(t + "\n" + strings.Join(body, "\n"))
}

func (m Model) paneContext(w, h int) string {
	tabs := []string{"COMMITS", "SESSIONS", "NOTES"}
	head := make([]string, 3)
	for i, t := range tabs {
		if section(i) == m.sec {
			head[i] = selectedStyle.Render("[" + t + "]")
		} else {
			head[i] = dimStyle.Render(" " + t + " ")
		}
	}
	header := strings.Join(head, "·")

	var lines []string
	switch m.sec {
	case sectionCommits:
		repo, ok := m.selectedRepo()
		if !ok {
			header += "  " + dimStyle.Render("(no repo selected)")
			break
		}
		header += "  " + dimStyle.Render(clip(repo.Branch+" @ "+filepath.Base(repo.Path), w-lipgloss.Width(header)-4))
		if m.commitsLoading {
			lines = []string{m.spinner.View() + " loading…"}
			break
		}
		if m.commitsFor != repo.Path && len(m.commits) == 0 {
			lines = []string{dimStyle.Render("(no commits)")}
			break
		}
		for i, c := range m.commits {
			line := fmt.Sprintf("%s %-46s %s", c.Hash, clip(c.Subject, 44),
				dimStyle.Render(fmt.Sprintf("%s · %s", c.Author, ageStr(c.When))))
			if i == m.cursors[sectionCommits] {
				line = selectedStyle.Render("> " + clip(line, w-4))
			} else {
				line = "  " + clip(line, w-4)
			}
			lines = append(lines, line)
		}
	case sectionSessions:
		list := m.sessionsFor()
		repo, ok := m.selectedRepo()
		scope := "project"
		if ok {
			scope = filepath.Base(repo.Path)
		}
		header += "  " + dimStyle.Render("("+strconv.Itoa(len(list))+" · "+scope+")")
		for i, s := range list {
			line := clip(s.Title, 40) + " " +
				dimStyle.Render(fmt.Sprintf("%s · $%.2f · %s", ageStr(s.Updated), s.Cost, filepath.Base(s.Directory)))
			if i == m.cursors[sectionSessions] {
				line = selectedStyle.Render("> " + clip(line, w-4))
			} else {
				line = "  " + clip(line, w-4)
			}
			lines = append(lines, line)
		}
	default:
		notes := m.proj().Notes
		header += "  " + dimStyle.Render("("+strconv.Itoa(len(notes))+" · vault)")
		for i, n := range notes {
			line := clip(n.Rel, 46) + " " + dimStyle.Render(ageStr(n.Modified))
			if i == m.cursors[sectionNotes] {
				line = selectedStyle.Render("> " + clip(line, w-4))
			} else {
				line = "  " + clip(line, w-4)
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		lines = []string{dimStyle.Render("(nothing here)")}
	}
	content := header + "\n" + listWindow(lines, m.cursors[m.sec], h-5)
	box, t := m.border(m.focus == paneContext, "CONTEXT", w, h)
	return box.Render(t + "\n" + content)
}

func (m Model) statusLine() string {
	left := m.status
	if left == "" {
		left = dimStyle.Render(clip(shortPath(m.proj().Path), 60))
	}
	right := ""
	if m.loading || m.commitsLoading {
		right = m.spinner.View()
	}
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// listWindow shows items around the selected index, clipping to innerH rows.
func listWindow(items []string, sel, innerH int) string {
	if innerH <= 0 || len(items) == 0 {
		if len(items) == 0 {
			return ""
		}
		innerH = 1
	}
	start := 0
	if sel >= innerH {
		start = sel - innerH/2
	}
	if start > len(items)-innerH {
		start = len(items) - innerH
	}
	if start < 0 {
		start = 0
	}
	end := start + innerH
	if end > len(items) {
		end = len(items)
	}
	return strings.Join(items[start:end], "\n")
}

// -------------------------------------------------------------- helpers

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
	if n <= 0 || len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func shortPath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		p = "~" + strings.TrimPrefix(p, h)
	}
	return p
}
