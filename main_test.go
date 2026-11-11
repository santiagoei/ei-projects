package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santiagoei/ei-projects/internal/hypr"
	"github.com/santiagoei/ei-projects/internal/tui"
)

// sandbox is a home with two watched folders, one of them a repository: the
// shape the launch commands resolve names against. It returns the three paths
// a test can name without depending on where the temp dir landed.
func sandbox(t *testing.T) (projA, repoA, projB string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	home := t.TempDir()
	projA = filepath.Join(home, "projA")
	repoA = filepath.Join(projA, "repoA")
	projB = filepath.Join(home, "projB")
	for _, d := range []string{repoA, projB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.email=t@e", "-c", "user.name=t"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(repoA, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoA, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repoA, "add", ".")
	git(repoA, "commit", "-q", "-m", "c")

	shell := filepath.Join(home, "shell.json")
	body, _ := json.Marshal(map[string]any{"bar": map[string]any{"layout": map[string]any{
		"left": []any{map[string]any{"id": "ei.git-status", "watchDirs": projA + ", " + projB}}}}})
	if err := os.WriteFile(shell, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("OMARCHY_SHELL_JSON", shell)
	return projA, repoA, projB
}

// The names the user types are the cockpit's own: a project's name, its number
// in the watched list, or any repository inside it -- and what comes back is a
// directory the editor or the agent can actually run in.
func TestResolve(t *testing.T) {
	_, repoA, projB := sandbox(t)

	for _, tc := range []struct{ in, want string }{
		{"projA", repoA},   // a project opens the repository inside it
		{"repoA", repoA},   // a repository answers by its own name
		{"PROJA", repoA},   // names are not a case tripwire
		{" projA ", repoA}, // and stray spaces are not the user's problem
	} {
		got, err := resolve(resolveFlags{Project: tc.in})
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if filepath.Base(got) != filepath.Base(tc.want) {
			t.Fatalf("%q resolved to %q, want %q", tc.in, got, tc.want)
		}
	}

	// a watched folder that is not a repository is its own directory
	got, err := resolve(resolveFlags{Project: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != filepath.Base(projB) {
		t.Fatalf("the second project: %q, want %q", got, projB)
	}

	if _, err := resolve(resolveFlags{Project: "nope"}); err == nil {
		t.Fatal("an unknown name must be refused")
	} else if !strings.Contains(err.Error(), "projA") {
		t.Fatalf("the refusal must say what exists: %v", err)
	}

	// --all is the project folder, not one repository of it...
	got, err = resolve(resolveFlags{Project: "projA", All: true})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "projA" {
		t.Fatalf("-all: %q", got)
	}
	// ...and it is not silently allowed without a project to widen
	if _, err := resolve(resolveFlags{All: true}); err == nil {
		t.Fatal("-all without a project must be refused")
	}
}

// With no name given, the launch commands fall back to the seat the cockpit
// left behind: the same repository it would restore its selection from.
func TestResolveFallsBackToTheCockpitSeat(t *testing.T) {
	_, repoA, _ := sandbox(t)
	_, err := resolve(resolveFlags{})
	if err != nil && strings.Contains(err.Error(), "no projects") {
		t.Fatalf("the sandbox always has one project: %v", err)
	}

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	state := filepath.Join(home, "ei-projects")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"project": "/somewhere/proj", "repo": repoA})
	if err := os.WriteFile(filepath.Join(state, "state.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if path, ok := tui.LastSelection(); !ok || path != repoA {
		t.Fatalf("the seat is not what the cockpit saved: %q %v", path, ok)
	}
	got, err := resolve(resolveFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != filepath.Base(repoA) {
		t.Fatalf("the fallback: %q, want %q", got, repoA)
	}
}

// The dispatch: a flag is not a command word, and an unknown word says so
// instead of quietly opening the cockpit.
func TestIsCommand(t *testing.T) {
	for _, c := range []string{"scan", "code", "agent", "terminal", "resume"} {
		if !isCommand(c) {
			t.Errorf("%q is a command", c)
		}
	}
	// the regression this guard exists for: `--version` reaching the command
	// switch made the cockpit's own flag an "unknown command"
	for _, not := range []string{"", "--version", "-version", "-v", "help", "-h", "--help", "codes"} {
		if isCommand(not) {
			t.Errorf("%q is not a command", not)
		}
	}
	for _, a := range []string{"", "--version", "-v", "--help"} {
		if !startsCockpit(a) {
			t.Errorf("%q should reach the cockpit's flag set", a)
		}
	}
	for _, a := range []string{"scan", "code", "resume", "bogus"} {
		if startsCockpit(a) {
			t.Errorf("%q is not a flag", a)
		}
	}
	// and every command listed in the usage text exists as one
	usage := usageText()
	for _, c := range commands {
		if !strings.Contains(usage, c) {
			t.Errorf("usage does not document %q", c)
		}
	}
}

// --here turns "open where I am" into a path without the user typing one. The
// compositor is the only one who knows the answer, so this tests what happens
// when it answers, and when it does not.
func TestResolveHere(t *testing.T) {
	_, repoA, projB := sandbox(t)
	hereFunc = func() (string, bool) { return repoA, true }
	t.Cleanup(func() { hereFunc = hypr.ActiveDir })

	got, err := resolve(resolveFlags{Here: true})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != filepath.Base(repoA) {
		t.Fatalf("--here resolved to %q, want %q", got, repoA)
	}

	// a directory outside every watched folder is refused, not guessed
	hereFunc = func() (string, bool) { return "/tmp/somewhere-else", true }
	if _, err := resolve(resolveFlags{Here: true}); err == nil {
		t.Fatal("an unwatched directory must be refused")
	}

	// and a desktop with no answer to give says so
	hereFunc = func() (string, bool) { return "", false }
	if _, err := resolve(resolveFlags{Here: true}); err == nil ||
		!strings.Contains(err.Error(), "focused window") {
		t.Fatalf("no compositor: %v", err)
	}
	// a name typed anyway wins over --here
	hereFunc = func() (string, bool) { return projB, true }
	if got, err := resolve(resolveFlags{Here: true, Project: "projA"}); err != nil ||
		filepath.Base(got) != filepath.Base(repoA) {
		t.Fatalf("the typed name lost to --here: %q %v", got, err)
	}
}

// resume takes either a session id or a place; the two have to be told apart
// without asking the user to be precise about which one they typed.
func TestLooksLikePath(t *testing.T) {
	for _, s := range []string{"/home/me/proj", "~/proj", "proj/repo"} {
		if !looksLikePath(s) {
			t.Errorf("%q is a path", s)
		}
	}
	for _, s := range []string{"ses_01AbC", "opn_9f8a", ""} {
		if looksLikePath(s) {
			t.Errorf("%q is not a path", s)
		}
	}
}
