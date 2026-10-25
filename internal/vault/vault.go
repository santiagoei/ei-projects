// Package vault reads the Obsidian vault straight off the filesystem: the
// notes a project has, newest first. No Obsidian API, no plugins -- mtime
// is the whole truth we need, and opening goes through xdg-open.
package vault

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Note struct {
	Path     string    `json:"path"`
	Rel      string    `json:"rel"` // path relative to the vault root, for display
	Modified time.Time `json:"modified"`
}

// maxNoteWalk bounds the traversal so a monster directory tree cannot hang
// the scan; the newest-note answer rarely lives deeper than this anyway.
const maxNoteWalk = 6

// Recent lists the newest limit .md files under dir, skipping Obsidian's
// and git's own machinery. dir itself may be the whole vault or one
// project's docs folder.
func Recent(dir string, limit int) []Note {
	if limit <= 0 {
		return nil
	}
	var notes []Note
	base := depth(dir)
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return fs.SkipAll
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && strings.HasPrefix(name, ".") {
				return fs.SkipDir // .obsidian, .git, ...
			}
			if depth(path)-base > maxNoteWalk {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(name, filepath.Ext(name)) && strings.EqualFold(filepath.Ext(name), ".md") {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				rel = path
			}
			notes = append(notes, Note{Path: path, Rel: rel, Modified: info.ModTime()})
		}
		return nil
	})
	sort.Slice(notes, func(i, j int) bool { return notes[i].Modified.After(notes[j].Modified) })
	if len(notes) > limit {
		notes = notes[:limit]
	}
	return notes
}

func depth(p string) int { return strings.Count(filepath.Clean(p), string(filepath.Separator)) }

// DocsDir finds the folder inside the vault that holds a project's notes.
// Ranking, best first: an exact name match ("humance"), a name that is a
// prefix of the project or the reverse ("icampus.fei.org" starts with the
// folder "icampus"), and finally shared tokens of at least three letters --
// the PARA-style folder "tr.fei" answers the project "icampus.fei.org" on
// the token "fei". Explicit config (docsMap) beats all of this; see scan.
func DocsDir(vaultDir, projectPath string) string {
	if vaultDir == "" {
		return ""
	}
	want := tokens(filepath.Base(projectPath))
	if len(want) == 0 {
		return ""
	}
	wantJoined := strings.Join(want, "")
	best, bestScore := "", 0
	filepath.WalkDir(vaultDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != vaultDir && strings.HasPrefix(name, ".") {
			return fs.SkipDir
		}
		if depth(path)-depth(vaultDir) > 3 {
			return fs.SkipDir
		}
		got := tokens(name)
		if len(got) == 0 {
			return nil
		}
		gotJoined := strings.Join(got, "")
		score := 0
		switch {
		case path == vaultDir:
			if gotJoined == wantJoined {
				score = 1000 + len(gotJoined) // the vault itself is "the folder" of the vault project
			}
		case gotJoined == wantJoined:
			score = 1000 + len(gotJoined)
		case strings.HasPrefix(wantJoined, gotJoined) && len(gotJoined) >= 3:
			score = 500 + len(gotJoined)
		case strings.HasPrefix(gotJoined, wantJoined) && len(wantJoined) >= 3:
			score = 400 + len(wantJoined)
		default:
			shared := 0
			for _, w := range want {
				for _, g := range got {
					if w == g && len(w) >= 3 {
						shared += len(w)
					}
				}
			}
			if shared > 0 {
				score = 200 + shared
			}
		}
		if score > bestScore {
			bestScore, best = score, path
		}
		return nil
	})
	return best
}

// tokens lowercases and splits on everything non-alphanumeric: "tr.fei" ->
// [tr fei], "icampus.fei.org" -> [icampus fei org].
func tokens(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
