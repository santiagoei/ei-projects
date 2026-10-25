// Package tui is the cockpit: a lazygit-shaped frame of five panes over a
// scan.Report -- projects top-left, the selected project's repositories
// bottom-left, and down the right edge: a one-line OPEN IN launcher bar for
// the selected repository, then three stacked list panes (commits, opencode
// sessions, vault notes). Focus walks panes with tab/h/l, or a click; the
// git history loads on demand for the selected repo so the standing scan
// stays cheap. Colours come from the Omarchy theme; the last selection is
// remembered across launches.
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
	scannedAt      time.Time
	commits        []gitx.Commit
	commitsFor     string
	commitsLoading bool
	spinner        spinner.Model
	help           bool
	restored       bool

	width  int
	height int
	status string
}

func New(cfg *config.Config) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(pal().accent)
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
	case paneProjects:
		return len(m.report.Projects)
	case paneRepos:
		return len(m.proj().Repos)
	case paneCommits:
		return len(m.commits)
	case paneSessions:
		return len(m.sessionsFor())
	case paneNotes:
		return len(m.proj().Notes)
	}
	return 0
}

// cursor/get/set for the two panes whose "cursor" is the project/repo
// index itself rather than an entry of m.cursors.
func (m Model) selectedIndex(p paneFocus) int {
	switch p {
	case paneProjects:
		return m.project
	case paneRepos:
		return m.repo
	default:
		return m.cursors[p]
	}
}

