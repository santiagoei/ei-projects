// Package tui is the cockpit: a lazygit-shaped frame of five panes over a
// scan.Report -- projects top-left, the selected project's repositories
// bottom-left, and down the right edge: a one-line OPEN IN launcher bar for
// the selected repository, then three stacked list panes (commits, opencode
// sessions, vault notes), each with its own cursor. Focus walks panes with
// tab/h/l; the git history loads on demand for the selected repo so the
// standing scan stays cheap.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	paneCommits
	paneSessions
	paneNotes
	paneCount
)

type scanDoneMsg struct {
	report scan.Report
	err    error
}

// commitsDoneMsg carries the repo it belongs to: a reply for a repo the
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
	project int
	repo    int
	cursors map[paneFocus]int

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
	return Model{cfg: cfg, spinner: sp, cursors: map[paneFocus]int{}, loading: true}
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

// listCount is the row count of the focus panes that have their own cursor
// (the list panes; projects/repos move through m.project / m.repo).
func (m Model) listCount(p paneFocus) int {
	switch p {
	case paneCommits:
		return len(m.commits)
	case paneSessions:
		return len(m.sessionsFor())
	case paneNotes:
		return len(m.proj().Notes)
	}
	return 0
}

// selectProject mutates: the receiver MUST be a pointer -- with a value
// receiver the new project silently evaporated when the method returned.
func (m *Model) selectProject(i int) {
	if i == m.project {
		return
	}
	m.project = clamp(i, 0, len(m.report.Projects)-1)
	m.repo = 0
	m.cursors = map[paneFocus]int{}
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
		for _, p := range []paneFocus{paneCommits, paneSessions, paneNotes} {
			m.cursors[p] = clamp(m.cursors[p], 0, m.listCount(p)-1)
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
		m.cursors[paneCommits] = clamp(m.cursors[paneCommits], 0, len(m.commits)-1)
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
		m.focus = (m.focus + 1) % paneCount
		return m, nil
	case "shift+tab", "h", "left", "esc":
		if m.focus == paneProjects {
			if key == "esc" {
				return m, tea.Quit
			}
			m.focus = paneNotes
		} else {
			m.focus--
		}
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

	// Global actions on the selected repo (the OPEN IN bar's promise).
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
		n := m.listCount(m.focus)
		if n == 0 {
			return m, nil
		}
		m.cursors[m.focus] = clamp(m.cursors[m.focus]+dy, 0, n-1)
	}
	return m, nil
}

func (m Model) onEnter() (tea.Model, tea.Cmd) {
	switch m.focus {
	case paneProjects:
		m.focus = paneRepos
	case paneRepos:
		if repo, ok := m.selectedRepo(); ok {
			m.reported(actions.Lazygit(repo.Path), "lazygit", repo.Path)
		}
	case paneCommits:
		return m.actCopy()
	case paneSessions:
		return m.actSession()
	default: // paneNotes
		return m.actNote()
	}
	return m, nil
}

func (m Model) actSession() (tea.Model, tea.Cmd) {
	list := m.sessionsFor()
	i := m.cursors[paneSessions]
	if i < 0 || i >= len(list) {
		return m, nil
	}
	s := list[i]
	m.reported(actions.ResumeSession(s.Directory, s.ID), "resume", s.Title)
	return m, nil
}

func (m Model) actNote() (tea.Model, tea.Cmd) {
	notes := m.proj().Notes
	i := m.cursors[paneNotes]
	if i < 0 || i >= len(notes) {
		return m, nil
	}
	m.reported(actions.OpenFile(notes[i].Path), "open", notes[i].Rel)
	return m, nil
}

