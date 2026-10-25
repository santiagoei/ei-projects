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
