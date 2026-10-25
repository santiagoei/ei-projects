package tui

// rect and geom split the window: View and the mouse handler must agree
// exactly on where every pane lives, so both derive the geometry from
// this one function instead of computing it twice.

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type geom struct {
	leftW, rightW, bodyH int
	projH, repoH         int
	oiBody               string // wrapped launcher line(s)
	actionH              int
	commitsH             int
	sessionsH            int
	notesH               int
	rects                map[paneFocus]rect
}

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

	rest := g.bodyH - g.actionH
	g.commitsH = clamp(rest*45/100, 5, rest-10)
	g.sessionsH = clamp(rest*30/100, 5, rest-g.commitsH-5)
	g.notesH = rest - g.commitsH - g.sessionsH

	g.rects = map[paneFocus]rect{
		paneProjects: {0, 0, g.leftW, g.projH},
		paneRepos:    {0, g.projH, g.leftW, g.repoH},
		paneCommits:  {g.leftW, g.actionH, g.rightW, g.commitsH},
		paneSessions: {g.leftW, g.actionH + g.commitsH, g.rightW, g.sessionsH},
		paneNotes:    {g.leftW, g.actionH + g.commitsH + g.sessionsH, g.rightW, g.notesH},
	}
	return g
}
