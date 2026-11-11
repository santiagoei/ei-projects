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
	Upstream string `json:"upstream,omitempty"` // "" when the branch has no remote to compare with
	Dirty    bool   `json:"dirty"`
	Changes  int    `json:"changes"` // porcelain lines, capped at countCap
	Ahead    int    `json:"ahead"`
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
	if porcelainV2() {
		return statusV2(ctx, path)
	}
	return statusLegacy(ctx, path)
}

// statusV2 is the one-subprocess read: since git 2.22, `status
// --porcelain=v2 --branch` answers branch, upstream, ahead/behind and the
// working-tree entries in a single call -- which used to cost three git
// invocations per repository, and on a slow mount those were the entire price
// of a scan. A failure here is the repository's own (an unreadable index,
// dubious ownership), and its message is what the row shows, instead of the
// generic "not a git repository" the legacy path could only guess.
func statusV2(ctx context.Context, path string) Repo {
	repo := Repo{Path: path}
	out, err := run(ctx, path, "status", "--porcelain=v2", "--branch")
	if err != nil {
		repo.Error = err.Error()
		return repo
	}
	head, oid := "", ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "# "):
			key, val, _ := strings.Cut(strings.TrimPrefix(line, "# "), " ")
			switch key {
			case "branch.head":
				head = val
			case "branch.oid":
				oid = val
			case "branch.upstream":
				repo.Upstream = val
			case "branch.ab":
				repo.Ahead, repo.Behind = parseAB(val)
			}
		default:
			repo.Changes++ // 1 changed, 2 renamed, u unmerged, ? untracked
		}
	}
	if repo.Changes > countCap {
		repo.Changes = countCap
	}
	repo.Dirty = repo.Changes > 0
	switch {
	case head == "(detached)":
		// v2 hands out the full sha; seven characters is what rev-parse --short
		// has been printing since the format was chosen
		repo.Branch = "(detached " + abbrev(oid) + ")"
	case head == "(unborn)":
		// a branch with no commits yet: git 2.22..2.27 said so instead of
		// naming the branch that is about to exist
		repo.Branch = firstBranchRef(ctx, path)
	default:
		repo.Branch = head
	}
	repo.Pending = repo.Dirty || repo.Ahead > 0
	return repo
}

// abbrev is git's own default short form: seven characters of a full sha.
func abbrev(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// parseAB reads `# branch.ab +2 -1` into ahead and behind. The signs are the
// answer: + is local work the remote lacks, - is remote work to pull -- the
// same pair rev-list --left-right --count reports, in the other order.
func parseAB(val string) (ahead, behind int) {
	for _, part := range strings.Fields(val) {
		n, err := strconv.Atoi(part[1:])
		if err != nil || len(part) < 2 {
			continue
		}
		switch part[0] {
		case '+':
			ahead = n
		case '-':
			behind = n
		}
	}
	return ahead, behind
}

// firstBranchRef names the branch an unborn repository is standing on, the
// way the legacy read did: symbolic-ref succeeds there even though the ref the
// branch points at does not exist yet.
func firstBranchRef(ctx context.Context, dir string) string {
	if b, err := run(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		return b
	}
	return ""
}

var (
	v2Once   sync.Once
	v2Usable bool
)

// porcelainV2 asks git once per process whether it speaks the v2 status
// format (2.22, June 2019). Cheaper than discovering it per repository, and
// it keeps "this git is old" separate from "this repo is broken" -- the two
// failures the legacy fallback used to blur together.
func porcelainV2() bool {
	v2Once.Do(func() {
		out, err := exec.Command("git", "version").Output()
		if err != nil {
			return
		}
		v2Usable = atLeast(strings.TrimSpace(string(out)), 2, 22)
	})
	return v2Usable
}

// atLeast reads "git version 2.56.0" (with whatever the distro appends) and
// answers whether major.minor reaches what the caller needs.
func atLeast(version string, major, minor int) bool {
	i := strings.LastIndex(version, "version ")
	if i >= 0 {
		version = version[i+len("version "):]
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	m, err1 := strconv.Atoi(parts[0])
	n, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return m > major || (m == major && n >= minor)
}

// statusLegacy is the three-call route: branch, working tree, ahead/behind.
// Only a git without the v2 status format takes it.
func statusLegacy(ctx context.Context, path string) Repo {
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
		// Git's advice ("To add an exception ... call: git config ...")
		// rides in the same stderr, lines and all: a multi-line error in a
		// one-line row or status inflates the frame past the window. The
		// first sentence is the whole story ("fatal: detected dubious
		// ownership in repository at ...").
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
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

// Commit is one line of history, as the cockpit's COMMITS tab shows it.
type Commit struct {
	Hash    string    `json:"hash"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	When    time.Time `json:"when"`
}

// Commits reads the newest limit entries of HEAD's log. It is on demand
// (the TUI fetches it when a repo is selected) rather than part of the
// scan report: history only matters for the one repo being looked at, and
// fetching it per scan of per repo would multiply the work for nothing.
func Commits(ctx context.Context, dir string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 15
	}
	out, err := run(ctx, dir, "log", "-n", strconv.Itoa(limit),
		"--pretty=format:%h\x1f%s\x1f%an\x1f%at")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 4 {
			continue
		}
		at, _ := strconv.ParseInt(f[3], 10, 64)
		commits = append(commits, Commit{
			Hash: f[0], Subject: f[1], Author: f[2], When: time.Unix(at, 0),
		})
	}
	return commits, nil
}
