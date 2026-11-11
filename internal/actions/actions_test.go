package actions

import (
	"path/filepath"
	"strings"
	"testing"
)

// record swaps the process door for the test: every action's argv is read
// back as the launcher will receive it, without anything spawning. PATH goes
// nowhere so launch() cannot find hyprctl, cannot count windows, and cannot
// start the focus dance on the developer's desktop.
func record(t *testing.T) *[]string {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	var got []string
	door := func(name string, args ...string) error {
		got = append(got, name)
		got = append(got, args...)
		return nil
	}
	real := Start
	Start = door
	t.Cleanup(func() { Start = real })
	return &got
}

// The package's promise is that a path never round-trips through a shell
// string: the odd names below have to arrive as ONE argument each.
func TestPathsStayWholeArgs(t *testing.T) {
	awkward := []string{
		"/p/with space",
		`/p/quote'here`,
		`/p/double"quote`,
		"/p/semicolon;rm -rf",
		"/p/dollar$HOME",
		"/p/back\\slash",
	}
	for _, p := range awkward {
		t.Run(p, func(t *testing.T) {
			got := record(t)
			for _, act := range []func(string) error{Lazygit, Terminal, Editor, Agent, Explore, OpenFile} {
				*got = nil
				if err := act(p); err != nil {
					t.Fatalf("%s(%q): %v", "action", p, err)
				}
				found := false
				for _, arg := range *got {
					if arg == p || arg == "--dir="+p {
						found = true
					}
					if strings.Contains(arg, " ") && arg != p && !strings.HasPrefix(arg, "--dir=") {
						t.Fatalf("the path was split into shell words: %v", *got)
					}
				}
				if !found {
					t.Fatalf("%q missing from %v", p, *got)
				}
			}
		})
	}
}

func TestLazygitAndAgentArgv(t *testing.T) {
	got := record(t)
	if err := Lazygit("/p/repo"); err != nil {
		t.Fatal(err)
	}
	want := "omarchy-launch-tui lazygit -p /p/repo"
	if strings.Join(*got, " ") != want {
		t.Fatalf("lazygit: %q, want %q", strings.Join(*got, " "), want)
	}

	*got = nil
	if err := Agent("/p/repo"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*got, " ")
	for _, part := range []string{"uwsm-app", "xdg-terminal-exec", "--dir=/p/repo", "omarchy-agent", "--inline"} {
		if !strings.Contains(joined, part) {
			t.Fatalf("agent argv missing %q: %s", part, joined)
		}
	}
}

// Compose has to land in the environment's directory, on the file that was
// detected (which may not be the default name), with the verbs after it.
func TestComposeArgv(t *testing.T) {
	got := record(t)
	if err := Compose("/p/proj", "/p/proj/docker-compose.yml", "logs", "-f", "web"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*got, " ")
	want := "docker compose -f /p/proj/docker-compose.yml logs -f web"
	if !strings.Contains(joined, want) {
		t.Fatalf("compose: %q does not carry %q", joined, want)
	}
	if !strings.Contains(joined, "--dir=/p/proj") {
		t.Fatalf("compose must run in the environment's directory: %q", joined)
	}
	if !strings.Contains(joined, "xdg-terminal-exec") {
		t.Fatalf("compose belongs in a terminal the user can watch: %q", joined)
	}
}

// Resuming a session is the jump omarchy-agent-sessions performs: a terminal
// in the directory the session ran in, opencode attached to that session.
func TestResumeSessionArgv(t *testing.T) {
	got := record(t)
	if err := ResumeSession("/p/repo", "ses_1a2b"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*got, " ")
	for _, part := range []string{"--dir=/p/repo", "opencode", "--auto", "--session ses_1a2b"} {
		if !strings.Contains(joined, part) {
			t.Fatalf("resume argv missing %q: %s", part, joined)
		}
	}
}

// The git verbs run in a terminal, in the repository, with the flags the
// cockpit promises -- and a branch name with a slash stays one argument.
func TestGitArgv(t *testing.T) {
	got := record(t)
	if err := Git("/p/moodle", "pull", "--ff-only"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*got, " ")
	for _, part := range []string{"--dir=/p/moodle", "-e git pull --ff-only"} {
		if !strings.Contains(joined, part) {
			t.Fatalf("missing %q in %q", part, joined)
		}
	}

	*got = nil
	if err := Git("/p/feature/x y", "push"); err != nil {
		t.Fatal(err)
	}
	for _, arg := range *got {
		if arg == "--dir=/p/feature/x y" || arg == "/p/feature/x y" {
			return
		}
	}
	t.Fatalf("the path was split: %v", *got)
}
