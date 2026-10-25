// Package gitx discovers and reads the git state of repositories.
//
// Discovery mirrors core/git-status.sh from the ei.git-status panel: a
// repository is any directory whose child named ".git" exists within
// maxDepth, repos nested inside a repo's worktree included -- the panel's
// `find -name .git -prune` prunes only the .git directory itself, and that
// is the contract kept here. Two deliberate differences: ".git" as a FILE
// also counts (worktrees/submodules, which `find -type d` misses), and
// node_modules-style dependency trees are pruned -- both only speed on the
// ntfs mounts.
//
// The git commands carry the panel's guards: --no-optional-locks so a scan
// never blocks a real git operation elsewhere, and -c core.fsmonitor= so a
// repo's own hook does not fire just because we looked.
package gitx

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var skipDirs = map[string]bool{
	"node_modules": true, "target": true, "dist": true, "build": true,
	".venv": true, "venv": true, ".tox": true, ".next": true, ".nuxt": true,
	"vendor": true, ".cache": true, "__pycache__": true, ".gradle": true,
}

const scanTimeout = 20 * time.Second

// Repo is one repository's state. Pending keeps the panel's meaning:
// something local that the remote does not have yet (uncommitted or
// unpushed) -- the bar icon and this badge want the same answer.
type Repo struct {
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	Dirty    bool   `json:"dirty"`
	Changes  int    `json:"changes"` // porcelain lines, capped at countCap
	Ahead   int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Pending  bool   `json:"pending"`
	Error    string `json:"error,omitempty"`
}

// Discover returns the repository roots under root, sorted, within maxDepth
// of root. root itself is included when it is a repo.
func Discover(root string, maxDepth int) []string {
	baseDepth := depthOf(root)
	var repos []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return filepath.SkipAll
			}
			return nil // unreadable subtree: skip it, the scan is best-effort
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
			// Hidden dirs are pruned, except ".git" itself is what we look for
			// one level up; dot-dirs never contain repos worth listing.
			return filepath.SkipDir
		}
		if depthOf(path)-baseDepth > maxDepth {
			return filepath.SkipDir
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			repos = append(repos, path)
			// And keep walking: a repository can hold more repositories --
			// the panel's find prunes only the .git directory itself, which
			// dot-pruning already does here. (uh.humance.app and its nested
			// www/moodledev are the case this mirrors.)
		}
		return nil
	})
	sort.Strings(repos)
	return repos
}

func depthOf(p string) int {
	return strings.Count(filepath.Clean(p), string(filepath.Separator))
}

// Status reads one repo. The context bounds the whole call; each git
// subprocess gets the remaining time.
func Status(ctx context.Context, path string) Repo {
	repo := Repo{Path: path}
	repo.Branch, repo.Error = branch(ctx, path)
	if repo.Error != "" {
		return repo
	}
	if n, err := porcelain(ctx, path); err != nil {
		repo.Error = err.Error()
		return repo
	} else {
		repo.Changes = n
		repo.Dirty = n > 0
	}
	repo.Behind, repo.Ahead, _ = counts(ctx, path) // no upstream -> zeros, not an error
	repo.Pending = repo.Dirty || repo.Ahead > 0
	return repo
}

// Scan reads every repo in parallel; results keep the input order.
func Scan(ctx context.Context, repos []string) []Repo {
	out := make([]Repo, len(repos))
	// The panel scans a handful of dirs with one process; here every repo is
	// its own git invocation, so the fan-out is bounded to keep ntfs sane.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range repos {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, scanTimeout)
			defer cancel()
			out[i] = Status(cctx, p)
		}(i, p)
	}
	wg.Wait()
	return out
}

// run is the single door every git call passes through.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "--no-optional-locks", "-c", "core.fsmonitor="}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &gitError{dir: dir, msg: msg}
	}
	return strings.TrimSpace(stdout.String()), nil
}

type gitError struct{ dir, msg string }

func (e *gitError) Error() string { return e.dir + ": " + e.msg }

func branch(ctx context.Context, dir string) (string, string) {
	if b, err := run(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && b != "" {
		return b, ""
	}
	// Detached (or a fresh repo without commits): the short sha still says
	// where we are; an error here means the dir is not a usable repo.
	c, err := run(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", "not a git repository"
	}
	return "(detached " + c + ")", ""
}

// countCap mirrors the panel's ceiling: a huge porcelain output only needs
// the answer "very dirty", not a full count into memory.
const countCap = 200

func porcelain(ctx context.Context, dir string) (int, error) {
	out, err := run(ctx, dir, "status", "--porcelain=v1")
	if err != nil {
		return 0, err
	}
	if out == "" {
		return 0, nil
	}
	n := len(strings.Split(out, "\n"))
	if n > countCap {
		return countCap, nil
	}
	return n, nil
}

// counts returns behind and ahead against the upstream, zeros without one.
func counts(ctx context.Context, dir string) (behind, ahead int, err error) {
	out, err := run(ctx, dir, "rev-list", "--left-right", "--count", "@{u}...HEAD")
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, nil
	}
	b, _ := strconv.Atoi(fields[0])
	a, _ := strconv.Atoi(fields[1])
	return b, a, nil
}
