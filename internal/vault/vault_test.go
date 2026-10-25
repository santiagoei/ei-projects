package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestRecentNewestFirstAndFiltered(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "old.md"), 10*time.Hour)
	touch(t, filepath.Join(root, "deep", "newest.md"), time.Minute)
	touch(t, filepath.Join(root, "middle.md"), 5*time.Hour)
	touch(t, filepath.Join(root, "notes.txt"), time.Second)           // not .md
	touch(t, filepath.Join(root, ".obsidian", "cache.md"), time.Hour) // hidden: skipped

	got := Recent(root, 10)
	if len(got) != 3 {
		t.Fatalf("want 3 notes, got %+v", got)
	}
	if filepath.Base(got[0].Path) != "newest.md" {
		t.Fatalf("newest first: %+v", got)
	}
	if got[0].Rel != filepath.Join("deep", "newest.md") {
		t.Fatalf("rel: %q", got[0].Rel)
	}
	if filepath.Base(got[2].Path) != "old.md" {
		t.Fatalf("order tail: %+v", got)
	}

	if limited := Recent(root, 2); len(limited) != 2 {
		t.Fatalf("limit ignored")
	}
}

func TestDocsDirMatching(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Clientes/icampus", "Otra/Cosa", "humance"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// "icampus.fei.org" finds "Clientes/icampus" (prefix after normalization).
	if got := DocsDir(root, filepath.Join("x", "icampus.fei.org")); got != filepath.Join(root, "Clientes/icampus") {
		t.Fatalf("prefix match failed: %q", got)
	}
	// exact hit beats everything
	if got := DocsDir(root, filepath.Join("x", "humance")); got != filepath.Join(root, "humance") {
		t.Fatalf("exact match failed: %q", got)
	}
	// no forced match for an unrelated name
	if got := DocsDir(root, filepath.Join("x", "omarchy")); got != "" {
		t.Fatalf("unrelated project should match nothing: %q", got)
	}
	// short names (<3 normalized) only match exactly
	if got := DocsDir(root, filepath.Join("x", "ummmm")); got != "" {
		t.Fatalf("noise matched: %q", got)
	}
	if got := DocsDir("", filepath.Join("x", "y")); got != "" {
		t.Fatal("empty vault must return empty")
	}
}

// The vault's own naming scheme (PARA-ish "tr.fei" for the project
// "icampus.fei.org") must still meet the project on a shared token.
func TestDocsDirTokenOverlap(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"50 area.tr/tr.fei", "50 area.tr/tr.uh"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := DocsDir(root, filepath.Join("p", "icampus.fei.org")); got != filepath.Join(root, "50 area.tr/tr.fei") {
		t.Fatalf("token 'fei' should bridge the names: %q", got)
	}
	// "uh" is a two-letter token: too generic to trust, so it must NOT match.
	if got := DocsDir(root, filepath.Join("p", "uh.humance.app")); got != "" {
		t.Fatalf("two-letter token must not match: %q", got)
	}
}
