package tui

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// palette holds every colour the cockpit paints with, sourced from the
// Omarchy theme so the floating window reads as part of the same desktop
// as the bar. Missing/unreadable theme falls back to safe 256-colour
// defaults; every field is a lipgloss.Color so both hex and indexes work.
type palette struct {
	accent lipgloss.Color // active pane border, cursor
	fg     lipgloss.Color // bright text
	faint  lipgloss.Color // secondary text
	sel    lipgloss.Color // selected row background
	selFg  lipgloss.Color // selected row foreground
	green  lipgloss.Color // ahead (local work the remote lacks)
	cyan   lipgloss.Color // behind (remote work to pull)
	orange lipgloss.Color // dirty
	red    lipgloss.Color // errors, missing paths
}

var (
	paletteOnce sync.Once
	paletteVal  palette
)

func pal() palette {
	paletteOnce.Do(func() { paletteVal = loadPalette() })
	return paletteVal
}

func loadPalette() palette {
	p := palette{
		accent: lipgloss.Color("204"),
		fg:     lipgloss.Color("255"),
		faint:  lipgloss.Color("244"),
		sel:    lipgloss.Color("236"),
		selFg:  lipgloss.Color("255"),
		green:  lipgloss.Color("71"),
		cyan:   lipgloss.Color("37"),
		orange: lipgloss.Color("214"),
		red:    lipgloss.Color("160"),
	}
	colors := readThemeColors()
	set := func(dst *lipgloss.Color, keys ...string) {
		for _, k := range keys { // first listed key present wins: deterministic
			if hex, ok := colors[k]; ok {
				*dst = lipgloss.Color(hex)
				return
			}
		}
	}
	set(&p.accent, "accent")
	set(&p.fg, "foreground")
	set(&p.selFg, "foreground") // white ink on a light selection band is invisible
	set(&p.faint, "dark_foreground")
	set(&p.sel, "selection")
	set(&p.green, "green")
	set(&p.cyan, "cyan")
	set(&p.orange, "orange", "yellow") // orange is the dirty cue; yellow only stands in
	set(&p.red, "red")
	return p
}

// readThemeColors digs hex strings out of Omarchy's colors.toml. The file
// is flat "key = "#hex"" lines; parsing it by hand skips a TOML
// dependency for a dozen well-known pairs. The theme "current" entry is
// a symlink under XDG state home.
func readThemeColors() map[string]string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		state = filepath.Join(home, ".local", "state")
	}
	data, err := os.ReadFile(filepath.Join(state, "omarchy", "current", "theme", "colors.toml"))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if strings.HasPrefix(val, "#") && len(val) == 7 {
			out[key] = val
		}
	}
	return out
}
