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
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/santiagoei/ei-projects/internal/actions"
	"github.com/santiagoei/ei-projects/internal/agent"
	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/env"
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
	paneEnv
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

// todosDoneMsg / envDoneMsg race-guard the same way: each carries the
// identity (session id / compose file) the request was made for.
type todosDoneMsg struct {
	sessionID string
	todos     []agent.Todo
}

type envDoneMsg struct {
	file     string
	services []env.Service
	err      error
}

// statusExpireMsg carries the generation of the line it belongs to: a tick
// armed by an older message must not wipe out the newer one that replaced it.
type statusExpireMsg struct{ gen int }

// statusTTL is how long an action's echo stays on the status line -- long
// enough to read, short enough not to hide the session todos or the feed
// note underneath it.
const statusTTL = 4 * time.Second

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
	todos          []agent.Todo
	todosFor       string
	envSvcs        []env.Service
	envFor         string
	envFetched     bool
	envLoading     bool
	envErr         string
	spinner        spinner.Model
	help           bool
	restored       bool

	width  int
	height int
	status string
	// statusGen counts the status lines set: only the tick armed by the
	// newest one is allowed to clear it.
	statusGen int
}

func New(cfg *config.Config) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(pal().accent)
	loadDefaultAgent()
	return Model{cfg: cfg, spinner: sp, cursors: map[paneFocus]int{}, loading: true}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), m.spinner.Tick, envTick())
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

// todosCmd fetches the open todos of the session the cursor rests on:
// "what was left hanging in here", shown in the status line.
func (m *Model) todosCmd() tea.Cmd {
	id := m.selectedSessionID()
	if id == "" {
		m.todos, m.todosFor = nil, ""
		return nil
	}
	if m.todosFor == id {
		return nil
	}
	m.todosFor = id
	return func() tea.Msg {
		td, err := agent.OpenTodos(id)
		if err != nil {
			return todosDoneMsg{sessionID: id}
		}
		return todosDoneMsg{sessionID: id, todos: td}
	}
}

func (m Model) selectedSessionID() string {
	repo, ok := m.rowAt(paneSessions)
	if !ok {
		return ""
	}
	return m.sessionsFor()[repo.idx].ID
}

// envCmd refreshes the compose service state of the selected project.
func (m *Model) envCmd() tea.Cmd {
	p := m.proj()
	if p.EnvFile == "" {
		m.envSvcs, m.envFor, m.envErr, m.envLoading, m.envFetched = nil, "", "", false, false
		return nil
	}
	if m.envFor == p.EnvFile && (m.envFetched || m.envLoading) {
		return nil // cached for this file (or a request already in flight)
	}
	if !env.Available() {
		m.envFor, m.envFetched, m.envErr = p.EnvFile, true, "docker CLI not installed"
		return nil
	}
	path := p.EnvFile
	m.envFor, m.envFetched, m.envLoading, m.envErr = path, false, true, ""
	return envFetch(path)
}

func envFetch(path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), env.StatusTimeout+2*time.Second)
		defer cancel()
		svcs, err := env.Status(ctx, path)
		return envDoneMsg{file: path, services: svcs, err: err}
	}
}

// envRefresh is how often the ENV pane re-reads compose state, so a
// container that stops or starts shows up without a keypress.
const envRefresh = 5 * time.Second

type envTickMsg struct{}

func envTick() tea.Cmd {
	return tea.Tick(envRefresh, func(time.Time) tea.Msg { return envTickMsg{} })
}

// focusOrder is the tab order, which only includes the ENV pane while the
// selected project actually has one AND the layout can afford to show it
// (geom drops the optional pane on very short windows; focus must not be
// allowed to walk into a pane that is not painted).
func (m Model) focusOrder() []paneFocus {
	order := []paneFocus{paneProjects, paneRepos, paneCommits, paneSessions, paneNotes}
	if m.geom().hasEnv {
		order = append(order[:2], append([]paneFocus{paneEnv}, order[2:]...)...)
	}
	return order
}

// focusVisible reports whether the focused pane is actually on screen: the
// ENV pane exists only for a project with a compose file, and the layout drops
// it on a short window.
func (m Model) focusVisible() bool {
	for _, p := range m.focusOrder() {
		if p == m.focus {
			return true
		}
	}
	return false
}

// cycleFocus moves through the order with wraparound; a focus the order no
// longer contains (the ENV pane vanished with the last project) parks at
// projects instead of falling off the list.
func (m *Model) cycleFocus(step int) {
	m.status = "" // transient feedback belongs to the moment it was made;
	// leaving it up would hide the session todos behind the status
	order := m.focusOrder()
	pos := -1
	for i, p := range order {
		if p == m.focus {
			pos = i
		}
	}
	if pos < 0 {
		m.focus = order[0]
		return
	}
	n := len(order)
	m.focus = order[((pos+step)%n+n)%n]
}

// -------------------------------------------------------------- selection

