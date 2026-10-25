// Package actions launches things out of the cockpit. Every command mirrors
// an ei.git-status panel launcher (same omarchy plumbing, same argv-not-shell
// discipline): paths never round-trip through a shell string, so a folder
// named with a quote or a space stays a literal path.
package actions

import (
	"os/exec"
	"syscall"
)

// start fires the command detached (its own session) so it outlives the TUI
// -- and never waits for it.
func start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func Lazygit(path string) error {
	return start("omarchy-launch-tui", "lazygit", "-p", path)
}

func Terminal(dir string) error {
	return start("uwsm-app", "--", "xdg-terminal-exec", "--dir="+dir)
}

func Editor(path string) error {
	return start("omarchy-launch-editor", path)
}

func Agent(dir string) error {
	return start("uwsm-app", "--", "xdg-terminal-exec", "--dir="+dir,
		"-e", "omarchy-agent", "--inline")
}

func Explore(dir string) error {
	return start("uwsm-app", "--", "xdg-open", dir)
}

// Compose opens a terminal already cd'd into the environment and runs the
// given `docker compose` arguments there: up/down build and pull can take
// minutes, and the user watching them happen is the point, not an optimisation.
func Compose(dir, file string, args ...string) error {
	full := append([]string{"--", "xdg-terminal-exec", "--dir=" + dir,
		"-e", "docker", "compose", "-f", file}, args...)
	return start("uwsm-app", full...)
}

func OpenFile(path string) error {
	return start("uwsm-app", "--", "xdg-open", path)
}

func Copy(path string) error {
	return start("wl-copy", path)
}

// ResumeSession reopens an opencode session in a terminal over the directory
// it was started in: the same jump omarchy-agent-sessions performs.
func ResumeSession(dir, id string) error {
	return start("uwsm-app", "--", "xdg-terminal-exec", "--dir="+dir,
		"-e", "opencode", "--auto", "--session", id)
}
