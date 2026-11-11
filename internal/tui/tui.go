// Package tui is the cockpit: a lazygit-shaped frame of six panes over a
// scan.Report -- projects top-left, the selected project's repositories
// bottom-left, and down the right edge: a one-line OPEN IN launcher bar for
// the selected repository, then the conditional compose pane and three
// stacked list panes (commits, opencode sessions, vault notes). Focus walks
// panes with tab/h/l, or a click; `/` narrows the rows a pane shows; the git
// history loads on demand for the selected repo so the standing scan stays
// cheap. Colours come from the Omarchy theme; the last seat is remembered
// across launches.
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
	"github.com/charmbracelet/x/ansi"

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

// sessionScope is what the SESSIONS pane is listing. `S` walks it: repo,
// project, everywhere. Three because "the repository I am looking at", "the
// project it belongs to" and "the last thing I touched anywhere" are the
// three questions a cockpit gets asked of a session list.
type sessionScope int

const (
	scopeRepo sessionScope = iota
	scopeProject
	scopeAll
)

func (s sessionScope) next() sessionScope {
	return sessionScope((int(s) + 1) % 3)
}

type scanDoneMsg struct {
	report scan.Report
	err    error
}

// remoteDoneMsg carries its repository for the same race guard as the history.
type remoteDoneMsg struct {
	repoPath string
	url      string
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
	scope          sessionScope // what the SESSIONS pane is listing
	envSvcs        []env.Service
	envFor         string
	envFetched     bool
	envLoading     bool
	envErr         string
	remote         string // where the selected repository pushes, for the detail block
	remoteFor      string
	spinner        spinner.Model
	help           bool
	restored       bool

	// The filter: a needle, the pane it applies to, and whether the keyboard
	// is still feeding it. One pane at a time on purpose -- a cockpit where
	// every pane is half-empty makes "there is nothing here" and "it did not
	// match" indistinguishable.
	filtering  bool
	filterText string
	filterPane paneFocus

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
			out = append(out, item{idx: i, label: s.Title + " " + s.Directory + " " + s.Project})
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

// visible is a pane's rows after the filter: the seam everything else was
// built around. Only the pane the needle belongs to is narrowed, and the order
// never changes -- a session list is chronology and a commit list is history,
// so the filter drops rows and never sorts them.
func (m Model) visible(p paneFocus) []item {
	all := m.items(p)
	if !m.filterOn() || p != m.filterPane {
		return all
	}
	out := make([]item, 0, len(all))
	for _, it := range all {
		if fuzzyMatch(it.label, m.filterText) {
			out = append(out, it)
		}
	}
	return out
}

// filterOn is a needle long enough to narrow anything. An empty one is not a
// filter: the panes have to show the world again, not an empty list.
func (m Model) filterOn() bool {
	return m.filterText != ""
}

// filterMark is the pane's own note that something is hiding its rows.
func (m Model) filterMark(p paneFocus) string {
	if m.filterOn() && p == m.filterPane {
		return " · ⌕"
	}
	return ""
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

// proj and selectedRepo read the selection, which for these two panes is an
// ITEM index (m.project, m.repo) rather than a position among the visible
// rows: it is what every other pane reads, so a filter that hides the
// selected project must narrow the view, not move the user somewhere else.
func (m Model) proj() scan.Project {
	i := clamp(m.project, 0, len(m.items(paneProjects))-1)
	if i < 0 || i >= len(m.report.Projects) {
		return scan.Project{}
	}
	return m.report.Projects[i]
}

func (m Model) selectedRepo() (gitx.Repo, bool) {
	repos := m.proj().Repos
	i := clamp(m.repo, 0, len(repos)-1)
	if i < 0 || i >= len(repos) {
		return gitx.Repo{}, false
	}
	return repos[i], true
}

// sessionsFor is what the SESSIONS pane lists: the repository under the cursor
// (falling back to the project when the repo has no sessions of its own --
// a session belongs to the directory opencode ran in, which is usually the
// project root), the whole project, or everywhere at once. The scope is the
// user's choice; a pane that changes its own source between keystrokes is a
// pane the user cannot predict.
func (m Model) sessionsFor() []agent.Session {
	switch m.scope {
	case scopeProject:
		return m.proj().Sessions
	case scopeAll:
		return m.report.AllSessions
	}
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

// scopeName is the scope as the pane titles spell it.
func (s sessionScope) name(repo string) string {
	switch s {
	case scopeProject:
		return "project"
	case scopeAll:
		return "all projects"
	}
	if repo == "" {
		return "project"
	}
	return repo
}

// listCount is the number of rows a pane shows -- the rows the user can land
// on, which is not the number of items it holds once a filter is on.
func (m Model) listCount(p paneFocus) int {
	return len(m.visible(p))
}

// selectedIndex is where a pane's cursor sits among the rows it shows. For the
// list panes that is the stored number; for projects and repos it is the
// position of the selected item, and -1 when a filter hides it -- the cursor
// has nothing to paint itself against, but the selection is untouched.
func (m Model) selectedIndex(p paneFocus) int {
	switch p {
	case paneProjects:
		return m.positionOf(p, clamp(m.project, 0, len(m.items(p))-1))
	case paneRepos:
		return m.positionOf(p, clamp(m.repo, 0, len(m.items(p))-1))
	default:
		return m.cursors[p]
	}
}

// positionOf and itemAt are the two directions of that translation. The list
// panes need neither: their cursor is a position and each row carries the item
// it stands for.
func (m Model) positionOf(p paneFocus, idx int) int {
	for pos, it := range m.visible(p) {
		if it.idx == idx {
			return pos
		}
	}
	return -1
}

func (m Model) itemAt(p paneFocus, pos int) int {
	list := m.visible(p)
	if pos < 0 || pos >= len(list) {
		// a position off the screen moves nobody: the selection stays where
		// the user left it instead of jumping to whatever row is nearest
		switch p {
		case paneProjects:
			return m.project
		case paneRepos:
			return m.repo
		}
		return -1
	}
	return list[pos].idx
}

func (m Model) cursorFor(p paneFocus) int {
	return clamp(m.selectedIndex(p), 0, m.listCount(p)-1)
}

// selectProject mutates: the receiver MUST be a pointer -- with a value
// receiver the new project silently evaporated when the method returned.
// The argument is an ITEM index; callers holding a row position translate it
// through itemAt first.
func (m *Model) selectProject(idx int) {
	if idx == m.project {
		return
	}
	m.project = clamp(idx, 0, len(m.items(paneProjects))-1)
	m.repo = 0
	m.cursors = map[paneFocus]int{}
	m.commits = nil
	m.commitsFor = ""
	m.remote = ""
	m.remoteFor = ""
}

// clampCursors parks every cursor inside the rows its pane actually shows --
// after a rescan, or after a filter hid the row someone was standing on.
func (m *Model) clampCursors() {
	// the selection is clamped against what EXISTS, not against what the
	// filter lets through: narrowing a pane must never move the user
	m.project = clamp(m.project, 0, len(m.items(paneProjects))-1)
	m.repo = clamp(m.repo, 0, len(m.items(paneRepos))-1)
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
	for _, it := range m.items(paneProjects) {
		if m.report.Projects[it.idx].Path != projectPath {
			continue
		}
		m.project = it.idx
		repos := m.proj().Repos
		for _, rit := range m.items(paneRepos) {
			if repos[rit.idx].Path == repoPath {
				m.repo = rit.idx
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
	return tea.Batch(m.commitsCmd(), m.todosCmd(), m.envCmd(), m.remoteCmd())
}

// remoteCmd fetches the push target of the selected repository -- on demand,
// like the history: one subprocess for the repository being looked at, never
// one per repository per scan.
func (m *Model) remoteCmd() tea.Cmd {
	repo, ok := m.selectedRepo()
	if !ok {
		m.remote, m.remoteFor = "", ""
		return nil
	}
	if m.remoteFor == repo.Path {
		return nil // cached, or a request already in flight
	}
	path := repo.Path
	m.remote, m.remoteFor = "", path
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commitDeadline)
		defer cancel()
		url, err := gitx.Remote(ctx, path)
		if err != nil {
			return remoteDoneMsg{repoPath: path}
		}
		return remoteDoneMsg{repoPath: path, url: url}
	}
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

	case remoteDoneMsg:
		if repo, ok := m.selectedRepo(); ok && repo.Path == msg.repoPath {
			m.remote, m.remoteFor = msg.url, msg.repoPath
		}
		return m, nil

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
		// characters are typed with the help on the screen: anything else
		// closes it, and the keys a search needs stay reachable
		switch key {
		case "?", "esc", "q", "ctrl+c", "ctrl+d", "up", "down", "pgup", "pgdown", "pgdn",
			"home", "end", "ctrl+u":
			m.help = false
		}
		return m, nil
	}

	if m.filtering {
		return m.onFilter(msg)
	}

	switch key {
	case "q", "ctrl+c":
		return m, m.quit()
	case "?":
		m.help = true
		return m, nil
	case "/":
		m.filtering = true
		m.filterPane = m.focus
		m.status = "" // the line is about to carry the needle
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
		if key == "esc" && m.filterOn() && m.focus == m.filterPane {
			// esc gets you out of the thing you are in: the first press drops
			// the needle and shows the rows again, the next walks panes
			m.setNeedle("")
			return m, nil
		}
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

	if key == "S" {
		// the sessions pane changes what it lists, nothing else: the cursor
		// stays a row position, the filter keeps its word, and the other
		// panes keep reading the repository that is actually selected
		m.scope = m.scope.next()
		if m.focus != paneSessions {
			m.cursors[paneSessions] = 0
		}
		m.clampCursors()
		return m, m.todosCmd()
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
			m.selectProject(m.itemAt(paneProjects, i))
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

// onFilter owns the keyboard while a search is open. Characters go to the
// needle; the arrows and the page keys still walk the rows that survived, tab
// moves the search to another pane without losing the word, enter leaves the
// filter running and gives the keys back, and esc forgets it entirely. Every
// one of those has an undo a user can reach without reading a manual.
func (m Model) onFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "esc":
		m.filtering = false
		m.setNeedle("")
		return m, nil
	case "enter":
		m.filtering = false // the needle stays on, the app keys come back
		return m, nil
	case "backspace":
		r := []rune(m.filterText)
		if len(r) > 0 {
			m.setNeedle(string(r[:len(r)-1]))
		}
		return m, nil
	case "ctrl+u", "ctrl+w":
		m.setNeedle("")
		return m, nil
	case "ctrl+c":
		// quitting outranks the input: a search that swallowed the only
		// universal escape hatch would be a small trap
		return m, m.quit()
	case "tab":
		m.retargetFilter(1)
		return m, nil
	case "shift+tab":
		m.retargetFilter(-1)
		return m, nil
	case "up":
		return m.moveCursor(-1)
	case "down":
		return m.moveCursor(1)
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.setNeedle(m.filterText + string(msg.Runes))
			return m, nil
		}
		return m, nil
	}
}

// setNeedle is every change of the search text. A list pane goes back to its
// first match -- standing on row seven of a list that now has two rows is not
// keeping context -- while projects and repos only get clamped, because there
// the cursor IS the selection and moving it would redraw every other pane.
func (m *Model) setNeedle(s string) {
	m.filterText = s
	switch m.filterPane {
	case paneProjects, paneRepos:
	default:
		m.cursors[m.filterPane] = 0
	}
	m.clampCursors()
}

// retargetFilter hands the same word to another pane: "moodle" is a question
// the whole cockpit can answer, and asking it of the sessions, then the
// commits, then the notes should not mean typing it again.
func (m *Model) retargetFilter(step int) {
	order := m.focusOrder()
	pos := -1
	for i, p := range order {
		if p == m.filterPane {
			pos = i
		}
	}
	if pos < 0 {
		m.filterPane = order[0]
	} else {
		n := len(order)
		m.filterPane = order[((pos+step)%n+n)%n]
	}
	m.clampCursors()
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
		m.selectProject(m.itemAt(p, pos))
		return m, m.commitSelection()
	case paneRepos:
		m.repo = m.itemAt(p, pos)
		return m, m.commitSelection()
	}
	m.cursors[p] = pos
	if p == paneSessions {
		return m, m.todosCmd()
	}
	return m, nil
}

func (m Model) moveCursor(dy int) (tea.Model, tea.Cmd) {
	pos := m.selectedIndex(m.focus)
	if pos < 0 {
		// a filter is hiding the selection: the first press takes the user to
		// the nearest edge of what is on screen instead of counting from a
		// row that is not there
		if dy > 0 {
			return m.setCursorPos(m.focus, 0)
		}
		return m.setCursorPos(m.focus, m.listCount(m.focus)-1)
	}
	return m.setCursorPos(m.focus, pos+dy)
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

// breadcrumb is the one line that says where the user is, whatever pane holds
// focus. The context used to be spread over five pane titles, which meant
// reading the whole frame to work out which repository the commits belonged
// to. Every part is clipped while it is still plain text and the optional
// ones drop from the right, because nothing here may soft-wrap: a wrapped
// breadcrumb pushes the frame's top border off the screen.
func (m Model) breadcrumb() string {
	p := m.proj()
	name := dim("(no project)")
	if p.Name != "" {
		name = accent(clip(p.Name, 26)) // clipped plain, so the row below never wraps
	}
	repo := ""
	if r, ok := m.selectedRepo(); ok {
		state := green("✓")
		switch {
		case r.Error != "":
			state = red("✗ " + clip(firstLine(r.Error), 12))
		case r.Dirty || r.Ahead > 0 || r.Behind > 0:
			state = repoState(r)
		}
		repo = dim(" › ") +
			lipgloss.NewStyle().Bold(true).Foreground(pal().fg).Render(clip(filepath.Base(r.Path), 22)) +
			dim(" · ") + clip(r.Branch, 18) + dim(" · ") + state
	}
	tail := ""
	if up, total := m.envUp(); total > 0 {
		tail += dim(" · env ") + green(fmt.Sprintf("%d/%d", up, total))
	}
	if n := len(p.Repos); n > 0 {
		tail += dim(" · " + plural(n, "repo"))
	}
	if n := len(p.Notes); n > 0 {
		tail += dim(" · " + plural(n, "note"))
	}
	right := ""
	if !m.scannedAt.IsZero() {
		right = dim("scanned " + shortAge(m.scannedAt))
	}

	// Nothing here is ever clipped after it is styled -- that would cut an
	// escape sequence in half and paint garbage for a row. The segments are
	// dropped instead, least important first, and what is left standing is
	// short enough by construction to fit the narrowest window the cockpit
	// accepts (60 columns), because each piece was clipped as plain text.
	// one column of air between the two ends, always: a segment that reaches
	// the timestamp reads as one long word
	fits := func(s string) bool { return lipgloss.Width(s+right) <= m.width-1 }
	bare := lipgloss.NewStyle().Foreground(pal().faint)
	switch {
	case fits(name + repo + tail):
	case fits(name + repo):
		tail = ""
	case fits(name):
		repo, tail = "", ""
	case m.width >= 8:
		name, repo, tail, right = bare.Render(clip(shortPath(p.Path), m.width-7)), "", "", ""
	default:
		name, repo, tail, right = "", "", "", ""
	}
	// the row is exactly as wide as the frame below it, same as the status
	// line: the two are read as one block
	return name + repo + tail +
		strings.Repeat(" ", max(1, m.width-lipgloss.Width(name+repo+tail)-lipgloss.Width(right))) + right
}

// envUp is the compose pane's headline answer -- how many of what is defined
// are actually running -- shared by the pane title and the breadcrumb.
func (m Model) envUp() (up, total int) {
	for _, s := range m.envSvcs {
		if s.State == "running" {
			up++
		}
	}
	return up, len(m.envSvcs)
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

	var frame string
	if g.single {
		// one column, in the order the mouse map was built with
		frame = lipgloss.JoinVertical(lipgloss.Left,
			append([]string{projects, repos}, right...)...)
	} else {
		left := []string{projects, repos}
		if detail := m.paneDetail(g); detail != "" {
			left = append(left, detail)
		}
		frame = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.JoinVertical(lipgloss.Left, left...),
			lipgloss.JoinVertical(lipgloss.Left, right...))
	}

	// breadcrumb on top, frame, then the two lines that read the frame: the
	// status (an action, a filter, a session's open items) and the keys
	return m.breadcrumb() + "\n" + frame + "\n" + m.statusLine() + "\n" + m.legend()
}

// legend is the app shortcut bar along the bottom. It says less when there is
// nothing to say: the page keys only appear on a pane whose list is longer
// than the rows it can paint.
func (m Model) legend() string {
	type kb struct{ key, label string }
	var keys, tail []kb
	switch {
	case m.filtering:
		keys = []kb{
			{"chars", "needle"},
			{"backspace", "edit"},
			{"tab", "pane"},
			{"↑/↓", "rows"},
			{"enter", "keep"},
			{"esc", "clear"},
		}
		tail = []kb{{"ctrl+c", "quit"}}
	case m.focus == paneEnv:
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
	default:
		keys = []kb{
			{"tab/h/l", "panes"},
			{"↑/↓", "move"},
			{"enter", "act"},
			{"1-9", "project"},
			{"s", "session"},
			{"d", "note"},
			{"S", "scope"},
		}
		tail = []kb{{"?", "help"}, {"r", "rescan"}, {"q", "quit"}}
	}
	if m.filterOn() && !m.filtering {
		keys = append(keys, kb{"esc", "clear filter"})
	}
	if m.listCount(m.focus) > m.geom().innerH(m.focus) {
		keys = append(keys, kb{"pgup/dn", "page"}, kb{"home/end", "top"})
	}
	parts := make([]string, 0, len(keys)+len(tail))
	for _, k := range append(keys, tail...) {
		parts = append(parts, keyStyle(k.key)+dim(" "+k.label))
	}
	// Every part carries its own separator, so the row is the plain sum of
	// what is kept: no soft wrap, no lost border. The bar keeps only what fits
	// and says so with an ellipsis -- blindly keeping five keys used to be
	// sixty-four columns in a sixty-column window.
	sep, ell := dim(" · "), dim("…")
	glue := lipgloss.Width(sep) // the separator is paid for by the key that follows
	kept, used := []string{}, 0
	for _, part := range parts {
		w := lipgloss.Width(part) + glue
		if used+w > m.width {
			// reserve the separator the ellipsis would ride on: an un-fitting
			// ellipsis is worse than a silently dropped key
			if m.width-used >= glue+lipgloss.Width(ell) {
				kept = append(kept, sep+ell)
			}
			break
		}
		if len(kept) > 0 { // the bar does not open with its own separator
			kept = append(kept, sep)
			used += glue
		}
		kept = append(kept, part)
		used += lipgloss.Width(part)
	}
	if len(kept) == 0 { // not even one key fits: the hint beats an empty row
		return ell
	}
	return strings.Join(kept, "")
}

// pane renders a bordered box whose title sits ON the top border line --
// "╭─ COMMITS ──────" lazygit-style -- so the title costs no content row.
// The border characters are the only thing on that row, so the whole line is
// rewritten in the pane's border colour, and the active pane swaps the corner
// dash for a ▶: colour alone made the focused pane hard to find in a frame of
// six, and a marker costs nothing when it replaces a character that was
// already there.
func pane(active bool, title string, w, h int, body string) string {
	p := pal()
	fg, corner := p.faint, "╭─ "
	if active {
		fg, corner = p.accent, "╭▶ "
	}
	// the box does not wrap and does not scroll: text longer than the frame
	// grows the box, and a frame one row taller than the window makes the
	// terminal eat the top border. Anything past the rows this pane owns gets
	// cut, with the sequences of the last kept line left balanced.
	if max := h - 2; max >= 0 {
		body = fitRows(body, max)
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(fg).
		Width(w - 2).Height(h - 2)
	lines := strings.Split(box.Render(body), "\n")
	if len(lines) > 0 {
		t := cut(title, w-6)
		// corner + title + " " + fill + "╮" must be exactly w wide
		fill := w - lipgloss.Width(corner) - lipgloss.Width(t) - 2
		if fill < 1 {
			fill = 1
		}
		base := lipgloss.NewStyle().Foreground(fg)
		lines[0] = base.Render(corner) + base.Bold(true).Render(t) +
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
	// the badge gets at least "missing" columns, so no row can ever overflow
	// the pane it is painted in (the box grows instead of clipping, and a
	// frame one column too wide loses its border)
	badgeW := max(7, min(12, inner/3))
	for pos, it := range m.visible(paneProjects) {
		p := m.report.Projects[it.idx]
		label := padR(fmt.Sprintf("%d %s", pos+1, clip(p.Name, inner-badgeW-3)), inner-badgeW) +
			projectBadge(p, badgeW)
		lines = append(lines, m.fillRow(paneProjects, "> "+label, it.idx == m.project, inner))
	}
	if len(lines) == 0 {
		lines = []string{m.emptyNote(paneProjects, "(no projects)", inner)}
	}
	pending := 0
	for _, p := range m.report.Projects {
		pending += p.Pending
	}
	title := fmt.Sprintf("PROJECTS · %d", len(m.report.Projects))
	if pending > 0 {
		title += orange(fmt.Sprintf(" · ●%d", pending))
	}
	title += m.filterMark(paneProjects)
	return pane(m.focus == paneProjects, title, w, h,
		listWindow(lines, m.selectedIndex(paneProjects), h-2, inner))
}

// projectBadge is a whole project in a handful of columns: one glyph per
// repository, so what everything is doing is readable without walking into
// each. The run is capped and the remainder becomes "+n", because a project
// with thirty repositories must not push its own name off the row.
func projectBadge(p scan.Project, budget int) string {
	switch {
	case p.Missing:
		return red("missing")
	case len(p.Repos) == 0:
		return dim("—")
	}
	shown := min(len(p.Repos), max(1, budget))
	for shown > 1 && shown+suffixLen(len(p.Repos)-shown) > budget {
		shown--
	}
	var out strings.Builder
	for _, r := range p.Repos[:shown] {
		switch {
		case r.Error != "":
			out.WriteString(red("✗"))
		case r.Dirty:
			out.WriteString(orange("●"))
		case r.Ahead > 0:
			out.WriteString(green("↑"))
		case r.Behind > 0:
			out.WriteString(cyan("↓"))
		default:
			out.WriteString(dim("✓"))
		}
	}
	if rest := len(p.Repos) - shown; rest > 0 {
		out.WriteString(dim(fmt.Sprintf("+%d", rest)))
	}
	return out.String()
}

// plural is the one count a row is allowed to get wrong-looking: "1 repos"
// reads like a bug even when the number is right.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// suffixLen is the width of the "+n" that says how many rows the badge could
// not show.
func suffixLen(n int) int {
	if n <= 0 {
		return 0
	}
	return 1 + len(strconv.Itoa(n))
}

// ageCell is a timestamp that recedes when it is old. Everything being the
// same grey is everything shouting, and "what did I touch today" should be
// answerable by contrast before it is answerable by reading a number.
func ageCell(t time.Time, staleAfter time.Duration) string {
	if t.IsZero() { // no date is not "a very long time ago"
		return dim("     ")
	}
	s := padL(shortAge(t), 5)
	if !t.IsZero() && time.Since(t) > staleAfter {
		return dim(s)
	}
	return lipgloss.NewStyle().Foreground(pal().fg).Render(s)
}

// detailLines is what the repository under the cursor is worth showing that
// no pane lists: where it lives, what it pushes to, its last commit, the vault
// folder and the compose file that belong to it. The list is the pane's height
// budget, so the layout asks the same function the renderer answers with.
//
// It is a display, not a focus: nothing to select, no cursor, no keys, and the
// tab order does not grow. A pane the user can stop in but not act on is a
// pane that steals keystrokes from the one they meant.
func (m Model) detailLines(inner int) []string {
	repo, ok := m.selectedRepo()
	if !ok {
		return nil
	}
	label := func(s string) string { return dim(s) }
	rows := []string{
		// the branch first: it is what the path does not say, and the row
		// under it is the push target that branch is standing on
		label("on   ") + clip(repo.Branch, inner-5),
		label("path ") + clip(shortPath(repo.Path), inner-5),
	}
	switch {
	case repo.Upstream != "":
		rows = append(rows, label("push ")+clip(repo.Upstream, inner-5))
	case repo.Error == "":
		rows = append(rows, label("push ")+dim("(no upstream)"))
	}
	if repo.Error == "" && m.remoteFor == repo.Path {
		if m.remote != "" {
			rows = append(rows, label("from ")+clip(m.remote, inner-5))
		} else {
			rows = append(rows, label("from ")+dim("(no origin)"))
		}
	}
	if len(m.commits) > 0 {
		c := m.commits[0]
		hash := accent(clip(c.Hash, 9))
		free := max(4, inner-lipgloss.Width(hash)-12)
		rows = append(rows, label("last ")+hash+" "+
			padR(clip(c.Subject, free), free)+ageCell(c.When, 30*24*time.Hour))
	}
	p := m.proj()
	if p.DocsFolder != "" {
		docs := p.DocsFolder
		if rel, err := filepath.Rel(m.cfg.VaultDir, docs); err == nil {
			if rel == "." { // the project IS the vault: there is no folder to name
				docs = ""
			} else if !strings.HasPrefix(rel, "..") {
				docs = rel
			}
		}
		if docs != "" {
			rows = append(rows, label("docs ")+clip(shortPath(docs), inner-5))
		}
	}
	if p.EnvFile != "" {
		rows = append(rows, label("env  ")+clip(shortPath(p.EnvFile), inner-5))
	}
	return rows
}

func (m Model) paneDetail(g geom) string {
	rows := m.detailLines(g.leftW - 4)
	if g.detailH < listFloor || len(rows) == 0 {
		return ""
	}
	// lipgloss pads a short box and lets a long one grow: the rows that do not
	// fit are dropped, because a detail block taller than its frame pushes the
	// bottom border -- and the row under it -- off the screen.
	return pane(false, "DETAIL", g.leftW, g.detailH, strings.Join(rows, "\n"))
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
	for _, it := range m.visible(paneRepos) {
		r := repos[it.idx]
		label := padR(clip(filepath.Base(r.Path), inner-14), inner-14) + repoState(r)
		lines = append(lines, m.fillRow(paneRepos, "> "+label, it.idx == m.repo, inner))
	}
	if len(lines) == 0 {
		lines = []string{m.emptyNote(paneRepos, "(no repositories)", inner)}
	}
	// the project it belongs to is on the breadcrumb, not repeated here
	title := "REPOSITORIES"
	return pane(m.focus == paneRepos, title+m.filterMark(paneRepos), w, h,
		listWindow(lines, m.selectedIndex(paneRepos), h-2, inner))
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
// openInTitle names the repository the launchers act on. The branch and the
// dirty count it used to repeat are on the breadcrumb, and reading them twice
// in one screen is how a title ends up clipped.
func (m Model) openInTitle(w int) string {
	title := "OPEN IN"
	if repo, ok := m.selectedRepo(); ok {
		title += " · " + filepath.Base(repo.Path)
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
	title += m.filterMark(paneEnv)
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
		title = "COMMITS"
		lines = []string{m.spinner.View() + dim(" loading…")}
	default:
		title = clip(fmt.Sprintf("COMMITS · %d", len(m.commits))+m.filterMark(paneCommits), inner)
		for pos, it := range m.visible(paneCommits) {
			c := m.commits[it.idx]
			// hash | subject | author | age: fixed visible columns
			line := accent(clip(c.Hash, 9)) + " " +
				padR(clip(c.Subject, inner-36), inner-36) +
				dim(padL(clip(c.Author, 14), 15)) + ageCell(c.When, 30*24*time.Hour)
			lines = append(lines, m.fillRow(paneCommits, "> "+line, pos == m.cursors[paneCommits], inner))
		}
		if len(lines) == 0 {
			lines = []string{m.emptyNote(paneCommits, "(no commits)", inner)}
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
	if m.filterOn() && p == m.filterPane {
		// the rows are there, the word just does not reach them: say both
		return dim(clip(fmt.Sprintf("⌕ nothing here matches %q (of %d)",
			m.filterText, len(m.items(p))), innerW-2))
	}
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
	repoName := ""
	if repo, ok := m.selectedRepo(); ok {
		repoName = filepath.Base(repo.Path)
	}
	scope := m.scope.name(repoName)
	open := 0
	for _, s := range list {
		open += s.OpenTodos
	}
	// the suffix is measured before the title is clipped: a title that
	// overruns the pane soft-wraps, and a wrapped row inside a fixed-height
	// frame pushes the top border off the screen
	suffix := ""
	if open > 0 {
		suffix = orange(fmt.Sprintf(" · %d open", open))
	}
	title := clip(fmt.Sprintf("SESSIONS · %s · %d", scope, len(list))+
		m.warn(paneSessions)+m.filterMark(paneSessions), inner-lipgloss.Width(suffix)) + suffix
	var lines []string
	for pos, it := range m.visible(paneSessions) {
		s := list[it.idx]
		// a session with unfinished items says so on its own row: the count is
		// the reason to open it, and it has to be readable before the cursor
		// lands, not after
		badge := ""
		if s.OpenTodos > 0 {
			badge = orange(fmt.Sprintf(" ▸%d", s.OpenTodos))
		}
		from := ""
		if m.scope == scopeAll && s.Project != "" {
			from = dim(" " + clip(s.Project, 10))
		}
		meta := ageCell(s.Updated, 7*24*time.Hour) + dim(" "+padL(fmt.Sprintf("$%.2f", s.Cost), 6))
		free := max(1, inner-lipgloss.Width(meta)-lipgloss.Width(badge)-lipgloss.Width(from)-2)
		line := padR(clip(s.Title, free), free) + badge + from + meta
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
	title := clip(fmt.Sprintf("NOTES · %d", len(notes))+m.warn(paneNotes)+m.filterMark(paneNotes), inner)
	var lines []string
	for pos, it := range m.visible(paneNotes) {
		n := notes[it.idx]
		meta := ageCell(n.Modified, 30*24*time.Hour)
		line := padR(clip(n.Rel, inner-lipgloss.Width(meta)-2), inner-lipgloss.Width(meta)-2) + meta
		lines = append(lines, m.fillRow(paneNotes, "> "+line, pos == m.cursors[paneNotes], inner))
	}
	if len(lines) == 0 {
		lines = []string{m.emptyNote(paneNotes, "(no notes)", inner)}
	}
	return pane(m.focus == paneNotes, title, w, h,
		listWindow(lines, m.cursors[paneNotes], h-2, inner))
}

// helpView is a help screen that has to survive a 60-column window and a
// 22-row one. Rows are laid out from key/label pairs that wrap BETWEEN pairs
// (never inside an escape sequence, and never mid-word where it would add a
// row the frame did not count for), and the whole is cut to the rows the
// window has -- a help overlay that scrolls off the bottom hides the keys it
// was opened to explain. The groups are ordered so the keys that get you out
// are the last ones to disappear.
// helpPair is one key with the thing it does, the unit the help screen lays
// out with.
type helpPair struct{ key, label string }

func (m Model) helpView() string {
	groups := []struct {
		title string
		pairs []helpPair
	}{
		{"app", []helpPair{
			{"q", "quit"}, {"esc", "back a pane"}, {"?", "this help"},
			{"r", "rescan"}, {"T", "reload theme and agent"},
		}},
		{"panes", []helpPair{
			{"tab · l · →", "next pane"}, {"h · ←", "previous pane"},
			{"mouse", "click focuses and selects, wheel moves"},
		}},
		{"lists", []helpPair{
			{"↑↓ · jk", "one row"}, {"pgup · pgdown", "page"},
			{"ctrl+u · ctrl+d", "half page"}, {"home · end", "first, last"},
		}},
		{"filter", []helpPair{
			{"/", "narrow the focused pane"}, {"tab", "give the word to the next pane"},
			{"enter", "keep it, keys back"}, {"esc", "clear it"},
			{"", "in order, case and accents ignored; rows keep their order"},
		}},
		{"projects", []helpPair{
			{"1-9", "jump to project"}, {"enter", "drill into its repos"},
		}},
		{"repos · the OPEN IN bar", []helpPair{
			{"g", "lazygit"}, {"t", "terminal"}, {"c", "code"}, {"a", "agent"},
			{"e", "explorer"}, {"y", "copy path"},
		}},
		{"context panes", []helpPair{
			{"s", "resume session"}, {"d", "open note"}, {"enter", "commits: sha · sessions: resume · notes: open"},
			{"S", "sessions scope: repo · project · all projects"},
		}},
		{"env · docker compose", []helpPair{
			{"u", "up"}, {"s", "stop"}, {"d", "down"}, {"r", "restart"},
			{"l", "logs"}, {"x", "shell — always in a terminal"},
		}},
	}

	innerW := min(m.width-8, 96)
	var lines []string
	for _, g := range groups {
		lines = append(lines, accent(g.title))
		lines = append(lines, helpRows(g.pairs, innerW)...)
	}
	// status + legend + the row the renderer needs to exist + the box borders
	if room := m.height - 6; len(lines) > room {
		lines = lines[:room]
	}
	body := strings.Join(lines, "\n")
	h := min(len(lines)+2, m.height-3)
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center,
		pane(true, "HELP · esc", innerW+2, h, body))
}

// helpRows packs key/label pairs onto rows no wider than w, wrapping between
// pairs the way the launcher bar does.
func helpRows(pairs []helpPair, w int) []string {
	const glue = " · "
	var lines, cur []string
	width := 0
	fit := func(s string) int { return lipgloss.Width(s) }
	for _, p := range pairs {
		part := keyStyle(p.key)
		if p.label != "" {
			part += dim(" " + p.label)
		}
		n := fit(part)
		if n > w {
			part = dim(clip(p.label, max(1, w-fit(p.key)-1)))
			part = keyStyle(p.key) + part
			n = fit(part)
		}
		if len(cur) > 0 && width+len(glue)+n > w {
			lines = append(lines, strings.Join(cur, dim(glue)))
			cur, width = nil, 0
		}
		if len(cur) > 0 {
			width += len(glue)
		}
		cur = append(cur, part)
		width += n
	}
	if len(cur) > 0 {
		lines = append(lines, strings.Join(cur, dim(glue)))
	}
	return lines
}

// statusLine carries, in order of urgency: the last action (which lives four
// seconds and then gets out of the way), the search if one is running, the
// open todos of the session under the cursor (while the SESSIONS pane is
// focused -- "what was left hanging in here"), the reason a feed is broken
// (while the pane that reads it is focused), or the project path.
func (m Model) statusLine() string {
	var left string
	switch {
	case m.status != "":
		left = dim(clip(strings.ReplaceAll(m.status, "\n", " "), m.width/2))
	case m.filtering || m.filterOn():
		// the needle takes the line the moment an action's echo expires: a
		// filter the user cannot read is worse than no filter at all
		left = m.filterLine(m.width / 2)
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
	// only the spinner shares the status line: the timestamp it used to carry
	// lives in the breadcrumb, and "? help" is in the legend
	right := ""
	if m.loading || m.commitsLoading || (m.envLoading && !m.envFetched) {
		right = m.spinner.View()
	}
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// filterLine is the search itself: the needle, the pane wearing it, and how
// many rows survived -- that count is what tells a narrowing from a wipeout.
// Everything is clipped while still plain text, because the status line has no
// room to spare and a styled string cut in half paints garbage for a row.
func (m Model) filterLine(budget int) string {
	name := paneNames[m.filterPane]
	stats := dim(fmt.Sprintf(" · %s · %d/%d", name, m.listCount(m.filterPane), len(m.items(m.filterPane))))
	if m.filterText == "" {
		hint := "type to filter · esc closes · tab moves the word"
		return accent("⌕") + dim(" "+clip(hint, max(1, budget-lipgloss.Width(stats)-2))) + stats
	}
	caret := ""
	if m.filtering {
		caret = accent("▏") // a caret the user can see themselves typing into
	}
	return accent("⌕ ") +
		keyStyle(clip(m.filterText, max(1, budget-lipgloss.Width(stats)-2))) + caret + stats
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

// fitRows cuts a box's body to the rows it owns. Dropping whole lines is the
// only safe cut here: slicing inside a line would stop in the middle of an
// escape sequence, and half a sequence paints garbage down the row.
func fitRows(body string, rows int) string {
	if rows < 0 {
		rows = 0
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= rows {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:rows], "\n")
}

// cut is clip for text that may already be styled: a pane title carries an
// accent count of its own, and slicing it by runes would cut an escape
// sequence in half and paint garbage for the rest of the row. This is the same
// measurement lipgloss itself renders with, so what fits is what shows.
func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
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
