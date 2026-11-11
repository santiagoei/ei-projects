package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/santiagoei/ei-projects/internal/agent"
	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/env"
	"github.com/santiagoei/ei-projects/internal/gitx"
	"github.com/santiagoei/ei-projects/internal/scan"
	"github.com/santiagoei/ei-projects/internal/vault"
)

func testReport() scan.Report {
	now := time.Now()
	return scan.Report{Projects: []scan.Project{
		{
			Name: "obsidian", Path: "/v/obsidian", RepoCount: 1, Pending: 1,
			Repos: []gitx.Repo{{Path: "/v/obsidian", Branch: "main", Dirty: true, Changes: 1, Pending: true}},
			Sessions: []agent.Session{
				{ID: "s1", Title: "Auditoría", Directory: "/v/obsidian", Updated: now.Add(-time.Hour)},
				{ID: "s2", Title: "Curaduría", Directory: "/v/obsidian", Updated: now.Add(-3 * time.Hour)},
			},
			Notes: []vault.Note{{Path: "/v/n/a.md", Rel: "a.md", Modified: now}},
		},
		{
			// the shape that bit the user: an inner directory with a broken
			// .git -- error repo, empty branch -- plus depth to overflow lists.
			Name: "icampus.fei.org", Path: "/p/icampus.fei.org", RepoCount: 6,
			Repos: []gitx.Repo{
				{Path: "/p/icampus.fei.org/icampus", Error: "fatal: detected dubious ownership in repository at '/p/icampus.fei.org/icampus'\n" +
					"To add an exception for this directory, call:\n\n\tgit config --global --add safe.directory /p/icampus.fei.org/icampus"},
				{Path: "/p/carryover400", Branch: "master"},
				{Path: "/p/moodle405", Branch: "master", Dirty: true, Changes: 3, Ahead: 2, Pending: true},
				{Path: "/p/moodle401", Branch: "master"},
				{Path: "/p/x", Branch: "feature/emailotp-queue-apply", Dirty: true, Changes: 3, Ahead: 12, Behind: 7, Pending: true},
				{Path: "/p/y", Branch: "master"},
			},
			Sessions: []agent.Session{
				{ID: "s1", Title: "Retomar pendientes en local_analytics", Directory: "/p/icampus.fei.org", Updated: now.Add(-2 * time.Hour), Cost: 1.234, OpenTodos: 3},
				{ID: "s2", Title: strings.Repeat("sesión larguísima ", 6), Directory: "/p/sub/dir", Updated: now.Add(-26 * time.Hour), Cost: 0.01},
			},
			Notes: []vault.Note{
				{Path: "/v/50 area.tr/tr.fei/upgrade.md", Rel: "50 area.tr/tr.fei/tr.fei.upgrade_a_4.5_OK.md", Modified: now.Add(-2 * time.Hour)},
				{Path: "/v/b.md", Rel: strings.Repeat("nota/reciente/", 6) + "b.md", Modified: now.Add(-4 * time.Hour)},
			},
		},
		{
			Name: "gone", Path: "/missing", Missing: true,
		},
	}}
}

func drive(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var nm tea.Model
		nm, cmd = m.Update(msg)
		m = nm.(Model)
	}
	_ = cmd
	return m
}

// The frame must never exceed the window: the alt-screen has no room for a
// 31st row and the terminal eats the top border to make it.
func TestViewNeverExceedsWindowSize(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // never let a j press touch the real state
	for _, size := range []struct{ w, h int }{
		{120, 30}, {150, 42}, {80, 24}, {200, 50}, {100, 22}, {300, 60},
	} {
		m := New(&config.Config{})
		m = drive(t, m, tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = drive(t, m, scanDoneMsg{report: testReport()})
		check := func(state string) {
			t.Helper()
			view := m.View()
			n := strings.Count(view, "\n") + 1
			// one spare row: a frame that ends on the last terminal row
			// leaves the renderer no cursor line and the top scrolls away
			if n > size.h-1 {
				t.Errorf("%dx%d %s: view is %d lines, window %d\n%s",
					size.w, size.h, state, n, size.h, view)
			}
		}
		check("projects focus")
		for i := 0; i < 3; i++ {
			m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		}
		check("after 3 downs")
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		check("context focus")
		m = drive(t, m, commitsDoneMsg{repoPath: "/p/icampus.fei.org/icampus", commits: nil})
		check("commits loaded")
		// a multi-line git failure must not leak rows into the status line
		m.status = "commits: fatal: detected dubious ownership\nTo add an exception for this directory, call:\n\n\tgit config ..."
		check("multiline status")
		m.help = true
		check("help open")
	}
}

// A click on a pane's row focuses the pane and selects exactly that row,
// using the same geometry the renderer just painted with.
func TestMouseClickSelectsRow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})

	g := m.geom()

	// wheel over the sessions pane moves only its cursor
	r := g.rects[paneSessions]
	before := m.cursors[paneSessions]
	m = drive(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown,
		X: r.x + 10, Y: r.y + 3})
	if m.cursors[paneSessions] != before+1 {
		t.Fatalf("wheel: %d -> %d", before, m.cursors[paneSessions])
	}
	if m.focus == paneSessions {
		t.Fatalf("the wheel must not steal focus")
	}

	r = g.rects[paneProjects]
	click := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
		X: r.x + 5, Y: r.y + 1 + 2} // third visible project row
	m = drive(t, m, click)
	if m.focus != paneProjects || m.project != 2 {
		t.Fatalf("click mapped wrong: focus=%d project=%d (rect %+v)", m.focus, m.project, r)
	}
}

// The whole seat round-trips through the state file: the project/repo pair by
// path, and the pane holding focus with the row each pane was standing on.
func TestSelectionPersistsByPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1)
	m.repo = 2
	m.focus = paneSessions
	m.cursors[paneSessions] = 1
	m.persist()

	s := loadState()
	if s.Project != "/p/icampus.fei.org" || s.Repo != "/p/moodle405" {
		t.Fatalf("roundtrip: %+v", s)
	}

	// a fresh model restores the whole seat, not just the repo
	fresh := New(&config.Config{})
	fresh = drive(t, fresh, tea.WindowSizeMsg{Width: 150, Height: 40})
	fresh = drive(t, fresh, scanDoneMsg{report: testReport()})
	if fresh.project != 1 || fresh.repo != 2 {
		t.Fatalf("restore: project=%d repo=%d", fresh.project, fresh.repo)
	}
	if fresh.focus != paneSessions || fresh.cursors[paneSessions] != 1 {
		t.Fatalf("restore: focus=%d cursors=%v", fresh.focus, fresh.cursors)
	}

	// and quitting writes the last cursor move, which is not a change of
	// selection and so never triggered a save on its own
	m.focus = paneNotes
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if q := loadState(); q.Cursors["notes"] == 1 {
		t.Fatalf("a cursor move alone must not rewrite the state: %+v", q)
	}
	m.quit()
	if q := loadState(); q.Focus != "notes" || q.Cursors["notes"] != 1 {
		t.Fatalf("the quit path must save the last move: %+v", loadState())
	}
}

