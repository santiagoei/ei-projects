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
)

// The cockpit is a window left open while the work happens somewhere else. A
// background re-scan keeps the state honest -- and must not disturb where the
// user is standing: same project, same repository, same row in every pane.
func TestBackgroundRefreshKeepsTheSeat(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rep := testReport()
	m := New(&config.Config{AutoRefresh: 30})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.repo = 2
	m.cursors[paneSessions] = 1
	m.cursors[paneNotes] = 1

	// the tick starts a scan and arms the next one
	_, cmd := m.Update(refreshTickMsg{})
	if cmd == nil {
		t.Fatal("the refresh tick did nothing")
	}
	m = drive(t, m, refreshTickMsg{})
	if !m.refreshing {
		t.Fatal("a refresh in flight should be marked")
	}

	// the reply arrives with the state changed: the repo got clean, a
	// project's repository disappeared, and a new session is the newest row
	updated := testReport()
	updated.Projects[1].Repos[2] = gitx.Repo{Path: "/p/moodle405", Branch: "master"}
	updated.Projects[0].Repos = nil
	m = drive(t, m, scanDoneMsg{report: updated})
	if m.refreshing || m.loading {
		t.Fatal("the refresh outlived its report")
	}
	if m.project != 1 || m.repo != 2 {
		t.Fatalf("the seat moved: project=%d repo=%d", m.project, m.repo)
	}
	if got := m.proj().Repos[2]; got.Dirty || got.Pending {
		t.Fatalf("the new state was not taken: %+v", got)
	}
	if m.cursors[paneSessions] != 1 || m.cursors[paneNotes] != 1 {
		t.Fatalf("the rows moved under the user: %+v", m.cursors)
	}

	// a report that lost this project entirely parks the selection on what is
	// left, rather than leaving a cursor on a row that no longer exists
	empty := scan.Report{Projects: []scan.Project{
		{Name: "obsidian", Path: "/v/obsidian"},
		{Name: "nope", Path: "/p/icampus.fei.org"},
	}}
	m = drive(t, m, scanDoneMsg{report: empty})
	if m.project != 1 {
		t.Fatalf("selection walked off the end of a shorter list: %d", m.project)
	}
	if m.listCount(paneRepos) != 0 || m.realIdx(paneRepos) != -1 {
		t.Fatalf("a repository cursor survived its list: %+v", m.cursors)
	}
}

// autoRefresh: 0 is the user saying "only when I press r" -- no timer at all.
func TestRefreshInterval(t *testing.T) {
	if cmd := refreshTick(0); cmd != nil {
		t.Fatal("a zero interval must not arm a tick")
	}
	if cmd := refreshTick(time.Second); cmd == nil {
		t.Fatal("a positive interval must arm one")
	}
	for _, tc := range []struct {
		cfg  *config.Config
		want time.Duration
	}{
		{&config.Config{AutoRefresh: 45}, 45 * time.Second},
		{&config.Config{AutoRefresh: 0}, 0},
		{nil, 0},
	} {
		m := Model{cfg: tc.cfg}
		if got := m.refreshEvery(); got != tc.want {
			t.Errorf("refreshEvery(%+v) = %v, want %v", tc.cfg, got, tc.want)
		}
	}
	// a model with no config at all still scans: the cockpit's own nil guard
	// is what keeps `New(nil)` from panicking on its first frame
	naked := New(nil)
	if cmd := naked.scanCmd(); cmd == nil {
		t.Fatal("scanCmd must run even without a config")
	}
	// an "off" stays off through a tick: the heartbeat is re-armed from the
	// config every time, so nothing restarts behind the user's back
	if _, cmd := naked.Update(refreshTickMsg{}); cmd != nil {
		t.Fatal("a cockpit with autoRefresh off must not arm a timer")
	}
	// a bare resize neither raises a command nor clears the size
	if _, cmd := (Model{}).Update(tea.WindowSizeMsg{Width: 150, Height: 40}); cmd != nil {
		t.Fatal("a resize should not raise a command")
	}
}

// The background tick re-reads what hangs off the selection too: the
// repository's history and the session's open items are exactly the things an
// agent changes while the cockpit sits open. They are refetched without any
// "loading" flag, because a pane that blanks itself every forty-five seconds
// is a pane the user stops reading.
func TestRefreshRefetchesTheSelectionsFeeds(t *testing.T) {
	// its own state dir: these tests drive a real selection, and persisting it
	// into the package-wide TestMain directory would leak the seat into every
	// test that runs after
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := New(&config.Config{AutoRefresh: 30})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1)
	m.focus = paneCommits
	m = drive(t, m, commitsDoneMsg{repoPath: m.proj().Repos[0].Path,
		commits: []gitx.Commit{{Hash: "old", Subject: "before"}}})
	m = drive(t, m, todosDoneMsg{sessionID: m.sessionsFor()[0].ID,
		todos: []agent.Todo{{Content: "stale item"}}})

	// the caches are full, so an ordinary selection refresh fetches nothing
	if cmd := m.commitsCmd(); cmd != nil {
		t.Fatal("the history cache is supposed to be warm")
	}
	if cmd := m.todosCmd(); cmd != nil {
		t.Fatal("the todos cache is supposed to be warm")
	}

	// the tick does not care about the cache: history and todos are re-read
	if got := len(m.refreshFeeds()); got != 2 {
		t.Fatalf("refreshFeeds fetched %d things, want history + todos", got)
	}
	m = drive(t, m, refreshTickMsg{})
	if m.commitsLoading {
		t.Fatal("a background refetch must not raise the loading flag")
	}
	if !strings.Contains(m.paneCommits(m.geom()), "before") {
		t.Fatal("the rows on screen vanished before the reply arrived")
	}

	// the rows on screen are the old ones until the reply lands
	before := m.paneCommits(m.geom())

	// and the replies land: the history, and the session's open items. The
	// status line only shows the todos while the SESSIONS pane holds focus,
	// so ask for it there.
	refreshed := testReport()
	refreshed.Projects[1].Repos[0] = gitx.Repo{Path: "/p/icampus.fei.org/icampus",
		Branch: "main", Dirty: true, Changes: 7, Pending: true}
	m.focus = paneSessions
	m = drive(t, m, scanDoneMsg{report: refreshed},
		commitsDoneMsg{repoPath: "/p/icampus.fei.org/icampus",
			commits: []gitx.Commit{{Hash: "new", Subject: "after"}}},
		todosDoneMsg{sessionID: m.sessionsFor()[0].ID,
			todos: []agent.Todo{{Content: "fresh item"}}})
	if !strings.Contains(m.paneCommits(m.geom()), "after") {
		t.Fatal("the new history was not taken")
	}
	if strings.Contains(before, "after") {
		t.Fatal("the refetched rows were there before the reply")
	}
	if !strings.Contains(m.statusLine(), "fresh item") {
		t.Fatalf("the new todos were not taken: %q", m.statusLine())
	}
	// a reply for a repo the user walked past is dropped, as always
	m = drive(t, m, commitsDoneMsg{repoPath: "/p/nowhere",
		commits: []gitx.Commit{{Hash: "x", Subject: "ghost"}}})
	if strings.Contains(m.View(), "ghost") {
		t.Fatal("a stale refetch reply was rendered")
	}
}
