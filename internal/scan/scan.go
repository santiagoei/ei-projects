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
	DocsFolder string          `json:"docsFolder,omitempty"`
	Notes      []vault.Note    `json:"notes"`
	Sessions   []agent.Session `json:"sessions"`
	FeedErrors []string        `json:"feedErrors,omitempty"`
}

type Report struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Projects    []Project `json:"projects"`
}

// Run scans every watched folder. The git fan-out is parallel; the agent and
// vault feeds are cheap enough to run inline but degrade per project: a
// missing feed appends to FeedErrors and never aborts the report.
func Run(ctx context.Context, cfg *config.Config) Report {
	report := Report{GeneratedAt: time.Now()}
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
	return report
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