// A restored seat that no longer fits the world it lands in is clamped, not
// trusted: a pane that does not exist any more cannot hold focus, and a row
// past the end of a shorter list cannot be stood on.
func TestPersistedStateClampsToWhatExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	rep := testReport()
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/docker-compose.yml"
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.focus = paneEnv
	m.cursors[paneSessions] = 9 // the fixture has two
	m.cursors[paneNotes] = 9
	m.persist()

	fresh := New(&config.Config{})
	fresh = drive(t, fresh, tea.WindowSizeMsg{Width: 150, Height: 40})
	// the same project, no compose file this time: the ENV pane is not painted
	fresh = drive(t, fresh, scanDoneMsg{report: testReport()})
	if fresh.focus != paneProjects {
		t.Fatalf("focus restored onto a pane that is not on screen: %d", fresh.focus)
	}
	if got := fresh.cursors[paneSessions]; got != 1 {
		t.Fatalf("sessions cursor past the end: %d", got)
	}
	if got := fresh.cursors[paneNotes]; got != 1 {
		t.Fatalf("notes cursor past the end: %d", got)
	}
}

// With a compose environment on the selected project, the ENV pane joins
// the tab order, the layout, and the click map; without one it is absent.
func TestEnvPaneLifecycle(t *testing.T) {
	rep := testReport() // icampus (index 1) gains the env below
	rep.Projects[1].EnvDir = "/p/icampus.fei.org"
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/docker-compose.yml"

	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	if m.geom().hasEnv {
		t.Fatal("env pane before any scan")
	}

	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1) // the project with the compose file
	if !m.geom().hasEnv {
		t.Fatalf("env pane missing for %+v", m.proj())
	}
	order := m.focusOrder()
	if len(order) != 6 || order[2] != paneEnv {
		t.Fatalf("focus order: %v", order)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab}, tea.KeyMsg{Type: tea.KeyTab}) // repos -> env
	if m.focus != paneEnv {
		t.Fatalf("tab into env: %d", m.focus)
	}
	// 'r' over the ENV pane means restart, not rescan: with no service
	// selected yet it reports instead of firing the rescan path.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.loading || m.status == "" {
		t.Fatalf("r should be the env's restart, not a rescan: %+v", m)
	}
	// and the frame still fits
	if n := strings.Count(m.View(), "\n") + 1; n > 39 {
		t.Fatalf("env view too tall: %d", n)
	}
}

// The ENV pane re-reads compose state on a tick, silently (no spinner, the
// list stays), and its docker keys replace the legend while it is focused.
func TestEnvPaneRefreshesAndShowsKeys(t *testing.T) {
	rep := testReport()
	rep.Projects[1].EnvDir = "/p/icampus.fei.org"
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/docker-compose.yml"

	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m = drive(t, m, envDoneMsg{file: rep.Projects[1].EnvFile,
		services: []env.Service{{Name: "web", State: "running"}}})
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab}, tea.KeyMsg{Type: tea.KeyTab})

	if l := m.legend(); !strings.Contains(l, "restart") || !strings.Contains(l, "stop") {
		t.Fatalf("env legend missing docker keys: %q", l)
	}

	m = drive(t, m, envTickMsg{})
	if !m.envLoading || len(m.envSvcs) != 1 {
		t.Fatalf("tick should refresh silently: loading=%v svcs=%d", m.envLoading, len(m.envSvcs))
	}
	if strings.Contains(m.paneEnv(m.geom()), "docker compose ps") {
		t.Fatal("background refresh must not show the first-load spinner")
	}
	m = drive(t, m, envDoneMsg{file: rep.Projects[1].EnvFile,
		services: []env.Service{{Name: "web", State: "exited"}}})
	if m.envLoading || m.envSvcs[0].State != "exited" {
		t.Fatalf("refresh result not applied: %+v", m.envSvcs)
	}
}

// A session that left work behind says so before the cursor lands on it: the
// badge is what decides WHICH session to resume, and the pane's title carries
// the total so the user knows the count is about this list, not one row.
func TestSessionBadgeShowsOpenTodos(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1) // s1 has three open items in the fixture, s2 none
	body := m.paneSessions(m.geom())
	if !strings.Contains(body, "▸3") {
		t.Fatalf("the badge never reached the row:\n%s", body)
	}
	if strings.Contains(body, "▸0") {
		t.Fatal("a session with nothing open is badged anyway")
	}
	if !strings.Contains(body, "3 open") {
		t.Fatalf("the pane total is missing:\n%s", body)
	}
	// the rows still fit the pane they are painted in
	for _, size := range []struct{ w, h int }{{150, 40}, {80, 24}, {60, 22}} {
		sm := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: size.w, Height: size.h})
		sm = drive(t, sm, scanDoneMsg{report: testReport()})
		sm.selectProject(1)
		if n := strings.Count(sm.View(), "\n") + 1; n > size.h-1 {
			t.Fatalf("%dx%d with badges: %d lines\n%s", size.w, size.h, n, sm.View())
		}
	}
}

// Which key runs what, on the row the cursor stands on: this is the net the
// filter is about to be hung on.
func TestKeysActOnTheSelectedRow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got := captureActions(t)

	rep := testReport()
	rep.Projects[1].EnvDir = "/p/icampus.fei.org"
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/docker-compose.yml"
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	key := func(k string) tea.Msg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}

	// the OPEN IN letters act on the selected repository
	m.repo = 2
	m = drive(t, m, key("c"))
	if c := lastCmd(t, got); !strings.Contains(c, "omarchy-launch-editor /p/moodle405") {
		t.Fatalf("c: %q", c)
	}
	m = drive(t, m, key("t"))
	if c := lastCmd(t, got); !strings.Contains(c, "--dir=/p/moodle405") {
		t.Fatalf("t: %q", c)
	}
	// enter on the repos pane is lazygit
	m.focus = paneRepos
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if c := lastCmd(t, got); c != "omarchy-launch-tui lazygit -p /p/moodle405" {
		t.Fatalf("enter: %q", c)
	}

	// a commit's enter copies its sha, not the row's position
	m = drive(t, m, commitsDoneMsg{repoPath: "/p/moodle405", commits: []gitx.Commit{
		{Hash: "97d5fe3", Subject: "Move files"}, {Hash: "82cde26", Subject: "Audit"},
	}})
	m.focus = paneCommits
	m.cursors[paneCommits] = 1
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if c := lastCmd(t, got); c != "wl-copy 82cde26" {
		t.Fatalf("copy sha: %q", c)
	}

	// resuming a session lands in the directory that session ran in
	sid := m.sessionsFor()[0].ID
	m.focus = paneSessions
	m = drive(t, m, key("s"))
	if c := lastCmd(t, got); !strings.Contains(c, "--dir="+m.sessionsFor()[0].Directory) ||
		!strings.Contains(c, "--session "+sid) {
		t.Fatalf("resume: %q", c)
	}

	// a note opens the vault file it names
	m.focus = paneNotes
	m.cursors[paneNotes] = 1
	m = drive(t, m, key("d"))
	if c := lastCmd(t, got); c != "uwsm-app -- xdg-open "+m.proj().Notes[1].Path {
		t.Fatalf("open note: %q", c)
	}

	// and the compose verbs run against the detected file, on the service the
	// ENV cursor holds
	m.envSvcs = []env.Service{{Name: "web", State: "running"}, {Name: "db", State: "exited"}}
	m.focus = paneEnv
	m.cursors[paneEnv] = 1
	m = drive(t, m, key("r"))
	if c := lastCmd(t, got); !strings.Contains(c,
		"docker compose -f /p/icampus.fei.org/docker-compose.yml restart db") {
		t.Fatalf("compose restart: %q", c)
	}
	// the echo in the status line names the thing it acted on, not a path
	if !strings.Contains(m.status, "compose") {
		t.Fatalf("no feedback for the compose verb: %q", m.status)
	}
}

