// Package config resolves what ei-projects should look at.
//
// The source of truth is the watchDirs string the ei.git-status bar widget
// persists into shell.json (the panel edits the same value through the bar's
// settings round-trip), so the TUI and the panel describe the same world
// without either owning a second copy. An optional
// ~/.config/ei-projects/config.json overrides anything for standalone use.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Entry kinds the scan produces per watched folder.
const (
	DefaultMaxDepth     = 4
	DefaultNotesLimit   = 5
	DefaultSessionLimit = 20 // per project; the TUI narrows it to the repo
	defaultWidgetID     = "ei.git-status"
)

type Config struct {
	WatchDirs    []string          `json:"watchDirs"`    // resolved absolute paths
	VaultDir     string            `json:"vault"`        // "" disables the notes feed
	DocsMap      map[string]string `json:"docsMap"`      // project path or name -> vault folder
	MaxDepth     int               `json:"maxDepth"`     // .git search depth per watch dir
	NotesLimit   int               `json:"notesLimit"`   // recent .md rows per project
	SessionLimit int               `json:"sessionLimit"` // opencode sessions per project
}

// widgetEntry is one layout slot in shell.json. Everything is an `any` bag:
// bar layouts are user-edited JSON and a strict struct would fail the whole
// read on the first unknown key another widget carries.
type widgetEntry map[string]any

func Load() (*Config, error) {
	cfg := Config{
		MaxDepth:     DefaultMaxDepth,
		NotesLimit:   DefaultNotesLimit,
		SessionLimit: DefaultSessionLimit,
	}

	dirs, _, shellErr := watchDirsFromShell()
	if shellErr == nil && len(dirs) > 0 {
		cfg.WatchDirs = dirs
	}

	// The override file is optional; a present-but-broken one is a hard error
	// (silently scanning a stale list would be worse).
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("no home directory: %w", err)
	}
	ownPath := filepath.Join(home, ".config", "ei-projects", "config.json")
	if data, err := os.ReadFile(ownPath); err == nil {
		var own struct {
			WatchDirs    *[]string          `json:"watchDirs"`
			VaultDir     *string            `json:"vault"`
			DocsMap      *map[string]string `json:"docsMap"`
			MaxDepth     *int               `json:"maxDepth"`
			NotesLimit   *int               `json:"notesLimit"`
			SessionLimit *int               `json:"sessionLimit"`
		}
		if err := json.Unmarshal(data, &own); err != nil {
			return nil, fmt.Errorf("%s: %w", ownPath, err)
		}
		if own.WatchDirs != nil {
			cfg.WatchDirs = *own.WatchDirs
		}
		if own.VaultDir != nil {
			cfg.VaultDir = *own.VaultDir
		}
		if own.DocsMap != nil {
			cfg.DocsMap = *own.DocsMap
		}
		if own.MaxDepth != nil && *own.MaxDepth > 0 {
			cfg.MaxDepth = *own.MaxDepth
		}
		if own.NotesLimit != nil && *own.NotesLimit > 0 {
			cfg.NotesLimit = *own.NotesLimit
		}
		if own.SessionLimit != nil && *own.SessionLimit > 0 {
			cfg.SessionLimit = *own.SessionLimit
		}
	}

	if cfg.VaultDir == "" {
		// No explicit vault: an "obsidian" watch dir is the same vault the
		// panel already scans; adopt it as the notes source.
		for _, d := range cfg.WatchDirs {
			if strings.EqualFold(filepath.Base(d), "obsidian") {
				cfg.VaultDir = d
				break
			}
		}
	}

	for i, d := range cfg.WatchDirs {
		cfg.WatchDirs[i] = resolve(d)
	}
	if cfg.VaultDir != "" {
		cfg.VaultDir = resolve(cfg.VaultDir)
	}
	// A vault that is not among the watched projects would otherwise vanish
	// from the panel-like list entirely: keep it visible as its own project.
	if cfg.VaultDir != "" {
		found := false
		for _, d := range cfg.WatchDirs {
			if d == cfg.VaultDir {
				found = true
				break
			}
		}
		if !found {
			cfg.WatchDirs = append(cfg.WatchDirs, cfg.VaultDir)
		}
	}

	if len(cfg.WatchDirs) == 0 {
		return nil, fmt.Errorf("no watch dirs: none in shell.json (%s) and no ~/.config/ei-projects/config.json", shellPath())
	}
	return &cfg, nil
}

func shellPath() string {
	if x := os.Getenv("OMARCHY_SHELL_JSON"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "omarchy", "shell.json")
}

// watchDirsFromShell digs the ei.git-status entry out of the bar layout:
// {"bar":{"layout":{"left":[{"id":"ei.git-status","watchDirs":"a, b"},...]}}}
func watchDirsFromShell() (dirs []string, ok bool, err error) {
	data, err := os.ReadFile(shellPath())
	if err != nil {
		return nil, false, err
	}
	var doc struct {
		Bar struct {
			Layout map[string][]widgetEntry `json:"layout"`
		} `json:"bar"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("%s: %w", shellPath(), err)
	}
	// Sections in a stable order; the first entry with our id wins.
	for _, section := range []string{"left", "center", "right"} {
		for _, entry := range doc.Bar.Layout[section] {
			if id, _ := entry["id"].(string); id != defaultWidgetID {
				continue
			}
			raw, _ := entry["watchDirs"].(string)
			for _, part := range strings.Split(raw, ",") {
				if p := strings.TrimSpace(part); p != "" {
					dirs = append(dirs, p)
				}
			}
			return dirs, len(dirs) > 0, nil
		}
	}
	return nil, false, fmt.Errorf("%s: no %s entry", shellPath(), defaultWidgetID)
}

// resolve expands ~, makes absolute, and follows symlinks when possible
// (~/Projects is a link onto the ntfs mount; matching what git resolves for
// repo paths keeps prefixes comparable against opencode's stored dirs).
func resolve(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}