func (m Model) cursorFor(p paneFocus, n int) int {
	return clamp(m.selectedIndex(p), 0, n-1)
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

// commitSelection persists the project/repo pair by path (not index: the
// watched list can grow between sessions) and refreshes the history pane.
func (m *Model) commitSelection() tea.Cmd {
	m.persistSelection()
	return m.commitsCmd()
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
		m.scannedAt = time.Now()
		if !m.restored {
			m.restored = true
			if p, r := loadSelection(); p != "" {
				for i, proj := range m.report.Projects {
					if proj.Path == p {
						m.project = i
						for j, repo := range proj.Repos {
							if repo.Path == r {
								m.repo = j
								break
							}
						}
						break
					}
				}
			}
		}
		m.project = clamp(m.project, 0, len(m.report.Projects)-1)
		if repos := m.proj().Repos; m.repo >= len(repos) {
			m.repo = clamp(m.repo, 0, len(repos)-1)
		}
		for p := range paneCount {
			m.cursors[paneFocus(p)] = clamp(m.cursors[paneFocus(p)], 0, m.listCount(paneFocus(p))-1)
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

	case tea.MouseMsg:
		return m.onMouse(msg)

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

	if m.help {
		switch key {
		case "?", "esc", "q", "ctrl+c":
			m.help = false
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
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
			return m, m.commitSelection()
		}
		return m, nil
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
		return m, m.commitSelection()
	case paneRepos:
		n := len(m.proj().Repos)
		if n == 0 {
			return m, nil
		}
		m.repo = clamp(m.repo+dy, 0, n-1)
		return m, m.commitSelection()
	default:
		n := m.listCount(m.focus)
		if n == 0 {
			return m, nil
		}
		m.cursors[m.focus] = clamp(m.cursors[m.focus]+dy, 0, n-1)
	}
	return m, nil
}

// onMouse routes clicks and wheels through the same geometry the renderer
// used: a click focuses a pane and selects the row under the pointer; a
// wheel nudges the cursor of the pane it hovers.
func (m Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.loading || m.help {
		return m, nil
	}
	g := m.geom()
	step := 0
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		step = -2 // sentinel: click
	case msg.Button == tea.MouseButtonWheelUp:
		step = -1
	case msg.Button == tea.MouseButtonWheelDown:
		step = 1
	default:
		return m, nil
	}
	for p := range paneCount {
		pane := paneFocus(p)
		r, ok := g.rects[pane]
		if !ok || !r.contains(msg.X, msg.Y) {
			continue
		}
		if step == -2 {
			if m.focus != pane {
				m.focus = pane
			}
			inner := r.h - 2
			if inner < 1 {
				break
			}
			n := m.listCount(pane)
			if n == 0 {
				break
			}
			start := listStart(n, m.selectedIndex(pane), inner)
			idx := clamp(start+msg.Y-r.y-1, 0, n-1)
			switch pane {
			case paneProjects:
				m.selectProject(idx)
				return m, m.commitSelection()
			case paneRepos:
				m.repo = idx
				return m, m.commitSelection()
			default:
				m.cursors[pane] = idx
			}
		} else {
			n := m.listCount(pane)
			if n == 0 {
				break
			}
			switch pane {
			case paneProjects:
				m.selectProject(m.project + step)
				return m, m.commitSelection()
			case paneRepos:
				m.repo = clamp(m.repo+step, 0, n-1)
				return m, m.commitSelection()
			default:
				m.cursors[pane] = clamp(m.selectedIndex(pane)+step, 0, n-1)
			}
		}
		return m, nil
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

// every style reads the palette through functions so the (theme) values
// are resolved after the package is initialised.
func selectedStyle() lipgloss.Style {
	p := pal()
	return lipgloss.NewStyle().Bold(true).Foreground(p.selFg).Background(p.sel)
}

func quietStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true)
}

func dim(s string) string {
	return lipgloss.NewStyle().Foreground(pal().faint).Render(s)
}

func accent(s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(pal().accent).Render(s)
}

func green(s string) string {
	return lipgloss.NewStyle().Foreground(pal().green).Render(s)
}

func cyan(s string) string {
	return lipgloss.NewStyle().Foreground(pal().cyan).Render(s)
}

func orange(s string) string {
	return lipgloss.NewStyle().Foreground(pal().orange).Render(s)
}

func red(s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(pal().red).Render(s)
}

func keyStyle(s string) string {
	p := pal()
	return lipgloss.NewStyle().Bold(true).Foreground(p.fg).Render(s)
}

func (m Model) View() string {
	if m.width < 60 || m.height < 22 {
		return fmt.Sprintf("ei-projects: resize the window (want >= 60x22, have %dx%d)", m.width, m.height)
	}
	if m.loading && m.report.Projects == nil {
		return fmt.Sprintf("%s scanning projects…", m.spinner.View())
	}
	if m.help {
		return m.helpView()
	}

	g := m.geom()
	projects := m.paneProjects(g)
	repos := m.paneRepos(g)
	openIn := pane(false, m.openInTitle(g.rightW), g.rightW, g.actionH, g.oiBody)
	commits := m.paneCommits(g)
	sessions := m.paneSessions(g)
	notes := m.paneNotes(g)

	left := lipgloss.JoinVertical(lipgloss.Left, projects, repos)
	right := lipgloss.JoinVertical(lipgloss.Left, openIn, commits, sessions, notes)
	frame := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	status := m.statusLine()
	legend := m.legend()
	return frame + "\n" + status + "\n" + legend
}

// legend is the app shortcut bar along the bottom.
func (m Model) legend() string {
	keys := []struct{ key, label string }{
		{"tab/h/l", "panes"},
		{"↑/↓", "move"},
		{"enter", "act"},
		{"1-9", "project"},
		{"s", "session"},
		{"d", "note"},
		{"?", "help"},
		{"r", "rescan"},
		{"q", "quit"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, keyStyle(k.key)+dim(" "+k.label))
	}
	line := strings.Join(parts, dim(" · "))
	if lipgloss.Width(line) > m.width {
		line = strings.Join(parts[:5], dim(" · "))
	}
	return line
}

// pane renders a bordered box whose title sits ON the top border line --
// "╭─ COMMITS · main ──────" lazygit-style -- so the title costs no
// content row. The border characters are the only thing on that row, so
// the whole line is rewritten in the pane's border color.
func pane(active bool, title string, w, h int, body string) string {
	p := pal()
	fg := p.faint
	if active {
		fg = p.accent
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(fg).
		Width(w - 2).Height(h - 2)
	lines := strings.Split(box.Render(body), "\n")
	if len(lines) > 0 {
		t := clip(title, w-6)
		prefix := "╭─ "
		// prefix + title + " " + fill + "╮" must be exactly w wide
		fill := w - lipgloss.Width(prefix) - lipgloss.Width(t) - 2
		if fill < 1 {
			fill = 1
		}
		base := lipgloss.NewStyle().Foreground(fg)
		lines[0] = base.Render(prefix) + base.Bold(true).Render(t) +
			base.Render(" "+strings.Repeat("─", fill)+"╮")
	}
	return strings.Join(lines, "\n")
}

// fillRow styles a list row: in the active pane the selection becomes a
// full-width bar in the theme's selection colours; in a quiet pane the
// marked row survives in bold only. Callers must hand it text already
// fitted to innerW -- clipping ANSI-styled strings here would cut escape
// sequences in half.
func (m Model) fillRow(p paneFocus, text string, selected bool, innerW int) string {
	if !selected {
		return "  " + strings.TrimPrefix(text, "> ")
	}
	if m.focus == p {
		return selectedStyle().Render(padR(text, innerW))
	}
	return quietStyle().Render("  " + strings.TrimPrefix(text, "> "))
}

func (m Model) paneProjects(g geom) string {
	w, h := g.leftW, g.projH
	inner := w - 4
	var lines []string
	for i, p := range m.report.Projects {
		label := fmt.Sprintf("%d %s", i+1, clip(p.Name, inner-14))
		switch {
		case p.Missing:
			label = padR(label, inner-9) + red("missing")
		case p.Pending > 0:
			label = padR(label, inner-6) + orange(fmt.Sprintf("●%d", p.Pending))
		default:
			label = padR(label, inner-3) + green("✓")
		}
		lines = append(lines, m.fillRow(paneProjects, "> "+label, i == m.project, inner))
	}
	if len(lines) == 0 {
		lines = []string{dim("(no projects)")}
	}
	return pane(m.focus == paneProjects, fmt.Sprintf("PROJECTS · %d", len(m.report.Projects)), w, h,
		listWindow(lines, m.project, h-2))
}

// repoState is the coloured status suffix for one repository. The widest
// form ("●123 ↑45 ↓67", or "✗ " + 10 chars) stays under 12 columns so the
// rows built around it fit any width the caller reserved.
func repoState(r gitx.Repo) string {
	if r.Error != "" {
		return red("✗ " + clip(firstLine(r.Error), 10))
	}
	var parts []string
	if r.Dirty {
		parts = append(parts, orange(fmt.Sprintf("●%d", r.Changes)))
	}
	if r.Ahead > 0 {
		parts = append(parts, green(fmt.Sprintf("↑%d", r.Ahead)))
	}
	if r.Behind > 0 {
		parts = append(parts, cyan(fmt.Sprintf("↓%d", r.Behind)))
	}
	if len(parts) == 0 {
		return green("✓")
	}
	return strings.Join(parts, " ")
}

func (m Model) paneRepos(g geom) string {
	w, h := g.leftW, g.repoH
	inner := w - 4
	repos := m.proj().Repos
	var lines []string
	for i, r := range repos {
		label := padR(clip(filepath.Base(r.Path), inner-14), inner-14) + repoState(r)
		lines = append(lines, m.fillRow(paneRepos, "> "+label, i == m.repo, inner))
	}
	if len(lines) == 0 {
		lines = []string{dim("(no repositories)")}
	}
	title := "REPOSITORIES"
	if n := m.proj().Name; n != "" {
		title += " · " + n
	}
	return pane(m.focus == paneRepos, title, w, h, listWindow(lines, m.repo, h-2))
}

// openInParts is the launcher list: the git-status row menu's keys, same
// letters same meaning.
func openInParts() []struct{ key, label string } {
	agent := defaultAgent()
	if agent == "" {
		agent = "agent"
	}
	return []struct{ key, label string }{
		{"g", "lazygit"},
		{"t", "terminal"},
		{"c", "code"},
		{"a", agent},
		{"e", "explorer"},
		{"y", "copy path"},
	}
}

// openInBody lays the launcher parts out, wrapping between parts so no
// line ever exceeds the pane's inner width: a terminal soft-wrap inside a
// fixed-height pane silently adds rows to the frame, and the frame
// growing past the window is what scrolled the top border away. It
// returns the body and how many rows it used.
func openInBody(innerW int) (string, int) {
	var lines []string
	cur := ""
	for _, k := range openInParts() {
		part := accent(k.key) + " " + dim(k.label)
		glue := dim(" · ")
		if cur != "" && lipgloss.Width(cur+glue+part) > innerW {
			lines = append(lines, cur)
			cur = part
		} else if cur != "" {
			cur += glue + part
		} else {
			cur = part
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n"), len(lines)
}

// openInTitle carries the repo the launchers act on.
func (m Model) openInTitle(w int) string {
	title := "OPEN IN"
	if repo, ok := m.selectedRepo(); ok {
		state := "clean"
		if repo.Error != "" {
			state = "error"
		} else if repo.Dirty {
			state = fmt.Sprintf("dirty %d", repo.Changes)
		}
		title = fmt.Sprintf("OPEN IN · %s · %s · %s",
			filepath.Base(repo.Path), repo.Branch, state)
	}
	return clip(title, w-6)
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

func (m Model) paneCommits(g geom) string {
	w, h := g.rightW, g.commitsH
	inner := w - 4
	repo, ok := m.selectedRepo()
	title := "COMMITS"
	var lines []string
	switch {
	case !ok:
		lines = []string{dim("(no repository selected)")}
	case m.commitsFor != repo.Path:
		title = clip("COMMITS · "+repo.Branch, inner)
		lines = []string{m.spinner.View() + dim(" loading…")}
	default:
		title = clip(fmt.Sprintf("COMMITS · %s · %d", repo.Branch, len(m.commits)), inner)
		for i, c := range m.commits {
			// hash | subject | author | age: fixed visible columns
			line := accent(clip(c.Hash, 9)) + " " +
				padR(clip(c.Subject, inner-36), inner-36) +
				dim(padL(clip(c.Author, 14), 15)+padL(shortAge(c.When), 5))
			lines = append(lines, m.fillRow(paneCommits, "> "+line, i == m.cursors[paneCommits], inner))
		}
		if len(lines) == 0 {
			lines = []string{dim("(no commits)")}
		}
	}
	return pane(m.focus == paneCommits, title, w, h,
		listWindow(lines, m.cursors[paneCommits], h-2))
}

func (m Model) paneSessions(g geom) string {
	w, h := g.rightW, g.sessionsH
	inner := w - 4
	list := m.sessionsFor()
	scope := "project"
	if repo, ok := m.selectedRepo(); ok {
		scope = filepath.Base(repo.Path)
	}
	title := clip(fmt.Sprintf("SESSIONS · %s · %d", scope, len(list)), inner)
	var lines []string
	for i, s := range list {
		meta := dim(padL(shortAge(s.Updated), 5) + " " + padL(fmt.Sprintf("$%.2f", s.Cost), 6))
		line := padR(clip(s.Title, inner-lipgloss.Width(meta)-2), inner-lipgloss.Width(meta)-2) + meta
		lines = append(lines, m.fillRow(paneSessions, "> "+line, i == m.cursors[paneSessions], inner))
	}
	if len(lines) == 0 {
		lines = []string{dim("(no sessions)")}
	}
	return pane(m.focus == paneSessions, title, w, h,
		listWindow(lines, m.cursors[paneSessions], h-2))
}

func (m Model) paneNotes(g geom) string {
	w, h := g.rightW, g.notesH
	inner := w - 4
	notes := m.proj().Notes
	title := clip(fmt.Sprintf("NOTES · %d", len(notes)), inner)
	var lines []string
	for i, n := range notes {
		meta := dim(padL(shortAge(n.Modified), 5))
		line := padR(clip(n.Rel, inner-lipgloss.Width(meta)-2), inner-lipgloss.Width(meta)-2) + meta
		lines = append(lines, m.fillRow(paneNotes, "> "+line, i == m.cursors[paneNotes], inner))
	}
	if len(lines) == 0 {
		lines = []string{dim("(no notes)")}
	}
	return pane(m.focus == paneNotes, title, w, h,
		listWindow(lines, m.cursors[paneNotes], h-2))
}

func (m Model) helpView() string {
	rows := []string{
		accent("panes"),
		"  " + keyStyle("tab") + dim(" / ") + keyStyle("l · →") + dim(" next pane    ") +
			keyStyle("h · ← · esc") + dim(" previous pane (esc quits at projects)") + "\n" +
			"  " + keyStyle("mouse") + dim(" click focuses and selects, wheel moves"),
		accent("projects"),
		"  " + keyStyle("1-9") + dim(" jump to project    ") + keyStyle("enter") + dim(" drill into its repos"),
		accent("repos"),
		"  " + keyStyle("enter") + dim(" or ") + keyStyle("g") + dim(" lazygit    ") +
			keyStyle("t") + dim(" terminal    ") + keyStyle("c") + dim(" code    ") +
			keyStyle("a") + " " + dim("agent    ") + keyStyle("e") + dim(" explorer    ") + keyStyle("y") + dim(" copy path"),
		accent("context panes"),
		"  " + keyStyle("s") + " " + dim("resume session    ") + keyStyle("d") + dim(" open note    ") +
			keyStyle("enter") + dim(" commits: copy sha · sessions: resume · notes: open"),
		accent("app"),
		"  " + keyStyle("r") + dim(" rescan    ") + keyStyle("?") + dim(" this help    ") + keyStyle("q") + dim(" quit"),
	}
	innerW := min(m.width-8, 96)
	body := strings.Join(rows, "\n")
	// stay one row under the window, same rule as the frame: the
	// renderer's cursor needs the last row to exist
	h := min(lipgloss.Height(body)+2, m.height-3)
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center,
		pane(true, "HELP", innerW+2, h, body))
}

func (m Model) statusLine() string {
	left := m.status
	if left == "" {
		left = shortPath(m.proj().Path)
	}
	left = dim(clip(strings.ReplaceAll(left, "\n", " "), m.width/2))
	right := ""
	if m.loading || m.commitsLoading {
		right = m.spinner.View()
	} else if !m.scannedAt.IsZero() {
		age := shortAge(m.scannedAt)
		if age == "now" {
			right = dim("scanned now · ? help")
		} else {
			right = dim("scanned " + age + " ago · ? help")
		}
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
	start := listStart(len(items), sel, innerH)
	end := start + innerH
	if end > len(items) {
		end = len(items)
	}
	return strings.Join(items[start:end], "\n")
}

// -------------------------------------------------------------- helpers

func listStart(n, sel, innerH int) int {
	start := 0
	if sel >= innerH {
		start = sel - innerH/2
	}
	if start > n-innerH {
		start = n - innerH
	}
	if start < 0 {
		start = 0
	}
	return start
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

func padR(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

func padL(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d <= 0 {
		return s
	}
	return strings.Repeat(" ", d) + s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func shortAge(t time.Time) string {
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