// fetch, pull and push run on the selected repository, in a terminal the user
// watches. Push leaves the machine, so it asks for the same keystroke twice,
// and anything that moves the selection or the focus disarms it.
func TestGitVerbs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got := captureActions(t)
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1)
	m.repo = 2 // /p/moodle405
	key := func(k string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)} }

	m = drive(t, m, key("f"))
	if c := lastCmd(t, got); c != "uwsm-app -- xdg-terminal-exec --dir=/p/moodle405 -e git fetch --prune" {
		t.Fatalf("fetch: %q", c)
	}
	m = drive(t, m, key("F"))
	if c := lastCmd(t, got); !strings.HasSuffix(c, "git pull --ff-only") {
		t.Fatalf("pull: %q", c)
	}

	*got = nil
	m = drive(t, m, key("P"))
	if len(*got) != 0 {
		t.Fatalf("one P must not push: %v", *got)
	}
	if !strings.Contains(m.status, "press P again") {
		t.Fatalf("no arming feedback: %q", m.status)
	}
	m = drive(t, m, key("P"))
	if c := lastCmd(t, got); !strings.HasSuffix(c, "git push") {
		t.Fatalf("the second P: %q", c)
	}

	// walking away disarms: the armed push belongs to a repository, and a
	// pane change is a walk. It also persists the seat without moving it.
	before := loadState()
	m = drive(t, m, key("P"))
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.pushArmed {
		t.Fatal("cycling panes left the push armed")
	}
	if after := loadState(); after.Repo != before.Repo || after.Project != before.Project {
		t.Fatalf("a pane change moved the seat: %+v -> %+v", before, after)
	}
	*got = nil
	m = drive(t, m, key("P"))
	if len(*got) != 0 {
		t.Fatalf("the push fired after being disarmed: %v", *got)
	}
	// the same for a cursor move inside the same pane
	m = drive(t, m, key("P"))
	m = drive(t, m, key("j"))
	*got = nil
	m = drive(t, m, key("P"))
	if len(*got) != 0 {
		t.Fatalf("the push followed the cursor to another repo: %v", *got)
	}
	// and the verbs do nothing at all without a repository selected
	bare := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: 150, Height: 40})
	bare = drive(t, bare, scanDoneMsg{report: scan.Report{Projects: []scan.Project{{Name: "empty", Path: "/e"}}}})
	bare = drive(t, bare, key("f"))
	if strings.Contains(bare.status, "failed") || len(*got) > 0 {
		t.Fatalf("no repository, no git: %q", bare.status)
	}
	if !strings.Contains(bare.status, "no repository") {
		t.Fatalf("silent refusal: %q", bare.status)
	}
}

// The compose verbs go to a terminal, on the service the cursor holds, and
// every one of them names the file that was detected rather than a default.
func TestComposeVerbs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got := captureActions(t)
	rep := testReport()
	rep.Projects[1].EnvDir = "/p/icampus.fei.org"
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/compose.yaml"
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.envSvcs = []env.Service{
		{Name: "web", State: "running", Image: "feicampus45-webserver", Ports: "8080→80"},
		{Name: "db", State: "exited", Image: "mariadb:11.4", Exit: 137},
	}
	m.focus = paneEnv
	key := func(k string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)} }

	for _, tc := range []struct{ key, want string }{
		{"u", "up -d"}, // the whole environment, no service
		{"s", "stop web"},
		{"d", "down"},
		{"b", "build web"},
		{"p", "pull web"},
		{"k", "kill web"},
		{"l", "logs -f web"},
		{"x", "exec web sh"},
	} {
		m.cursors[paneEnv] = 0
		m = drive(t, m, key(tc.key))
		if c := lastCmd(t, got); !strings.Contains(c, "-f /p/icampus.fei.org/compose.yaml "+tc.want) {
			t.Fatalf("%s: %q, want ...%s", tc.key, c, tc.want)
		}
	}

	// enter is the same verb as l: following logs cannot break anything
	m.cursors[paneEnv] = 1
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if c := lastCmd(t, got); !strings.Contains(c, "logs -f db") {
		t.Fatalf("enter: %q", c)
	}
	// the rows carry what the pane knows: published ports beat the image
	body := m.paneEnv(m.geom())
	if !strings.Contains(body, "8080→80") {
		t.Fatalf("no ports in the rows:\n%s", body)
	}
	if !strings.Contains(body, "mariadb:11.4") {
		t.Fatalf("no image for the service with no ports:\n%s", body)
	}
	// a service name, an image and a port are all searchable
	m.cursors[paneEnv] = 0
	m = drive(t, m, key("/"), key("8080"))
	if n := m.listCount(paneEnv); n != 1 {
		t.Fatalf("filter by port: %d rows", n)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEscape}, tea.KeyMsg{Type: tea.KeyEscape})
	// and verbs that need a service say so instead of acting on nothing
	// (the second esc walked the focus off the ENV pane, as it should)
	m.focus = paneEnv
	m.envSvcs = nil
	m = drive(t, m, key("r"))
	if !strings.Contains(m.status, "no service") {
		t.Fatalf("restart with nothing selected: %q", m.status)
	}
	// the legend shows the docker verbs while the pane holds focus
	if l := m.legend(); !strings.Contains(l, "build") || !strings.Contains(l, "kill") {
		t.Fatalf("the env legend is missing its new keys: %q", l)
	}
}