// item is one row of a pane: the index of the thing it stands for, and the
// plain text a filter matches against.
//
// The cursor of every pane is a POSITION in the pane's visible rows, never an
// index into a backing slice. Those two are the same number while nothing is
// filtered, and different the moment something is -- and acting on the
// position would then hit a row the user is not looking at. Everything that
// needs "the row under the cursor" goes through realIdx, so there is only one
// place where the mapping can be wrong.
type item struct {
	idx   int
	label string
}

// items lists a pane's rows in display order, unfiltered. It is the one place
// that knows what each pane holds: the renderers, the click map, the clamps,
// the layout and every action read through it.
func (m Model) items(p paneFocus) []item {
	switch p {
	case paneProjects:
		out := make([]item, 0, len(m.report.Projects))
		for i, proj := range m.report.Projects {
			out = append(out, item{idx: i, label: proj.Name + " " + proj.Path})
		}
		return out
	case paneRepos:
		repos := m.proj().Repos
		out := make([]item, 0, len(repos))
		for i, r := range repos {
			out = append(out, item{idx: i, label: filepath.Base(r.Path) + " " + r.Branch})
		}
		return out
	case paneEnv:
		out := make([]item, 0, len(m.envSvcs))
		for i, s := range m.envSvcs {
			out = append(out, item{idx: i, label: s.Name + " " + s.State})
		}
		return out
	case paneCommits:
		out := make([]item, 0, len(m.commits))
		for i, c := range m.commits {
			out = append(out, item{idx: i, label: c.Hash + " " + c.Subject + " " + c.Author})
		}
		return out
	case paneSessions:
		list := m.sessionsFor()
		out := make([]item, 0, len(list))
		for i, s := range list {
			out = append(out, item{idx: i, label: s.Title + " " + s.Directory})
		}
		return out
	case paneNotes:
		notes := m.proj().Notes
		out := make([]item, 0, len(notes))
		for i, n := range notes {
			out = append(out, item{idx: i, label: n.Rel})
		}
		return out
	}
	return nil
}

// visible is a pane's rows after the active filter. It is the seam the filter
// hangs on: everything above it is the world, everything below is what the
// user is looking at.
func (m Model) visible(p paneFocus) []item {
	return m.items(p)
}

// rowAt is the item under the pane's cursor.
func (m Model) rowAt(p paneFocus) (item, bool) {
	list := m.visible(p)
	i := m.selectedIndex(p)
	if i < 0 || i >= len(list) {
		return item{}, false
	}
	return list[i], true
}

// realIdx resolves the cursor's position to the index of the row it points
// at; -1 when the pane holds nothing.
func (m Model) realIdx(p paneFocus) int {
	it, ok := m.rowAt(p)
	if !ok {
		return -1
	}
	return it.idx
}

func (m Model) proj() scan.Project {
	i := m.realIdx(paneProjects)
	if i < 0 || i >= len(m.report.Projects) {
		return scan.Project{}
	}
	return m.report.Projects[i]
}

