package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/santiagoei/ei-projects/internal/actions"
	"github.com/santiagoei/ei-projects/internal/herdr"
)

// TestMain keeps the package's tests off the developer's machine, and off each
// other.
//
// The cockpit persists the seat it closed on under XDG_STATE_HOME and restores
// it on the first scan -- the focus, the row each pane was on. Left alone, a
// test that drives a scan writes its seat and the next test inherits it: the
// assertions start describing somebody else's session, in whatever order the
// runtime chose to run them.
//
// So the directory the package falls back to is read-only. Writing to it fails,
// and persist is written to fail silently -- which is exactly the guarantee the
// app makes to the user, now turned on the test suite. A test that wants to
// check the round-trip itself points XDG_STATE_HOME at its own t.TempDir().
//
// The opencode database gets the same treatment: with no db every session feed
// is empty, which is the deterministic answer a rendering test wants.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ei-projects-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "test home:", err)
		os.Exit(1)
	}
	state := filepath.Join(dir, "ei-projects")
	if err := os.MkdirAll(state, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "test home:", err)
		os.Exit(1)
	}
	if err := os.Chmod(state, 0o500); err != nil { // r-x: nothing gets written
		fmt.Fprintln(os.Stderr, "test home:", err)
		os.Exit(1)
	}
	// herdr is never asked for real: a machine running it must not change what
	// the suite sees, and a jump must never raise a window on the desktop
	herdr.Bin = filepath.Join(dir, "no-herdr")
	actions.RaiseHerdr = func() bool { return false }
	os.Setenv("XDG_STATE_HOME", dir)
	os.Setenv("EI_PROJECTS_OPENCODE_DB", filepath.Join(dir, "absent.db"))

	code := m.Run()

	// the one thing this setup can promise is that it kept its own directory
	// clean; a file here means the write succeeded somewhere it should not
	if _, err := os.Stat(filepath.Join(state, "state.json")); err == nil {
		fmt.Fprintln(os.Stderr, "the shared test state dir was written to")
		code = 1
	}
	os.Chmod(state, 0o700)
	os.RemoveAll(dir)
	os.Exit(code)
}
