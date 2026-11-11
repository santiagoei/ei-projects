package tui

import (
	"strings"
	"testing"

	"github.com/santiagoei/ei-projects/internal/actions"
)

// captureActions swaps the two process doors for the duration of a test: the
// assertions read back what the cockpit asked to run, and nobody spawns
// lazygit on the developer's desktop or polls hyprctl for three seconds.
func captureActions(t *testing.T) *[]string {
	t.Helper()
	var got []string
	record := func(name string, args ...string) error {
		got = append(got, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	realStart, realOpen := actions.Start, actions.OpenWindow
	actions.Start, actions.OpenWindow = record, record
	t.Cleanup(func() { actions.Start, actions.OpenWindow = realStart, realOpen })
	return &got
}

// lastCmd is the most recent command the cockpit tried to run.
func lastCmd(t *testing.T, got *[]string) string {
	t.Helper()
	if len(*got) == 0 {
		t.Fatal("no action ran")
	}
	return (*got)[len(*got)-1]
}