// A broken feed announces itself: a warning in the pane title, the reason in
// the status line while that pane holds focus, and an empty list that
// explains why it is empty instead of looking dead. A feed that merely has
// nothing for this project explains itself without the warning.
func TestFeedErrorsAreVisible(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rep := testReport()
	rep.Projects[1].FeedErrors = []string{"sessions: opencode.db session schema changed"}
	rep.Projects[2].FeedNotes = []string{"notes: no docs folder matched gone"}

	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.status = "" // a rescan must not leave a stale action in the status
	m.focus = paneSessions
	if !strings.Contains(m.paneSessions(m.geom()), "⚠") {
		t.Fatal("the sessions title lost its warning")
	}
	if !strings.Contains(m.statusLine(), "schema changed") {
		t.Fatalf("the reason never reached the status: %q", m.statusLine())
	}

	m.selectProject(2) // no docs, no failure: the pane says so quietly
	m.focus = paneNotes
	if body := m.paneNotes(m.geom()); !strings.Contains(body, "no docs folder matched") {
		t.Fatalf("an empty pane should explain itself:\n%s", body)
	}
	if strings.Contains(m.paneNotes(m.geom()), "⚠") {
		t.Fatal("a note is not a broken feed")
	}

	// the same note at report level reaches the pane too
	m2 := New(&config.Config{})
	m2 = drive(t, m2, tea.WindowSizeMsg{Width: 150, Height: 40})
	m2 = drive(t, m2, scanDoneMsg{report: scan.Report{
		FeedNotes: []string{"sessions: sqlite3 not installed"},
		Projects:  rep.Projects,
	}})
	m2.selectProject(2)
	if got, broken := m2.feedMsg(paneSessions); got != "sqlite3 not installed" || broken {
		t.Fatalf("report-level note: %q broken=%v", got, broken)
	}
	why, broken := m2.feedMsg(paneCommits)
	if why != "" || broken {
		t.Fatalf("a pane that reads no feed must not warn: %q %v", why, broken)
	}
	// and finding a note never rewrites the slices it looked at
	m2 = drive(t, m2, scanDoneMsg{report: rep})
	if len(rep.Projects[2].FeedNotes) != 1 || rep.Projects[2].FeedNotes[0] == "" {
		t.Fatalf("feed notes were mutated by the lookup: %+v", rep.Projects[2].FeedNotes)
	}
}

// The cursor of every pane addresses a VISIBLE ROW, and the item it stands
// for is resolved through the row. While nothing is filtered the two numbers
// are equal; this pins the mapping so a later filter cannot quietly move the
// target out from under the cursor, and so no action can index a backing
// slice with a position that is off the end of it.
func TestCursorPositionsResolveToRows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rep := testReport()
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/docker-compose.yml"

	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.envSvcs = []env.Service{{Name: "web", State: "running"}}
	m = drive(t, m, commitsDoneMsg{repoPath: m.proj().Repos[0].Path, commits: []gitx.Commit{
		{Hash: "97d5fe3", Subject: "Move files", Author: "santiago"},
		{Hash: "82cde26", Subject: "Audit", Author: "santiago"},
	}})
	m.envSvcs = []env.Service{{Name: "web", State: "running"}, {Name: "db", State: "exited"}}

	backing := func(p paneFocus) int {
		switch p {
		case paneProjects:
			return len(m.report.Projects)
		case paneRepos:
			return len(m.proj().Repos)
		case paneEnv:
			return len(m.envSvcs)
		case paneCommits:
			return len(m.commits)
		case paneSessions:
			return len(m.sessionsFor())
		default:
			return len(m.proj().Notes)
		}
	}
	set := func(p paneFocus, pos int) Model {
		nm, _ := m.setCursorPos(p, pos)
		return nm.(Model)
	}

	for _, p := range m.focusOrder() {
		n := backing(p)
		if got := m.listCount(p); got != n {
			t.Fatalf("pane %d rows: %d, want %d", p, got, n)
		}
		for pos := 0; pos < n; pos++ {
			m = set(p, pos)
			if got := m.realIdx(p); got != pos {
				t.Fatalf("pane %d pos %d resolved to index %d", p, pos, got)
			}
			if _, ok := m.rowAt(p); !ok {
				t.Fatalf("pane %d pos %d has no row", p, pos)
			}
		}
		// past the end clamps instead of falling off the list
		m = set(p, n+5)
		if got := m.realIdx(p); n == 0 {
			if got != -1 {
				t.Fatalf("empty pane %d resolved to %d", p, got)
			}
		} else if got != n-1 {
			t.Fatalf("pane %d clamped to %d, want %d", p, got, n-1)
		}
	}

	// a wheel over a pane that is not focused moves it without stealing focus
	m.focus = paneProjects
	before := m.focus
	nm, _ := m.setCursorPos(paneSessions, 1)
	m = nm.(Model)
	if m.focus != before {
		t.Fatal("moving another pane stole the focus")
	}
}

// Paging jumps by the rows a pane can actually show, not by the window: a
// list pane is a handful of rows tall, and a window-sized jump would fly past
// its end and land clamped without the user seeing why.
func TestPageKeysMoveByVisibleRows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var commits []gitx.Commit
	for i := range 15 {
		commits = append(commits, gitx.Commit{Hash: fmt.Sprintf("%07x", i),
			Subject: fmt.Sprintf("commit %d", i), Author: "santiago"})
	}

	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1)
	m.repo = 1 // the commits reply is race-guarded on the repo it was asked for
	m = drive(t, m, commitsDoneMsg{repoPath: m.proj().Repos[1].Path, commits: commits})
	m.focus = paneCommits

	rows, n := m.geom().innerH(paneCommits), m.listCount(paneCommits)
	if n <= rows {
		t.Fatalf("the fixture must overflow the pane to page it: %d rows shown, %d commits", rows, n)
	}
	page := func(t tea.KeyType) tea.Msg { return tea.KeyMsg{Type: t} }

	m = drive(t, m, page(tea.KeyPgDown))
	if got := m.cursors[paneCommits]; got != rows {
		t.Fatalf("pgdown moved %d, want a page of %d", got, rows)
	}
	m = drive(t, m, page(tea.KeyPgUp))
	if m.cursors[paneCommits] != 0 {
		t.Fatalf("pgup did not come back: %d", m.cursors[paneCommits])
	}
	m = drive(t, m, page(tea.KeyEnd))
	if m.cursors[paneCommits] != n-1 {
		t.Fatalf("end: %d, want %d", m.cursors[paneCommits], n-1)
	}
	m = drive(t, m, page(tea.KeyHome))
	if m.cursors[paneCommits] != 0 {
		t.Fatalf("home: %d", m.cursors[paneCommits])
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlD})
	if half := m.cursors[paneCommits]; half != max(1, rows/2) {
		t.Fatalf("ctrl+d: %d, want half of %d", half, rows)
	}
	// and the legend offers the page keys only to a pane that needs them
	if !strings.Contains(m.legend(), "pgup/dn") {
		t.Fatal("an overflowing pane should advertise paging")
	}
	m.focus = paneProjects
	if strings.Contains(m.legend(), "pgup/dn") {
		t.Fatal("a pane that fits should not")
	}
	// paging an empty pane is a no-op, not a panic
	m = drive(t, m, scanDoneMsg{report: scan.Report{Projects: []scan.Project{{Name: "empty", Path: "/e"}}}})
	m.focus = paneCommits
	m = drive(t, m, page(tea.KeyPgDown), page(tea.KeyEnd))
	if m.listCount(paneCommits) != 0 {
		t.Fatalf("a new project must drop the old repo's history: %d rows", m.listCount(paneCommits))
	}
	if m.cursors[paneCommits] != 0 {
		t.Fatalf("empty pane moved: %d", m.cursors[paneCommits])
	}
}

