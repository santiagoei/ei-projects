package scan

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
	if p.RepoCount != 1 || p.Pending != 1 {
		t.Fatalf("git feed: %+v", *p)
	}
	if p.DocsFolder == "" || len(p.Notes) != 1 || p.Notes[0].Rel != filepath.Join("Sprint.md") {
		t.Fatalf("vault feed: %+v", *p)
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
