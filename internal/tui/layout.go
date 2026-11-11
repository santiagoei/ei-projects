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

// innerH is how many rows a pane can paint: its frame height minus the two
// border rows. Paging and the scroll window both need it, and both must agree
// with what the renderer actually drew.
func (g geom) innerH(p paneFocus) int {
	r, ok := g.rects[p]
	if !ok {
		return 1
	}
	return max(1, r.h-2)
}

type geom struct {
	leftW, rightW, bodyH int
	projH, repoH         int
	// detailH is the read-only block under the repositories, when the window
	// has rows for a third left column pane
	detailH int
	// single is the compact shape: one column, every box as wide as the window
	single    bool
	oiBody    string // wrapped launcher line(s)
	actionH   int
	hasEnv    bool
	envH      int
	commitsH  int
	sessionsH int
	notesH    int
	rects     map[paneFocus]rect
}

// compact is the shape the window gets: one column, every box as wide as the
// terminal. "auto" picks it for a narrow window -- below 110 columns the two
// columns start fighting for width -- and the config can force either shape.
//
// A stack needs its rows, counted from what this geometry actually decided:
// the OPEN IN bar as it wrapped, five lists at their floor, the conditional
// ENV. A window that short gets the two columns no matter what was asked,
// because a frame taller than the terminal is the one failure the whole layout
// exists to prevent -- and a pane cut off the bottom of the screen is worse
// than a pane two columns wide.
func (m Model) compact(g geom) bool {
	if !m.wantsCompact() {
		return false
	}
	need := g.actionH + 5*listFloor
	if g.hasEnv {
		need += g.envH
	}
	return g.bodyH >= need
}

func (m Model) wantsCompact() bool {
	switch m.cfg.Layout {
	case "wide":
		return false
	case "compact":
		return true
	}
	return m.width < compactWidth
}

const (
	listFloor    = 4 // border 2 + title 1 + one row
	envMax       = 8
	compactWidth = 110 // below this, two columns start fighting for width
)

func (m Model) geom() geom {
	g := geom{}
	g.leftW = clamp(m.width*36/100, 30, 52)
	g.rightW = m.width - g.leftW
	// breadcrumb + status + legend + one spare row for the cursor: a frame
	// that ends on the last terminal row makes the renderer scroll away the
	// top border
	g.bodyH = m.height - 4

	// The left column is projects, repositories, and -- when the window can
	// afford a third box without taking the floors away from the other two --
	// the detail block. It pays for itself out of the repositories' slack:
	// a list of five needs six rows and the projects pane has rarely more.
	detailWant := len(m.detailLines(g.leftW-4)) + 2
	if detailWant >= listFloor && g.bodyH >= detailWant+2*listFloor {
		g.projH = clamp(m.height*42/100, listFloor, g.bodyH-detailWant-listFloor)
		g.detailH = detailWant
		g.repoH = g.bodyH - g.projH - g.detailH
	} else {
		g.detailH = 0
		g.projH = clamp(m.height*42/100, 7, g.bodyH-7)
		g.repoH = g.bodyH - g.projH
	}

	var oiRows int
	g.oiBody, oiRows = openInBody(g.rightW - 4)
	g.actionH = 2 + oiRows

	// The ENV pane wants a row per service (bounded), the lists need
	// listFloor each, and whatever is left goes to commits.
	avail := g.bodyH - g.actionH
	envWant := 0
	if p := m.proj(); p.EnvFile != "" {
		rows := m.listCount(paneEnv)
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

	if m.compact(g) {
		// One column, every box as wide as the window. The detail block is the
		// first to go: its rows are worth more to the lists in a docked window,
		// and the breadcrumb already carries what it says. The right-hand panes
		// are measured here, and the renderers read g.commitsH and friends.
		g.single, g.leftW, g.rightW, g.detailH = true, m.width, m.width, 0
		room := g.bodyH - g.actionH - g.envH // >= 5*listFloor, checked above
		g.projH = clamp(len(m.items(paneProjects))+2, listFloor, 9)
		g.repoH = clamp(len(m.items(paneRepos))+2, listFloor, 9)
		if room-g.projH-g.repoH < 3*listFloor { // the lists pay before the ladders
			g.projH, g.repoH = listFloor, listFloor
		}
		rest := room - g.projH - g.repoH
		g.commitsH = clamp(rest*45/100, listFloor, rest-2*listFloor)
		g.sessionsH = clamp(rest*30/100, listFloor, rest-g.commitsH-listFloor)
		g.notesH = rest - g.commitsH - g.sessionsH
	}

	if g.single {
		// the stack, painted in this order: projects, repositories, the
		// launcher bar, the conditional ENV, then the three lists
		g.rects = map[paneFocus]rect{}
		y := g.projH + g.repoH + g.actionH
		g.rects[paneProjects] = rect{0, 0, g.leftW, g.projH}
		g.rects[paneRepos] = rect{0, g.projH, g.leftW, g.repoH}
		if g.hasEnv {
			g.rects[paneEnv] = rect{0, y, g.rightW, g.envH}
			y += g.envH
		}
		g.rects[paneCommits] = rect{0, y, g.rightW, g.commitsH}
		g.rects[paneSessions] = rect{0, y + g.commitsH, g.rightW, g.sessionsH}
		g.rects[paneNotes] = rect{0, y + g.commitsH + g.sessionsH, g.rightW, g.notesH}
		return g
	}

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
