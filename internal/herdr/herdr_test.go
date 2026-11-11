package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const snapshot = `{"id":"cli:api:snapshot","result":{"snapshot":{"agents":[],"panes":[
 {"pane_id":"w3:p1","agent_status":"unknown","cwd":"/home/ei","workspace_id":"w3","terminal_title_stripped":"ei@omarchy:~"},
 {"pane_id":"w5:p1","agent":"opencode","display_agent":"opencode","agent_status":"working","cwd":"/p/a","title":"OC | fix","workspace_id":"w5","tab_id":"w5:t1"},
 {"pane_id":"w6:p2","agent":"claude","agent_status":"blocked","cwd":"/p/b","terminal_title_stripped":"claude","workspace_id":"w6"}
]}}}`

func TestParseKeepsOnlyAgentPanes(t *testing.T) {
	panes, err := Parse([]byte(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 2 {
		t.Fatalf("a plain shell pane is not an agent: %+v", panes)
	}
	if panes[0].Name() != "opencode" || panes[0].Label() != "OC | fix" {
		t.Fatalf("display name and title: %+v", panes[0])
	}
	if panes[1].Name() != "claude" || panes[1].Label() != "claude" {
		t.Fatalf("fallback to agent and terminal title: %+v", panes[1])
	}
}

func TestParseRefusesAnErrorAnswer(t *testing.T) {
	for _, bad := range []string{`{"id":"x","error":{"code":"no_server"}}`, `not json`, `{"result":{}}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) should fail: an answer without a snapshot has no panes to show", bad)
		}
	}
}

func TestRankPutsTheQuestionFirst(t *testing.T) {
	if !(Rank("blocked") < Rank("working") && Rank("working") < Rank("done") &&
		Rank("done") < Rank("idle") && Rank("idle") < Rank("unknown")) {
		t.Fatal("blocked must rank before working, and so on down to unknown")
	}
}

// A fake herdr on disk: it answers the snapshot from a file and logs the
// focus requests. Nothing here touches the real server.
func fakeHerdr(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap.json")
	log = filepath.Join(dir, "focus.log")
	if err := os.WriteFile(snap, []byte(snapshot), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  'api snapshot') cat '" + snap + "' ;;\n" +
		"  'agent focus') echo \"$3\" >> '" + log + "' ;;\n" +
		"  *) exit 2 ;;\nesac\n"
	bin = filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestSnapshotAndFocusThroughTheBinary(t *testing.T) {
	bin, log := fakeHerdr(t)
	old := Bin
	Bin = bin
	t.Cleanup(func() { Bin = old })

	if !Available() {
		t.Fatal("the fake binary should be found")
	}
	panes, err := Snapshot(context.Background())
	if err != nil || len(panes) != 2 {
		t.Fatalf("snapshot through the binary: %d panes, %v", len(panes), err)
	}
	if err := Focus(context.Background(), "w5:p1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if strings.TrimSpace(string(b)) != "w5:p1" {
		t.Fatalf("focus asked for the wrong pane: %q", b)
	}
	if err := Focus(context.Background(), ""); err == nil {
		t.Fatal("focus without a pane must not ask herdr anything")
	}
}

// No binary, or a binary whose server is down, is a quiet absence -- and an
// Available that says yes to a missing file is the lie this guards against.
func TestMissingHerdrIsQuiet(t *testing.T) {
	old := Bin
	Bin = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { Bin = old })
	if Available() {
		t.Fatal("a path that does not exist is not an available herdr")
	}
	if _, err := Snapshot(context.Background()); err == nil {
		t.Fatal("no binary, no snapshot")
	}
}

// The window that shows herdr's client is found by walking up from the client,
// and the server process never counts as a window.
func TestClientPidsAndParents(t *testing.T) {
	root := t.TempDir()
	oldRoot := ProcRoot
	ProcRoot = root
	t.Cleanup(func() { ProcRoot = oldRoot })

	proc := func(pid, comm, cmdline, stat string) {
		t.Helper()
		d := filepath.Join(root, pid)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmdline), 0o644)
		os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644)
	}
	proc("100", "foot", "foot\x00", "100 (foot) S 1 0 0")
	proc("200", "herdr", "herdr\x00", "200 (herdr) S 100 0 0") // the client, a child of foot
	proc("300", "herdr", "herdr\x00server\x00", "300 (herdr) S 1 0 0")
	proc("400", "bash", "bash\x00", "400 (bash) S 100 0 0")

	pids := ClientPids()
	if len(pids) != 1 || pids[0] != 200 {
		t.Fatalf("only the client is a window's process: %v", pids)
	}
	if ppid, ok := Parent(200); !ok || ppid != 100 {
		t.Fatalf("parent of the client: %d %v", ppid, ok)
	}
	// a command name with spaces and a parenthesis in it does not shift fields
	proc("500", "a (odd) name", "x\x00", "500 (a (odd) name) S 100 0 0")
	if ppid, ok := Parent(500); !ok || ppid != 100 {
		t.Fatalf("a command name with brackets: %d %v", ppid, ok)
	}
}
