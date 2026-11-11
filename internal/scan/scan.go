// Package scan assembles the one report every front end (the TUI today,
// `ei-projects scan --json`, maybe the bar panel later) reads. It owns no
// rendering and no state: it is the contract.
package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/santiagoei/ei-projects/internal/agent"
	"github.com/santiagoei/ei-projects/internal/config"
	"github.com/santiagoei/ei-projects/internal/env"
	"github.com/santiagoei/ei-projects/internal/gitx"
	"github.com/santiagoei/ei-projects/internal/vault"
)

type Project struct {
	Name       string          `json:"name"`
	Path       string          `json:"path"`
	Missing    bool            `json:"missing"`
	Repos      []gitx.Repo     `json:"repos"`
	RepoCount  int             `json:"repoCount"`
	Pending    int             `json:"pending"`
	EnvDir     string          `json:"envDir,omitempty"`
	EnvFile    string          `json:"envFile,omitempty"`
	DocsFolder string          `json:"docsFolder,omitempty"`
	Notes      []vault.Note    `json:"notes"`
	Sessions   []agent.Session `json:"sessions"`
	// FeedErrors is a feed that tried and failed (schema changed, unreadable
	// database); FeedNotes is one that has nothing to say for this project
	// (no docs folder matched). The UI warns only for the first.
	FeedErrors []string `json:"feedErrors,omitempty"`
	FeedNotes  []string `json:"feedNotes,omitempty"`
}

type Report struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Projects    []Project `json:"projects"`
	// FeedNotes describes a feed absent on this machine as a whole (no
	// sqlite3, no vault): the per-project FeedErrors stay quiet for exactly
	// those cases, and without this note an empty SESSIONS pane would look
	// like a bug instead of a missing dependency.
	FeedNotes []string `json:"feedNotes,omitempty"`
}

// Run scans every watched folder. The git fan-out is parallel; the agent and
// vault feeds are cheap enough to run inline but degrade per project: a
// missing feed appends to FeedErrors and never aborts the report.
func Run(ctx context.Context, cfg *config.Config) Report {
	report := Report{GeneratedAt: time.Now(), FeedNotes: feedNotes(cfg)}
	projects := make([]Project, len(cfg.WatchDirs))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // watch dirs at a time; gitx parallelizes within
	for i, dir := range cfg.WatchDirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			projects[i] = scanOne(ctx, dir, cfg)
		}(i, dir)
	}
	wg.Wait()

	report.Projects = projects
	fillOpenTodos(projects)
	return report
}

// fillOpenTodos decorates every session of the report with the number of
// items it left unfinished: one query for the whole scan instead of one per
// project, and silent when the todo table does not exist. The badge is what
// tells a user WHICH session to resume, and it has to arrive with the list --
// a count the user reads only after landing on the row answers the question
// after the fact.
func fillOpenTodos(projects []Project) {
	var ids []string
	seen := map[string]bool{}
	for _, p := range projects {
		for _, s := range p.Sessions {
			if !seen[s.ID] {
				seen[s.ID] = true
				ids = append(ids, s.ID)
			}
		}
	}
	counts := agent.OpenCounts(ids)
	if len(counts) == 0 {
		return
	}
	for i := range projects {
		for j := range projects[i].Sessions {
			s := &projects[i].Sessions[j]
			s.OpenTodos = counts[s.ID]
		}
	}
}

func scanOne(ctx context.Context, dir string, cfg *config.Config) Project {
	p := Project{Name: filepath.Base(dir), Path: dir}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		p.Missing = true
		return p
	}

	repos := gitx.Discover(dir, cfg.MaxDepth)
	p.Repos = gitx.Scan(ctx, repos)
	p.RepoCount = len(p.Repos)
	for _, r := range p.Repos {
		if r.Pending {
			p.Pending++
		}
	}

	// A docker environment is auto-detected, never configured: a compose
	// file at the project root wins over one in any of its repos (the
	// environment belongs to the project, a repo merely runs in it).
	candidates := append([]string{dir}, make([]string, 0, len(p.Repos))...)
	for _, r := range p.Repos {
		candidates = append(candidates, r.Path)
	}
	if ed, ef := env.Detect(candidates); ed != "" {
		p.EnvDir, p.EnvFile = ed, ef
	}

	if sessions, err := agent.Recent(dir, cfg.SessionLimit); err != nil {
		if !silentFeedError(err) {
			p.FeedErrors = append(p.FeedErrors, fmt.Sprintf("sessions: %v", err))
		}
	} else {
		p.Sessions = sessions
	}

	if docs := docsFolder(cfg, dir); docs != "" {
		p.DocsFolder = docs
		p.Notes = vault.Recent(docs, cfg.NotesLimit)
	} else if cfg.VaultDir != "" {
		// A vault that exists but matched no folder: an empty NOTES pane
		// with no reason attached is indistinguishable from a broken feed.
		// This is a note, not an error: the project simply has no docs.
		p.FeedNotes = append(p.FeedNotes, fmt.Sprintf("notes: no docs folder matched %s", p.Name))
	}

	// The contract stays array-shaped when a feed is empty: JSON `null`
	// would make every consumer re-check for absence the reader already
	// checked.
	if p.Repos == nil {
		p.Repos = []gitx.Repo{}
	}
	if p.Sessions == nil {
		p.Sessions = []agent.Session{}
	}
	if p.Notes == nil {
		p.Notes = []vault.Note{}
	}

	return p
}

// feedNotes asks whether each feed can exist at all on this machine. These
// are report-level notes: the per-project FeedErrors stay silent for exactly
// the "feature is not installed here" cases (see silentFeedError), so without
// this the UI would show empty panes and never say why.
func feedNotes(cfg *config.Config) []string {
	var notes []string
	if why := agent.FeedNote(); why != "" {
		notes = append(notes, "sessions: "+why)
	}
	if cfg.VaultDir == "" {
		notes = append(notes, "notes: no vault configured")
	} else if _, err := os.Stat(cfg.VaultDir); err != nil {
		notes = append(notes, "notes: vault not readable")
	}
	if !env.Available() {
		notes = append(notes, "env: docker CLI not installed")
	}
	return notes
}

// silentFeedError: the agent feeds degrade quietly when the feature simply
// is not on this machine (no sqlite3, opencode never ran) -- that is not an
// error worth a row in the UI.
func silentFeedError(err error) bool {
	return err == agent.ErrNoBinary || err == agent.ErrNoDatabase
}

// docsFolder resolves a project's notes folder: an explicit docsMap entry
// (keyed by the project's full path or its basename) wins over the name
// heuristic. Map values are vault-relative unless they are absolute.
func docsFolder(cfg *config.Config, projectPath string) string {
	for _, key := range []string{projectPath, filepath.Base(projectPath)} {
		if entry, ok := cfg.DocsMap[key]; ok {
			if filepath.IsAbs(entry) {
				return entry
			}
			return filepath.Join(cfg.VaultDir, entry)
		}
	}
	return vault.DocsDir(cfg.VaultDir, projectPath)
}

func (r Report) JSON(pretty bool) (string, error) {
	var (
		out []byte
		err error
	)
	if pretty {
		out, err = json.MarshalIndent(r, "", "  ")
	} else {
		out, err = json.Marshal(r)
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}
