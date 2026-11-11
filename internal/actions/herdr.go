package actions

import (
	"context"

	"github.com/santiagoei/ei-projects/internal/herdr"
	"github.com/santiagoei/ei-projects/internal/hypr"
)

// RaiseHerdr brings the window that runs herdr's client to the front. A
// variable so the tests never move a window on the developer's desktop.
var RaiseHerdr = raiseHerdr

// FocusAgent makes the agent's pane the focused one in herdr, then raises the
// terminal showing herdr when it can find that window. raised is false when
// herdr moved its focus but no window of it is on screen -- the caller says so
// instead of pretending the jump landed on the user's eyes.
func FocusAgent(pane string) (raised bool, err error) {
	if err := herdr.Focus(context.Background(), pane); err != nil {
		return false, err
	}
	return RaiseHerdr(), nil
}

func raiseHerdr() bool {
	for _, pid := range herdr.ClientPids() {
		if hypr.RaiseOwner(pid, herdr.Parent) {
			return true
		}
	}
	return false
}