// A clipped list marks the cut: the rows grow by exactly the gutter column,
// stay the same width as each other, and a list that fits grows by nothing.
func TestScrollbarMarksTheCut(t *testing.T) {
	items := make([]string, 20)
	for i := range items {
		items[i] = fmt.Sprintf("row %d", i)
	}

	rows := strings.Split(listWindow(items, 0, 5, 12), "\n")
	if len(rows) != 5 {
		t.Fatalf("window height: %d rows", len(rows))
	}
	for i, line := range rows {
		if w := lipgloss.Width(line); w != 13 {
			t.Fatalf("row %d is %d wide with the gutter, want 13: %q", i, w, line)
		}
	}
	if thumbs := strings.Count(strings.Join(rows, "\n"), "┃"); thumbs < 1 {
		t.Fatalf("no thumb in the gutter:\n%s", strings.Join(rows, "\n"))
	}

	// the thumb follows the window down the list
	rowOf := func(s string) int {
		for i, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "┃") {
				return i
			}
		}
		return -1
	}
	top, bottom := rowOf(listWindow(items, 0, 5, 12)), rowOf(listWindow(items, 19, 5, 12))
	if top < 0 || bottom < 0 || top >= bottom {
		t.Fatalf("the thumb stayed put: row %d at the top, row %d at the end", top, bottom)
	}

	// a list that fits needs no gutter, and its rows keep their own width
	fits := strings.Split(listWindow(items[:3], 0, 5, 12), "\n")
	if len(fits) != 3 || strings.Contains(fits[0], "┃") || strings.Contains(fits[0], "│") {
		t.Fatalf("an unclipped pane grew a gutter: %q", fits[0])
	}
	// an empty pane paints nothing
	if got := listWindow(nil, 0, 5, 12); got != "" {
		t.Fatalf("empty list: %q", got)
	}
}

// The help screen is the one view whose height comes from its own content, so
// it is checked on its own: every window the cockpit accepts has to show it
// without a row walking off the bottom -- the rows may not wrap either, which
// is what made the old one overflow at 80 columns.
func TestHelpFitsEveryWindowSize(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, size := range []struct{ w, h int }{
		{60, 22}, {61, 23}, {80, 24}, {100, 30}, {120, 22}, {150, 40}, {200, 22}, {300, 60},
	} {
		m := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = drive(t, m, scanDoneMsg{report: testReport()})
		m.help = true
		view := m.View()
		m.help = false
		if n := strings.Count(view, "\n") + 1; n > size.h-1 {
			t.Errorf("%dx%d: help is %d lines, window %d\n%s", size.w, size.h, n, size.h, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if w := lipgloss.Width(line); w > size.w {
				t.Errorf("%dx%d: a help row is %d wide\n%s", size.w, size.h, w, line)
			}
		}
	}
}

// An action's echo leaves on its own: the status line is shared with the
// session todos and the feed note, and it used to sit there until the next
// pane cycle hid them both. The expiry tick carries the generation of the
// line that armed it, so a slow tick cannot wipe a newer message.
func TestStatusExpires(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})

	m.setStatus("lazygit: moodle405")
	stale := m.statusGen
	m.setStatus("copy: /p/moodle405")
	if m.statusGen == stale {
		t.Fatal("setStatus must count the lines it writes")
	}
	m = drive(t, m, statusExpireMsg{gen: stale})
	if m.status != "copy: /p/moodle405" {
		t.Fatalf("a stale tick wiped the newer line: %q", m.status)
	}
	m = drive(t, m, statusExpireMsg{gen: m.statusGen})
	if m.status != "" {
		t.Fatalf("the line never expired: %q", m.status)
	}
	// with nothing to show, the status falls back to the project path
	if got := m.statusLine(); !strings.Contains(got, "obsidian") {
		t.Fatalf("the path vanished from the status: %q", got)
	}
}

