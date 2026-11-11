package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Setenv("GIT_TEST_DEFAULT_INITIAL_BRANCH_NAME", "main")
	full := append([]string{"-C", dir, "-c", "user.email=t@e", "-c", "user.name=t"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "c1")
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	mk := func(rel ...string) string {
		p := filepath.Join(root, filepath.Join(rel...))
		if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	one := mk("one")
	inside := mk("one", "sub") // repo nested in a repo: the panel finds these
	deep := mk("two", "three")
	tooDeep := mk("a", "b", "c", "d") // depth 4 > maxDepth 2
	junk := mk("node_modules", "pkg") // dependency tree, pruned

	repos := Discover(root, 2)
	want := []string{one, inside, deep}
	if len(repos) != len(want) {
		t.Fatalf("got %v want %v", repos, want)
	}
	for i := range want {
		rp, _ := filepath.EvalSymlinks(repos[i])
		wp, _ := filepath.EvalSymlinks(want[i])
		if rp != wp {
			t.Fatalf("repos[%d]=%s want %s", i, repos[i], want[i])
		}
	}
	for _, r := range repos {
		if r == tooDeep || r == junk || strings.HasPrefix(r, filepath.Dir(junk)) {
			t.Fatalf("%q should not be discovered", r)
		}
	}
}

func TestStatusCleanDirtyAhead(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, tmp, "init", "-q", "--bare", remote)

	repo := filepath.Join(tmp, "work")
	git(t, tmp, "clone", "-q", remote, "work")
	if err := os.WriteFile(filepath.Join(repo, "a"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "first")
	// push -u is what gives the clone an upstream at all: cloning the empty
	// bare left no remote-tracking branch to compare against.
	git(t, repo, "push", "-q", "-u", "origin", "main")

	ctx := context.Background()
	r := Status(ctx, repo)
	if r.Error != "" || r.Branch != "main" || r.Dirty || r.Ahead != 0 || r.Pending {
		t.Fatalf("fresh repo should be clean/branch main: %+v", r)
	}

	// one local commit the remote has not seen: ahead, pending.
	if err := os.WriteFile(filepath.Join(repo, "b"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "second")
	r = Status(ctx, repo)
	if r.Error != "" || r.Dirty || r.Ahead != 1 || !r.Pending {
		t.Fatalf("want ahead=1 pending: %+v", r)
	}

	// and uncommitted work on top.
	if err := os.WriteFile(filepath.Join(repo, "c"), []byte("3"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = Status(ctx, repo)
	if !r.Dirty || r.Changes != 1 || !r.Pending || r.Ahead != 1 {
		t.Fatalf("want dirty 1 + ahead 1: %+v", r)
	}
}

func TestStatusNotARepo(t *testing.T) {
	r := Status(context.Background(), t.TempDir())
	if r.Error == "" {
		t.Fatalf("plain dir must report an error, got %+v", r)
	}
}

// The v2 read must answer exactly what the three-call route answered: the
// panel's badge, the bar widget's icon and this cockpit all read the same
// Repo, and one subprocess is only worth it if nothing else moved.
func TestStatusV2MatchesLegacy(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name string
		dir  func(t *testing.T) string
		want Repo
	}{
		{"plain dir", func(t *testing.T) string { return t.TempDir() }, Repo{}},
		{"unborn branch", func(t *testing.T) string {
			d := filepath.Join(t.TempDir(), "u")
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			git(t, d, "init", "-q", "-b", "main")
			return d
		}, Repo{Branch: "main"}},
	}

	for _, tc := range cases {
		dir := tc.dir(t)
		got := statusV2(context.Background(), dir)
		legacy := statusLegacy(context.Background(), dir)
		if got.Error == "" && tc.name == "plain dir" {
			t.Fatalf("%s: no error reported", tc.name)
		}
		if got.Branch != legacy.Branch {
			t.Fatalf("%s: branch %q, legacy %q", tc.name, got.Branch, legacy.Branch)
		}
		if (got.Error == "") != (legacy.Error == "") {
			t.Fatalf("%s: v2 %q vs legacy %q", tc.name, got.Error, legacy.Error)
		}
		if tc.want.Branch != "" && got.Branch != tc.want.Branch {
			t.Fatalf("%s: branch %q, want %q", tc.name, got.Branch, tc.want.Branch)
		}
	}

	// a working tree with a rename, a staged delete, an untracked file and
	// an unstashed modification: the entry count is the same number porcelain
	// v1 rows produce, which is what the ●N badge has always meant.
	repo := filepath.Join(tmp, "mixed")
	newRepo(t, repo)
	git(t, repo, "mv", "f", "ff")
	if err := os.WriteFile(filepath.Join(repo, "n"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("2"), 0o644); err != nil {
		t.Fatal(err) // an untracked path again after the rename
	}
	v2 := statusV2(context.Background(), repo)
	old := statusLegacy(context.Background(), repo)
	if v2.Error != "" || old.Error != "" {
		t.Fatalf("errors: %q %q", v2.Error, old.Error)
	}
	if v2.Changes != old.Changes || v2.Dirty != old.Dirty || v2.Pending != old.Pending {
		t.Fatalf("working tree state differs: v2 %+v legacy %+v", v2, old)
	}
	if v2.Changes < 3 {
		t.Fatalf("rename + untracked + untracked should count: %+v", v2)
	}
}

// Detached is a place a cockpit gets driven into by a submodule or a
// bisect, and v2 says it differently from rev-parse: the full sha instead of
// a short one, and "(detached)" where the branch name would be.
func TestStatusDetachedAndUpstream(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, tmp, "init", "-q", "--bare", remote)
	repo := filepath.Join(tmp, "work")
	git(t, tmp, "clone", "-q", remote, "work")
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(repo, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-q", "-m", "c "+n)
	}
	git(t, repo, "push", "-q", "-u", "origin", "main")

	r := Status(context.Background(), repo)
	if r.Upstream != "origin/main" {
		t.Fatalf("upstream: %+v", r)
	}

	one := git(t, repo, "rev-parse", "--short", "HEAD")
	git(t, repo, "checkout", "-q", "--detach", "HEAD")
	r = Status(context.Background(), repo)
	if r.Error != "" {
		t.Fatalf("detached: %v", r.Error)
	}
	if r.Branch != "(detached "+one+")" {
		t.Fatalf("detached branch label: %q, want (detached %s)", r.Branch, one)
	}
	if legacy := statusLegacy(context.Background(), repo); legacy.Branch != r.Branch {
		t.Fatalf("detached: v2 %q, legacy %q", r.Branch, legacy.Branch)
	}
}

func TestAtLeast(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"git version 2.56.0", true},
		{"git version 2.22.0", true},
		{"git version 2.22.0.rpmforce", true},
		{"git version 2.21.4", false},
		{"git version 1.9.1", false},
		{"git version 3.0", true},
		{"git version weird", false},
		{"", false},
	} {
		if got := atLeast(tc.version, 2, 22); got != tc.want {
			t.Errorf("atLeast(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestCommitsNewestFirstWithMeta(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	newRepo(t, dir) // one commit "c1"
	if err := os.WriteFile(filepath.Join(dir, "g"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "second one")

	got, err := Commits(context.Background(), dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 commits: %+v", got)
	}
	if got[0].Subject != "second one" || got[1].Subject != "c1" {
		t.Fatalf("newest first: %+v", got)
	}
	if got[0].Hash == "" || got[0].Author != "t" || got[0].When.IsZero() {
		t.Fatalf("metadata missing: %+v", got[0])
	}

	limited, err := Commits(context.Background(), dir, 1)
	if err != nil || len(limited) != 1 || limited[0].Subject != "second one" {
		t.Fatalf("limit: %+v err=%v", limited, err)
	}
}

func TestScanKeepsOrderAndParallelism(t *testing.T) {
	root := t.TempDir()
	var dirs []string
	for _, n := range []string{"alpha", "beta", "gamma"} {
		d := filepath.Join(root, n)
		newRepo(t, d)
		dirs = append(dirs, d)
	}
	out := Scan(context.Background(), dirs)
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	for i := range out {
		if filepath.Base(out[i].Path) != filepath.Base(dirs[i]) {
			t.Fatalf("order broken at %d: %+v", i, out[i])
		}
		if out[i].Error != "" || out[i].Pending {
			t.Fatalf("repo %d: %+v", i, out[i])
		}
	}
}
