package scan

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santiagoei/ei-projects/internal/agent"
	"github.com/santiagoei/ei-projects/internal/config"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=t@e", "-c", "user.name=t"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A feed that cannot exist here is a report-level note, not a per-project
// error: the notes say why panes are empty without repeating themselves.
func TestFeedNotes(t *testing.T) {
	t.Setenv("EI_PROJECTS_OPENCODE_DB", filepath.Join(t.TempDir(), "absent.db"))

	notes := feedNotes(&config.Config{})
	sessions, vault := 0, 0
	for _, n := range notes {
		switch {
		case strings.HasPrefix(n, "sessions: "):
			sessions++
		case strings.HasPrefix(n, "notes: "):
			vault++
		}
	}
	if sessions != 1 {
		t.Fatalf("sessions feed note: %v", notes)
	}
	if vault != 1 { // no vault configured at all
		t.Fatalf("vault feed note missing: %v", notes)
	}

	cfg := &config.Config{VaultDir: t.TempDir()}
	for _, n := range feedNotes(cfg) {
		if strings.HasPrefix(n, "notes: ") {
			t.Fatalf("a readable vault must not be a failure: %v", n)
		}
	}
}

// A session is answered by the deepest watched folder holding it: the cockpit
// reads a name out of the row, and "obsidian" beats "notes" when the vault is
// watched both ways.
func TestProjectOfIsTheLongestMatch(t *testing.T) {
	projects := []Project{
		{Name: "notes", Path: "/home/notes"},
		{Name: "obsidian", Path: "/home/notes/obsidian"},
		{Name: "proj", Path: "/srv/proj"},
	}
	for _, tc := range []struct{ dir, want string }{
		{"/home/notes/obsidian/20/x.md"[:len("/home/notes/obsidian/20")], "obsidian"},
		{"/home/notes", "notes"},
		{"/home/notes/other", "notes"},
		{"/home/notesometry", ""}, // a text prefix is not a parent directory
		{"/srv/proj/repo", "proj"},
		{"/elsewhere", ""},
	} {
		if got := projectOf(tc.dir, projects); got != tc.want {
			t.Errorf("projectOf(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

// The badge on a session row is filled for the whole report in one query:
// every session of every project gets its count, and a session listed under
// two roots (a watch dir nested in another) is asked about once and appears
// twice.
func TestFillOpenTodos(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	stmt := "CREATE TABLE todo(session_id TEXT, content TEXT, status TEXT, position INTEGER, time_created INTEGER, time_updated INTEGER);" +
		"INSERT INTO todo VALUES('s1','a','pending',0,1,1);" +
		"INSERT INTO todo VALUES('s1','b','in_progress',1,1,1);" +
		"INSERT INTO todo VALUES('s1','c','completed',2,1,1);" +
		"INSERT INTO todo VALUES('s2','d','pending',0,1,1);"
	if out, err := exec.Command("sqlite3", db, stmt).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", db)

	projects := []Project{
		{Sessions: []agent.Session{{ID: "s1"}, {ID: "s2"}}},
		{Sessions: []agent.Session{{ID: "s1"}}}, // the nested watch dir repeats it
		{Sessions: []agent.Session{{ID: "s3"}}},
	}
	fillOpenTodos(projects, nil)
	if got := projects[0].Sessions[0].OpenTodos; got != 2 {
		t.Fatalf("s1 wants its two open items, got %d", got)
	}
	if got := projects[0].Sessions[1].OpenTodos; got != 1 {
		t.Fatalf("s2: %d", got)
	}
	if got := projects[1].Sessions[0].OpenTodos; got != 2 {
		t.Fatalf("the repeat lost its badge: %d", got)
	}
	if got := projects[2].Sessions[0].OpenTodos; got != 0 {
		t.Fatalf("a session with no list must stay unbadged: %d", got)
	}
}

// TestRunEndToEnd wires every feed through the real entry point over a
// sandbox: a watched folder that is a dirty repo, a fake opencode db whose
// newest session lives in a nested directory of it, and a vault folder named
// after the project holding one note.
func TestRunEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	home := t.TempDir()
	watch := filepath.Join(home, "Projects", "icampus.fei.org")
	if err := os.MkdirAll(watch, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, home, "init", "-q", "-b", "main", watch)
	if err := os.WriteFile(filepath.Join(watch, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, watch, "add", ".")
	git(t, watch, "commit", "-q", "-m", "c")
	if err := os.WriteFile(filepath.Join(watch, "file"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	} // now dirty
	// a compose environment at the project root must be auto-detected
	if err := os.WriteFile(filepath.Join(watch, "docker-compose.yml"), []byte("services: {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// build a real repo through the full path (tmp git repo, one commit)
	if err := os.MkdirAll(filepath.Join(watch, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, watch, "init", "-q", "-b", "main", filepath.Join(watch, "repo"))
	if err := os.WriteFile(filepath.Join(watch, "repo", "r"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Join(watch, "repo"), "add", ".")
	git(t, filepath.Join(watch, "repo"), "commit", "-q", "-m", "r")

	vaultRoot := filepath.Join(home, "obsidian")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Clientes", "icampus"), 0o755); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(vaultRoot, "Clientes", "icampus", "Sprint.md")
	if err := os.WriteFile(note, []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := exec.LookPath("sqlite3"); err == nil {
		db := filepath.Join(home, "opencode.db")
		// a session in the project (nested dir) -- prefix rules must count it
		_, err := exec.Command("sqlite3", db,
			"CREATE TABLE session(id TEXT, title TEXT, directory TEXT, parent_id TEXT, time_updated INTEGER, time_archived INTEGER, cost REAL); "+
				"INSERT INTO session VALUES('s1','work','"+filepath.Join(watch, "www")+"',NULL,3000,NULL,0.2);").CombinedOutput()
		if err != nil {
			t.Fatalf("seed db")
		}
		t.Setenv("EI_PROJECTS_OPENCODE_DB", db)
	} else {
		t.Setenv("EI_PROJECTS_OPENCODE_DB", filepath.Join(home, "absent.db")) // silent degradation
	}

	shell := filepath.Join(home, "shell.json")
	body, _ := json.Marshal(map[string]any{"bar": map[string]any{"layout": map[string]any{
		"left": []any{map[string]any{"id": "ei.git-status", "watchDirs": watch + ", " + vaultRoot}}}}})
	if err := os.WriteFile(shell, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("OMARCHY_SHELL_JSON", shell)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), cfg)
	if len(rep.Projects) != 2 {
		t.Fatalf("projects: %+v", rep.Projects)
	}

	var p *Project
	for i := range rep.Projects {
		if rep.Projects[i].Name == "icampus.fei.org" {
			p = &rep.Projects[i]
		}
	}
	if p == nil {
		t.Fatalf("project missing: %+v", rep.Projects)
	}
	if p.RepoCount != 2 || p.Pending != 1 { // the watch root + the nested repo
		t.Fatalf("git feed: %+v", *p)
	}
	if p.DocsFolder == "" || len(p.Notes) != 1 || p.Notes[0].Rel != filepath.Join("Sprint.md") {
		t.Fatalf("vault feed: %+v", *p)
	}
	if filepath.Base(p.EnvFile) != "docker-compose.yml" ||
		p.EnvFile != filepath.Join(p.Path, "docker-compose.yml") {
		t.Fatalf("env feed: %+v", *p)
	}
	if _, err := exec.LookPath("sqlite3"); err == nil && len(p.Sessions) != 1 {
		t.Fatalf("sessions feed: %+v", *p)
	}
	if _, err := exec.LookPath("sqlite3"); err == nil &&
		!p.Sessions[0].Updated.Equal(time.Unix(3, 0)) {
		t.Fatalf("session ts: %+v", p.Sessions[0])
	}

	// and the whole report survives the contract round-trip
	if _, err := rep.JSON(false); err != nil {
		t.Fatalf("json: %v", err)
	}
}
