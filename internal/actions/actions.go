// Package actions launches things out of the cockpit. Every command mirrors
// an ei.git-status panel launcher (same omarchy plumbing, same argv-not-shell
// discipline): paths never round-trip through a shell string, so a folder
// named with a quote or a space stays a literal path.
package actions

import (
	"encoding/json"
	"os/exec"
	"syscall"
	"time"
)

// start fires the command detached (its own session) so it outlives the TUI
// -- and never waits for it.
func start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// The two doors every action goes through, as variables: a test swaps them
// and reads back what the cockpit asked for, instead of spawning lazygit on
// the developer's desktop or polling hyprctl for three seconds. Nothing else
// in the package needs to be replaceable -- the argv discipline, the path
// handling and the focus dance are the parts worth pinning, and they stay
// exactly where they are.
var (
	Start      = start
	OpenWindow = launch
)

// launch is start for anything that opens a window. Hyprland focuses every
// new window, which would pull the keyboard away from the cockpit; so once
// the window count grows, the cockpit takes focus back.
func launch(name string, args ...string) error {
	before := windowCount()
	if err := Start(name, args...); err != nil {
		return err
	}
	if before >= 0 {
		go refocusCockpit(before)
	}
	return nil
}

// windowCount is the number of Hyprland clients, or -1 when there is no
// Hyprland to ask (then nothing is refocused).
func windowCount() int {
	out, err := exec.Command("hyprctl", "-j", "clients").Output()
	if err != nil {
		return -1
	}
	var clients []json.RawMessage
	if err := json.Unmarshal(out, &clients); err != nil {
		return -1
	}
	return len(clients)
}

func refocusCockpit(before int) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if n := windowCount(); n > before {
			time.Sleep(100 * time.Millisecond) // let the new window take its focus first
			exec.Command("hyprctl", "dispatch", `hl.dsp.focus({ window = "class:^ei-projects$" })`).Run()
			return
		}
	}
}

func Lazygit(path string) error {
	return OpenWindow("omarchy-launch-tui", "lazygit", "-p", path)
}

func Terminal(dir string) error {
	return OpenWindow("uwsm-app", "--", "xdg-terminal-exec", "--dir="+dir)
}

func Agent(dir string) error {
	return OpenWindow("uwsm-app", "--", "xdg-terminal-exec", "--dir="+dir,
		"-e", "omarchy-agent", "--inline")
}

// Git runs a git verb on the selected repository, in a terminal the user
// watches: a fetch across the ntfs mount, a pull that has to merge, or a push
// that needs credentials are all things worth seeing happen rather than
// awaiting blind. Same argv discipline as every other launcher.
func Git(dir string, args ...string) error {
	full := append([]string{"--", "xdg-terminal-exec", "--dir=" + dir, "-e", "git"}, args...)
	return OpenWindow("uwsm-app", full...)
}

func Explore(dir string) error {
	return OpenWindow("uwsm-app", "--", "xdg-open", dir)
}

// Editor opens one directory in the configured editor. A project folder is a
// fine argument: every repository inside it is then in the same window, which
// is what `ei-projects code --all [project]` asks for. Opening several
// repositories one call at a time would be six editor windows instead of one,
// so there is no such thing here.
func Editor(dir string) error {
	return OpenWindow("omarchy-launch-editor", dir)
}

// Compose opens a terminal already cd'd into the environment and runs the
// given `docker compose` arguments there: up/down build and pull can take
// minutes, and the user watching them happen is the point, not an optimisation.
func Compose(dir, file string, args ...string) error {
	full := append([]string{"--", "xdg-terminal-exec", "--dir=" + dir,
		"-e", "docker", "compose", "-f", file}, args...)
	return OpenWindow("uwsm-app", full...)
}

func OpenFile(path string) error {
	return OpenWindow("uwsm-app", "--", "xdg-open", path)
}

func Copy(path string) error {
	return Start("wl-copy", path)
}

// ResumeSession reopens an opencode session in a terminal over the directory
// it was started in: the same jump omarchy-agent-sessions performs.
func ResumeSession(dir, id string) error {
	return OpenWindow("uwsm-app", "--", "xdg-terminal-exec", "--dir="+dir,
		"-e", "opencode", "--auto", "--session", id)
}
