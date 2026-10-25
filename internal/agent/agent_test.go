package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildDB seeds a session table shaped like the guarded columns and returns
// its path. sqlite3 is a hard requirement of these tests (LookPath above).
func buildDB(t *testing.T, rows ...string) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	stmt := "CREATE TABLE session(id TEXT PRIMARY KEY, title TEXT, directory TEXT, " +
		"parent_id TEXT, time_updated INTEGER, time_archived INTEGER, cost REAL);"
	for _, r := range rows {
		stmt += " INSERT INTO session VALUES(" + r + ");"
	}
	if out, err := exec.Command("sqlite3", db, stmt).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", db)
	return db
}

func TestRecentFiltersByPrefixAndShape(t *testing.T) {
	buildDB(t,
		// newest first in time; the project root itself
		"'s1','root work','/srv/proj',NULL,300000,NULL,1.0",
		// inside a repo of the project -- counts for the project
		"'s2','repo work','/srv/proj/repoA',NULL,200000,NULL,0.5",
		// child session: parent_id set, never listed on its own
		"'s3','child','/srv/proj', 's2',250000,NULL,0.1",
		// archived
		"'s4','gone','/srv/proj',NULL,290000,299999,9",
		// another project
		"'s5','elsewhere','/srv/other',NULL,100000,NULL,2",
		// a directory that merely starts with the same text is NOT the project
		"'s6','lookalike','/srv/projection',NULL,150000,NULL,3",
	)

	got, err := Recent("/srv/proj", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want s1,s2 got %+v", got)
	}
	if got[0].ID != "s1" || got[1].ID != "s2" {
		t.Fatalf("order: %+v", got)
	}
	if got[0].Updated.Unix() != 300 { // ms -> seconds
		t.Fatalf("timestamp not normalized: %+v", got[0])
	}
}

func TestRecentLimit(t *testing.T) {
	buildDB(t,
		"'s1','a','/p',NULL,3000,NULL,0",
		"'s2','b','/p',NULL,2000,NULL,0",
		"'s3','c','/p',NULL,1000,NULL,0",
	)
	got, err := Recent("/p", 2)
	if err != nil || len(got) != 2 || got[1].ID != "s2" {
		t.Fatalf("%+v err=%v", got, err)
	}
}

func TestQuoteInPathStillMatches(t *testing.T) {
	root := "/srv/it's-proj"
	// The seed doubles the quote the way the reader's escaper must; if the
	// escaping ever regresses, the query looks for a broken literal and this
	// row goes missing (or sqlite errors), which fails the test loudly.
	buildDB(t, "'s1','here','"+strings.ReplaceAll(root, "'", "''")+"',NULL,1000,NULL,0")
	got, err := Recent(root, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("escaped root missed its sessions: %+v", got)
	}
}

func TestMissingColumnsDegrade(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	out, err := exec.Command("sqlite3", db,
		"CREATE TABLE session(id TEXT, title TEXT, directory TEXT, parent_id TEXT, time_updated INTEGER);").CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", db)
	if _, err := Recent("/x", 5); err != ErrSchema {
		t.Fatalf("want ErrSchema, got %v", err)
	}
}

func TestMissingDatabase(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", filepath.Join(t.TempDir(), "nope.db"))
	if _, err := Recent("/x", 5); err != ErrNoDatabase {
		t.Fatalf("want ErrNoDatabase, got %v", err)
	}
	// An empty file is not a database either (fresh writes can land as 0).
	empty := filepath.Join(t.TempDir(), "empty.db")
	f, _ := os.Create(empty)
	f.Close()
	t.Setenv("EI_PROJECTS_OPENCODE_DB", empty)
	if _, err := Recent("/x", 5); err != ErrNoDatabase {
		t.Fatalf("empty file: want ErrNoDatabase, got %v", err)
	}
}

// opencode 2.14 moved session history to a JSONL store: no session table
// (in either spelling) is not a broken schema, it is simply no sessions.
func TestNoSessionTablesDegradesQuietly(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	out, err := exec.Command("sqlite3", db,
		"CREATE TABLE session_message(id TEXT); CREATE TABLE event(id TEXT);").CombinedOutput()
	if err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", db)
	// ErrNoDatabase is the degradation scan.silentFeedError swallows: a db
	// with no session tables is as unusable for this feed as an absent one.
	if got, err := Recent("/x", 5); err != ErrNoDatabase || len(got) != 0 {
		t.Fatalf("want silent ErrNoDatabase, got %+v %v", got, err)
	}
}

// opencode 2.x writes session_v2 and freezes the legacy session table:
// with both present, only the live one may answer.
func TestPrefersSessionV2(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	cols := "id TEXT, title TEXT, directory TEXT, parent_id TEXT, time_updated INTEGER, time_archived INTEGER, cost REAL"
	stmt := "CREATE TABLE session(" + cols + "); CREATE TABLE session_v2(" + cols + ");" +
		"INSERT INTO session VALUES('old','legacy row','/p',NULL,9000,NULL,0);" +
		"INSERT INTO session_v2 VALUES('live','v2 row','/p',NULL,1000,NULL,0);"
	if out, err := exec.Command("sqlite3", db, stmt).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", db)

	got, err := Recent("/p", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "live" {
		t.Fatalf("must read session_v2 only: %+v", got)
	}
}

func TestOpenTodos(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	stmt := "CREATE TABLE todo(session_id TEXT, content TEXT, status TEXT, priority TEXT, position INTEGER, time_created INTEGER, time_updated INTEGER);" +
		"INSERT INTO todo VALUES('s1','finish migration','pending','high',2,100,100);" +
		"INSERT INTO todo VALUES('s1','write tests','in_progress','medium',1,90,90);" +
		"INSERT INTO todo VALUES('s1','deploy','completed','low',0,80,80);" +
		"INSERT INTO todo VALUES('s1','someday','cancelled','low',3,70,70);" +
		"INSERT INTO todo VALUES('s2','other session todo','pending','medium',0,1,1);"
	if out, err := exec.Command("sqlite3", db, stmt).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", db)

	got, err := OpenTodos("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 { // completed and cancelled excluded
		t.Fatalf("open todos only: %+v", got)
	}
	if got[0].Content != "write tests" || got[1].Content != "finish migration" {
		t.Fatalf("position order: %+v", got)
	}
	// A session with no list at all, and a missing table, are both silent.
	if got, err := OpenTodos("nobody"); err != nil || len(got) != 0 {
		t.Fatalf("empty session: %+v %v", got, err)
	}
	noTodos := filepath.Join(t.TempDir(), "plain.db")
	if _, err := exec.Command("sqlite3", noTodos, "CREATE TABLE session(id TEXT);").CombinedOutput(); err != nil {
		t.Skip("sqlite3 seed failed")
	}
	t.Setenv("EI_PROJECTS_OPENCODE_DB", noTodos)
	if got, err := OpenTodos("s1"); err != nil || len(got) != 0 {
		t.Fatalf("missing todo table must degrade quietly: %+v %v", got, err)
	}
}