func (m Model) selectedRepo() (gitx.Repo, bool) {
	repos := m.proj().Repos
	i := m.realIdx(paneRepos)
	if i < 0 || i >= len(repos) {
		return gitx.Repo{}, false
	}
	return repos[i], true
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

// listCount is the number of rows a pane shows -- the rows the user can land
// on, which is not the number of items it holds once a filter is on.
func (m Model) listCount(p paneFocus) int {
	return len(m.visible(p))
}

// cursor/get/set for the two panes whose cursor is the project/repo position
// itself rather than an entry of m.cursors. Like every other cursor here, it
// addresses a visible row.
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

func (m Model) cursorFor(p paneFocus) int {
	return clamp(m.selectedIndex(p), 0, m.listCount(p)-1)
}

// selectProject mutates: the receiver MUST be a pointer -- with a value
// receiver the new project silently evaporated when the method returned.
// The argument is a visible row position, the same thing a click or a digit
// key produced.
func (m *Model) selectProject(pos int) {
	if pos == m.project {
		return
	}
	m.project = clamp(pos, 0, m.listCount(paneProjects)-1)
	m.repo = 0
	m.cursors = map[paneFocus]int{}
	m.commits = nil
	m.commitsFor = ""
}

// clampCursors parks every cursor inside the rows its pane actually shows --
// after a rescan, or after a filter hid the row someone was standing on.
func (m *Model) clampCursors() {
	m.project = clamp(m.project, 0, m.listCount(paneProjects)-1)
	m.repo = clamp(m.repo, 0, m.listCount(paneRepos)-1)
	for p := range paneCount {
		f := paneFocus(p)
		if !m.feedReady(f) {
			continue
		}
		m.cursors[f] = m.cursorFor(f)
	}
}

// feedReady is whether a pane has the rows it will show yet. The repo's
// history and the compose state arrive after the scan, so clamping their
// cursors at scan time would park them on row zero and throw away the row the
// user was reading in the previous run; the clamp runs again when the feed
// lands. A pane with nothing on its way (no repository, or a refresh that kept
// its list) is settled and gets clamped now.
func (m Model) feedReady(p paneFocus) bool {
	switch p {
	case paneCommits:
		// with no repository selected nothing is coming; with one, the
		// history is still on its way and its rows cannot be counted yet
		repo, ok := m.selectedRepo()
		return !ok || m.commitsFor == repo.Path
	case paneEnv:
		// a background refresh keeps the rows it already had; the first read
		// of a compose file has nothing to stand on
		return !m.envLoading || m.envFetched
	}
	return true
}

// selectByPath parks the selection on the project/repo pair remembered from
// the last run, by PATH (the watched list can grow, indices must not be the
// thing persisted). Both are positions in the visible rows.
func (m *Model) selectByPath(projectPath, repoPath string) {
	for pos, it := range m.visible(paneProjects) {
		if m.report.Projects[it.idx].Path != projectPath {
			continue
		}
		m.project = pos
		repos := m.proj().Repos
		for rpos, rit := range m.visible(paneRepos) {
			if repos[rit.idx].Path == repoPath {
				m.repo = rpos
				return
			}
		}
		return
	}
}

// commitSelection is what every change of selection must do: persist the
// project/repo pair by path (not index: the watched list can grow between
// sessions) and refresh every feed that hangs off the selection -- history,
// the selected session's todos, the compose state.
func (m *Model) commitSelection() tea.Cmd {
	m.persist()
	return tea.Batch(m.commitsCmd(), m.todosCmd(), m.envCmd())
}

// afterSelection is what a report swap owes the cursors. The order is the
// point: the feeds refresh first (walking onto a project with no repos
// empties the old repo's history), and only then can every cursor be parked
// inside the rows that survived -- clamping before the emptying left a cursor
// standing on row six of a list that no longer has one.
func (m *Model) afterSelection() tea.Cmd {
	m.clampCursors()
	cmd := m.commitSelection()
	m.clampCursors()
	return cmd
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
			return m, m.setStatus("scan failed: " + msg.err.Error())
		}
		m.report = msg.report
		m.scannedAt = time.Now()
		if !m.restored {
			m.restored = true
			s := loadState()
			if s.Project != "" {
				m.selectByPath(s.Project, s.Repo)
				m.restoreCursors(s)
			}
		}
		return m, m.afterSelection()

	case commitsDoneMsg:
		m.commitsLoading = false
		if repo, ok := m.selectedRepo(); ok && repo.Path == msg.repoPath {
			m.commits = msg.commits
			m.commitsFor = msg.repoPath
			if msg.err != nil {
				return m, tea.Batch(m.setStatus("commits: "+firstLine(msg.err.Error())), m.tickIfBusy())
			}
		}
		m.cursors[paneCommits] = m.cursorFor(paneCommits)
		return m, m.tickIfBusy()

	case todosDoneMsg:
		if m.selectedSessionID() != msg.sessionID {
			return m, nil // the cursor moved on; this answer is already stale
		}
		m.todos = msg.todos
		return m, nil

	case envDoneMsg:
		m.envLoading = false
		if m.proj().EnvFile != msg.file {
			return m, m.tickIfBusy()
		}
		m.envSvcs = msg.services
		m.envFetched = true
		m.envErr = ""
		if msg.err != nil {
			m.envErr = firstLine(msg.err.Error())
		}
		m.cursors[paneEnv] = m.cursorFor(paneEnv)
		return m, m.tickIfBusy()

	case envTickMsg:
		if p := m.proj(); p.EnvFile != "" && m.envFetched && !m.envLoading && env.Available() {
			m.envLoading = true
			return m, tea.Batch(envTick(), envFetch(p.EnvFile))
		}
		return m, envTick()

	case statusExpireMsg:
		if msg.gen == m.statusGen && m.status != "" {
			m.status = ""
		}
		return m, nil

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
		// ctrl+d closes the help and pages the pane underneath, like a
		// pager: the chord is taken, and a help screen that swallowed it
		// twice would leave the user stuck on the first row of a long list
		switch key {
		case "?", "esc", "q", "ctrl+c", "ctrl+d":
			m.help = false
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		return m, m.quit()
	case "?":
		m.help = true
		return m, nil
	case "tab", "right":
		m.cycleFocus(1)
		return m, nil
	case "l":
		if m.focus == paneEnv {
			break // over there, l is compose logs
		}
		m.cycleFocus(1)
		return m, nil
	case "shift+tab", "h", "left", "esc":
		if m.focus == paneProjects && key == "esc" {
			return m, m.quit()
		}
		m.cycleFocus(-1)
		return m, nil
	}

	// While the ENV pane is focused its compose verbs win over the global
	// keys they would otherwise steal: r restarts instead of rescanning,
	// d brings services down instead of opening a note.
	if m.focus == paneEnv {
		if handled, cmd := m.envAction(key); handled {
			return m, cmd
		}
	}

	switch key {
	case "r":
		m.loading = true
		// force every on-demand feed to refetch after the rescan
		m.commitsFor, m.todosFor, m.envFetched = "", "", false
		return m, tea.Batch(m.setStatus("rescanning…"), m.scanCmd(), m.spinner.Tick)
	case "T":
		// the Omarchy theme is a file the desktop rewrites under a running
		// cockpit; so is the default agent. Reload both, and the spinner's
		// colour with them -- it captured the palette at construction.
		reloadPalette()
		loadDefaultAgent()
		m.spinner.Style = lipgloss.NewStyle().Foreground(pal().accent)
		return m, m.setStatus("theme reloaded")
	}

	// Project jump digits: global, like lazygit's global keys. The digit is a
	// row position, so with a filter on it means the Nth visible project.
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		i := int(key[0] - '1')
		if i < m.listCount(paneProjects) {
			m.selectProject(i)
			return m, m.commitSelection()
		}
		return m, nil
	}

	// Global actions on the selected repo (the OPEN IN bar's promise).
	if fn, ok := m.repoAction(key); ok {
		if repo, has := m.selectedRepo(); has {
			return m, m.reported(fn(repo.Path), key, repo.Path)
		}
		return m, nil
	}

	switch key {
	case "up", "k":
		return m.moveCursor(-1)
	case "down", "j":
		return m.moveCursor(1)
	case "pgup", "pgdown", "pgdn", "ctrl+u", "ctrl+d", "home", "end":
		return m.movePage(key)
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

// envAction maps the ENV pane's keys onto `docker compose`, always shown
// in a terminal so builds, pulls and logs are watched rather than awaited.
// It reports whether the key was one of the compose verbs, with the cmd that
// expires the status echo it wrote.
func (m *Model) envAction(key string) (bool, tea.Cmd) {
	p := m.proj()
	if p.EnvFile == "" {
		return false, nil
	}
	svc := ""
	if i := m.realIdx(paneEnv); i >= 0 && i < len(m.envSvcs) {
		svc = m.envSvcs[i].Name
	}
	var args []string
	switch key {
	case "u":
		args = []string{"up", "-d"}
	case "s":
		args = []string{"stop"}
		if svc != "" {
			args = append(args, svc)
		}
	case "d":
		args = []string{"down"}
	case "r":
		if svc == "" {
			return true, m.setStatus("restart: no service selected")
		}
		args = []string{"restart", svc}
	case "l":
		args = []string{"logs", "-f"}
		if svc != "" {
			args = append(args, svc)
		}
	case "x":
		if svc == "" {
			return true, m.setStatus("shell: no service selected")
		}
		args = []string{"exec", svc, "sh"}
	default:
		return false, nil
	}
	return true, m.reported(actions.Compose(p.EnvDir, p.EnvFile, args...), "compose", strings.Join(args, " "))
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

// setCursorPos parks a pane's cursor on a visible row and refreshes whatever
// hangs off that row. The pane is an argument, not m.focus, because the mouse
// wheel scrolls the pane it hovers without stealing focus.
func (m Model) setCursorPos(p paneFocus, pos int) (tea.Model, tea.Cmd) {
	n := m.listCount(p)
	if n == 0 {
		return m, nil
	}
	pos = clamp(pos, 0, n-1)
	switch p {
	case paneProjects:
		m.selectProject(pos)
		return m, m.commitSelection()
	case paneRepos:
		m.repo = pos
		return m, m.commitSelection()
	}
	m.cursors[p] = pos
	if p == paneSessions {
		return m, m.todosCmd()
	}
	return m, nil
}

func (m Model) moveCursor(dy int) (tea.Model, tea.Cmd) {
	return m.setCursorPos(m.focus, m.selectedIndex(m.focus)+dy)
}

// movePage jumps by what the pane can actually show. The rows are the height
// its frame has, not the window's: a list pane at the bottom of the frame is
// a few rows tall, and paging by the window height would fly past it and land
// clamped at the end without the user seeing why.
func (m Model) movePage(key string) (tea.Model, tea.Cmd) {
	rows := m.geom().innerH(m.focus)
	switch key {
	case "pgup":
		return m.setCursorPos(m.focus, m.selectedIndex(m.focus)-rows)
	case "pgdown", "pgdn": // bubbletea calls it pgdown; lazygit's name stays too
		return m.setCursorPos(m.focus, m.selectedIndex(m.focus)+rows)
	case "ctrl+u":
		return m.setCursorPos(m.focus, m.selectedIndex(m.focus)-max(1, rows/2))
	case "ctrl+d":
		return m.setCursorPos(m.focus, m.selectedIndex(m.focus)+max(1, rows/2))
	case "home":
		return m.setCursorPos(m.focus, 0)
	default: // "end"
		return m.setCursorPos(m.focus, m.listCount(m.focus)-1)
	}
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
		if step != -2 { // wheel: the hovered pane scrolls, its focus stays put
			return m.setCursorPos(pane, m.selectedIndex(pane)+step)
		}
		// click: the pane takes focus, and the cursor lands on the row under
		// the pointer -- measured against the window of rows the pane painted
		m.focus = pane
		inner, n := r.h-2, m.listCount(pane)
		if inner < 1 || n == 0 {
			return m, nil
		}
		start := listStart(n, m.selectedIndex(pane), inner)
		return m.setCursorPos(pane, start+msg.Y-r.y-1)
	}
	return m, nil
}

func (m Model) onEnter() (tea.Model, tea.Cmd) {
	switch m.focus {
	case paneProjects:
		m.focus = paneRepos
	case paneRepos:
		if repo, ok := m.selectedRepo(); ok {
			return m, m.reported(actions.Lazygit(repo.Path), "lazygit", repo.Path)
		}
	case paneEnv:
		// inert on purpose: compose verbs need a deliberate letter, and
		// an accidental enter should never spin up (or down) a server
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
	i := m.realIdx(paneSessions)
	if i < 0 {
		return m, nil
	}
	s := m.sessionsFor()[i]
	return m, m.reported(actions.ResumeSession(s.Directory, s.ID), "resume", s.Title)
}

func (m Model) actNote() (tea.Model, tea.Cmd) {
	i := m.realIdx(paneNotes)
	if i < 0 {
		return m, nil
	}
	n := m.proj().Notes[i]
	return m, m.reported(actions.OpenFile(n.Path), "open", n.Rel)
}

// copied is the text the cursor stands on, whatever pane holds focus -- the
// row's own identity, resolved through the visible rows so what lands in the
// clipboard is the row the user is looking at and not the one that happens to
// share its position.
func (m Model) copied() string {
	i := m.realIdx(m.focus)
	if i < 0 {
		return ""
	}
	switch m.focus {
	case paneProjects:
		return m.report.Projects[i].Path
	case paneRepos:
		return m.proj().Repos[i].Path
	case paneEnv:
		return m.proj().EnvFile
	case paneCommits:
		return m.commits[i].Hash
	case paneSessions:
		return m.sessionsFor()[i].Directory
	default: // paneNotes
		return m.proj().Notes[i].Path
	}
}

func (m Model) actCopy() (tea.Model, tea.Cmd) {
	what := m.copied()
	if what == "" {
		return m, nil
	}
	return m, m.reported(actions.Copy(what), "copy", what)
}

// setStatus shows a transient line and arms its own disappearance: the status
// line is shared with the session todos and the feed notes, and an echo that
// lived until the next pane cycle hid both of them.
func (m *Model) setStatus(text string) tea.Cmd {
	m.status = text
	m.statusGen++
	gen := m.statusGen
	return tea.Tick(statusTTL, func(time.Time) tea.Msg { return statusExpireMsg{gen: gen} })
}

// reported turns an action's outcome into a status line and returns the cmd
// that expires it -- callers must pass it on or the line never goes away.
func (m *Model) reported(err error, what, target string) tea.Cmd {
	if err != nil {
		return m.setStatus(fmt.Sprintf("%s failed: %v", what, err))
	}
	return m.setStatus(fmt.Sprintf("%s: %s", what, filepath.Base(target)))
}

// quit writes the state one last time on the way out: the last thing a user
// did was usually move a cursor inside a pane, which is not a change of
// selection and so never triggered the save on its own.
func (m *Model) quit() tea.Cmd {
	m.persist()
	return tea.Quit
}

// -------------------------------------------------------------- feed errors

// feedMsg is why a feed is quiet for the selected project, and whether that
// is a failure or merely a fact: per-project errors first, then per-project
// notes, then the report-level notes saying a feed cannot exist here at all.
// A broken feed warns in the pane title; a note is only spelled out while the
// pane holds focus, so a project without docs does not look broken.
func (m Model) feedMsg(p paneFocus) (string, bool) {
	key := feedKey(p)
	if key == "" {
		return "", false
	}
	prefix := key + ": "
	for _, e := range m.proj().FeedErrors {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	// two loops, never one append: the project's slice shares its backing
	// array with the report's, and a third feed note would write into it
	for _, list := range [][]string{m.proj().FeedNotes, m.report.FeedNotes} {
		for _, n := range list {
			if strings.HasPrefix(n, prefix) {
				return strings.TrimPrefix(n, prefix), false
			}
		}
	}
	return "", false
}

// feedErr is the broken-feed reason alone ("" for a note).
func (m Model) feedErr(p paneFocus) string {
	why, broken := m.feedMsg(p)
	if !broken {
		return ""
	}
	return why
}

// feedKey names the feed behind a pane; "" for the panes that read nothing
// (the ENV pane already carries its own docker error, the git panes their ✗).
func feedKey(p paneFocus) string {
	switch p {
	case paneSessions:
		return "sessions"
	case paneNotes:
		return "notes"
	}
	return ""
}

// warn marks a pane whose feed is broken, right in its title.
func (m Model) warn(p paneFocus) string {
	if m.feedErr(p) != "" {
		return " · ⚠"
	}
	return ""
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

	right := []string{openIn}
	if g.hasEnv {
		right = append(right, m.paneEnv(g))
	}
	right = append(right, commits, sessions, notes)

	left := lipgloss.JoinVertical(lipgloss.Left, projects, repos)
	frame := lipgloss.JoinHorizontal(lipgloss.Top, left,
		lipgloss.JoinVertical(lipgloss.Left, right...))

	status := m.statusLine()
	legend := m.legend()
	return frame + "\n" + status + "\n" + legend
}

// legend is the app shortcut bar along the bottom. It says less when there is
// nothing to say: the page keys only appear on a pane whose list is longer
// than the rows it can paint.
func (m Model) legend() string {
	type kb struct{ key, label string }
	var keys, tail []kb
	if m.focus == paneEnv {
		keys = []kb{
			{"u", "up"},
			{"s", "stop"},
			{"d", "down"},
			{"r", "restart"},
			{"l", "logs"},
			{"x", "shell"},
			{"tab", "panes"},
		}
		tail = []kb{{"?", "help"}, {"q", "quit"}}
	} else {
		keys = []kb{
			{"tab/h/l", "panes"},
			{"↑/↓", "move"},
			{"enter", "act"},
			{"1-9", "project"},
			{"s", "session"},
			{"d", "note"},
		}
		tail = []kb{{"?", "help"}, {"r", "rescan"}, {"q", "quit"}}
	}
	if m.listCount(m.focus) > m.geom().innerH(m.focus) {
		keys = append(keys, kb{"pgup/dn", "page"}, kb{"home/end", "top"})
	}
	parts := make([]string, 0, len(keys)+len(tail))
	for _, k := range append(keys, tail...) {
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
	for pos, it := range m.visible(paneProjects) {
		p := m.report.Projects[it.idx]
		label := fmt.Sprintf("%d %s", pos+1, clip(p.Name, inner-14))
		switch {
		case p.Missing:
			label = padR(label, inner-9) + red("missing")
		case p.Pending > 0:
			label = padR(label, inner-6) + orange(fmt.Sprintf("●%d", p.Pending))
		default:
			label = padR(label, inner-3) + green("✓")
		}
		lines = append(lines, m.fillRow(paneProjects, "> "+label, pos == m.project, inner))
	}
	if len(lines) == 0 {
		lines = []string{dim("(no projects)")}
	}
	return pane(m.focus == paneProjects, fmt.Sprintf("PROJECTS · %d", len(m.report.Projects)), w, h,
		listWindow(lines, m.project, h-2, inner))
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
	for pos, it := range m.visible(paneRepos) {
		r := repos[it.idx]
		label := padR(clip(filepath.Base(r.Path), inner-14), inner-14) + repoState(r)
		lines = append(lines, m.fillRow(paneRepos, "> "+label, pos == m.repo, inner))
	}
	if len(lines) == 0 {
		lines = []string{dim("(no repositories)")}
	}
	title := "REPOSITORIES"
	if n := m.proj().Name; n != "" {
		title += " · " + n
	}
	return pane(m.focus == paneRepos, title, w, h, listWindow(lines, m.repo, h-2, inner))
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

var agentCache string

// defaultAgent mirrors what the bar panel shows: omarchy stores the
// configured agent name in a plain file. The launcher label has to agree with
// the agent that will actually start, so the file is read again when the
// cockpit reloads its theme.
func defaultAgent() string { return agentCache }

func loadDefaultAgent() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "omarchy", "defaults", "agent"))
	if err != nil {
		return
	}
	agentCache = strings.TrimSpace(string(data))
}

// paneEnv lists the selected project's compose services with their state;
// the whole project's environment is at a glance and a keystroke away.
func (m Model) paneEnv(g geom) string {
	w, h := g.rightW, g.envH
	p := m.proj()
	title := "ENV · " + filepath.Base(p.EnvDir)
	up := 0
	for _, s := range m.envSvcs {
		if s.State == "running" {
			up++
		}
	}
	if len(m.envSvcs) > 0 {
		title += fmt.Sprintf(" · %d/%d up", up, len(m.envSvcs))
	}
	inner := w - 4
	var lines []string
	switch {
	case m.envLoading && !m.envFetched:
		lines = []string{m.spinner.View() + dim(" docker compose ps…")}
	case m.envErr != "":
		lines = []string{red("✗ ") + dim(clip(m.envErr, inner-2))}
	case len(m.envSvcs) == 0:
		lines = []string{dim("(no containers — compose down?)")}
	default:
		for pos, it := range m.visible(paneEnv) {
			s := m.envSvcs[it.idx]
			state := orange(s.State)
			switch s.State {
			case "running":
				state = green("▲ running")
			case "exited", "dead":
				state = red("● " + s.State)
			}
			switch s.Health {
			case "healthy":
				state += green(" ✓")
			case "unhealthy":
				state += red(" unhealthy")
			}
			nameW := max(1, min(inner-16, 24))
			line := padR(clip(s.Name, nameW), nameW) + state
			lines = append(lines, m.fillRow(paneEnv, "> "+line, pos == m.cursors[paneEnv], inner))
		}
	}
	return pane(m.focus == paneEnv, clip(title, w-6), w, h,
		listWindow(lines, m.cursors[paneEnv], h-2, inner))
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
		for pos, it := range m.visible(paneCommits) {
			c := m.commits[it.idx]
			// hash | subject | author | age: fixed visible columns
			line := accent(clip(c.Hash, 9)) + " " +
				padR(clip(c.Subject, inner-36), inner-36) +
				dim(padL(clip(c.Author, 14), 15)+padL(shortAge(c.When), 5))
			lines = append(lines, m.fillRow(paneCommits, "> "+line, pos == m.cursors[paneCommits], inner))
		}
		if len(lines) == 0 {
			lines = []string{dim("(no commits)")}
		}
	}
	return pane(m.focus == paneCommits, title, w, h,
		listWindow(lines, m.cursors[paneCommits], h-2, inner))
}

// emptyNote explains an empty pane: the feed's reason when there is one
// (kept plain text before it is styled, so clipping never cuts an escape
// sequence), the quiet placeholder otherwise. innerW is the pane's usable
// width: the box does not wrap, it grows, and a frame wider than the window
// loses its border.
func (m Model) emptyNote(p paneFocus, placeholder string, innerW int) string {
	why, broken := m.feedMsg(p)
	if why == "" {
		return dim(placeholder)
	}
	msg := feedKey(p) + ": " + why
	if broken {
		return red("✗ ") + dim(clip(msg, innerW-4))
	}
	return dim(clip(msg, innerW-2))
}

func (m Model) paneSessions(g geom) string {
	w, h := g.rightW, g.sessionsH
	inner := w - 4
	list := m.sessionsFor()
	scope := "project"
	if repo, ok := m.selectedRepo(); ok {
		scope = filepath.Base(repo.Path)
	}
	title := clip(fmt.Sprintf("SESSIONS · %s · %d", scope, len(list))+m.warn(paneSessions), inner)
	var lines []string
	for pos, it := range m.visible(paneSessions) {
		s := list[it.idx]
		meta := dim(padL(shortAge(s.Updated), 5) + " " + padL(fmt.Sprintf("$%.2f", s.Cost), 6))
		line := padR(clip(s.Title, inner-lipgloss.Width(meta)-2), inner-lipgloss.Width(meta)-2) + meta
		lines = append(lines, m.fillRow(paneSessions, "> "+line, pos == m.cursors[paneSessions], inner))
	}
	if len(lines) == 0 {
		lines = []string{m.emptyNote(paneSessions, "(no sessions)", inner)}
	}
	return pane(m.focus == paneSessions, title, w, h,
		listWindow(lines, m.cursors[paneSessions], h-2, inner))
}

func (m Model) paneNotes(g geom) string {
	w, h := g.rightW, g.notesH
	inner := w - 4
	notes := m.proj().Notes
	title := clip(fmt.Sprintf("NOTES · %d", len(notes))+m.warn(paneNotes), inner)
	var lines []string
	for pos, it := range m.visible(paneNotes) {
		n := notes[it.idx]
		meta := dim(padL(shortAge(n.Modified), 5))
		line := padR(clip(n.Rel, inner-lipgloss.Width(meta)-2), inner-lipgloss.Width(meta)-2) + meta
		lines = append(lines, m.fillRow(paneNotes, "> "+line, pos == m.cursors[paneNotes], inner))
	}
	if len(lines) == 0 {
		lines = []string{m.emptyNote(paneNotes, "(no notes)", inner)}
	}
	return pane(m.focus == paneNotes, title, w, h,
		listWindow(lines, m.cursors[paneNotes], h-2, inner))
}

func (m Model) helpView() string {
	rows := []string{
		accent("panes"),
		"  " + keyStyle("tab") + dim(" / ") + keyStyle("l · →") + dim(" next pane    ") +
			keyStyle("h · ← · esc") + dim(" previous pane (esc quits at projects)") + "\n" +
			"  " + keyStyle("mouse") + dim(" click focuses and selects, wheel moves"),
		accent("lists"),
		"  " + keyStyle("↑↓ · jk") + dim(" one row    ") + keyStyle("pgup · pgdn") + dim(" page    ") +
			keyStyle("ctrl+u · ctrl+d") + dim(" half page    ") + keyStyle("home · end") + dim(" first, last"),
		accent("projects"),
		"  " + keyStyle("1-9") + dim(" jump to project    ") + keyStyle("enter") + dim(" drill into its repos"),
		accent("repos"),
		"  " + keyStyle("enter") + dim(" or ") + keyStyle("g") + dim(" lazygit    ") +
			keyStyle("t") + dim(" terminal    ") + keyStyle("c") + dim(" code    ") +
			keyStyle("a") + " " + dim("agent    ") + keyStyle("e") + dim(" explorer    ") + keyStyle("y") + dim(" copy path"),
		accent("context panes"),
		"  " + keyStyle("s") + " " + dim("resume session    ") + keyStyle("d") + dim(" open note    ") +
			keyStyle("enter") + dim(" commits: copy sha · sessions: resume · notes: open"),
		accent("env · docker compose"),
		"  " + keyStyle("u") + dim(" up    ") + keyStyle("s") + dim(" stop    ") +
			keyStyle("d") + dim(" down    ") + keyStyle("r") + dim(" restart    ") + keyStyle("l") + dim(" logs    ") +
			keyStyle("x") + dim(" shell — in a terminal, on the selected service"),
		accent("app"),
		"  " + keyStyle("r") + dim(" rescan    ") + keyStyle("T") + dim(" reload theme and agent    ") +
			keyStyle("?") + dim(" this help    ") + keyStyle("q") + dim(" quit"),
	}
	innerW := min(m.width-8, 96)
	body := strings.Join(rows, "\n")
	// stay one row under the window, same rule as the frame: the
	// renderer's cursor needs the last row to exist
	h := min(lipgloss.Height(body)+2, m.height-3)
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center,
		pane(true, "HELP", innerW+2, h, body))
}

