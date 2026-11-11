// Package herdr reads the live terminal panes of a running herdr server.
//
// herdr keeps agents alive in panes and knows, per pane, whether the agent is
// working, blocked on a question, idle or done. The cockpit only asks: one
// read-only `herdr api snapshot` for the panes and their working directories,
// and one `herdr agent focus` when the user asks to jump to an agent. Nothing
// here writes to a pane, sends it input, or creates one.
//
// Every failure is a quiet "no answer": herdr is an optional feed, and a
// machine without it, or with its server stopped, must look exactly like one
// that never had it.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Bin is the herdr executable. A variable so the tests can point it at a fake
// script, or at nothing at all.
var Bin = "herdr"

// ProcRoot is where the process table is read from for ClientPids. Also a
// variable, for the same reason.
var ProcRoot = "/proc"

// deadline bounds every call: the cockpit repaints on its own clock and a
// wedged herdr socket must not hold a frame hostage.
const deadline = 1500 * time.Millisecond

// Pane is one herdr pane that holds an agent. Only panes with an agent are
// kept: a plain shell pane says nothing the cockpit can use.
type Pane struct {
	ID        string `json:"pane_id"`
	Workspace string `json:"workspace_id"`
	Tab       string `json:"tab_id"`
	Cwd       string `json:"cwd"`
	Status    string `json:"agent_status"`
	Agent     string `json:"agent"`
	Display   string `json:"display_agent"`
	Title     string `json:"title"`
	TermTitle string `json:"terminal_title_stripped"`
	Focused   bool   `json:"focused"`
}

// Name is the agent's name as herdr shows it.
func (p Pane) Name() string {
	if p.Display != "" {
		return p.Display
	}
	return p.Agent
}

// Label is what the pane is about: the title the agent set, else the terminal's.
func (p Pane) Label() string {
	if p.Title != "" {
		return p.Title
	}
	return p.TermTitle
}

// Rank orders states by what needs the user first: a blocked agent is a
// question waiting, a working one is busy, a done one has output to read.
func Rank(status string) int {
	switch status {
	case "blocked":
		return 0
	case "working":
		return 1
	case "done":
		return 2
	case "idle":
		return 3
	}
	return 4
}

// Available reports whether there is a herdr binary to ask at all. It does not
// know whether a server is running: that is what Snapshot finds out.
func Available() bool {
	_, err := exec.LookPath(Bin)
	return err == nil
}

// Snapshot returns the agent panes of the running herdr server.
func Snapshot(ctx context.Context) ([]Pane, error) {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin, "api", "snapshot").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr snapshot: %w", err)
	}
	return Parse(out)
}

// Parse reads the JSON of `herdr api snapshot`. The envelope is checked, not
// assumed: a server that answers with an error object has no panes to show.
func Parse(b []byte) ([]Pane, error) {
	var env struct {
		Result *struct {
			Snapshot *struct {
				Panes []Pane `json:"panes"`
			} `json:"snapshot"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("herdr snapshot: %w", err)
	}
	if env.Result == nil || env.Result.Snapshot == nil {
		return nil, errors.New("herdr snapshot: no snapshot in the answer")
	}
	var out []Pane
	for _, p := range env.Result.Snapshot.Panes {
		if p.Name() != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// Focus makes the agent in pane the focused one inside herdr. It moves herdr's
// focus, and touches nothing on disk or in git.
func Focus(ctx context.Context, pane string) error {
	if pane == "" {
		return errors.New("no pane")
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin, "agent", "focus", pane).CombinedOutput()
	if err != nil {
		return fmt.Errorf("herdr focus: %s", firstLine(string(out), err))
	}
	return nil
}

// ClientPids returns the processes running a herdr client -- the terminal
// window a user looks at. The server process is skipped: it has no window.
func ClientPids() []int {
	entries, err := os.ReadDir(ProcRoot)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(ProcRoot, e.Name(), "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != "herdr" {
			continue
		}
		cmd, _ := os.ReadFile(filepath.Join(ProcRoot, e.Name(), "cmdline"))
		if strings.Contains(string(cmd), "server") {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// Parent is the parent pid of pid as the process table has it.
func Parent(pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join(ProcRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	// the command name sits in parentheses and may hold spaces: the fields
	// after the last ')' are the stable part
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	return ppid, err == nil
}

func firstLine(out string, err error) string {
	if s := strings.TrimSpace(out); s != "" {
		return strings.SplitN(s, "\n", 2)[0]
	}
	return err.Error()
}