func (m Model) actCopy() (tea.Model, tea.Cmd) {
	var what string
	switch m.focus {
	case paneProjects:
		what = m.proj().Path
	case paneRepos:
		if repo, ok := m.selectedRepo(); ok {
			what = repo.Path
		}
	case paneCommits:
		i := m.cursors[paneCommits]
		if i >= 0 && i < len(m.commits) {
			what = m.commits[i].Hash
		}
	case paneSessions:
		list := m.sessionsFor()
		i := m.cursors[paneSessions]
		if i >= 0 && i < len(list) {
			what = list[i].Directory
		}
	default: // paneNotes
		notes := m.proj().Notes
		i := m.cursors[paneNotes]
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
	boldStyle     = lipgloss.NewStyle().Bold(true)
	keyStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func (m Model) View() string {
	if m.width < 60 || m.height < 22 {
		return fmt.Sprintf("ei-projects: resize the window (want >= 60x22, have %dx%d)", m.width, m.height)
	}
	if m.loading && m.report.Projects == nil {
		return fmt.Sprintf("%s scanning projects…", m.spinner.View())
	}

	leftW := clamp(m.width*36/100, 30, 52)
	rightW := m.width - leftW
	// two bottom rows: transient status, then the app shortcut bar
	bodyH := m.height - 2

	projH := clamp(m.height*42/100, 7, m.height-13)
	repoH := bodyH - projH

	// right edge: one launcher row on top, three list panes under it
	const actionH = 3
	rest := bodyH - actionH
	commitsH := clamp(rest*45/100, 5, rest-10)
	sessionsH := clamp(rest*30/100, 5, rest-commitsH-5)
	notesH := rest - commitsH - sessionsH

	projects := m.paneProjects(leftW, projH)
	repos := m.paneRepos(leftW, repoH)
	openIn := m.paneOpenIn(rightW, actionH)
	commits := m.paneCommits(rightW, commitsH)
	sessions := m.paneSessions(rightW, sessionsH)
	notes := m.paneNotes(rightW, notesH)

	left := lipgloss.JoinVertical(lipgloss.Left, projects, repos)
	right := lipgloss.JoinVertical(lipgloss.Left, openIn, commits, sessions, notes)
	frame := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	status := m.statusLine()
	legend := m.legend()
	return frame + "\n" + status + "\n" + legend
}

// legend is the app shortcut bar along the bottom -- the keys that belong
// to the cockpit itself, not to a selected repository (those live in the
// top-right OPEN IN bar).
func (m Model) legend() string {
	keys := []struct{ key, label string }{
		{"tab/h/l", "panes"},
		{"↑/↓", "move"},
		{"enter", "act"},
		{"1-9", "project"},
		{"s", "session"},
		{"d", "note"},
		{"r", "rescan"},
		{"q", "quit"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, keyStyle.Render(k.key)+dimStyle.Render(" "+k.label))
	}
	line := strings.Join(parts, dimStyle.Render(" · "))
	if lipgloss.Width(line) > m.width {
		line = strings.Join(parts[:4], dimStyle.Render(" · "))
	}
	return line
}

// pane renders a bordered box whose title sits ON the top border line --
// "╭─ COMMITS · main ──────╮" lazygit-style -- so the title costs no
// content row. The border characters are the only thing on that row, so
// the whole line is rewritten in the pane's border color.
func pane(active bool, title string, w, h int, body string) string {
	fg := lipgloss.Color("244")
	if active {
		fg = lipgloss.Color("204")
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(fg).
		Width(w - 2).Height(h - 2)
	lines := strings.Split(box.Render(body), "\n")
	if len(lines) > 0 {
		t := clip(title, w-6)
		headW := 4 + lipgloss.Width(t) // "╭─ " + title + " "
		fill := w - headW - 1          // minus the "╮"
		if fill < 1 {
			fill = 1
		}
		base := lipgloss.NewStyle().Foreground(fg)
		lines[0] = base.Render("╭─ ") +
			base.Bold(true).Render(t) +
			base.Render(" "+strings.Repeat("─", fill)+"╮")
	}
	return strings.Join(lines, "\n")
}

func (m Model) paneProjects(w, h int) string {
	var lines []string
	for i, p := range m.report.Projects {
		label := fmt.Sprintf("%d %s", i+1, p.Name)
		switch {
		case p.Missing:
			label += "  " + warnStyle.Render("missing")
		case p.Pending > 0:
			label += fmt.Sprintf("  (%d)", p.Pending)
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
	return pane(m.focus == paneProjects, fmt.Sprintf("PROJECTS · %d", len(m.report.Projects)), w, h,
		listWindow(lines, m.project, h-2))
}

func (m Model) paneRepos(w, h int) string {
	repos := m.proj().Repos
	var lines []string
	for i, r := range repos {
		state := repoState(r)
		label := clip(filepath.Base(r.Path), w-20) + " " + state
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
	return pane(m.focus == paneRepos, title, w, h, listWindow(lines, m.repo, h-2))
}

// repoState is the compact "dirty 3 ↑2 ↓1" suffix used wherever a repo
// is named.
func repoState(r gitx.Repo) string {
	state := "clean"
	switch {
	case r.Error != "":
		state = "error"
		return state
	case r.Dirty:
		state = fmt.Sprintf("dirty %d", r.Changes)
	}
	if r.Ahead > 0 {
		state += fmt.Sprintf(" ↑%d", r.Ahead)
	}
	if r.Behind > 0 {
		state += fmt.Sprintf(" ↓%d", r.Behind)
	}
	return state
}

// paneOpenIn is the one-line launcher bar: the git-status row menu's keys,
// same letters same meaning, header carrying the repo it acts on.
func (m Model) paneOpenIn(w, h int) string {
	title := "OPEN IN"
	if repo, ok := m.selectedRepo(); ok {
		title = clip(fmt.Sprintf("OPEN IN · %s · %s · %s",
			filepath.Base(repo.Path), repo.Branch, repoState(repo)), w-8)
	}
	agent := defaultAgent()
	if agent == "" {
		agent = "agent"
	}
	keys := []struct{ key, label string }{
		{"g", "lazygit"},
		{"t", "terminal"},
		{"c", "code"},
		{"a", agent},
		{"e", "explorer"},
		{"y", "copy path"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, keyStyle.Render(k.key)+dimStyle.Render(" "+k.label))
	}
	return pane(false, title, w, h, strings.Join(parts, warnStyle.Render(" · ")))
}

var (
	agentOnce  sync.Once
	agentCache string
)

// defaultAgent mirrors what the bar panel shows: omarchy stores the
// configured agent name in a plain file. Read once; the cockpit does not
// need to notice a mid-session "omarchy default agent" change.
func defaultAgent() string {
	agentOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		data, err := os.ReadFile(filepath.Join(home, ".config", "omarchy", "defaults", "agent"))
		if err != nil {
			return
		}
		agentCache = strings.TrimSpace(string(data))
	})
	return agentCache
}

func (m Model) paneCommits(w, h int) string {
	repo, ok := m.selectedRepo()
	title := "COMMITS"
	var lines []string
	switch {
	case !ok:
		lines = []string{dimStyle.Render("(no repository selected)")}
	case m.commitsFor != repo.Path:
		title = clip("COMMITS · "+repo.Branch, w-8)
		lines = []string{m.spinner.View() + dimStyle.Render(" loading…")}
	default:
		title = clip(fmt.Sprintf("COMMITS · %s · %d", repo.Branch, len(m.commits)), w-8)
		for i, c := range m.commits {
			line := fmt.Sprintf("%s %-46s", c.Hash, clip(c.Subject, 44)) +
				dimStyle.Render(fmt.Sprintf("%s · %s", c.Author, ageStr(c.When)))
			lines = append(lines, m.row(paneCommits, "> "+clip(line, w-4), i == m.cursors[paneCommits]))
		}
		if len(lines) == 0 {
			lines = []string{dimStyle.Render("(no commits)")}
		}
	}
	return pane(m.focus == paneCommits, title, w, h,
		listWindow(lines, m.cursors[paneCommits], h-2))
}

func (m Model) paneSessions(w, h int) string {
	list := m.sessionsFor()
	scope := "project"
	if repo, ok := m.selectedRepo(); ok {
		scope = filepath.Base(repo.Path)
	}
	title := clip(fmt.Sprintf("SESSIONS · %s · %d", scope, len(list)), w-8)
	var lines []string
	for i, s := range list {
		line := clip(s.Title, 40) + " " +
			dimStyle.Render(fmt.Sprintf("%s · $%.2f · %s", ageStr(s.Updated), s.Cost, filepath.Base(s.Directory)))
		lines = append(lines, m.row(paneSessions, "> "+clip(line, w-4), i == m.cursors[paneSessions]))
	}
	if len(lines) == 0 {
		lines = []string{dimStyle.Render("(no sessions)")}
	}
	return pane(m.focus == paneSessions, title, w, h,
		listWindow(lines, m.cursors[paneSessions], h-2))
}

func (m Model) paneNotes(w, h int) string {
	notes := m.proj().Notes
	title := clip(fmt.Sprintf("NOTES · %d", len(notes)), w-8)
	var lines []string
	for i, n := range notes {
		line := clip(n.Rel, 46) + " " + dimStyle.Render(ageStr(n.Modified))
		lines = append(lines, m.row(paneNotes, "> "+clip(line, w-4), i == m.cursors[paneNotes]))
	}
	if len(lines) == 0 {
		lines = []string{dimStyle.Render("(no notes)")}
	}
	return pane(m.focus == paneNotes, title, w, h,
		listWindow(lines, m.cursors[paneNotes], h-2))
}

// row paints the selection marker for a pane's own list: a selected row in
// a focused pane is the bright cursor; in an unfocused pane it stays
// marked, quietly (bold, no color), so "> " survives a tab away.
func (m Model) row(p paneFocus, text string, selected bool) string {
	if !selected {
		return "  " + strings.TrimPrefix(text, "> ")
	}
	if m.focus == p {
		return selectedStyle.Render(text)
	}
	return boldStyle.Render(text)
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
