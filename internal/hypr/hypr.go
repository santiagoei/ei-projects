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
