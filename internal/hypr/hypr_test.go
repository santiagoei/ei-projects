package hypr

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParsePID(t *testing.T) {
	if pid, ok := parsePID([]byte(`{"pid":4242,"class":"foot"}`)); !ok || pid != 4242 {
		t.Fatalf("window: %d %v", pid, ok)
	}
	// a layer-shell surface or a lockscreen has no process to follow
	for _, j := range []string{`{"pid":0}`, `{}`, `not json`, ``} {
		if pid, ok := parsePID([]byte(j)); ok {
			t.Errorf("parsePID(%q) = %d, ok", j, pid)
		}
	}
}

// The answer is only useful when it names a place a project could live: the
// home directory -- where every .desktop-launched application sits -- and the
// filesystem root are both "no idea".
func TestReadCwd(t *testing.T) {
	root := t.TempDir()
	old := procRoot
	procRoot = filepath.Join(root, "proc")
	t.Cleanup(func() { procRoot = old })

	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	link := func(pid int, target string) {
		t.Helper()
		dir := filepath.Join(procRoot, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "cwd")); err != nil {
			t.Fatal(err)
		}
	}

	link(100, filepath.Join(root, "projects", "fei"))
	if dir, ok := readCwd(100); !ok || filepath.Base(dir) != "fei" {
		t.Fatalf("real project: %q %v", dir, ok)
	}

	link(101, home)
	if _, ok := readCwd(101); ok {
		t.Fatal("the home directory is not an answer")
	}
	link(102, "/")
	if _, ok := readCwd(102); ok {
		t.Fatal("/ is not an answer")
	}
	if _, ok := readCwd(999); ok {
		t.Fatal("a process that is not running has no cwd")
	}
}

// With no Hyprland to ask -- the test binary's PATH, an ssh session, X11 --
// ActiveDir says nothing instead of failing.
func TestActiveDirWithoutHyprland(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	if dir, ok := ActiveDir(); ok || dir != "" {
		t.Fatalf("no compositor should mean no answer: %q %v", dir, ok)
	}
}
