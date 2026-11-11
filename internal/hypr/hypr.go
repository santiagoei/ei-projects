// Package hypr asks Hyprland where the user is looking.
//
// The cockpit and the launchers can be told "open where I am", and the only
// honest answer to that on a Wayland desktop is the working directory of the
// focused window's process: the folder a terminal was started in, which is
// where the work actually is. Everything else about it is best effort, so
// every failure here is a quiet "no answer" and the caller falls back to the
// seat it had saved.
package hypr

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// overridable for tests: a fake hyprctl on PATH and a fake /proc
var (
	hyprctl  = "hyprctl"
	procRoot = "/proc"
)

// ActiveDir is the working directory of the focused window, and false when
// there is no answer to give: no Hyprland (an ssh session, X11, a test), no
// window focused, a process that already exited, or a directory that says
// nothing because it is the home or the root of the filesystem -- which is
// where a GUI application launched by a .desktop file usually sits.
func ActiveDir() (string, bool) {
	out, err := exec.Command(hyprctl, "-j", "activewindow").Output()
	if err != nil || len(out) == 0 {
		return "", false
	}
	pid, ok := parsePID(out)
	if !ok {
		return "", false
	}
	return readCwd(pid)
}

// parsePID reads the pid out of activewindow's JSON. A focused window with no
// process (a layer-shell widget, a lockscreen surface) reports 0.
func parsePID(out []byte) (int, bool) {
	var w struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(out, &w); err != nil || w.PID <= 0 {
		return 0, false
	}
	return w.PID, true
}

func readCwd(pid int) (string, bool) {
	dir, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "cwd"))
	if err != nil || dir == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	home, _ := os.UserHomeDir()
	if dir == "/" || (home != "" && dir == home) {
		return "", false // a directory that could be any project is none
	}
	return dir, true
}

// RaiseOwner focuses the Hyprland window that owns pid: the first ancestor of
// pid that is a window's process. A terminal running herdr's client is such a
// window, and the client itself is only a child of it. False when nothing that
// owns pid is on screen, or Hyprland cannot be asked.
func RaiseOwner(pid int, parent func(int) (int, bool)) bool {
	out, err := exec.Command(hyprctl, "-j", "clients").Output()
	if err != nil {
		return false
	}
	var clients []struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(out, &clients) != nil {
		return false
	}
	windows := map[int]bool{}
	for _, c := range clients {
		windows[c.PID] = true
	}
	owner, ok := owner(pid, windows, parent)
	if !ok {
		return false
	}
	return exec.Command(hyprctl, "dispatch",
		fmt.Sprintf(`hl.dsp.focus({ window = "pid:%d" })`, owner)).Run() == nil
}

// owner walks up from pid until it meets a window's process. The walk is
// bounded: a process table with a cycle in it must not hang the cockpit.
func owner(pid int, windows map[int]bool, parent func(int) (int, bool)) (int, bool) {
	for range 64 {
		if windows[pid] {
			return pid, true
		}
		next, ok := parent(pid)
		if !ok || next <= 1 {
			return 0, false
		}
		pid = next
	}
	return 0, false
}