// The theme and the default agent are files the desktop rewrites under a
// running cockpit: they are read again on T, and not before.
func TestThemeAndAgentReload(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTheme(t, state, "#ff0000")
	if err := os.MkdirAll(filepath.Join(home, ".config", "omarchy", "defaults"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "omarchy", "defaults", "agent"),
		[]byte("opencode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reloadPalette()
	loadDefaultAgent()
	if got := pal().accent; got != lipgloss.Color("#ff0000") {
		t.Fatalf("accent from the theme: %q", got)
	}
	if defaultAgent() != "opencode" {
		t.Fatalf("agent: %q", defaultAgent())
	}

	// omarchy changed the theme and the agent underneath: nothing notices on
	// its own, and the model was built before the change
	m := New(&config.Config{})
	writeTheme(t, state, "#00ff00")
	if err := os.WriteFile(filepath.Join(home, ".config", "omarchy", "defaults", "agent"),
		[]byte("claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pal().accent; got != lipgloss.Color("#ff0000") {
		t.Fatal("the palette moved without being asked")
	}
	if defaultAgent() != "opencode" {
		t.Fatal("the agent name moved without being asked")
	}

	m = drive(t, m,
		tea.WindowSizeMsg{Width: 150, Height: 40},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})
	if got := pal().accent; got != lipgloss.Color("#00ff00") {
		t.Fatalf("T did not reload the theme: %q", got)
	}
	if defaultAgent() != "claude" {
		t.Fatalf("T did not reload the agent: %q", defaultAgent())
	}
	body, _ := openInBody(120)
	if !strings.Contains(body, "claude") {
		t.Fatal("the launcher still names the old agent")
	}
	if !strings.Contains(m.status, "theme") {
		t.Fatalf("T gave no feedback: %q", m.status)
	}
}

// writeTheme drops an Omarchy colors.toml at the state path the palette reads.
func writeTheme(t *testing.T, state, accent string) {
	t.Helper()
	dir := filepath.Join(state, "omarchy", "current", "theme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "accent = \"" + accent + "\"\nforeground = \"#dddddd\"\n"
	if err := os.WriteFile(filepath.Join(dir, "colors.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// `/` narrows one pane at a time. What survives is what the cursor, the
// actions and the counts can reach; the pane next to it keeps showing the
// world, and clearing the word gives the rows back.
func TestFilterNarrowsOnePane(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got := captureActions(t)

	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.selectProject(1) // six repositories, two of them moodles
	m.focus = paneRepos
	key := func(k string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)} }

	// typing, then handing the keys back
	m = drive(t, m, key("/"), key("moo"), tea.KeyMsg{Type: tea.KeyEnter})
	if n := m.listCount(paneRepos); n != 2 {
		t.Fatalf("rows after the filter: %d, want 2", n)
	}
	if n := m.listCount(paneNotes); n != len(m.proj().Notes) {
		t.Fatalf("another pane got narrowed too: %d", n)
	}
	if !strings.Contains(m.paneRepos(m.geom()), "⌕") {
		t.Fatal("the narrowed pane does not say so")
	}
	if sl := m.statusLine(); !strings.Contains(sl, "moo") || !strings.Contains(sl, "2/6") {
		t.Fatalf("the status lost the needle or its count: %q", sl)
	}

	// the selection did not move: a filter narrows what is on screen, it does
	// not decide where the user is working
	if got := m.repo; got != 0 {
		t.Fatalf("the filter moved the selection: repo index %d", got)
	}
	if sel := m.selectedIndex(paneRepos); sel != -1 {
		t.Fatalf("the hidden selection should not be on screen: %d", sel)
	}
	// the first press walks into the matches, the next into the second one
	m = drive(t, m, key("j"))
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if c := lastCmd(t, got); !strings.HasSuffix(c, "-p /p/moodle405") {
		t.Fatalf("first match: %q", c)
	}
	m = drive(t, m, key("j"), tea.KeyMsg{Type: tea.KeyEnter})
	if c := lastCmd(t, got); !strings.HasSuffix(c, "-p /p/moodle401") {
		t.Fatalf("second match: %q", c)
	}

	// nothing matches: the pane explains itself and no row can be acted on
	m = drive(t, m, key("/"), key("zz"), tea.KeyMsg{Type: tea.KeyEnter})
	if m.listCount(paneRepos) != 0 {
		t.Fatalf("moozz should match nothing: %d", m.listCount(paneRepos))
	}
	if body := m.paneRepos(m.geom()); !strings.Contains(body, "no repositories") &&
		!strings.Contains(body, "matches") {
		t.Fatalf("an empty filtered pane is silent:\n%s", body)
	}

	// esc gets the user out of the input; a second press drops the needle and
	// the rows come back
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEscape}, tea.KeyMsg{Type: tea.KeyEscape})
	if m.listCount(paneRepos) != 6 || m.filterText != "" {
		t.Fatalf("esc did not clear: %d rows, needle %q", m.listCount(paneRepos), m.filterText)
	}

	// tab hands the same word to the next pane instead of typing a tab
	m = drive(t, m, key("/"), key("aud"))
	before := m.filterPane
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.filterPane == before {
		t.Fatal("tab did not move the filter")
	}
	if m.filterText != "aud" {
		t.Fatalf("tab lost the word: %q", m.filterText)
	}
	// backspace edits, and the pane under the cursor stays inside the rows
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filterText != "a" {
		t.Fatalf("backspace: %q", m.filterText)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	if m.filtering || m.listCount(paneRepos) != 6 || m.filterText != "" {
		t.Fatalf("esc did not clear: filtering=%v %d rows, needle %q",
			m.filtering, m.listCount(paneRepos), m.filterText)
	}
}

// The SESSIONS pane answers three different questions -- this repository,
// this project, everywhere -- and switching which one it answers moves nothing
// else: the selection, the other panes and the filter's word stay where the
// user left them, and a global session resumes in the directory IT ran in.
func TestSessionsScopeCycles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got := captureActions(t)
	now := time.Now()

	rep := testReport()
	rep.AllSessions = []agent.Session{
		{ID: "g1", Title: "theme_fei4 mejoras", Directory: "/p/other/repo", Project: "other", Updated: now},
		{ID: "g2", Title: "argentina background", Directory: "/h/elsewhere", Project: "elsewhere", Updated: now, OpenTodos: 2},
		{ID: "g3", Title: "carryover notes", Directory: "/p/carryover400", Project: "icampus.fei.org", Updated: now},
	}
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.repo = 2 // moodle405, a repository of its own
	m = drive(t, m, commitsDoneMsg{repoPath: "/p/moodle405", commits: []gitx.Commit{{Hash: "abc", Subject: "one"}}})
	S := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")}
	key := func(k string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)} }

	title := func() string { return m.paneSessions(m.geom()) }
	if !strings.Contains(title(), "SESSIONS · moodle405") {
		t.Fatalf("repo scope: %s", title())
	}

	m = drive(t, m, S)
	if m.scope != scopeProject || !strings.Contains(title(), "SESSIONS · project") {
		t.Fatalf("project scope: %d / %s", m.scope, title())
	}
	m = drive(t, m, S)
	if m.scope != scopeAll || !strings.Contains(title(), "all projects") {
		t.Fatalf("global scope: %d / %s", m.scope, title())
	}
	body := title()
	// the global list says where each session lives, and keeps its badges
	if !strings.Contains(body, "elsewhere") || !strings.Contains(body, "▸2") {
		t.Fatalf("the global rows lost their project or their badge:\n%s", body)
	}
	// resuming one lands on the directory that session ran in, not on the
	// repository the cursor happens to be pointing at elsewhere
	m.cursors[paneSessions] = 1
	m = drive(t, m, key("s"))
	c := lastCmd(t, got)
	if !strings.Contains(c, "--dir=/h/elsewhere") || !strings.Contains(c, "--session g2") {
		t.Fatalf("global resume: %q", c)
	}

	// and cycling back leaves the rest of the cockpit alone
	m = drive(t, m, S)
	if m.scope != scopeRepo || m.repo != 2 || len(m.commits) != 1 {
		t.Fatalf("cycling moved the selection: scope=%d repo=%d commits=%d", m.scope, m.repo, len(m.commits))
	}
	// a global list narrows with the same word as any other
	m = drive(t, m, S, S) // repo -> project -> all projects
	m.focus = paneSessions
	m = drive(t, m, key("/"), key("themefe"))
	if n := m.listCount(paneSessions); n != 1 {
		t.Fatalf("global filter: %d rows", n)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEscape}, tea.KeyMsg{Type: tea.KeyEscape})
	if n := m.listCount(paneSessions); n != 3 {
		t.Fatalf("the filter outlived esc: %d rows", n)
	}
}

// The context line says where the user is whatever pane holds focus, and it
// costs the frame no border: at every window size it stays ONE row, which is
// what the segment-dropping (never styled-string clipping) is for.
func TestBreadcrumbCarriesContext(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rep := testReport()
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/docker-compose.yml"
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.repo = 2

	line := m.breadcrumb()
	for _, want := range []string{"icampus.fei.org", "moodle405", "master", "●3", "↑2"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the breadcrumb lost %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("a wrapping breadcrumb eats the frame's top border:\n%s", line)
	}

	for _, w := range []int{60, 64, 70, 80, 100, 140, 300} {
		m.width = w
		got := m.breadcrumb()
		if strings.Contains(got, "\n") {
			t.Fatalf("%d columns: the row wrapped: %q", w, got)
		}
		if n := lipgloss.Width(got); n > w {
			t.Fatalf("%d columns: the breadcrumb is %d wide: %q", w, n, got)
		}
	}
	// nothing selected at all still says something
	m = drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: 150, Height: 40})
	if got := m.breadcrumb(); !strings.Contains(got, "no project") {
		t.Fatalf("empty cockpit: %q", got)
	}
}

// A row is one row: the key bar keeps what the window has room for, marks the
// rest with an ellipsis, and never soft-wraps -- a wrapped bottom bar costs the
// frame its top border, which is the bug the whole layout section is written
// around.
func TestLegendNeverWraps(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, w := range []int{40, 60, 70, 90, 120, 200} {
		for _, f := range []paneFocus{paneProjects, paneRepos, paneCommits, paneSessions, paneNotes, paneEnv} {
			m := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: w, Height: 30})
			m = drive(t, m, scanDoneMsg{report: testReport()})
			m.focus = f
			l := m.legend()
			if strings.HasPrefix(l, dim(" · ")) || strings.HasPrefix(l, " · ") {
				t.Fatalf("%d wide: the bar opens with its separator: %q", w, l)
			}
			if strings.Contains(l, "\n") {
				t.Fatalf("%d wide, pane %d: the bar wrapped: %q", w, f, l)
			}
			if n := lipgloss.Width(l); n > w {
				t.Fatalf("%d wide, pane %d: the bar is %d: %q", w, f, n, l)
			}
		}
	}
	m := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: 240, Height: 30})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	if l := m.legend(); strings.Contains(l, "…") {
		t.Fatalf("a wide window truncated the bar: %q", l)
	}
}