// statusLine carries, in order of urgency: the last action, the open
// todos of the session under the cursor (while the SESSIONS pane is
// focused -- "what was left hanging in here"), the reason a feed is broken
// (while the pane that reads it is focused), or the project path.
func (m Model) statusLine() string {
	var left string
	switch {
	case m.status != "":
		left = dim(clip(strings.ReplaceAll(m.status, "\n", " "), m.width/2))
	case m.focus == paneSessions && len(m.todos) > 0:
		left = m.todosLine()
	default:
		why, broken := m.feedMsg(m.focus)
		switch {
		case broken:
			left = red("⚠ ") + dim(clip(feedKey(m.focus)+": "+why, m.width/2-2))
		case why != "":
			left = dim(clip(feedKey(m.focus)+": "+why, m.width/2))
		default:
			left = dim(clip(shortPath(m.proj().Path), m.width/2))
		}
	}
	right := ""
	if m.loading || m.commitsLoading || (m.envLoading && !m.envFetched) {
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

// todosLine renders "N open: a · b · c" for the selected session: each
// item is clipped while still plain text (never inside an escape
// sequence) and items are added only while they fit the status budget.
func (m Model) todosLine() string {
	head := orange(fmt.Sprintf("%d open:", len(m.todos)))
	budget := m.width/2 - lipgloss.Width(head) - 1
	var parts []string
	used := 0
	for i, t := range m.todos {
		if i == 3 {
			parts = append(parts, dim("…"))
			break
		}
		it := clip(t.Content, 40)
		w := lipgloss.Width(it) + 3 // item + " · "
		if used+w > budget {
			if len(parts) > 0 {
				parts = append(parts, dim("…"))
			}
			break
		}
		parts = append(parts, dim(it))
		used += w
	}
	if len(parts) == 0 {
		parts = []string{dim(clip(m.todos[0].Content, max(1, budget-2)))}
	}
	return head + " " + strings.Join(parts, " · ")
}

// listWindow shows items around the selected index, clipping to innerH rows,
// and marks the cut: a list pane is a few rows tall, and without a thumb on
// its right edge "3 of 20" and "there are only 3" paint identically.
//
// rowW is the width the rows were built to; the strip is padded to it and
// appended, so a gutter appears inside the frame's existing slack instead of
// stealing a column from every row.
func listWindow(items []string, sel, innerH, rowW int) string {
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
	shown := items[start:end]
	if len(items) <= innerH {
		return strings.Join(shown, "\n")
	}
	gutter := scrollBar(len(items), start, innerH)
	out := make([]string, len(shown))
	for i := range shown {
		out[i] = padR(shown[i], rowW) + gutter[i]
	}
	return strings.Join(out, "\n")
}

// scrollBar is the thumb on a clipped list: one column tall as the pane, with
// the visible window drawn as its own position inside the whole. The thumb
// scales with how much of the list is on screen, so a pane showing half of it
// says so with a fat bar and a pane showing a tenth with a sliver.
func scrollBar(total, start, visible int) []string {
	track := make([]string, visible)
	for i := range track {
		track[i] = dim("│")
	}
	thumb := visible * visible / total
	if thumb < 1 {
		thumb = 1
	}
	pos := 0
	if span := total - visible; span > 0 {
		pos = start * (visible - thumb) / span
	}
	hot := lipgloss.NewStyle().Bold(true).Foreground(pal().accent)
	for i := pos; i < pos+thumb && i < visible; i++ {
		track[i] = hot.Render("┃")
	}
	return track
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
