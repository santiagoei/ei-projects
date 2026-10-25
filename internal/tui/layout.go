package tui

// rect and geom split the window: View and the mouse handler must agree
// exactly on where every pane lives, so both derive the geometry from
// this one function instead of computing it twice. The ENV pane is
// conditional -- a project without a compose file has no sixth pane --
// and when the window is too short the conditional pane yields first, so
// the three list panes keep their floors.

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type geom struct {
	leftW, rightW, bodyH int
	projH, repoH         int
	oiBody               string // wrapped launcher line(s)
	actionH              int
	hasEnv               bool
	envH                 int
	commitsH             int
	sessionsH            int
	notesH               int
	rects                map[paneFocus]rect
}

const (
	listFloor = 4 // border 2 + title 1 + one row
	envMax    = 8
)

func (m Model) geom() geom {
	g := geom{}
	g.leftW = clamp(m.width*36/100, 30, 52)
	g.rightW = m.width - g.leftW
	// body + status + legend + one spare row for the cursor: a frame that
	// ends on the last terminal row makes the renderer scroll away the top
	g.bodyH = m.height - 3

	g.projH = clamp(m.height*42/100, 7, g.bodyH-7)
	g.repoH = g.bodyH - g.projH

	var oiRows int
	g.oiBody, oiRows = openInBody(g.rightW - 4)
	g.actionH = 2 + oiRows

	// The ENV pane wants a row per service (bounded), the lists need
	// listFloor each, and whatever is left goes to commits.
	avail := g.bodyH - g.actionH
	envWant := 0
	if p := m.proj(); p.EnvFile != "" {
		rows := len(m.envSvcs)
		if m.envLoading || m.envErr != "" {
			rows = 1
		}
		if rows < 1 {
			rows = 1
		}
		envWant = clamp(3+rows, listFloor, envMax)
	}
	g.envH = clamp(envWant, 0, avail-3*listFloor)
	if g.envH < listFloor {
		g.envH = 0 // too tight: the optional pane yields to the permanent ones
	}
	g.hasEnv = g.envH > 0

	rest := avail - g.envH
	g.commitsH = clamp(rest*45/100, listFloor, rest-2*listFloor)
	g.sessionsH = clamp(rest*30/100, listFloor, rest-g.commitsH-listFloor)
	g.notesH = rest - g.commitsH - g.sessionsH

	y := g.actionH
	if g.hasEnv {
		g.rects = map[paneFocus]rect{
			paneProjects: {0, 0, g.leftW, g.projH},
			paneRepos:    {0, g.projH, g.leftW, g.repoH},
			paneEnv:      {g.leftW, y, g.rightW, g.envH},
			paneCommits:  {g.leftW, y + g.envH, g.rightW, g.commitsH},
			paneSessions: {g.leftW, y + g.envH + g.commitsH, g.rightW, g.sessionsH},
			paneNotes:    {g.leftW, y + g.envH + g.commitsH + g.sessionsH, g.rightW, g.notesH},
		}
		return g
	}
	g.rects = map[paneFocus]rect{
		paneProjects: {0, 0, g.leftW, g.projH},
		paneRepos:    {0, g.projH, g.leftW, g.repoH},
		paneCommits:  {g.leftW, y, g.rightW, g.commitsH},
		paneSessions: {g.leftW, y + g.commitsH, g.rightW, g.sessionsH},
		paneNotes:    {g.leftW, y + g.commitsH + g.sessionsH, g.rightW, g.notesH},
	}
	return g
}