// cut is the clip that survives style, and pane titles need it: they now carry
// an accent count inside their own escape sequences, and slicing one of those
// in half paints garbage for the rest of the row. Styles are forced on so this
// test says something the plain-text renderer would pass by accident.
func TestCutKeepsSequencesBalanced(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	// lipgloss keeps the profile in a package global, and every other test in
	// this file asserts on plain rendered text: hand it back stripped.
	t.Cleanup(func() {
		lipgloss.DefaultRenderer().SetColorProfile(termenv.Ascii)
		lipgloss.SetColorProfile(termenv.Ascii)
	})

	styled := accent("PROJECTS") + dim(" · 4 · ") + orange("●12")
	if !strings.Contains(styled, "\x1b[") {
		t.Fatalf("styles produced no escapes at all: %q", styled)
	}
	got := cut(styled, 12)
	if n := lipgloss.Width(got); n > 12 {
		t.Fatalf("cut left %d columns: %q", n, got)
	}
	if opens := strings.Count(got, "\x1b["); opens != strings.Count(got, "m") {
		t.Fatalf("unbalanced escapes: %q", got)
	}
	// nothing is left holding a colour the terminal will never close
	if strings.Contains(got, "\x1b[") && !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("the truncated tail never resets: %q", got)
	}
}

// The projects pane shows a whole project at a glance, and the focused pane is
// marked in its border rather than only in its colour.
func TestProjectRowsAndActiveBorder(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rep := scan.Report{Projects: []scan.Project{
		{Name: "many", Path: "/m", Pending: 2, Repos: func() []gitx.Repo {
			var rs []gitx.Repo
			for i := range 6 {
				r := gitx.Repo{Path: fmt.Sprintf("/m/r%d", i), Branch: "main"}
				switch i % 3 {
				case 0:
					r.Dirty, r.Changes, r.Pending = true, 2, true
				case 1:
					r.Ahead = 1
					r.Pending = true
				}
				rs = append(rs, r)
			}
			return rs
		}()},
		{Name: "bare", Path: "/b"},
		{Name: "gone", Path: "/g", Missing: true},
	}}
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})

	body := m.paneProjects(m.geom())
	// one glyph per repository, in the order they are listed
	if !strings.Contains(body, "●↑✓●↑✓") {
		t.Fatalf("the glyph run never landed:\n%s", body)
	}
	if !strings.Contains(body, "missing") || !strings.Contains(body, "—") {
		t.Fatalf("a missing or empty project lost its badge:\n%s", body)
	}
	// a project with a docker environment is marked without asking docker:
	// the compose file is on disk, and a subprocess per project per scan is
	// not a price the list is worth
	withEnv := scan.Report{Projects: []scan.Project{
		{Name: "fei", Path: "/p/fei", EnvFile: "/p/fei/compose.yaml",
			Repos: []gitx.Repo{{Path: "/p/fei/moodle", Branch: "main"}}},
	}}
	em := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: 150, Height: 40})
	em = drive(t, em, scanDoneMsg{report: withEnv})
	if e := em.paneProjects(em.geom()); !strings.Contains(e, "⎈") {
		t.Fatalf("no compose marker:\n%s", e)
	}
	// the pending total moved from the row to the title
	if !strings.Contains(body, "PROJECTS · 3") || !strings.Contains(body, "●2") {
		t.Fatalf("the title carries no pending count:\n%s", body)
	}
	// the active pane is the one with the corner marker
	if !strings.Contains(body, "╭▶ PROJECTS") {
		t.Fatal("the focused pane is not marked")
	}
	if s := m.paneNotes(m.geom()); strings.Contains(s, "╭▶") {
		t.Fatal("a quiet pane marked itself active")
	}

	// a project with more repositories than the badge has columns says how
	// many it could not show, and no row overruns the pane it lives in
	wide := scan.Report{Projects: []scan.Project{{Name: "many", Path: "/m",
		Repos: func() []gitx.Repo {
			var rs []gitx.Repo
			for i := range 40 {
				rs = append(rs, gitx.Repo{Path: fmt.Sprintf("/m/r%d", i), Dirty: i%3 == 0, Changes: 1})
			}
			return rs
		}()}}}
	for _, size := range []struct{ w, h int }{{80, 24}, {150, 40}, {60, 22}} {
		wm := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: size.w, Height: size.h})
		wm = drive(t, wm, scanDoneMsg{report: wide})
		view := wm.View()
		if n := strings.Count(view, "\n") + 1; n > size.h-1 {
			t.Fatalf("%dx%d: %d lines\n%s", size.w, size.h, n, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.w {
				t.Fatalf("%dx%d: a row is %d wide: %q", size.w, size.h, got, line)
			}
		}
		if !strings.Contains(wm.paneProjects(wm.geom()), "+") {
			t.Fatal("forty repositories and no remainder marker")
		}
	}
}

// The dead space under a short repository list becomes a block about the
// repository the user is actually standing on: where it lives, what it pushes
// to, its last commit, the vault folder and the compose file belonging to it.
// It is a display, not a stop: the tab order does not grow, because a pane the
// user cannot act on should not steal the keys.
func TestDetailBlockUsesTheSpareRows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rep := testReport()
	rep.Projects[1].DocsFolder = "/v/50 area.tr/tr.fei"
	rep.Projects[1].EnvFile = "/p/icampus.fei.org/docker-compose.yml"

	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: rep})
	m.selectProject(1)
	m.repo = 2 // moodle405
	m = drive(t, m, commitsDoneMsg{repoPath: "/p/moodle405",
		commits: []gitx.Commit{{Hash: "97d5fe3", Subject: "Move files"}}})
	m = drive(t, m, remoteDoneMsg{repoPath: "/p/moodle405", url: "git@github.com:fei/moodle405.git"})

	g := m.geom()
	if g.detailH < listFloor {
		t.Fatalf("a 40-row window has no detail block: %d", g.detailH)
	}
	body := m.paneDetail(g)
	for _, want := range []string{"path ", "/p/moodle405", "97d5fe3", "Move files",
		"github.com:fei/moodle405.git", "tr.fei", "docker-compose.yml"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the detail block lost %q:\n%s", want, body)
		}
	}
	// it costs the frame nothing: projects and repositories keep their floors
	if g.projH < listFloor || g.repoH < listFloor {
		t.Fatalf("the left column starved: proj=%d repos=%d detail=%d", g.projH, g.repoH, g.detailH)
	}
	if g.projH+g.repoH+g.detailH != g.bodyH {
		t.Fatalf("the left column does not add up: %d+%d+%d != %d", g.projH, g.repoH, g.detailH, g.bodyH)
	}
	// no repository, no block
	m2 := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: 150, Height: 40})
	m2 = drive(t, m2, scanDoneMsg{report: scan.Report{Projects: []scan.Project{{Name: "bare", Path: "/b"}}}})
	if m2.geom().detailH != 0 || m2.paneDetail(m2.geom()) != "" {
		t.Fatal("a project with no repositories painted a detail block")
	}
	// a short window drops it rather than starving the lists, and the view
	// never overflows at any size
	for _, size := range []struct{ w, h int }{{60, 22}, {80, 24}, {100, 30}, {150, 40}, {200, 60}} {
		sm := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: size.w, Height: size.h})
		sm = drive(t, sm, scanDoneMsg{report: rep})
		sm.selectProject(1)
		sm = drive(t, sm, remoteDoneMsg{repoPath: m.proj().Repos[2].Path, url: "git@github.com:fei/moodle405.git"})
		view := sm.View()
		if n := strings.Count(view, "\n") + 1; n > size.h-1 {
			t.Fatalf("%dx%d: %d lines\n%s", size.w, size.h, n, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.w {
				t.Fatalf("%dx%d: a row is %d wide: %q", size.w, size.h, got, line)
			}
		}
	}
}

// A narrow tall window docks the cockpit: one column, every box as wide as the
// terminal, the boxes in the order the click map says, and nothing cut off the
// bottom. A short window refuses the stack however it is asked, because a
// frame taller than the screen eats its own top border.
func TestCompactStack(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := drive(t, New(&config.Config{}), tea.WindowSizeMsg{Width: 90, Height: 46})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	g := m.geom()
	if !g.single {
		t.Fatalf("90 columns x 46 rows should stack (bodyH %d)", g.bodyH)
	}
	view := m.View()
	if n := strings.Count(view, "\n") + 1; n > 45 {
		t.Fatalf("the stack is %d lines\n%s", n, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 90 {
			t.Fatalf("a stacked row is %d wide: %q", w, line)
		}
	}
	// the order on screen is the order in the map
	plain := strings.Split(view, "\n")
	row := func(prefix string) int {
		for i, l := range plain {
			if strings.Contains(l, prefix) {
				return i
			}
		}
		t.Fatalf("%q never painted", prefix)
		return -1
	}
	if a, b, c := row("PROJECTS"), row("REPOSITORIES"), row("OPEN IN"); !(a < b && b < c) {
		t.Fatalf("the stack order is wrong: projects %d, repos %d, open in %d", a, b, c)
	}
	if a, b := row("COMMITS"), row("SESSIONS"); a >= b {
		t.Fatalf("commits %d, sessions %d", a, b)
	}
	// a click lands on the box the map says it does
	r := g.rects[paneSessions]
	m = drive(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
		X: r.x + 5, Y: r.y + 1})
	if m.focus != paneSessions {
		t.Fatalf("click at row %d focused %d", r.y+1, m.focus)
	}
	// the detail block is the first thing the stack gives up
	if g.detailH != 0 {
		t.Fatalf("the stack kept the detail block: %d rows", g.detailH)
	}

	// forced shapes: "wide" at a dockable size stays two columns...
	wide := drive(t, New(&config.Config{Layout: "wide"}), tea.WindowSizeMsg{Width: 90, Height: 46})
	wide = drive(t, wide, scanDoneMsg{report: testReport()})
	if wide.geom().single {
		t.Fatal("layout=wide stacked anyway")
	}
	// ... and "compact" refuses when the rows are not there
	short := drive(t, New(&config.Config{Layout: "compact"}), tea.WindowSizeMsg{Width: 90, Height: 24})
	short = drive(t, short, scanDoneMsg{report: testReport()})
	if short.geom().single {
		t.Fatal("a 24-row window cannot hold six boxes")
	}
	if n := strings.Count(short.View(), "\n") + 1; n > 23 {
		t.Fatalf("24 rows: the view is %d lines", n)
	}
	// every size the cockpit accepts, in both shapes, fits
	for _, size := range []struct{ w, h int }{
		{60, 22}, {90, 30}, {90, 46}, {100, 60}, {109, 40}, {110, 40}, {140, 24},
	} {
		for _, layout := range []string{"", "wide", "compact"} {
			sm := drive(t, New(&config.Config{Layout: layout}),
				tea.WindowSizeMsg{Width: size.w, Height: size.h})
			sm = drive(t, sm, scanDoneMsg{report: testReport()})
			v := sm.View()
			if n := strings.Count(v, "\n") + 1; n > size.h-1 {
				t.Fatalf("%dx%d %s: %d lines\n%s", size.w, size.h, layout, n, v)
			}
			for _, line := range strings.Split(v, "\n") {
				if got := lipgloss.Width(line); got > size.w {
					t.Fatalf("%dx%d %s: a row is %d wide: %q", size.w, size.h, layout, got, line)
				}
			}
		}
	}
}

// Open todos of the selected session appear in the status line while the
// SESSIONS pane holds focus.
func TestTodosInStatusLine(t *testing.T) {
	m := New(&config.Config{})
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	m.focus = paneSessions
	m.cursors[paneSessions] = 0
	id := m.selectedSessionID()
	if id == "" {
		t.Skip("fixture has no selected session")
	}
	m = drive(t, m, todosDoneMsg{sessionID: id, todos: []agent.Todo{
		{Content: "deploy hotfix", Status: "pending"},
		{Content: "write report", Status: "in_progress"},
	}})
	view := m.View()
	if !strings.Contains(view, "2 open:") || !strings.Contains(view, "deploy hotfix") {
		t.Fatalf("todos missing from status:\n%s", view[strings.LastIndex(view, "\n")-200:])
	}
	// a stale answer (cursor moved on) is dropped
	m = drive(t, m, todosDoneMsg{sessionID: "ses_gone", todos: []agent.Todo{{Content: "ghost"}}})
	if strings.Contains(m.View(), "ghost") {
		t.Fatal("stale todos reply was rendered")
	}
}

// A cockpit opened from a keybind that knows where the user is looking starts
// on that project and repository -- and a directory nobody watches moves
// nothing, because opening on the first project would be a lie.
func TestSeedOpensOnTheFocusedWindow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// the seat the last run left behind, which a seed must outrank
	// the fixture's repositories are siblings of the project except for
	// icampus, which lives inside it -- the deepest match has to win
	m := New(&config.Config{}).WithSeed("/p/icampus.fei.org/icampus/mod")
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	m = drive(t, m, scanDoneMsg{report: testReport()})
	if m.project != 1 {
		t.Fatalf("seeded project: %d", m.project)
	}
	if repo, ok := m.selectedRepo(); !ok || filepath.Base(repo.Path) != "icampus" {
		t.Fatalf("seeded repository: %+v %v", repo, ok)
	}
	if m.cursors[paneSessions] != 0 {
		t.Fatal("a seed inherits rows from the last run")
	}

	// outside every watched folder: the ordinary default, no guess
	m2 := New(&config.Config{}).WithSeed("/tmp/random")
	m2 = drive(t, m2, tea.WindowSizeMsg{Width: 150, Height: 40})
	m2 = drive(t, m2, scanDoneMsg{report: testReport()})
	if m2.project != 0 || m2.repo != 0 {
		t.Fatalf("an unknown seed moved the selection: %d/%d", m2.project, m2.repo)
	}
}
